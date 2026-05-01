// Package uvbin is a MINIMAL STUB of the full uvbin package in the main
// molt codebase. It exists so internal/deps_strategy compiles in this
// slice of work.
//
// MERGE NOTE: if you are merging these changes into a molt tree that
// already has pkg/internal/uvbin (full-fat: auto-downloads pinned uv,
// Status/Remove/Install, etc.), DELETE THIS FILE. The full package is
// strictly richer — this stub implements only Ensure() with a system-PATH
// fallback and carries no state.
//
// Public API preserved (from the full package):
//   - Ensure() (string, error) — returns path to a uv binary
//   - PinnedVersion             — target version, used by launcher
//   - EnvOverride               — env var that forces a specific uv path
package uvbin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// PinnedVersion is the uv release the full package would self-install.
// The stub never installs anything — it just searches PATH — but the
// constant is referenced elsewhere in the codebase.
const PinnedVersion = "0.4.18"

// EnvOverride is the only env var that can redirect uv resolution.
const EnvOverride = "MOLT_UV"

// Ensure returns a usable uv binary path or an error.
// Resolution order:
//   1. $MOLT_UV if set and file exists
//   2. ~/.molt/uv/bin/uv (location the full package installs into)
//   3. `uv` on $PATH
func Ensure() (string, error) {
	if override := os.Getenv(EnvOverride); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("%s=%q: not found", EnvOverride, override)
	}
	// Managed location (what the full package writes to).
	home, _ := os.UserHomeDir()
	managed := filepath.Join(home, ".molt", "uv", "bin", "uv")
	if runtime.GOOS == "windows" {
		managed += ".exe"
	}
	if _, err := os.Stat(managed); err == nil {
		return managed, nil
	}
	// System PATH fallback.
	if p, err := exec.LookPath("uv"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("uv not found — install from https://github.com/astral-sh/uv or set %s", EnvOverride)
}

// Find is the same as Ensure but never attempts installation. In this
// stub both are equivalent; the full package separates them.
func Find() (string, error) { return Ensure() }

// Version returns the uv binary's version string, or error if uv is absent.
func Version() (string, error) {
	p, err := Ensure()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(p, "--version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
