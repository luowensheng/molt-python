// Package runtimecfg reads [tool.molt.runtime] from pyproject.toml and
// applies its `extra_paths` to the environment when molt spawns a
// subprocess (Python, uv, tasks, etc.).
//
// Schema:
//
//   [tool.molt.runtime]
//   extra_paths = ["vendor/lib", "vendor/bin", "vendor/Frameworks"]
//
// Each path (resolved relative to the project root) is prepended to:
//
//   PATH                            — exec lookup; on Windows also DLLs
//   LD_LIBRARY_PATH                 — Linux dynamic-linker
//   DYLD_FALLBACK_LIBRARY_PATH      — macOS dynamic-linker (SIP-safe)
//   DYLD_FALLBACK_FRAMEWORK_PATH    — macOS framework lookup
//
// One field, one behaviour: drop a directory in extra_paths and
// whatever it contains becomes findable, without the user having to
// know which env var the runtime uses.
package runtimecfg

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config is the parsed [tool.molt.runtime] section.
type Config struct {
	// ExtraPaths is the list of directories to prepend to runtime
	// path env vars. Project-relative paths are resolved against the
	// project dir at load time so callers receive absolute paths.
	ExtraPaths []string
}

// Load parses [tool.molt.runtime] from projectDir/pyproject.toml.
// Missing file or section returns an empty Config (no-op).
func Load(projectDir string) Config {
	cfg := Config{}
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return cfg
	}
	inSection := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = (line == "[tool.molt.runtime]")
			continue
		}
		if !inSection || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key == "extra_paths" {
			if arr := parseTOMLStringArray(val); arr != nil {
				cfg.ExtraPaths = arr
			}
		}
	}
	// Resolve relative paths against the project dir.
	for i, p := range cfg.ExtraPaths {
		if !filepath.IsAbs(p) {
			cfg.ExtraPaths[i] = filepath.Join(projectDir, filepath.FromSlash(p))
		}
	}
	return cfg
}

// Apply prepends cfg.ExtraPaths to the runtime env vars on `parent`,
// returning the new env slice. Variables that don't apply to the
// current OS pass through unchanged.
//
//   PATH                            — always
//   LD_LIBRARY_PATH                 — Linux
//   DYLD_FALLBACK_LIBRARY_PATH      — macOS
//   DYLD_FALLBACK_FRAMEWORK_PATH    — macOS
func (c Config) Apply(parent []string) []string {
	if len(c.ExtraPaths) == 0 {
		return parent
	}
	want := []string{"PATH"}
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		want = append(want, "LD_LIBRARY_PATH")
	case "darwin":
		want = append(want, "DYLD_FALLBACK_LIBRARY_PATH", "DYLD_FALLBACK_FRAMEWORK_PATH")
	}

	dirsJoined := strings.Join(c.ExtraPaths, string(os.PathListSeparator))
	updated := map[string]bool{}

	out := make([]string, 0, len(parent)+len(want))
	for _, kv := range parent {
		k := envKey(kv)
		if contains(want, k) {
			existing := strings.TrimPrefix(kv, k+"=")
			out = append(out, k+"="+dirsJoined+string(os.PathListSeparator)+existing)
			updated[k] = true
			continue
		}
		out = append(out, kv)
	}
	// Add vars that weren't in parent at all.
	for _, k := range want {
		if !updated[k] {
			out = append(out, k+"="+dirsJoined)
		}
	}
	return out
}

// envKey returns the key portion of a "KEY=VALUE" pair.
func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// parseTOMLStringArray accepts ["a","b"] / ['a','b']. Returns nil when
// the value isn't an array literal.
func parseTOMLStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
