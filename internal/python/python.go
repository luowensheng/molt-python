// Package python manages Python version installation, selection, and auditing.
package python

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"molt/pkg/types"
)

const standaloneBase = "https://github.com/indygreg/python-build-standalone/releases/download"

// Manager handles Python version CRUD.
type Manager struct {
	installBase string // e.g. ~/.molt/python/
	projectDir  string
}

// New creates a Manager.
func New(projectDir string) (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Manager{
		installBase: filepath.Join(home, ".molt", "python"),
		projectDir:  projectDir,
	}, nil
}

// List returns all Python versions: installed standalone + system Pythons.
func (m *Manager) List() ([]types.PythonVersion, error) {
	var versions []types.PythonVersion

	// Installed standalone versions.
	entries, _ := os.ReadDir(m.installBase)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		binPath := filepath.Join(m.installBase, e.Name(), "bin", pythonBin())
		if runtime.GOOS == "windows" {
			binPath = filepath.Join(m.installBase, e.Name(), "python.exe")
		}
		versions = append(versions, types.PythonVersion{
			Version:   e.Name(),
			Installed: true,
			Path:      binPath,
			Source:    "standalone",
		})
	}

	// System Pythons.
	for _, candidate := range systemPythonCandidates() {
		v, err := getPythonVersion(candidate)
		if err != nil {
			continue
		}
		already := false
		for _, existing := range versions {
			if existing.Version == v {
				already = true
				break
			}
		}
		if !already {
			versions = append(versions, types.PythonVersion{
				Version:   v,
				Installed: true,
				Path:      candidate,
				Source:    "system",
			})
		}
	}

	// Mark active.
	active := m.Active()
	for i := range versions {
		if versions[i].Version == active {
			versions[i].Active = true
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Version > versions[j].Version
	})
	return versions, nil
}

// Install downloads and installs a Python standalone build.
func (m *Manager) Install(version string) error {
	destDir := filepath.Join(m.installBase, version)
	if _, err := os.Stat(destDir); err == nil {
		fmt.Printf("Python %s already installed at %s\n", version, destDir)
		return nil
	}

	url, err := standaloneURL(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	fmt.Printf("Downloading Python %s...\n", version)
	fmt.Printf("  URL: %s\n", url)

	tmp, err := os.CreateTemp("", "python-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.Close()

	// Use curl/wget.
	if err := downloadFile(url, tmp.Name()); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	fmt.Printf("  Extracting to %s...\n", destDir)
	if err := extractTarGz(tmp.Name(), destDir); err != nil {
		os.RemoveAll(destDir)
		return fmt.Errorf("extract: %w", err)
	}

	fmt.Printf("  ✓ Python %s installed\n", version)
	return nil
}

// Use sets the Python version for the current project.
// Writes to .python-version and updates pyproject.toml requires-python.
func (m *Manager) Use(version string, global bool) error {
	if global {
		home, _ := os.UserHomeDir()
		versionFile := filepath.Join(home, ".molt", ".python-version")
		if err := os.MkdirAll(filepath.Dir(versionFile), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Printf("Global Python set to %s\n", version)
		return nil
	}

	// Project-level: write .python-version.
	versionFile := filepath.Join(m.projectDir, ".python-version")
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
		return err
	}

	// Update pyproject.toml requires-python.
	if err := updateRequiresPython(m.projectDir, version); err != nil {
		fmt.Printf("  ⚠ Could not update pyproject.toml: %v\n", err)
	}

	fmt.Printf("Project Python set to %s\n", version)
	fmt.Println("Run 'molt env reset' to recreate the venv with the new version.")
	return nil
}

// Remove uninstalls a standalone Python version.
func (m *Manager) Remove(version string) error {
	destDir := filepath.Join(m.installBase, version)
	if _, err := os.Stat(destDir); os.IsNotExist(err) {
		return fmt.Errorf("Python %s is not installed as a standalone version", version)
	}
	if err := os.RemoveAll(destDir); err != nil {
		return err
	}
	fmt.Printf("✓ Python %s removed\n", version)
	return nil
}

// Which returns the full path to the active Python binary.
func (m *Manager) Which() (string, error) {
	active := m.Active()
	if active == "" {
		return exec.LookPath(pythonBin())
	}
	standalone := filepath.Join(m.installBase, active, "bin", pythonBin())
	if _, err := os.Stat(standalone); err == nil {
		return standalone, nil
	}
	return exec.LookPath(pythonBin())
}

// Active returns the active Python version for the project (reads .python-version).
func (m *Manager) Active() string {
	// Project-level.
	if data, err := os.ReadFile(filepath.Join(m.projectDir, ".python-version")); err == nil {
		return strings.TrimSpace(string(data))
	}
	// Global.
	home, _ := os.UserHomeDir()
	if data, err := os.ReadFile(filepath.Join(home, ".molt", ".python-version")); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

// Audit finds every Python on the machine and reports interference risks.
func (m *Manager) Audit() error {
	fmt.Println("Python installations found on this machine:")
	fmt.Println()

	candidates := append(systemPythonCandidates(), m.standaloneInstalled()...)
	seen := map[string]bool{}

	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		v, err := getPythonVersion(p)
		if err != nil {
			continue
		}
		fmt.Printf("  %s\n", p)
		fmt.Printf("    version:     %s\n", v)

		// Check PYTHONPATH pollution.
		if pp := os.Getenv("PYTHONPATH"); pp != "" {
			fmt.Printf("    ⚠ PYTHONPATH is set: %s\n", pp)
		}

		// Get site-packages.
		out, err := exec.Command(p, "-c",
			"import site; print('\\n'.join(site.getsitepackages()))").Output()
		if err == nil {
			fmt.Printf("    site-packages:\n")
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line != "" {
					fmt.Printf("      %s\n", line)
				}
			}
		}
		fmt.Println()
	}
	return nil
}

// IsolationCheck verifies the venv is genuinely isolated.
func (m *Manager) IsolationCheck() error {
	venvPython := filepath.Join(m.projectDir, ".venv", "bin", pythonBin())
	if runtime.GOOS == "windows" {
		venvPython = filepath.Join(m.projectDir, ".venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(venvPython); os.IsNotExist(err) {
		return fmt.Errorf("no venv found at .venv/ — run 'molt sync' first")
	}

	fmt.Println("Checking venv isolation...")

	// Check PYTHONPATH is not set.
	if pp := os.Getenv("PYTHONPATH"); pp != "" {
		fmt.Printf("  ⚠ PYTHONPATH=%s — this leaks packages into the venv\n", pp)
	} else {
		fmt.Println("  ✓ PYTHONPATH not set")
	}

	// Check sys.path for unexpected entries.
	out, err := exec.Command(venvPython, "-c",
		`import sys, json; print(json.dumps(sys.path))`).Output()
	if err != nil {
		return fmt.Errorf("check sys.path: %w", err)
	}
	var syspath []string
	json.Unmarshal(out, &syspath)

	venvAbs, _ := filepath.Abs(filepath.Join(m.projectDir, ".venv"))
	unexpected := 0
	for _, p := range syspath {
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, venvAbs) {
			continue
		}
		// stdlib is OK.
		if strings.Contains(p, "python3") && strings.Contains(p, "lib") {
			continue
		}
		fmt.Printf("  ⚠ Unexpected sys.path entry: %s\n", p)
		unexpected++
	}
	if unexpected == 0 {
		fmt.Println("  ✓ sys.path is clean — no unexpected entries")
	}

	return nil
}

// ConflictsCheck detects when multiple Pythons interfere.
func (m *Manager) ConflictsCheck() error {
	fmt.Println("Checking for Python environment conflicts...")

	issues := 0

	if pp := os.Getenv("PYTHONPATH"); pp != "" {
		fmt.Printf("  ⚠ PYTHONPATH=%s\n", pp)
		issues++
	}
	if ph := os.Getenv("PYTHONHOME"); ph != "" {
		fmt.Printf("  ⚠ PYTHONHOME=%s\n", ph)
		issues++
	}

	// Check for sitecustomize.py files.
	for _, siteDir := range []string{"/usr/lib/python3", "/usr/local/lib/python3"} {
		entries, _ := filepath.Glob(siteDir + "*/sitecustomize.py")
		for _, e := range entries {
			fmt.Printf("  ⚠ sitecustomize.py found: %s (executes at Python startup)\n", e)
			issues++
		}
	}

	if issues == 0 {
		fmt.Println("  ✓ No conflicts detected")
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func pythonBin() string {
	if runtime.GOOS == "windows" {
		return "python.exe"
	}
	return "python3"
}

func getPythonVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "Python ")
	return v, nil
}

func systemPythonCandidates() []string {
	names := []string{"python3", "python3.12", "python3.11", "python3.10", "python3.9", "python3.8"}
	var found []string
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			found = append(found, p)
		}
	}
	return found
}

