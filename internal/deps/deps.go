// Package deps builds and analyses the full dependency graph of a Python project.
package deps

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"molt/internal/uvbin"
	"molt/pkg/types"
)

// Analyser builds the full dependency graph for a project.
type Analyser struct {
	ProjectDir string
	Verbose    bool
}

// New creates an Analyser.
func New(projectDir string, verbose bool) *Analyser {
	return &Analyser{ProjectDir: projectDir, Verbose: verbose}
}

// Build constructs the complete DepGraph for the project.
func (a *Analyser) Build() (*types.DepGraph, error) {
	g := &types.DepGraph{
		GeneratedAt: time.Now().UTC(),
		Platform:    fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}

	// Read pyproject.toml for root name/version.
	name, version := readProjectMeta(a.ProjectDir)
	g.Root = name
	g.Version = version

	// Python interpreter.
	if a.Verbose {
		fmt.Println("  Analysing Python interpreter...")
	}
	g.Python = a.buildPythonInfo()

	// Source files.
	if a.Verbose {
		fmt.Println("  Hashing source files...")
	}
	g.Source = a.buildSourceInfo()

	// Installed packages.
	if a.Verbose {
		fmt.Println("  Reading installed packages...")
	}
	directDeps := a.readDirectDeps()
	g.Packages = a.buildPackageInfo(directDeps)

	// Native extensions.
	if a.Verbose {
		fmt.Println("  Scanning native extensions...")
	}
	g.NativeExts = a.buildNativeExts()

	// System libraries.
	if a.Verbose {
		fmt.Println("  Tracing system libraries...")
	}
	g.SystemLibs = a.buildSystemLibs(g.NativeExts)

	// glibc version.
	g.GlibcVer = detectGlibcVersion()

	// Build environment.
	g.BuildEnv = a.buildEnvInfo()

	// Security summary.
	g.Security = a.buildSecuritySummary(g)

	return g, nil
}

// ── Python info ───────────────────────────────────────────────────────────────

// internal/deps/deps.go (Key changes in buildPythonInfo and buildEnvInfo)
func (a *Analyser) buildPythonInfo() types.PythonInfo {
	uv, _ := uvbin.Ensure()

	info := types.PythonInfo{}
	if out, err := exec.Command(uv, "run", "python", "--version").Output(); err == nil {
		info.Version = strings.TrimPrefix(strings.TrimSpace(string(out)), "Python ")
	}
	if out, err := exec.Command(uv, "run", "python", "-c", `import ssl; print(ssl.OPENSSL_VERSION)`).Output(); err == nil {
		info.OpenSSL = strings.TrimSpace(string(out))
	}
	// Determine if standalone via uv python list or path prefix
	if out, err := exec.Command(uv, "python", "find").Output(); err == nil {
		p := strings.TrimSpace(string(out))
		info.Path = p
		info.BuildType = "standalone"
		if strings.Contains(p, "python") && strings.Contains(p, "lib") {
			info.BuildType = "system"
		}
	}
	info.SHA256, _ = hashFilePath(info.Path)
	return info
}

// ... [rest of deps.go unchanged, but all exec.Command(python...) replaced with exec.Command(uv, "run", "python"...)]
// ── Source files ──────────────────────────────────────────────────────────────

func (a *Analyser) buildSourceInfo() []types.SourceInfo {
	var files []types.SourceInfo
	skip := map[string]bool{
		".venv": true, "__pycache__": true, ".git": true,
		"dist": true, "build": true, ".mypy_cache": true,
	}
	sourceExts := map[string]string{
		".py": "source", ".toml": "config", ".cfg": "config",
		".ini": "config", ".json": "config", ".yaml": "config",
		".yml": "config", ".env": "config", ".lock": "config",
		".txt": "config", ".md": "data",
	}

	filepath.WalkDir(a.ProjectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(a.ProjectDir, path)
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if skip[part] {
				return filepath.SkipDir
			}
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		kind, ok := sourceExts[ext]
		if !ok {
			return nil
		}
		h, _ := hashFilePath(path)
		info, _ := d.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		files = append(files, types.SourceInfo{
			RelPath: filepath.ToSlash(rel),
			SHA256:  h,
			Size:    size,
			Kind:    kind,
		})
		return nil
	})
	return files
}

