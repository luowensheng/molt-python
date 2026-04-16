// internal/python/python.go
package python

import (
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

func (m *Manager) runUv(args ...string) error {
	uv, err := uvbin.Ensure()
	if err != nil {
		return err
	}
	cmd := exec.Command(uv, append([]string{"python"}, args...)...)
	cmd.Dir = m.projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *Manager) runUvOutput(args ...string) (string, error) {
	uv, err := uvbin.Ensure()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(uv, append([]string{"python"}, args...)...)
	cmd.Dir = m.projectDir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (m *Manager) List() ([]types.PythonVersion, error) {
	out, err := m.runUvOutput("list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var versions []types.PythonVersion
	if err := json.Unmarshal([]byte(out), &versions); err != nil {
		return nil, err
	}
	active := m.Active()
	for i := range versions {
		versions[i].Active = versions[i].Version == active
		versions[i].Installed = true
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Version > versions[j].Version
	})
	return versions, nil
}

func (m *Manager) Install(version string) error {
	return m.runUv("install", version)
}

func (m *Manager) Use(version string, global bool) error {
	if global {
		home, _ := os.UserHomeDir()
		if err := os.MkdirAll(filepath.Join(home, ".molt"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(home, ".molt", ".python-version"), []byte(version+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Printf("Global Python set to %s\n", version)
		return nil
	}
	if err := os.WriteFile(filepath.Join(m.projectDir, ".python-version"), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	// uv handles pyproject.toml requires-python updates automatically when pinning.
	if err := m.runUv("pin", version); err != nil {
		fmt.Printf("  ⚠ Could not update uv python pin: %v\n", err)
	}
	fmt.Printf("Project Python set to %s\n", version)
	return nil
}

func (m *Manager) Remove(version string) error {
	return m.runUv("remove", version)
}

func (m *Manager) Which() (string, error) {
	return m.runUvOutput("find")
}

func (m *Manager) Active() string {
	if data, err := os.ReadFile(filepath.Join(m.projectDir, ".python-version")); err == nil {
		return strings.TrimSpace(string(data))
	}
	home, _ := os.UserHomeDir()
	if data, err := os.ReadFile(filepath.Join(home, ".molt", ".python-version")); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func (m *Manager) Audit() error {
	fmt.Println("Python installations managed by uv:")
	out, err := m.runUvOutput("list")
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func (m *Manager) IsolationCheck() error {
	venvDir := filepath.Join(m.projectDir, ".venv")
	if _, err := os.Stat(venvDir); os.IsNotExist(err) {
		return fmt.Errorf("no venv found at .venv/ — run 'molt env reset' first")
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
		fmt.Printf("  ⚠ Unexpected sys.path entry: %s\n", p)
		unexpected++
	}
	if unexpected == 0 {
		fmt.Println("  ✓ sys.path is clean — no unexpected entries")
	}
	return nil
}

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
