// internal/uv/uv.go
// Package uv is a thin command wrapper around uv.
// All binary resolution goes through [uvbin.Find] / [uvbin.Ensure] so that
// molt always uses its own pinned uv copy rather than whatever happens to be
// on the host PATH.
package uv

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"molt/internal/syncplan"
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
	// Filter out implementation-detail noise: messages about uv's project
	// env (which we redirected into .molt/) are not useful to molt users —
	// they manage the global store, not a venv. The lock-resolution and
	// install summaries from uv stay visible.
	cmd.Stdout = newUVStreamFilter(os.Stdout)
	cmd.Stderr = newUVStreamFilter(os.Stderr)
	cmd.Env = projectEnv(dir)
	return cmd.Run()
}

// uvStreamFilter is an io.Writer that drops lines describing uv's internal
// project environment lifecycle. Everything else passes through unchanged.
type uvStreamFilter struct {
	dst io.Writer
	buf []byte
}

func newUVStreamFilter(dst io.Writer) *uvStreamFilter {
	return &uvStreamFilter{dst: dst}
}

func (f *uvStreamFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := -1
		for j, b := range f.buf {
			if b == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		line := f.buf[:i+1]
		f.buf = f.buf[i+1:]
		if !shouldSuppress(line) {
			if _, err := f.dst.Write(line); err != nil {
				return len(p), err
			}
		}
	}
	return len(p), nil
}

func shouldSuppress(line []byte) bool {
	trimmed := strings.TrimSpace(string(line))
	return strings.HasPrefix(trimmed, "Creating virtual environment at:")
}

// projectEnv builds the env passed to uv. It redirects UV_PROJECT_ENVIRONMENT
// into the project's hidden .molt/ directory so commands like `uv add` don't
// litter the user's project tree with a top-level .venv/. Materialisation of
// dependencies into the global content-addressed store happens separately
// via syncplan.Sync, so this hidden env is mostly empty — it exists only
// because newer uv versions insist on having one.
func projectEnv(dir string) []string {
	env := os.Environ()
	if dir == "" {
		return env
	}
	if _, set := lookupEnv(env, "UV_PROJECT_ENVIRONMENT"); set {
		return env // user override wins
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return env
	}
	target := filepath.Join(abs, ".molt", "uv-env")
	return append(env, "UV_PROJECT_ENVIRONMENT="+target)
}

func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if len(e) > len(prefix) && e[:len(prefix)] == prefix {
			return e[len(prefix):], true
		}
	}
	return "", false
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
	// --no-sync prevents uv from materialising a .venv/. Materialisation
	// into the global content-addressed store happens via syncplan.Sync
	// after this returns.
	args := []string{"add", "--no-sync"}
	if dev {
		args = append(args, "--dev")
	}
	return run(dir, append(args, packages...)...)
}

func Remove(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("remove: at least one package required")
	}
	args := []string{"remove", "--no-sync"}
	if dev {
		args = append(args, "--dev")
	}
	return run(dir, append(args, packages...)...)
}

func Lock(dir string) error { return run(dir, "lock") }

// Sync populates the global content-addressed package store and writes the
// project's .molt/syspath.json. Replaces the legacy `uv sync` path that
// materialised a per-project .venv.
func Sync(dir string, frozen bool) error {
	return syncplan.Sync(dir, syncplan.Options{Frozen: frozen, Verbose: true})
}

func Tree(dir string) error { return run(dir, "tree") }

// LockIfStale regenerates uv.lock only when pyproject.toml is newer than
// uv.lock (or uv.lock does not exist). Returns true if a lock was run.
func LockIfStale(dir string) (bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	pyproj, err := os.Stat(filepath.Join(abs, "pyproject.toml"))
	if err != nil {
		return false, nil // no pyproject.toml — nothing to do
	}
	lock, err := os.Stat(filepath.Join(abs, "uv.lock"))
	if err == nil && !pyproj.ModTime().After(lock.ModTime()) {
		return false, nil // lock is fresh
	}
	return true, Lock(dir)
}

func Raw(dir string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no args")
	}
	return run(dir, args...)
}
