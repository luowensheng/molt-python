// internal/env/env.go
// Package env manages environment snapshots, validation, and watching.
package env

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

	"molt/pkg/types"
)

const snapshotsDir = ".molt/snapshots"

// Manager handles environment operations.
type Manager struct {
	ProjectDir string
}

// New creates a Manager.
func New(projectDir string) *Manager {
	return &Manager{ProjectDir: projectDir}
}

// Snapshot saves the current venv state to a named snapshot.
func (m *Manager) Snapshot(name string) error {
	if name == "" {
		name = time.Now().UTC().Format("20060102-150405")
	}

	snapDir := filepath.Join(m.ProjectDir, snapshotsDir)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return err
	}

	snap := &types.EnvSnapshot{
		Name:        name,
		CreatedAt:   time.Now().UTC(),
		ProjectPath: m.ProjectDir,
	}

	// Python version.
	venvPython := m.venvPython()
	if out, err := exec.Command(venvPython, "--version").Output(); err == nil {
		snap.Python = strings.TrimPrefix(strings.TrimSpace(string(out)), "Python ")
	}

	// Hash all venv files.
	fmt.Printf("Snapshotting venv state...")
	venvDir := filepath.Join(m.ProjectDir, ".venv")
	if _, err := os.Stat(venvDir); os.IsNotExist(err) {
		return fmt.Errorf("no venv at .venv/ — run 'molt sync' first")
	}

	var files []types.HashEntry
	filepath.WalkDir(venvDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(venvDir, path)
		h, _ := hashFilePath(path)
		info, _ := d.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		files = append(files, types.HashEntry{
			Path:   filepath.ToSlash(rel),
			SHA256: h,
			Size:   size,
		})
		return nil
	})
	snap.Files = files

	// Hash source files.
	snap.Packages = m.hashPackageMeta()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	snapPath := filepath.Join(snapDir, name+".json")
	if err := os.WriteFile(snapPath, data, 0o644); err != nil {
		return err
	}

	fmt.Printf("\r✓ Snapshot '%s' saved (%d files)\n", name, len(files))
	fmt.Printf("  Path: %s\n", snapPath)
	return nil
}

// Restore restores the venv to a named snapshot using uv.
func (m *Manager) Restore(name string) error {
	snap, err := m.LoadSnapshot(name)
	if err != nil {
		return err
	}

	fmt.Printf("Restoring to snapshot '%s' (created %s)...\n",
		snap.Name, snap.CreatedAt.Format("2006-01-02 15:04:05"))

	venvDir := filepath.Join(m.ProjectDir, ".venv")

	// Nuke current venv.
	fmt.Println("  Removing current venv...")
	if err := os.RemoveAll(venvDir); err != nil {
		return fmt.Errorf("remove venv: %w", err)
	}

	// Recreate with correct Python version using uv.
	fmt.Printf("  Creating venv with Python %s via uv...\n", snap.Python)
	uv, err := findUV()
	if err != nil {
		return fmt.Errorf("uv not found: %w", err)
	}

	cmd := exec.Command(uv, "venv", "--python", snap.Python, venvDir)
	cmd.Dir = m.ProjectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uv venv: %w", err)
	}

	// Install packages from the snapshot's package list using uv.
	pkgs := extractPackagesFromSnapshot(snap)
	if len(pkgs) > 0 {
		fmt.Printf("  Installing %d packages via uv...\n", len(pkgs))
		args := append([]string{"pip", "install", "--quiet"}, pkgs...)
		cmd := exec.Command(uv, args...)
		cmd.Dir = m.ProjectDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("  ⚠ Some packages may not have installed: %v\n", err)
		}
	}

	fmt.Printf("✓ Restored to snapshot '%s'\n", name)
	fmt.Println("Run 'molt env validate' to verify.")
	return nil
}

