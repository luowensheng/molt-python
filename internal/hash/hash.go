// Package hash builds and verifies the full project hash manifest.
package hash

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"molt/internal/uvbin"
	"molt/pkg/types"
)

const LockFile = ".molt-deps.lock"

// Builder creates hash manifests.
type Builder struct {
	ProjectDir string
}

// New creates a Builder.
func New(projectDir string) *Builder {
	return &Builder{ProjectDir: projectDir}
}

// Build computes the full hash manifest for the project.
func (b *Builder) Build() (*types.HashManifest, error) {
	m := &types.HashManifest{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Molt:        "dev",
		Platform:    fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		BuildEnv:    map[string]string{},
	}

	// Python binary.
	fmt.Println("  Hashing Python interpreter...")
	m.Python = b.hashPython()

	// Source files.
	fmt.Println("  Hashing source files...")
	m.Source = b.hashSource()

	// Installed packages.
	fmt.Println("  Hashing installed packages...")
	m.Packages = b.hashPackages()

	// Native extensions.
	fmt.Println("  Hashing native extensions...")
	m.NativeExts = b.hashNativeExts()

	// System libraries.
	fmt.Println("  Hashing system libraries...")
	m.SystemLibs = b.hashSystemLibs()

	// Build env.
	m.GlibcVer = detectGlibcVersion()
	m.BuildEnv["os"] = detectOS()
	m.BuildEnv["glibc"] = m.GlibcVer
	m.BuildEnv["molt"] = "dev"

	return m, nil
}

// Write writes the hash manifest to .molt-deps.lock.
func (b *Builder) Write(m *types.HashManifest) error {
	path := filepath.Join(b.ProjectDir, LockFile)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("✓ Hash manifest written to %s\n", path)
	return nil
}