// ── Packages ──────────────────────────────────────────────────────────────────

func (a *Analyser) readDirectDeps() map[string]bool {
	direct := map[string]bool{}
	tomlPath := filepath.Join(a.ProjectDir, "pyproject.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return direct
	}
	inDeps := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "dependencies = [" || line == "dependencies=[" {
			inDeps = true
			continue
		}
		if inDeps {
			if line == "]" {
				break
			}
			// Extract package name from "requests>=2.0" or "requests"
			name := strings.Trim(line, `"', `)
			for _, sep := range []string{">=", "<=", "!=", "==", ">", "<", "~="} {
				if idx := strings.Index(name, sep); idx > 0 {
					name = name[:idx]
				}
			}
			if name != "" {
				direct[strings.ToLower(name)] = true
			}
		}
	}
	return direct
}

// internal/deps/deps.go

// buildPackageInfo constructs package info from site-packages and enriches it
// with lockfile metadata (sha256, requires) and reverse dependencies (required_by).
func (a *Analyser) buildPackageInfo(directDeps map[string]bool) []types.PackageInfo {
	sitePackages := a.sitePackagesDir()
	if sitePackages == "" {
		return nil
	}
	lockData, _ := os.ReadFile(filepath.Join(a.ProjectDir, "uv.lock"))
	lockMeta := parseUvLockMeta(lockData)

	var packages []types.PackageInfo
	entries, _ := os.ReadDir(sitePackages)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".dist-info") {
			continue
		}
		parts := strings.SplitN(strings.TrimSuffix(e.Name(), ".dist-info"), "-", 2)
		if len(parts) != 2 {
			continue
		}
		name, version := parts[0], parts[1]
		distDir := filepath.Join(sitePackages, e.Name())

		pkg := types.PackageInfo{
			Name:      name,
			Version:   version,
			DirectDep: directDeps[strings.ToLower(name)],
		}

		if wheelData, err := os.ReadFile(filepath.Join(distDir, "WHEEL")); err == nil {
			for _, line := range strings.Split(string(wheelData), "\n") {
				if strings.HasPrefix(line, "Root-Is-Purelib: true") {
					pkg.Pure = true
				}
				if strings.HasPrefix(line, "Tag: ") {
					pkg.ABI = strings.TrimPrefix(strings.TrimSpace(line), "Tag: ")
				}
			}
		}
		if recordData, err := os.ReadFile(filepath.Join(distDir, "RECORD")); err == nil {
			pkg.Files = parseRecord(recordData, sitePackages)
		}
		pkg.License = readLicense(filepath.Join(distDir, "METADATA"))

		packages = append(packages, pkg)
	}

	// Build quick-lookup map & populate Requires + compute RequiredBy
	pkgMap := map[string]*types.PackageInfo{}
	for i := range packages {
		pkgMap[strings.ToLower(packages[i].Name)] = &packages[i]
	}
	for i := range packages {
		pkg := &packages[i]
		if meta, ok := lockMeta[strings.ToLower(pkg.Name)]; ok {
			pkg.WheelSHA256 = meta.sha256
			pkg.Requires = meta.requires
			for _, req := range meta.requires {
				reqLower := strings.ToLower(req)
				if parent, exists := pkgMap[reqLower]; exists {
					parent.RequiredBy = append(parent.RequiredBy, pkg.Name)
				}
			}
		}
	}

	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Name < packages[j].Name
	})
	return packages
}

type pkgMeta struct {
	sha256   string
	requires []string
}

