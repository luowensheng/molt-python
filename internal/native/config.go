package native

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// CythonConfig holds optional overrides read from [tool.molt.cython] in
// pyproject.toml. All fields have sensible defaults applied by
// LoadCythonConfig so callers never need to check for zero values.
type CythonConfig struct {
	// Paths are project-relative roots scanned for *.pyx files.
	// Default: [".", "src"] (src is silently skipped when absent).
	Paths []string

	// ExtraCompileArgs are appended to the cc invocation after the fixed flags.
	ExtraCompileArgs []string

	// Directives are Cython language-level settings passed as
	// --directive key=value. E.g. {"boundscheck": "false"}.
	Directives map[string]string
}

// LoadCythonConfig reads [tool.molt.cython] and [tool.molt.cython.directives]
// from pyproject.toml in projectDir. Missing file or missing section both
// return defaults silently — callers should always get a usable config.
func LoadCythonConfig(projectDir string) CythonConfig {
	cfg := CythonConfig{
		Paths:      []string{".", "src"},
		Directives: map[string]string{},
	}

	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return cfg
	}

	const (
		sectionNone       = 0
		sectionCython     = 1
		sectionDirectives = 2
	)

	section := sectionNone
	var pathsSet, argsSet bool

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Strip inline comments.
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}

		// Detect section headers.
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			switch line {
			case "[tool.molt.cython]":
				section = sectionCython
			case "[tool.molt.cython.directives]":
				section = sectionDirectives
			default:
				section = sectionNone
			}
			continue
		}

		if section == sectionNone || line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch section {
		case sectionCython:
			switch key {
			case "paths":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.Paths = arr
					pathsSet = true
				}
			case "extra_compile_args":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.ExtraCompileArgs = arr
					argsSet = true
				}
			}
		case sectionDirectives:
			cfg.Directives[key] = strings.Trim(val, `"'`)
		}
	}

	_ = pathsSet
	_ = argsSet
	return cfg
}

// parseTOMLStringArray parses a TOML inline string array like ["a", "b"] or
// ['a', 'b']. Returns nil when the value doesn't look like an array so the
// caller can leave the default in place.
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
