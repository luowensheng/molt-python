// Package uv provides a Go interface to the uv Python package manager.
// Every public function delegates to the uv binary found on PATH, streaming
// stdout/stderr directly so the user sees live output.
package uv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned when uv is not installed.
var ErrNotFound = fmt.Errorf("uv not found — install it with: curl -LsSf https://astral.sh/uv/install.sh | sh")

// Find returns the path to the uv binary or ErrNotFound.
func Find() (string, error) {
	candidates := []string{
		"uv",                          // rely on PATH
		filepath.Join(os.Getenv("HOME"), ".cargo", "bin", "uv"),
		"/usr/local/bin/uv",
		"/usr/bin/uv",
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", ErrNotFound
}

// Available reports whether uv is on PATH.
func Available() bool {
	_, err := Find()
	return err == nil
}

// Version returns the installed uv version string.
func Version() (string, error) {
	uv, err := Find()
	if err != nil {
		return "", err
	}
    out, err := exec.Command(uv, "self", "version").Output()
	if err != nil {
		return "", fmt.Errorf("uv version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// run executes uv with the given arguments in dir, streaming output.
func run(dir string, args ...string) error {
	uv, err := Find()
	if err != nil {
		return err
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// output executes uv and returns its combined output (quiet; no streaming).
func output(dir string, args ...string) (string, error) {
	uv, err := Find()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ── Project lifecycle ────────────────────────────────────────────────────────

// InitOptions controls uv init behaviour.
type InitOptions struct {
	// Python version constraint, e.g. "3.12". Empty means uv's default.
	Python string
	// Lib creates a library project instead of an app (adds src/ layout).
	Lib bool
	// NoReadme skips generating README.md.
	NoReadme bool
	// NoPin skips generating .python-version.
	NoPin bool
}

// Init runs `uv init` in dir with the given project name.
// Equivalent to: uv init [--python X] [--lib] <name>
func Init(dir, name string, opts InitOptions) error {
	args := []string{"init"}
	if opts.Python != "" {
		args = append(args, "--python", opts.Python)
	}
	if opts.Lib {
		args = append(args, "--lib")
	}
	if opts.NoReadme {
		args = append(args, "--no-readme")
	}
	if opts.NoPin {
		args = append(args, "--no-pin-python")
	}
	if name != "" {
		args = append(args, name)
	}
	return run(dir, args...)
}

// ── Dependency management ────────────────────────────────────────────────────

// Add runs `uv add <packages...>` in dir.
// Automatically updates pyproject.toml and regenerates uv.lock.
func Add(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("add: at least one package required")
	}
	args := []string{"add"}
	if dev {
		args = append(args, "--dev")
	}
	args = append(args, packages...)
	return run(dir, args...)
}

// Remove runs `uv remove <packages...>` in dir.
func Remove(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("remove: at least one package required")
	}
	args := []string{"remove"}
	if dev {
		args = append(args, "--dev")
	}
	args = append(args, packages...)
	return run(dir, args...)
}

// Lock runs `uv lock` in dir, regenerating uv.lock from pyproject.toml.
func Lock(dir string) error {
	return run(dir, "lock")
}

// LockIfStale runs `uv lock` only when uv.lock is absent or older than
// pyproject.toml. Returns (true, nil) if a lock was performed.
func LockIfStale(dir string) (ran bool, err error) {
	lockPath := filepath.Join(dir, "uv.lock")
	tomlPath := filepath.Join(dir, "pyproject.toml")

	lockInfo, lockErr := os.Stat(lockPath)
	tomlInfo, tomlErr := os.Stat(tomlPath)

	stale := lockErr != nil || // lock missing
		tomlErr == nil && tomlInfo.ModTime().After(lockInfo.ModTime()) // toml newer

	if !stale {
		return false, nil
	}
	return true, Lock(dir)
}

// Sync runs `uv sync` in dir, installing packages from uv.lock into the venv.
func Sync(dir string, frozen bool) error {
	args := []string{"sync"}
	if frozen {
		args = append(args, "--frozen")
	}
	return run(dir, args...)
}

// ── Python version management ────────────────────────────────────────────────

// PythonList returns the list of available Python versions known to uv.
func PythonList(dir string) (string, error) {
	return output(dir, "python", "list")
}

// PythonInstall installs a specific Python version via uv.
func PythonInstall(dir, version string) error {
	return run(dir, "python", "install", version)
}

// PythonPin writes a .python-version file pinning the project to version.
func PythonPin(dir, version string) error {
	return run(dir, "python", "pin", version)
}

// ── Venv management ──────────────────────────────────────────────────────────

// Venv creates a virtual environment in dir/.venv using the given python.
// python may be empty (uv picks based on pyproject.toml).
func Venv(dir, python string) error {
	args := []string{"venv"}
	if python != "" {
		args = append(args, "--python", python)
	}
	return run(dir, args...)
}

// ── Information ──────────────────────────────────────────────────────────────

// Tree runs `uv tree` showing the dependency tree for the project.
func Tree(dir string) error {
	return run(dir, "tree")
}

// Show runs `uv pip show <package>` returning package metadata.
func Show(dir, pkg string) (string, error) {
	return output(dir, "pip", "show", pkg)
}

// List runs `uv pip list` and returns its output.
func List(dir string) (string, error) {
	return output(dir, "pip", "list")
}

// ── Raw passthrough ──────────────────────────────────────────────────────────

// Raw runs uv with exactly the given args in dir, streaming output.
// This is the escape hatch for any uv subcommand not explicitly wrapped above.
func Raw(dir string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no arguments given to uv")
	}
	return run(dir, args...)
}