// parseUvLockMeta extracts sha256 and dependency lists from uv.lock
func parseUvLockMeta(data []byte) map[string]pkgMeta {
	result := map[string]pkgMeta{}
	if len(data) == 0 {
		return result
	}
	var currentName string
	inDeps := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			currentName = ""
			inDeps = false
			continue
		}
		if strings.HasPrefix(line, "name = ") {
			currentName = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
			continue
		}
		if currentName == "" {
			continue
		}

		// Track dependencies block
		if line == "dependencies = [" || line == "dependencies=[" {
			inDeps = true
			continue
		}
		if inDeps && line == "]" {
			inDeps = false
			continue
		}

		if inDeps {
			if depName := extractDepName(line); depName != "" {
				m := result[currentName]
				m.requires = append(m.requires, depName)
				result[currentName] = m
			}
		}

		// Extract sha256 (can appear before or after dependencies)
		if strings.HasPrefix(line, "sha256 = ") {
			m := result[currentName]
			m.sha256 = strings.Trim(strings.TrimPrefix(line, "sha256 = "), `"`)
			result[currentName] = m
		}
	}
	return result
}

// extractDepName safely parses dependency entries from uv.lock.
// Handles both inline tables: { name = "urllib3" } and plain strings: "urllib3"
func extractDepName(line string) string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, ",")

	// Handle inline table { name = "pkg", specifier = ">=1.0" }
	if strings.HasPrefix(line, "{") {
		if idx := strings.Index(line, `name = "`); idx != -1 {
			start := idx + len(`name = "`)
			if end := strings.Index(line[start:], `"`); end != -1 {
				return line[start : start+end]
			}
		}
		return ""
	}
	// Handle plain quoted string "pkg"
	return strings.Trim(line, `" `)
}

// ── Native extensions ─────────────────────────────────────────────────────────

func (a *Analyser) buildNativeExts() []types.NativeExt {
	sitePackages := a.sitePackagesDir()
	if sitePackages == "" {
		return nil
	}

	ext := nativeLibExt()
	var exts []types.NativeExt

	filepath.WalkDir(sitePackages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.Contains(filepath.Base(path), ext) {
			return nil
		}
		rel, _ := filepath.Rel(sitePackages, path)
		h, _ := hashFilePath(path)

		ne := types.NativeExt{
			Path:   filepath.ToSlash(rel),
			SHA256: h,
		}

		// Determine which package owns this.
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) > 0 {
			ne.Package = parts[0]
		}

		// Trace system library imports.
		ne.ImportsFrom = traceLibraryDeps(path)

		// Check ELF hardening (Linux only).
		if runtime.GOOS == "linux" {
			ne.Hardening = checkELFHardening(path)
			ne.RPATH = readRPATH(path)
		}

		exts = append(exts, ne)
		return nil
	})
	return exts
}

// ── System libraries ──────────────────────────────────────────────────────────

func (a *Analyser) buildSystemLibs(nativeExts []types.NativeExt) []types.SysLibInfo {
	seen := map[string]*types.SysLibInfo{}

	// Collect all system library deps from native extensions.
	for _, ne := range nativeExts {
		for _, libName := range ne.ImportsFrom {
			if _, ok := seen[libName]; !ok {
				info := &types.SysLibInfo{
					Name:     libName,
					Standard: isStandardLib(libName),
				}
				// Try to find the actual file.
				if path := resolveLibPath(libName); path != "" {
					info.Path = path
					info.SHA256, _ = hashFilePath(path)
					info.SONAME = readSONAME(path)
					info.OSPackage = queryOSPackage(path)
					info.MinRequired = readMinGlibcRequired(path)
				}
				seen[libName] = info
			}
			seen[libName].RequiredBy = append(seen[libName].RequiredBy, ne.Path)
		}
	}

	// Also trace the Python binary itself.
	venvPython := filepath.Join(a.ProjectDir, ".venv", "bin", pythonBin())
	if _, err := os.Stat(venvPython); err == nil {
		for _, libName := range traceLibraryDeps(venvPython) {
			if _, ok := seen[libName]; !ok {
				info := &types.SysLibInfo{
					Name:     libName,
					Standard: isStandardLib(libName),
				}
				if path := resolveLibPath(libName); path != "" {
					info.Path = path
					info.SHA256, _ = hashFilePath(path)
					info.OSPackage = queryOSPackage(path)
					info.MinRequired = readMinGlibcRequired(path)
				}
				seen[libName] = info
			}
			seen[libName].RequiredBy = append(seen[libName].RequiredBy, "python")
		}
	}

	var libs []types.SysLibInfo
	for _, lib := range seen {
		libs = append(libs, *lib)
	}
	sort.Slice(libs, func(i, j int) bool {
		return libs[i].Name < libs[j].Name
	})
	return libs
}