// Load reads the existing hash manifest.
func Load(projectDir string) (*types.HashManifest, error) {
	path := filepath.Join(projectDir, LockFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no hash manifest found — run 'molt hash lock' first")
	}
	var m types.HashManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Verify re-hashes everything and compares against the stored manifest.
func (b *Builder) Verify(stored *types.HashManifest) error {
	fmt.Println("Verifying hash manifest...")
	current, err := b.Build()
	if err != nil {
		return err
	}

	issues := 0

	// Compare Python.
	if stored.Python.SHA256 != current.Python.SHA256 {
		fmt.Printf("  ✗ Python binary changed: %s → %s\n",
			short(stored.Python.SHA256), short(current.Python.SHA256))
		issues++
	} else {
		fmt.Println("  ✓ Python binary unchanged")
	}

	// Compare source files.
	storedSource := indexEntries(stored.Source)
	for _, sf := range current.Source {
		if prev, ok := storedSource[sf.Path]; ok {
			if prev.SHA256 != sf.SHA256 {
				fmt.Printf("  ✗ Modified: %s\n", sf.Path)
				issues++
			}
		} else {
			fmt.Printf("  + New file: %s\n", sf.Path)
			issues++
		}
	}
	for path := range storedSource {
		found := false
		for _, sf := range current.Source {
			if sf.Path == path {
				found = true
				break
			}
		}
		if !found {
			fmt.Printf("  - Deleted: %s\n", path)
			issues++
		}
	}

	// Compare packages.
	storedPkgs := indexEntries(stored.Packages)
	for _, pkg := range current.Packages {
		if prev, ok := storedPkgs[pkg.Path]; ok {
			if prev.SHA256 != pkg.SHA256 {
				fmt.Printf("  ✗ Package changed: %s\n", pkg.Path)
				issues++
			}
		} else {
			fmt.Printf("  + New package: %s\n", pkg.Path)
			issues++
		}
	}

	// Compare native extensions.
	storedNative := indexEntries(stored.NativeExts)
	for _, ne := range current.NativeExts {
		if prev, ok := storedNative[ne.Path]; ok {
			if prev.SHA256 != ne.SHA256 {
				fmt.Printf("  ✗ Native extension changed: %s\n", ne.Path)
				issues++
			}
		} else {
			fmt.Printf("  + New native extension: %s\n", ne.Path)
			issues++
		}
	}

	// Compare system libs.
	storedLibs := indexEntries(stored.SystemLibs)
	for _, lib := range current.SystemLibs {
		if prev, ok := storedLibs[lib.Path]; ok {
			if prev.SHA256 != lib.SHA256 {
				fmt.Printf("  ✗ System library changed: %s (OS update?)\n", lib.Path)
				issues++
			}
		}
	}

	fmt.Println()
	if issues == 0 {
		fmt.Println("✓ All hashes match — environment is verified")
	} else {
		fmt.Printf("✗ %d differences found\n", issues)
		return fmt.Errorf("%d hash mismatches", issues)
	}
	return nil
}

// Diff shows what changed since the last hash lock.
func (b *Builder) Diff(stored *types.HashManifest) error {
	current, err := b.Build()
	if err != nil {
		return err
	}

	fmt.Printf("Changes since %s:\n\n", stored.GeneratedAt)
	changes := 0

	storedSource := indexEntries(stored.Source)
	for _, sf := range current.Source {
		if prev, ok := storedSource[sf.Path]; ok {
			if prev.SHA256 != sf.SHA256 {
				fmt.Printf("  M %s\n", sf.Path)
				changes++
			}
		} else {
			fmt.Printf("  A %s\n", sf.Path)
			changes++
		}
	}
	for path := range storedSource {
		found := false
		for _, sf := range current.Source {
			if sf.Path == path {
				found = true
				break
			}
		}
		if !found {
			fmt.Printf("  D %s\n", path)
			changes++
		}
	}

	storedPkgs := indexEntries(stored.Packages)
	for _, pkg := range current.Packages {
		if prev, ok := storedPkgs[pkg.Path]; ok {
			if prev.SHA256 != pkg.SHA256 {
				fmt.Printf("  M [pkg] %s\n", pkg.Path)
				changes++
			}
		} else {
			fmt.Printf("  A [pkg] %s\n", pkg.Path)
			changes++
		}
	}

	if changes == 0 {
		fmt.Println("  (no changes)")
	} else {
		fmt.Printf("\n%d changes\n", changes)
	}
	return nil
}

// HashFile hashes a single file and prints the result.
func HashFile(path string) error {
	h, err := hashFilePath(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Printf("File:   %s\n", path)
	fmt.Printf("Size:   %d bytes\n", info.Size())
	fmt.Printf("SHA256: %s\n", h)
	return nil
}

// PrintManifest prints the hash manifest in human-readable form.
func PrintManifest(m *types.HashManifest) {
	fmt.Printf("# molt-deps.lock\n")
	fmt.Printf("# generated: %s\n", m.GeneratedAt)
	fmt.Printf("# platform:  %s\n", m.Platform)
	if m.GlibcVer != "" {
		fmt.Printf("# glibc:     %s\n", m.GlibcVer)
	}
	fmt.Println()

	fmt.Printf("[python]\n")
	fmt.Printf("%-60s sha256:%s\n\n", m.Python.Path, short(m.Python.SHA256))

	fmt.Printf("[source]\n")
	for _, sf := range m.Source {
		fmt.Printf("%-60s sha256:%s\n", sf.Path, short(sf.SHA256))
	}
	fmt.Println()

	fmt.Printf("[packages]\n")
	for _, pkg := range m.Packages {
		fmt.Printf("%-60s sha256:%s\n", pkg.Path, short(pkg.SHA256))
	}
	fmt.Println()

	fmt.Printf("[native-extensions]\n")
	for _, ne := range m.NativeExts {
		fmt.Printf("%-60s sha256:%s\n", ne.Path, short(ne.SHA256))
	}
	fmt.Println()

	fmt.Printf("[system-libs]\n")
	for _, lib := range m.SystemLibs {
		std := ""
		if lib.Standard {
			std = " (standard)"
		}
		fmt.Printf("%-60s sha256:%s%s\n", lib.Path, short(lib.SHA256), std)
	}
}

// ── Internal hashing ──────────────────────────────────────────────────────────

// internal/hash/hash.go
func (b *Builder) hashPython() types.HashEntry {
	uv, _ := uvbin.Ensure()
	out, err := exec.Command(uv, "python", "find").Output()
	if err != nil {
		return types.HashEntry{Path: "python (not resolved)"}
	}
	path := strings.TrimSpace(string(out))

	h, _ := hashFilePath(path)
	info, _ := os.Stat(path)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return types.HashEntry{Path: path, SHA256: h, Size: size, Kind: "python"}
}


func (b *Builder) hashSource() []types.HashEntry {
	var entries []types.HashEntry
	skip := map[string]bool{
		".venv": true, "__pycache__": true, ".git": true,
		"dist": true, "build": true,
	}
	exts := map[string]bool{
		".py": true, ".toml": true, ".cfg": true, ".ini": true,
		".json": true, ".yaml": true, ".yml": true, ".env": true,
		".lock": true, ".txt": true,
	}

	filepath.WalkDir(b.ProjectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(b.ProjectDir, path)
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if skip[part] {
				return filepath.SkipDir
			}
		}
		if !exts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		h, _ := hashFilePath(path)
		info, _ := d.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		entries = append(entries, types.HashEntry{
			Path:   filepath.ToSlash(rel),
			SHA256: h,
			Size:   size,
			Kind:   "source",
		})
		return nil
	})
	return entries
}