// ListSnapshots lists all saved snapshots.
func (m *Manager) ListSnapshots() error {
	snapDir := filepath.Join(m.ProjectDir, snapshotsDir)
	entries, err := os.ReadDir(snapDir)
	if os.IsNotExist(err) {
		fmt.Println("No snapshots found.")
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Printf("Snapshots in %s:\n\n", snapDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		snap, err := m.LoadSnapshot(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		fmt.Printf("  %-30s Python %-10s  %d files  %s\n",
			snap.Name,
			snap.Python,
			len(snap.Files),
			snap.CreatedAt.Format("2006-01-02 15:04:05"),
		)
	}
	return nil
}

// LoadSnapshot loads a named snapshot.
func (m *Manager) LoadSnapshot(name string) (*types.EnvSnapshot, error) {
	snapPath := filepath.Join(m.ProjectDir, snapshotsDir, name+".json")
	data, err := os.ReadFile(snapPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot '%s' not found", name)
	}
	var snap types.EnvSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// Diff compares the current venv to the lockfile using uv.
func (m *Manager) Diff() error {
	fmt.Println("Comparing venv to lockfile...")

	lockPath := filepath.Join(m.ProjectDir, "uv.lock")
	lockData, _ := os.ReadFile(lockPath)
	lockPackages := parseLockPackages(lockData)

	// Get installed packages via uv pip list.
	installed := m.installedPackages()

	diffs := 0
	for name, lockVer := range lockPackages {
		if installedVer, ok := installed[name]; ok {
			if installedVer != lockVer {
				fmt.Printf("  ≠ %-30s lock:%-15s installed:%s\n", name, lockVer, installedVer)
				diffs++
			}
		} else {
			fmt.Printf("  - %-30s in lock but NOT installed\n", name)
			diffs++
		}
	}
	for name, ver := range installed {
		if _, ok := lockPackages[name]; !ok {
			fmt.Printf("  + %-30s installed (%s) but NOT in lock\n", name, ver)
			diffs++
		}
	}

	if diffs == 0 {
		fmt.Println("✓ Venv matches lockfile exactly")
	} else {
		fmt.Printf("\n%d differences found. Run 'molt env reset' to fix.\n", diffs)
	}
	return nil
}

// Validate checks the venv is consistent with pyproject.toml and uv.lock.
func (m *Manager) Validate() error {
	fmt.Println("Validating environment...")

	issues := 0

	// Check venv exists.
	venvDir := filepath.Join(m.ProjectDir, ".venv")
	if _, err := os.Stat(venvDir); os.IsNotExist(err) {
		fmt.Println("  ✗ No venv found — run 'molt sync'")
		return fmt.Errorf("venv missing")
	}
	fmt.Println("  ✓ Venv exists")

	// Check Python version matches .python-version.
	if data, err := os.ReadFile(filepath.Join(m.ProjectDir, ".python-version")); err == nil {
		pinned := strings.TrimSpace(string(data))
		venvPython := m.venvPython()
		if out, err := exec.Command(venvPython, "--version").Output(); err == nil {
			installed := strings.TrimPrefix(strings.TrimSpace(string(out)), "Python ")
			if installed == pinned {
				fmt.Printf("  ✓ Python version matches: %s\n", pinned)
			} else {
				fmt.Printf("  ✗ Python version mismatch: pinned=%s installed=%s\n", pinned, installed)
				issues++
			}
		}
	}

	// Check packages match lockfile using uv pip list.
	lockPath := filepath.Join(m.ProjectDir, "uv.lock")
	lockData, _ := os.ReadFile(lockPath)
	lockPkgs := parseLockPackages(lockData)
	installed := m.installedPackages()

	mismatches := 0
	for name, ver := range lockPkgs {
		if iv, ok := installed[name]; !ok || iv != ver {
			mismatches++
		}
	}
	if mismatches == 0 {
		fmt.Printf("  ✓ All %d packages match lockfile\n", len(lockPkgs))
	} else {
		fmt.Printf("  ✗ %d packages don't match lockfile\n", mismatches)
		issues++
	}

	// Check for PYTHONPATH pollution.
	if pp := os.Getenv("PYTHONPATH"); pp != "" {
		fmt.Printf("  ⚠ PYTHONPATH=%s (may leak packages into venv)\n", pp)
	} else {
		fmt.Println("  ✓ PYTHONPATH not set")
	}

	if issues == 0 {
		fmt.Println("\n✓ Environment is valid")
	} else {
		fmt.Printf("\n✗ %d issues found\n", issues)
	}
	return nil
}

// Reset nukes and recreates the venv from the lockfile using uv.
func (m *Manager) Reset() error {
	fmt.Println("Resetting environment...")

	venvDir := filepath.Join(m.ProjectDir, ".venv")
	fmt.Println("  Removing venv...")
	if err := os.RemoveAll(venvDir); err != nil {
		return err
	}

	uv, err := findUV()
	if err != nil {
		return fmt.Errorf("uv not found — install uv first: https://github.com/astral-sh/uv")
	}

	// Determine Python version from .python-version if present.
	uvVenvArgs := []string{"venv"}
	if data, err := os.ReadFile(filepath.Join(m.ProjectDir, ".python-version")); err == nil {
		uvVenvArgs = append(uvVenvArgs, "--python", strings.TrimSpace(string(data)))
	}
	uvVenvArgs = append(uvVenvArgs, venvDir)

	fmt.Printf("  Creating venv via uv...\n")
	cmd := exec.Command(uv, uvVenvArgs...)
	cmd.Dir = m.ProjectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uv venv: %w", err)
	}

	// Sync from lockfile using uv sync --frozen.
	fmt.Println("  Syncing packages from lockfile via uv sync --frozen...")
	syncCmd := exec.Command(uv, "sync", "--frozen")
	syncCmd.Dir = m.ProjectDir
	syncCmd.Stdout = os.Stdout
	syncCmd.Stderr = os.Stderr
	if err := syncCmd.Run(); err != nil {
		return fmt.Errorf("uv sync: %w", err)
	}

	fmt.Println("✓ Environment reset complete")
	return nil
}

// Vars prints all environment variables the project sets.
func (m *Manager) Vars() error {
	envFile := filepath.Join(m.ProjectDir, ".env")
	envExample := filepath.Join(m.ProjectDir, ".env.example")

	var source string
	if _, err := os.Stat(envFile); err == nil {
		source = envFile
	} else if _, err := os.Stat(envExample); err == nil {
		source = envExample
	}

	if source == "" {
		fmt.Println("No .env or .env.example found.")
		return nil
	}

	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}

	fmt.Printf("Environment variables (from %s):\n\n", filepath.Base(source))
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			live := os.Getenv(parts[0])
			if live != "" {
				fmt.Printf("  %-30s = %s (from env)\n", parts[0], live)
			} else {
				fmt.Printf("  %-30s = %s (default)\n", parts[0], parts[1])
			}
		}
	}
	return nil
}