// ── Build environment ─────────────────────────────────────────────────────────

func (a *Analyser) buildEnvInfo() types.BuildEnvInfo {
	info := types.BuildEnvInfo{Molt: "dev"}

	// OS info.
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				info.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	if info.OS == "" {
		info.OS = runtime.GOOS
	}

	// Kernel.
	if out, err := exec.Command("uname", "-r").Output(); err == nil {
		info.Kernel = strings.TrimSpace(string(out))
	}

	// glibc.
	info.Glibc = detectGlibcVersion()

	// GCC.
	if out, err := exec.Command("gcc", "--version").Output(); err == nil {
		lines := strings.Split(string(out), "\n")
		if len(lines) > 0 {
			info.GCC = strings.TrimSpace(lines[0])
		}
	}

	return info
}

// ── Security summary ──────────────────────────────────────────────────────────

func (a *Analyser) buildSecuritySummary(g *types.DepGraph) types.SecuritySummary {
	s := types.SecuritySummary{}

	// Check hardening on native extensions.
	for _, ne := range g.NativeExts {
		if !ne.Hardening.Fortify {
			s.Warnings++
			s.Issues = append(s.Issues, fmt.Sprintf("%s: FORTIFY_SOURCE not enabled", ne.Path))
		}
		if ne.Hardening.RELRO == "none" || ne.Hardening.RELRO == "partial" {
			s.Warnings++
			s.Issues = append(s.Issues, fmt.Sprintf("%s: RELRO is %s", ne.Path, ne.Hardening.RELRO))
		}
	}

	// CVE count from system libs.
	for _, lib := range g.SystemLibs {
		s.CVECount += len(lib.CVEs)
		for _, cve := range lib.CVEs {
			s.Warnings++
			s.Issues = append(s.Issues, fmt.Sprintf("%s: %s (%s)", lib.Name, cve.ID, cve.Severity))
		}
	}

	return s
}

// ── Display ───────────────────────────────────────────────────────────────────

// PrintTree prints the full dependency tree.
func PrintTree(g *types.DepGraph) {
	fmt.Printf("\n%s %s\n", g.Root, g.Version)
	fmt.Printf("Platform: %s", g.Platform)
	if g.GlibcVer != "" {
		fmt.Printf("  glibc: %s", g.GlibcVer)
	}
	fmt.Println()
	fmt.Println()

	// Python.
	fmt.Printf("Python %s [%s]\n", g.Python.Version, g.Python.BuildType)
	fmt.Printf("  sha256: %s\n", shortHash(g.Python.SHA256))
	if g.Python.OpenSSL != "" {
		fmt.Printf("  openssl: %s\n", g.Python.OpenSSL)
	}
	fmt.Println()

	// Direct deps first, then transitive.
	fmt.Println("Packages:")
	for _, pkg := range g.Packages {
		marker := ""
		if pkg.DirectDep {
			marker = " [direct]"
		}
		pureStr := "native"
		if pkg.Pure {
			pureStr = "pure"
		}
		fmt.Printf("  ├─ %s %s (%s)%s\n", pkg.Name, pkg.Version, pureStr, marker)
		if pkg.WheelSHA256 != "" {
			fmt.Printf("  │   sha256: %s\n", shortHash(pkg.WheelSHA256))
		}
		if pkg.License != "" {
			fmt.Printf("  │   license: %s\n", pkg.License)
		}
	}
	fmt.Println()

	// Native extensions.
	if len(g.NativeExts) > 0 {
		fmt.Println("Native Extensions:")
		for _, ne := range g.NativeExts {
			fmt.Printf("  ├─ %s\n", ne.Path)
			fmt.Printf("  │   sha256: %s\n", shortHash(ne.SHA256))
			if len(ne.ImportsFrom) > 0 {
				fmt.Printf("  │   links: %s\n", strings.Join(ne.ImportsFrom, ", "))
			}
			h := ne.Hardening
			flags := hardeningFlags(h)
			fmt.Printf("  │   hardening: %s\n", flags)
		}
		fmt.Println()
	}

	// System libs.
	if len(g.SystemLibs) > 0 {
		fmt.Println("System Libraries:")
		for _, lib := range g.SystemLibs {
			standardStr := ""
			if !lib.Standard {
				standardStr = " ⚠ NON-STANDARD"
			}
			fmt.Printf("  ├─ %s%s\n", lib.Name, standardStr)
			if lib.Path != "" {
				fmt.Printf("  │   path: %s\n", lib.Path)
			}
			if lib.SHA256 != "" {
				fmt.Printf("  │   sha256: %s\n", shortHash(lib.SHA256))
			}
			if lib.OSPackage != "" {
				fmt.Printf("  │   os-pkg: %s\n", lib.OSPackage)
			}
			if lib.MinRequired != "" {
				fmt.Printf("  │   min-required: %s\n", lib.MinRequired)
			}
		}
		fmt.Println()
	}

	// Security summary.
	if g.Security.Warnings > 0 {
		fmt.Printf("⚠ Security warnings: %d\n", g.Security.Warnings)
		for _, issue := range g.Security.Issues {
			fmt.Printf("  - %s\n", issue)
		}
	} else {
		fmt.Println("✓ No security warnings")
	}
	fmt.Println()
}