func (m *Manager) standaloneInstalled() []string {
	entries, _ := os.ReadDir(m.installBase)
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		binPath := filepath.Join(m.installBase, e.Name(), "bin", pythonBin())
		out = append(out, binPath)
	}
	return out
}

func standaloneURL(version, goos, goarch string) (string, error) {
	archMap := map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
	}
	pbsArch, ok := archMap[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported arch: %s", goarch)
	}
	var platform string
	switch goos {
	case "linux":
		platform = pbsArch + "-unknown-linux-gnu"
	case "darwin":
		platform = pbsArch + "-apple-darwin"
	case "windows":
		platform = pbsArch + "-pc-windows-msvc"
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}
	const tag = "20240107"
	filename := fmt.Sprintf("cpython-%s+%s-%s-install_only.tar.gz", version, tag, platform)
	return fmt.Sprintf("%s/%s/%s", standaloneBase, tag, filename), nil
}

func downloadFile(url, dest string) error {
	for _, tool := range []string{"curl", "wget"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		var args []string
		if tool == "curl" {
			args = []string{"-L", "-o", dest, url}
		} else {
			args = []string{"-O", dest, url}
		}
		cmd := exec.Command(tool, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return fmt.Errorf("curl or wget required to download Python")
}

func extractTarGz(src, dst string) error {
	cmd := exec.Command("tar", "xzf", src, "-C", dst, "--strip-components=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateRequiresPython(projectDir, version string) error {
	tomlPath := filepath.Join(projectDir, "pyproject.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}
	// Simple replacement — full TOML parser would be better but avoids extra deps.
	content := string(data)
	major := version
	if idx := strings.LastIndex(version, "."); idx > 0 {
		major = version[:idx] // strip patch: 3.12.1 → 3.12
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "requires-python") {
			lines[i] = fmt.Sprintf(`requires-python = ">=%s"`, major)
		}
	}
	return os.WriteFile(tomlPath, []byte(strings.Join(lines, "\n")), 0o644)
}