// VarsCheck verifies .env has all keys from .env.example.
func (m *Manager) VarsCheck() error {
	examplePath := filepath.Join(m.ProjectDir, ".env.example")
	envPath := filepath.Join(m.ProjectDir, ".env")

	exampleData, err := os.ReadFile(examplePath)
	if err != nil {
		return fmt.Errorf(".env.example not found")
	}

	envData, _ := os.ReadFile(envPath)
	envKeys := parseEnvKeys(envData)
	exampleKeys := parseEnvKeys(exampleData)

	issues := 0
	for key := range exampleKeys {
		if !envKeys[key] {
			fmt.Printf("  ✗ Missing: %s (in .env.example but not in .env)\n", key)
			issues++
		}
	}
	for key := range envKeys {
		if !exampleKeys[key] {
			fmt.Printf("  ⚠ Extra: %s (in .env but not in .env.example)\n", key)
		}
	}

	if issues == 0 {
		fmt.Println("✓ .env matches .env.example")
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m *Manager) venvPython() string {
	p := filepath.Join(m.ProjectDir, ".venv", "bin", "python3")
	if runtime.GOOS == "windows" {
		p = filepath.Join(m.ProjectDir, ".venv", "Scripts", "python.exe")
	}
	return p
}

// installedPackages uses `uv pip list --format=json` instead of pip.
func (m *Manager) installedPackages() map[string]string {
	uv, err := findUV()
	if err != nil {
		return nil
	}
	// uv pip list operates on the active venv; point it at ours explicitly.
	cmd := exec.Command(uv, "pip", "list", "--format=json")
	cmd.Dir = m.ProjectDir
	cmd.Env = uvEnvWithVenv(m.ProjectDir)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var pkgs []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	json.Unmarshal(out, &pkgs)
	result := map[string]string{}
	for _, p := range pkgs {
		result[strings.ToLower(p.Name)] = p.Version
	}
	return result
}

func (m *Manager) hashPackageMeta() []types.HashEntry {
	sitePackages := m.sitePackagesDir()
	var entries []types.HashEntry
	if sitePackages == "" {
		return entries
	}
	filepath.WalkDir(sitePackages, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "METADATA" {
			return nil
		}
		rel, _ := filepath.Rel(sitePackages, path)
		h, _ := hashFilePath(path)
		entries = append(entries, types.HashEntry{
			Path:   filepath.ToSlash(rel),
			SHA256: h,
		})
		return nil
	})
	return entries
}

func (m *Manager) sitePackagesDir() string {
	base := filepath.Join(m.ProjectDir, ".venv", "lib")
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

func parseLockPackages(data []byte) map[string]string {
	result := map[string]string{}
	var name string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			name = ""
			continue
		}
		if strings.HasPrefix(line, "name = ") {
			name = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
			continue
		}
		if strings.HasPrefix(line, "version = ") && name != "" {
			ver := strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
			result[strings.ToLower(name)] = ver
		}
	}
	return result
}

func extractPackagesFromSnapshot(snap *types.EnvSnapshot) []string {
	var pkgs []string
	for _, f := range snap.Packages {
		parts := strings.Split(f.Path, "/")
		if len(parts) > 0 && strings.HasSuffix(parts[0], ".dist-info") {
			nameVer := strings.TrimSuffix(parts[0], ".dist-info")
			dashIdx := strings.LastIndex(nameVer, "-")
			if dashIdx > 0 {
				pkgs = append(pkgs, nameVer[:dashIdx]+"=="+nameVer[dashIdx+1:])
			}
		}
	}
	seen := map[string]bool{}
	var unique []string
	for _, p := range pkgs {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}
	return unique
}

func parseEnvKeys(data []byte) map[string]bool {
	keys := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) >= 1 {
			keys[parts[0]] = true
		}
	}
	return keys
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

// findUV locates the uv binary.
func findUV() (string, error) {
	if p, err := exec.LookPath("uv"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".cargo", "bin", "uv"),
		"/usr/local/bin/uv",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("uv not found — install from https://github.com/astral-sh/uv")
}

// uvEnvWithVenv returns an environment slice that tells uv which venv to use.
func uvEnvWithVenv(projectDir string) []string {
	venvPath := filepath.Join(projectDir, ".venv")
	env := os.Environ()
	// Replace or add VIRTUAL_ENV so uv targets our venv.
	filtered := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "VIRTUAL_ENV=") {
			filtered = append(filtered, e)
		}
	}
	return append(filtered, "VIRTUAL_ENV="+venvPath)
}