// PrintFlat prints a flat dep list.
func PrintFlat(g *types.DepGraph) {
	fmt.Printf("# %s %s — %s\n\n", g.Root, g.Version, g.Platform)
	fmt.Printf("[python]\n%s %s (sha256:%s)\n\n", g.Python.Version, g.Python.BuildType, shortHash(g.Python.SHA256))

	fmt.Println("[packages]")
	for _, pkg := range g.Packages {
		fmt.Printf("%-30s %-12s pure:%-5v sha256:%s\n",
			pkg.Name, pkg.Version, pkg.Pure, shortHash(pkg.WheelSHA256))
	}
	fmt.Println()

	fmt.Println("[native-extensions]")
	for _, ne := range g.NativeExts {
		fmt.Printf("%-50s sha256:%s\n", ne.Path, shortHash(ne.SHA256))
	}
	fmt.Println()

	fmt.Println("[system-libs]")
	for _, lib := range g.SystemLibs {
		std := "standard"
		if !lib.Standard {
			std = "NON-STANDARD"
		}
		fmt.Printf("%-30s %-12s %s\n", lib.Name, shortHash(lib.SHA256), std)
	}
}

// Why explains why a given package/library is in the graph.
func Why(g *types.DepGraph, target string) {
	target = strings.ToLower(target)
	found := false

	// Check packages.
	for _, pkg := range g.Packages {
		if strings.ToLower(pkg.Name) == target {
			found = true
			fmt.Printf("%s %s is in the graph because:\n", pkg.Name, pkg.Version)
			if pkg.DirectDep {
				fmt.Println("  → it is a direct dependency in pyproject.toml")
			}
			if len(pkg.RequiredBy) > 0 {
				fmt.Printf("  → required by: %s\n", strings.Join(pkg.RequiredBy, ", "))
			}
		}
	}

	// Check system libs.
	for _, lib := range g.SystemLibs {
		if strings.ToLower(lib.Name) == target || strings.Contains(strings.ToLower(lib.Name), target) {
			found = true
			fmt.Printf("%s is a system library required by:\n", lib.Name)
			for _, req := range lib.RequiredBy {
				fmt.Printf("  → %s\n", req)
			}
		}
	}

	if !found {
		fmt.Printf("%s not found in dependency graph\n", target)
	}
}

