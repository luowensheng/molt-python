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
	"sort"
	"strings"
)

// Config is the parsed [tool.molt.runtime] section.
type Config struct {
	// ExtraPaths is the list of directories to prepend to runtime
	// path env vars. Project-relative paths are resolved against the
	// project dir at load time so callers receive absolute paths.
	ExtraPaths []string

	// Env is the project-scoped env-var map parsed from
	// [tool.molt.runtime.env]. Layered on top of the global
	// ~/.molt/env.yaml at apply time; project values win on conflict.
	// Reserved names (PYTHONPATH / VIRTUAL_ENV / PYTHONHOME) are
	// dropped at parse time — molt always manages those.
	Env map[string]string
}

// Load parses [tool.molt.runtime] and [tool.molt.runtime.env] from
// projectDir/pyproject.toml. Missing file or section returns an empty
// Config (no-op).
func Load(projectDir string) Config {
	cfg := Config{Env: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return cfg
	}
	// Section state: "" / "main" / "env"
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			switch line {
			case "[tool.molt.runtime]":
				section = "main"
			case "[tool.molt.runtime.env]":
				section = "env"
			default:
				section = ""
			}
			continue
		}
		if section == "" || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch section {
		case "main":
			if key == "extra_paths" {
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.ExtraPaths = arr
				}
			}
		case "env":
			// Reserved names are silently dropped — they can't be
			// overridden via config.
			if isReservedEnv(key) {
				continue
			}
			cfg.Env[key] = strings.Trim(val, `"'`)
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

// isReservedEnv keeps the reserved-set local so this package doesn't
// hard-depend on internal/globalenv.
func isReservedEnv(name string) bool {
	switch name {
	case "PYTHONPATH", "VIRTUAL_ENV", "PYTHONHOME":
		return true
	}
	return false
}

// Apply layers env-var configuration onto `parent`:
//
//   1. Merge global env vars (from globalEnv) — set if not already in parent.
//      (Use the `parent` value if user explicitly exported the var in
//      their shell — explicit wins.)
//   2. Merge project env vars from c.Env — overrides everything.
//   3. Prepend c.ExtraPaths to PATH plus the platform's dynamic-linker
//      / framework env vars.
//
// `globalEnv` is normally `globalenv.Load()`; the parameter exists so
// tests can inject a fixed map.
//
// Variables that don't apply to the current OS pass through unchanged.
//
//   PATH                            — always
//   LD_LIBRARY_PATH                 — Linux
//   DYLD_FALLBACK_LIBRARY_PATH      — macOS
//   DYLD_FALLBACK_FRAMEWORK_PATH    — macOS
func (c Config) Apply(parent []string, globalEnv map[string]string) []string {
	out := append([]string(nil), parent...)
	out = applyEnvLayer(out, globalEnv, false) // globals don't override existing
	out = applyEnvLayer(out, c.Env, true)      // project always overrides

	if len(c.ExtraPaths) == 0 {
		return out
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

	parentForPath := out
	out = make([]string, 0, len(parentForPath)+len(want))
	for _, kv := range parentForPath {
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

// applyEnvLayer merges `vars` into `parent`. When `override` is true,
// existing values are replaced; when false, parent values take priority.
// Reserved variable names are always skipped.
func applyEnvLayer(parent []string, vars map[string]string, override bool) []string {
	if len(vars) == 0 {
		return parent
	}
	have := map[string]int{} // key → index into parent
	for i, kv := range parent {
		have[envKey(kv)] = i
	}
	out := append([]string(nil), parent...)
	// Stable iteration for deterministic result.
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if isReservedEnv(k) {
			continue
		}
		v := vars[k]
		if i, exists := have[k]; exists {
			if override {
				out[i] = k + "=" + v
			}
			continue
		}
		out = append(out, k+"="+v)
		have[k] = len(out) - 1
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