func (b *Builder) hashPackages() []types.HashEntry {
	sitePackages := b.sitePackagesDir()
	if sitePackages == "" {
		return nil
	}
	var entries []types.HashEntry
	filepath.WalkDir(sitePackages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Only hash WHEEL and METADATA files (the authoritative package descriptors).
		base := filepath.Base(path)
		if base != "WHEEL" && base != "METADATA" && base != "RECORD" {
			return nil
		}
		rel, _ := filepath.Rel(sitePackages, path)
		h, _ := hashFilePath(path)
		info, _ := d.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		entries = append(entries, types.HashEntry{
			Path:   filepath.ToSlash(rel),
			SHA256: h,
			Size:   size,
			Kind:   "package",
		})
		return nil
	})
	return entries
}

func (b *Builder) hashNativeExts() []types.HashEntry {
	sitePackages := b.sitePackagesDir()
	if sitePackages == "" {
		return nil
	}
	var entries []types.HashEntry
	filepath.WalkDir(sitePackages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.Contains(filepath.Base(path), ".so") &&
			!strings.HasSuffix(path, ".pyd") {
			return nil
		}
		rel, _ := filepath.Rel(sitePackages, path)
		h, _ := hashFilePath(path)
		info, _ := d.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		entries = append(entries, types.HashEntry{
			Path:   filepath.ToSlash(rel),
			SHA256: h,
			Size:   size,
			Kind:   "native",
		})
		return nil
	})
	return entries
}

func (b *Builder) hashSystemLibs() []types.HashEntry {
	// Find libs linked by the venv Python and native exts.
	libPaths := map[string]bool{}

	venvPython := filepath.Join(b.ProjectDir, ".venv", "bin", pythonBin())
	for _, lib := range traceLDDPaths(venvPython) {
		libPaths[lib] = true
	}

	sitePackages := b.sitePackagesDir()
	if sitePackages != "" {
		filepath.WalkDir(sitePackages, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.Contains(filepath.Base(path), ".so") {
				for _, lib := range traceLDDPaths(path) {
					libPaths[lib] = true
				}
			}
			return nil
		})
	}

	var entries []types.HashEntry
	for path := range libPaths {
		if path == "" {
			continue
		}
		h, _ := hashFilePath(path)
		info, _ := os.Stat(path)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		entries = append(entries, types.HashEntry{
			Path:     path,
			SHA256:   h,
			Size:     size,
			Kind:     "syslib",
			Standard: isStandardLib(filepath.Base(path)),
		})
	}
	return entries
}

func traceLDDPaths(binary string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	out, err := exec.Command("ldd", binary).Output()
	if err != nil {
		return nil
	}
	var paths []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.Fields(line)
		if len(parts) >= 4 && parts[1] == "=>" && parts[2] != "not" {
			paths = append(paths, parts[2])
		}
	}
	return paths
}

func (b *Builder) sitePackagesDir() string {
	base := filepath.Join(b.ProjectDir, ".venv", "lib")
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "python") && e.IsDir() {
			sp := filepath.Join(base, e.Name(), "site-packages")
			if _, err := os.Stat(sp); err == nil {
				return sp
			}
		}
	}
	return ""
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func hashFilePath(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func short(h string) string {
	if len(h) > 16 {
		return h[:16] + "..."
	}
	return h
}

func indexEntries(entries []types.HashEntry) map[string]types.HashEntry {
	m := map[string]types.HashEntry{}
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}

func pythonBin() string {
	if runtime.GOOS == "windows" {
		return "python.exe"
	}
	return "python3"
}

func isStandardLib(name string) bool {
	standard := []string{
		"libc.so", "libm.so", "libpthread.so", "libdl.so", "librt.so",
		"libutil.so", "ld-linux", "libgcc_s.so", "libstdc++.so",
	}
	for _, s := range standard {
		if strings.HasPrefix(name, s) {
			return true
		}
	}
	return false
}

func detectGlibcVersion() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	out, err := exec.Command("ldd", "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.Split(string(out), "\n")[0])
	if len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return ""
}

func detectOS() string {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	return runtime.GOOS
}