// PinnedBy shows which constraints are responsible for a package's version.
func PinnedBy(projectDir, packageName string) error {
	lockPath := filepath.Join(projectDir, "uv.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read uv.lock: %w", err)
	}

	target := strings.ToLower(packageName)
	fmt.Printf("Version constraints for %s:\n\n", packageName)

	// Parse uv.lock for dependency declarations.
	var currentPkg string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			currentPkg = ""
			continue
		}
		if strings.HasPrefix(line, "name = ") {
			currentPkg = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
			continue
		}
		// Look for dependencies sections.
		if strings.Contains(line, target) && currentPkg != "" && currentPkg != target {
			fmt.Printf("  %-30s requires %s\n", currentPkg, line)
		}
	}
	return nil
}

// Conflicts detects version conflicts in the dependency graph.
func Conflicts(projectDir string) error {
	lockPath := filepath.Join(projectDir, "uv.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return fmt.Errorf("read uv.lock: %w", err)
	}

	// Build a map of package → version.
	versions := map[string]string{}
	constraints := map[string][]types.DepConstraint{}

	var currentPkg, currentVer string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			currentPkg = ""
			currentVer = ""
			continue
		}
		if strings.HasPrefix(line, "name = ") {
			currentPkg = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
			continue
		}
		if strings.HasPrefix(line, "version = ") && currentPkg != "" {
			currentVer = strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
			versions[currentPkg] = currentVer
			continue
		}
		// Parse dependency constraints.
		for _, sep := range []string{">=", "<=", "!=", "==", ">", "<"} {
			if strings.Contains(line, sep) && currentPkg != "" {
				parts := strings.SplitN(line, sep, 2)
				if len(parts) == 2 {
					depName := strings.Trim(parts[0], `"' []`)
					constraint := sep + strings.Trim(parts[1], `"' ,]`)
					constraints[depName] = append(constraints[depName], types.DepConstraint{
						RequiredBy: currentPkg,
						Constraint: constraint,
					})
				}
			}
		}
	}

	conflicts := 0
	for pkg, cons := range constraints {
		if len(cons) < 2 {
			continue
		}
		// Check if constraints are potentially incompatible (simplified check).
		hasMin := false
		hasMax := false
		for _, c := range cons {
			if strings.HasPrefix(c.Constraint, ">=") || strings.HasPrefix(c.Constraint, ">") {
				hasMin = true
			}
			if strings.HasPrefix(c.Constraint, "<=") || strings.HasPrefix(c.Constraint, "<") {
				hasMax = true
			}
		}
		if hasMin && hasMax {
			fmt.Printf("⚠ %s has potentially conflicting constraints:\n", pkg)
			for _, c := range cons {
				fmt.Printf("   %s requires %s%s\n", c.RequiredBy, pkg, c.Constraint)
			}
			fmt.Printf("   resolved: %s\n\n", versions[pkg])
			conflicts++
		}
	}

	if conflicts == 0 {
		fmt.Println("✓ No version conflicts detected")
	}
	return nil
}

// Minimal finds which direct deps could potentially be removed.
func Minimal(projectDir string) error {
	// Read direct deps from pyproject.toml.
	tomlPath := filepath.Join(projectDir, "pyproject.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}

	directDeps := []string{}
	inDeps := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "dependencies") && strings.Contains(line, "[") {
			inDeps = true
			continue
		}
		if inDeps {
			if line == "]" {
				break
			}
			name := strings.Trim(line, `"', `)
			for _, sep := range []string{">=", "<=", "!=", "==", ">", "<"} {
				if idx := strings.Index(name, sep); idx > 0 {
					name = name[:idx]
				}
			}
			if name != "" {
				directDeps = append(directDeps, name)
			}
		}
	}

	// Build import graph to check which are actually imported.
	fmt.Println("Analysing which direct dependencies are imported by your source code...")
	fmt.Println()

	// Walk source files and collect top-level imports.
	imported := map[string]bool{}
	filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		if strings.Contains(path, ".venv") || strings.Contains(path, "__pycache__") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, imp := range extractImports(string(data)) {
			imported[imp] = true
		}
		return nil
	})

	// Now check each direct dep.
	possibly_removable := 0
	for _, dep := range directDeps {
		depNorm := strings.ToLower(strings.ReplaceAll(dep, "-", "_"))
		if imported[depNorm] || imported[dep] {
			fmt.Printf("  ✓ %-30s imported directly by source\n", dep)
		} else {
			fmt.Printf("  ? %-30s NOT directly imported — may be transitive or unused\n", dep)
			possibly_removable++
		}
	}

	fmt.Println()
	if possibly_removable > 0 {
		fmt.Printf("%d dependencies may be removable. Verify before removing.\n", possibly_removable)
		fmt.Println("Use 'molt deps why <package>' to understand each one.")
	} else {
		fmt.Println("All direct dependencies appear to be used.")
	}
	return nil
}

