// internal/uv/uv.go
// Package uv is a thin command wrapper around uv.
// All binary resolution goes through [uvbin.Find] / [uvbin.Ensure] so that
// molt always uses its own pinned uv copy rather than whatever happens to be
// on the host PATH.
package uv

import (
	"fmt"
	"os"
	"os/exec"

	"molt/internal/uvbin"
)

var ErrNotFound = fmt.Errorf("uv not found — run 'molt uv install'")

// InitOptions controls `uv init` behaviour.
type InitOptions struct {
	Python   string
	Lib      bool
	NoReadme bool
	NoPin    bool
}

// Available reports whether a uv binary can be found (does not download).
func Available() bool {
	_, err := uvbin.Find()
	return err == nil
}

// Version returns the version string of the resolved uv binary.
func Version() (string, error) {
	return uvbin.Version()
}

// run executes uv with the given arguments in dir, using the resolved binary.
// It calls uvbin.Ensure so the managed copy is downloaded on first use.
func run(dir string, args ...string) error {
	uv, err := uvbin.Ensure()
	if err != nil {
		return err
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Init(dir, name string, opts InitOptions) error {
	args := []string{"init"}
	if opts.Python != "" {
		args = append(args, "--python", opts.Python)
	}
	if opts.Lib {
		args = append(args, "--lib")
	}
	if name != "" {
		args = append(args, name)
	}
	return run(dir, args...)
}

func Add(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("add: at least one package required")
	}
	args := []string{"add"}
	if dev {
		args = append(args, "--dev")
	}
	return run(dir, append(args, packages...)...)
}

func Remove(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("remove: at least one package required")
	}
	args := []string{"remove"}
	if dev {
		args = append(args, "--dev")
	}
	return run(dir, append(args, packages...)...)
}

func Lock(dir string) error { return run(dir, "lock") }

func Sync(dir string, frozen bool) error {
	args := []string{"sync"}
	if frozen {
		args = append(args, "--frozen")
	}
	return run(dir, args...)
}

func Tree(dir string) error { return run(dir, "tree") }

func LockIfStale(dir string) (bool, error) {
	return true, Lock(dir)
}

func Raw(dir string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no args")
	}
	return run(dir, args...)
}
