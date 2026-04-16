// internal/python/python.go
package python

import (
	"bufio"
	"encoding/json"
	"fmt"
	"molt/internal/uvbin"
	"molt/pkg/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Manager struct {
	projectDir string
}

func New(projectDir string) (*Manager, error) {
	return &Manager{projectDir: projectDir}, nil
}

// runUv executes uv with the given args and returns combined output.
func (m *Manager) runUv(args ...string) (string, error) {
	uv, err := uvbin.Ensure()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(uv, append([]string{"python"}, args...)...)
	cmd.Dir = m.projectDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// List returns all Python versions: installed standalone + system Pythons.
func (m *Manager) List() ([]types.PythonVersion, error) {
	var versions []types.PythonVersion

	// Get uv python list output (table format).
	out, err := m.runUv("list")
	if err != nil {
		// Fallback: scan system for pythons if uv list fails.
		return m.listSystemPythons(), nil
	}

	// Parse table output:
	// cpython-3.12.3-macos-aarch64-none     ~/.molt/python/3.12.3/bin/python3
	// or:
	// cpython-3.12.3-macos-aarch64-none (/Users/.../python3.12)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Found") {
			continue
		}
		// Extract version from cpython-X.Y.Z-... or pypy-X.Y.Z-...
		var ver, path, source string
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		// First field is the implementation-version-platform string.
		implVer := parts[0]
		// Extract version: cpython-3.12.3 -> 3.12.3
		if idx := strings.Index(implVer, "-"); idx > 0 {
			rest := implVer[idx+1:]
			if nextIdx := strings.Index(rest, "-"); nextIdx > 0 {
				ver = rest[:nextIdx]
			}
		}
		if ver == "" {
			continue
		}
		// Second field is the path, possibly with parentheses.
		path = strings.Trim(parts[1], "()")
		// Determine source.
		home, _ := os.UserHomeDir()
		standaloneBase := filepath.Join(home, ".molt", "python")
		if strings.HasPrefix(path, standaloneBase) {
			source = "standalone"
		} else {
			source = "system"
		}
		versions = append(versions, types.PythonVersion{
			Version:   ver,
			Installed: true,
			Path:      path,
			Source:    source,
		})
	}

	// Also scan for system pythons not managed by uv.
	for _, sysPy := range m.listSystemPythons() {
		found := false
		for _, v := range versions {
			if v.Version == sysPy.Version {
				found = true
				break
			}
		}
		if !found {
			versions = append(versions, sysPy)
		}
	}

	// Mark active.
	active := m.Active()
	for i := range versions {
		if versions[i].Version == active {
			versions[i].Active = true
		}
	}

	// Sort by version descending.
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

// listSystemPythons scans PATH for system Python installations.
func (m *Manager) listSystemPythons() []types.PythonVersion {
	var versions []types.PythonVersion
	seen := map[string]bool{}
	for _, name := range []string{"python3.13", "python3.12", "python3.11", "python3.10", "python3.9", "python3.8", "python3"} {
		if p, err := exec.LookPath(name); err == nil {
			if seen[p] {
				continue
			}
			seen[p] = true
			out, err := exec.Command(p, "--version").CombinedOutput()
			if err != nil {
				continue
			}
			v := strings.TrimSpace(string(out))
			v = strings.TrimPrefix(v, "Python ")
			versions = append(versions, types.PythonVersion{
				Version:   v,
				Installed: true,
				Path:      p,
				Source:    "system",
			})
		}
	}
	return versions
}

// Install downloads and installs a Python standalone build via uv.
func (m *Manager) Install(version string) error {
	// Use uv python install <version>
	out, err := m.runUv("install", version)
	if err != nil {
		return fmt.Errorf("uv python install %s: %w\n%s", version, err, out)
	}
	fmt.Printf("✓ Python %s installed\n", version)
	return nil
}

// Use sets the Python version for the current project.
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
	// Project-level: use uv python pin
	out, err := m.runUv("pin", version)
	if err != nil {
		return fmt.Errorf("uv python pin %s: %w\n%s", version, err, out)
	}
	// Also write .python-version for compatibility
	versionFile := filepath.Join(m.projectDir, ".python-version")
	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("Project Python set to %s\n", version)
	return nil
}

// Remove uninstalls a standalone Python version via uv.
func (m *Manager) Remove(version string) error {
	out, err := m.runUv("remove", version)
	if err != nil {
		return fmt.Errorf("uv python remove %s: %w\n%s", version, err, out)
	}
	fmt.Printf("✓ Python %s removed\n", version)
	return nil
}

// Which returns the full path to the active Python binary via uv.
func (m *Manager) Which() (string, error) {
	out, err := m.runUv("find")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Active returns the active Python version for the project.
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

// Audit finds every Python on the machine.
func (m *Manager) Audit() error {
	fmt.Println("Python installations found on this machine:")
	fmt.Println()
	versions, err := m.List()
	if err != nil {
		return err
	}
	for _, v := range versions {
		fmt.Printf("  %s\n", v.Path)
		fmt.Printf("    version:  %s\n", v.Version)
		fmt.Printf("    source:   %s\n", v.Source)
		if v.Active {
			fmt.Println("    ← active")
		}
		fmt.Println()
	}
	return nil
}

// IsolationCheck verifies the venv is genuinely isolated.
func (m *Manager) IsolationCheck() error {
	venvDir := filepath.Join(m.projectDir, ".venv")
	if _, err := os.Stat(venvDir); os.IsNotExist(err) {
		return fmt.Errorf("no venv found at .venv/ — run 'molt sync' first")
	}
	fmt.Println("Checking venv isolation...")
	if pp := os.Getenv("PYTHONPATH"); pp != "" {
		fmt.Printf("  ⚠ PYTHONPATH=%s — this leaks packages into the venv\n", pp)
	} else {
		fmt.Println("  ✓ PYTHONPATH not set")
	}
	uv, _ := uvbin.Ensure()
	cmd := exec.Command(uv, "run", "python", "-c", `import sys, json; print(json.dumps(sys.path))`)
	cmd.Dir = m.projectDir
	cmd.Env = append(os.Environ(), "VIRTUAL_ENV="+venvDir)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check sys.path: %w", err)
	}
	var syspath []string
	json.Unmarshal(out, &syspath)
	venvAbs, _ := filepath.Abs(venvDir)
	unexpected := 0
	for _, p := range syspath {
		if p == "" || strings.HasPrefix(p, venvAbs) {
			continue
		}
		if strings.Contains(p, "python") && strings.Contains(p, "lib") {
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
	if issues == 0 {
		fmt.Println("  ✓ No conflicts detected")
	}
	return nil
}

// compareVersions returns 1 if a > b, -1 if a < b, 0 if equal.
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