// ── ELF analysis ──────────────────────────────────────────────────────────────

func checkELFHardening(path string) types.HardeningInfo {
	h := types.HardeningInfo{}
	f, err := elf.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()

	// PIE: ET_DYN type.
	h.PIE = f.Type == elf.ET_DYN

	// NX: GNU_STACK segment without exec flag.
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_GNU_STACK {
			h.NX = prog.Flags&elf.PF_X == 0
		}
		if prog.Type == elf.PT_GNU_RELRO {
			h.RELRO = "partial"
		}
	}

	// Full RELRO: check BIND_NOW in dynamic section.
	if dynSyms, err := f.DynamicSymbols(); err == nil {
		_ = dynSyms
		h.RELRO = "full" // simplified
	}

	// Stack canary and FORTIFY: check for __stack_chk_fail and __printf_chk symbols.
	if syms, err := f.ImportedSymbols(); err == nil {
		for _, sym := range syms {
			if sym.Name == "__stack_chk_fail" {
				h.StackCanary = true
			}
			if sym.Name == "__printf_chk" || sym.Name == "__memcpy_chk" {
				h.Fortify = true
			}
		}
	}

	if h.RELRO == "" {
		h.RELRO = "none"
	}

	return h
}

func readRPATH(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	rpath, _ := f.DynString(elf.DT_RPATH)
	if len(rpath) > 0 {
		return rpath[0]
	}
	runpath, _ := f.DynString(elf.DT_RUNPATH)
	if len(runpath) > 0 {
		return runpath[0]
	}
	return ""
}

func readSONAME(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	soname, _ := f.DynString(elf.DT_SONAME)
	if len(soname) > 0 {
		return soname[0]
	}
	return ""
}

// readMinGlibcRequired reads the minimum glibc version required by an ELF binary.
func readMinGlibcRequired(path string) string {
	f, err := elf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	minVer := ""
	syms, err := f.ImportedSymbols()
	if err != nil {
		return ""
	}
	for _, sym := range syms {
		if strings.HasPrefix(sym.Version, "GLIBC_") {
			v := strings.TrimPrefix(sym.Version, "GLIBC_")
			if minVer == "" || compareVersions(v, minVer) > 0 {
				minVer = v
			}
		}
	}
	return minVer
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (a *Analyser) sitePackagesDir() string {
	candidates := []string{
		filepath.Join(a.ProjectDir, ".venv", "lib"),
	}
	for _, base := range candidates {
		entries, _ := os.ReadDir(base)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "python") && e.IsDir() {
				sp := filepath.Join(base, e.Name(), "site-packages")
				if _, err := os.Stat(sp); err == nil {
					return sp
				}
			}
		}
	}
	return ""
}

func traceLibraryDeps(binary string) []string {
	switch runtime.GOOS {
	case "linux":
		return traceLDD(binary)
	case "darwin":
		return traceOtool(binary)
	}
	return nil
}

func traceLDD(binary string) []string {
	out, err := exec.Command("ldd", binary).Output()
	if err != nil {
		return nil
	}
	var libs []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.Fields(line)
		if len(parts) >= 3 && parts[1] == "=>" {
			libs = append(libs, parts[0])
		}
	}
	return libs
}

func traceOtool(binary string) []string {
	out, err := exec.Command("otool", "-L", binary).Output()
	if err != nil {
		return nil
	}
	var libs []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		parts := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(parts) > 0 {
			libs = append(libs, filepath.Base(parts[0]))
		}
	}
	return libs
}

func resolveLibPath(name string) string {
	// Common library search paths.
	searchDirs := []string{
		"/lib/x86_64-linux-gnu",
		"/lib/aarch64-linux-gnu",
		"/usr/lib/x86_64-linux-gnu",
		"/usr/lib",
		"/lib",
	}
	for _, dir := range searchDirs {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func queryOSPackage(path string) string {
	// Try dpkg first (Debian/Ubuntu).
	if out, err := exec.Command("dpkg", "-S", path).Output(); err == nil {
		parts := strings.SplitN(strings.TrimSpace(string(out)), ":", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0])
		}
	}
	// Try rpm (RHEL/CentOS/Fedora).
	if out, err := exec.Command("rpm", "-qf", path).Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func isStandardLib(name string) bool {
	standard := []string{
		"libc.so", "libm.so", "libpthread.so", "libdl.so", "librt.so",
		"libutil.so", "libnsl.so", "libcrypt.so", "ld-linux",
		"libgcc_s.so", "libstdc++.so",
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
	lines := strings.Split(string(out), "\n")
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) > 0 {
			return fields[len(fields)-1]
		}
	}
	return ""
}

func nativeLibExt() string {
	switch runtime.GOOS {
	case "darwin":
		return ".so"
	case "windows":
		return ".pyd"
	default:
		return ".so"
	}
}

func pythonBin() string {
	if runtime.GOOS == "windows" {
		return "python.exe"
	}
	return "python3"
}

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

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "..."
	}
	return h
}

func hardeningFlags(h types.HardeningInfo) string {
	flags := []string{}
	if h.StackCanary {
		flags = append(flags, "canary:✓")
	} else {
		flags = append(flags, "canary:✗")
	}
	flags = append(flags, "relro:"+h.RELRO)
	if h.NX {
		flags = append(flags, "nx:✓")
	} else {
		flags = append(flags, "nx:✗")
	}
	if h.PIE {
		flags = append(flags, "pie:✓")
	} else {
		flags = append(flags, "pie:✗")
	}
	if h.Fortify {
		flags = append(flags, "fortify:✓")
	} else {
		flags = append(flags, "fortify:✗")
	}
	return strings.Join(flags, "  ")
}

func readProjectMeta(projectDir string) (name, version string) {
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return filepath.Base(projectDir), "unknown"
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "name = ") {
			name = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
		}
		if strings.HasPrefix(line, "version = ") {
			version = strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
		}
	}
	if name == "" {
		name = filepath.Base(projectDir)
	}
	if version == "" {
		version = "unknown"
	}
	return
}

func parseRecord(data []byte, sitePackages string) []types.RecordFile {
	var files []types.RecordFile
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ",", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[0]
		hash := strings.TrimPrefix(parts[1], "sha256=")
		files = append(files, types.RecordFile{
			Path:   path,
			SHA256: hash,
		})
		if len(files) > 20 { // cap for display
			break
		}
	}
	return files
}

func readLicense(metadataPath string) string {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "License: ") {
			return strings.TrimPrefix(line, "License: ")
		}
		if strings.HasPrefix(line, "License-Expression: ") {
			return strings.TrimPrefix(line, "License-Expression: ")
		}
	}
	return ""
}

func extractImports(source string) []string {
	var imports []string
	scanner := bufio.NewScanner(strings.NewReader(source))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "import ") {
			pkg := strings.TrimPrefix(line, "import ")
			pkg = strings.SplitN(pkg, ".", 2)[0]
			pkg = strings.SplitN(pkg, " ", 2)[0]
			imports = append(imports, strings.TrimSpace(pkg))
		}
		if strings.HasPrefix(line, "from ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				pkg := strings.SplitN(parts[1], ".", 2)[0]
				if pkg != "." && pkg != ".." {
					imports = append(imports, pkg)
				}
			}
		}
	}
	return imports
}

func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	for i := 0; i < len(partsA) && i < len(partsB); i++ {
		if partsA[i] > partsB[i] {
			return 1
		}
		if partsA[i] < partsB[i] {
			return -1
		}
	}
	return len(partsA) - len(partsB)
}

// JSON output.
func JSON(g *types.DepGraph) ([]byte, error) {
	return json.MarshalIndent(g, "", "  ")
}
