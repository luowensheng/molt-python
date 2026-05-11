// Package native — kernel manifest parser.
//
// A manifest is a TOML file (<basename>.molt.toml by default) that
// describes a Python module's exported functions. Each manifest pairs
// with a sibling source file (.zig, .c, ...) found via the configured
// SourceExtensions. The pair compiles to a single .so + .pyi.
//
// Supported manifest schema (MVP):
//
//   module      = "math_kernel"            # optional; defaults to filename basename
//   source      = "math_kernel.zig"        # optional; defaults to sibling
//                                          # by SourceExtensions
//
//   [[fn]]
//   name        = "add"
//   description = "Sum two i32s."
//   args        = ["i32", "i32"]           # string array form
//   returns     = "i32"                    # string form
//
//   [[fn]]
//   name        = "mul"
//   args        = [
//       { name = "a", type = "f32", description = "First." },
//       { name = "b", type = "f32" },
//   ]                                       # inline-table array form
//   returns     = { type = "f32", description = "Product." }
//
// Both arg forms are accepted in the same manifest. Anything outside
// the recognised primitive type set causes the function to be dropped
// from the binding with a warning.

package native

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is the parsed in-memory shape of <name>.molt.toml.
type Manifest struct {
	Module    string
	Source    string // optional override; relative to manifest dir
	Library   string // optional pre-built shared library (.so/.dylib/.dll)
	Functions []ManifestFn
}

// ManifestFn is one [[fn]] entry.
type ManifestFn struct {
	Name        string
	Description string
	Args        []ManifestArg
	Returns     ManifestReturn
}

// ManifestArg is a parsed function parameter.
type ManifestArg struct {
	Name        string // optional; "" → emitter substitutes _arg<i>
	Type        string // primitive type spelling: "i32", "f32", etc.
	Description string
}

// ManifestReturn is the parsed return signature.
type ManifestReturn struct {
	Type        string // "" or "void" → returns None
	Description string
}

// ParseManifest reads and parses a manifest file.
func ParseManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	var cur *ManifestFn

	lines := splitLogicalLines(data)
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		// Strip trailing comments (after a space then `#`).
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if line == "[[fn]]" {
			if cur != nil {
				m.Functions = append(m.Functions, *cur)
			}
			cur = &ManifestFn{}
			continue
		}
		// Any other section header ends the current [[fn]].
		if strings.HasPrefix(line, "[") {
			if cur != nil {
				m.Functions = append(m.Functions, *cur)
				cur = nil
			}
			continue
		}

		// Key/value line.
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if cur == nil {
			// Top-level keys.
			switch key {
			case "module":
				m.Module = strings.Trim(val, `"'`)
			case "source":
				m.Source = strings.Trim(val, `"'`)
			case "library":
				m.Library = strings.Trim(val, `"'`)
			}
			continue
		}

		// Inside [[fn]].
		switch key {
		case "name":
			cur.Name = strings.Trim(val, `"'`)
		case "description":
			cur.Description = strings.Trim(val, `"'`)
		case "args":
			args, parseErr := parseArgsField(val)
			if parseErr != nil {
				return nil, fmt.Errorf("%s: fn %s: args: %w", path, cur.Name, parseErr)
			}
			cur.Args = args
		case "returns":
			ret, parseErr := parseReturnField(val)
			if parseErr != nil {
				return nil, fmt.Errorf("%s: fn %s: returns: %w", path, cur.Name, parseErr)
			}
			cur.Returns = ret
		}
	}
	if cur != nil {
		m.Functions = append(m.Functions, *cur)
	}
	return m, nil
}

// splitLogicalLines splits the manifest into logical TOML lines, joining
// continuations so a multi-line `args = [\n {…},\n {…},\n]` value comes
// out as one line. We track bracket / brace / paren / quote depth and
// only break on a newline when depth is zero.
func splitLogicalLines(data []byte) []string {
	var lines []string
	var cur strings.Builder
	depth := 0
	inStr := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '"' && (i == 0 || data[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr {
			switch c {
			case '[', '{', '(':
				depth++
			case ']', '}', ')':
				depth--
			}
		}
		if c == '\n' && depth == 0 && !inStr {
			lines = append(lines, cur.String())
			cur.Reset()
			continue
		}
		// Collapse newlines inside multi-line values to single spaces so
		// the rest of the parser sees a logical single line.
		if c == '\n' {
			cur.WriteByte(' ')
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// parseArgsField parses either a string array form (`["i32", "f32"]`)
// or an inline-table array form (`[{name="a",type="i32"},…]`). Returns
// a normalised []ManifestArg.
func parseArgsField(s string) ([]ManifestArg, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("expected an array, got %q", s)
	}
	inner := s[1 : len(s)-1]
	if strings.TrimSpace(inner) == "" {
		return nil, nil
	}

	// Decide between string and inline-table form by peeking at the
	// first non-whitespace character.
	trimmed := strings.TrimLeft(inner, " \t\n")
	if strings.HasPrefix(trimmed, "{") {
		return parseInlineTableArray(inner)
	}

	// String array form.
	var out []ManifestArg
	for _, p := range splitTopLevel(inner, ',') {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p == "" {
			continue
		}
		out = append(out, ManifestArg{Type: p})
	}
	return out, nil
}

// parseReturnField parses `returns = "i32"` or
// `returns = {type = "i32", description = "..."}`.
func parseReturnField(s string) (ManifestReturn, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		fields, err := parseInlineTable(strings.Trim(s, "{}"))
		if err != nil {
			return ManifestReturn{}, err
		}
		return ManifestReturn{
			Type:        fields["type"],
			Description: fields["description"],
		}, nil
	}
	t := strings.Trim(s, `"'`)
	return ManifestReturn{Type: t}, nil
}

// parseInlineTableArray parses an array of inline tables:
//
//   {name="a",type="i32"}, {name="b",type="i32"}
//
// (without the outer brackets — caller already stripped them).
func parseInlineTableArray(inner string) ([]ManifestArg, error) {
	var out []ManifestArg
	for _, t := range splitTopLevel(inner, ',') {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") {
			return nil, fmt.Errorf("expected inline table, got %q", t)
		}
		fields, err := parseInlineTable(t[1 : len(t)-1])
		if err != nil {
			return nil, err
		}
		out = append(out, ManifestArg{
			Name:        fields["name"],
			Type:        fields["type"],
			Description: fields["description"],
		})
	}
	return out, nil
}

// parseInlineTable parses `name="a",type="i32",description="..."` into
// a map. Handles double-quoted string values; doesn't handle nested
// tables or arrays (we don't need those for arg/return entries).
func parseInlineTable(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range splitTopLevel(s, ',') {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return nil, fmt.Errorf("expected key=value in inline table, got %q", kv)
		}
		k := strings.TrimSpace(kv[:eq])
		v := strings.TrimSpace(kv[eq+1:])
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out, nil
}

// splitTopLevel splits s on the given separator, ignoring separators
// that are inside (), [], {}, or "...".  Used for both inline-table
// internals and string arrays.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inStr = !inStr
		case !inStr && (c == '(' || c == '[' || c == '{'):
			depth++
		case !inStr && (c == ')' || c == ']' || c == '}'):
			depth--
		case !inStr && depth == 0 && c == sep:
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// ResolveSource finds the source-or-library file paired with a manifest.
// Returns (absolute path, language-tag, ok). The language tag is either
// a source language ("zig", "c", "cpp", "rust", "odin", ...) which
// triggers the compile-from-source path, or "prebuilt" which triggers
// the dlopen-wrapper path.
//
// Resolution order:
//   1. manifest.Library (explicit pre-built lib, takes priority).
//   2. manifest.Source  (explicit source override, source-compile path).
//   3. Sibling file <basename>.<ext> for each ext in SourceExtensions
//      AND for the platform's shared-library extensions (.so/.dylib/.dll).
//
// In other words: drop a `<name>.so` next to a `<name>.molt.toml` and
// molt builds a wrapper for it automatically.
func ResolveSource(manifestPath string, m *Manifest, exts []string) (string, string, bool) {
	dir := filepath.Dir(manifestPath)
	base := manifestBasename(manifestPath)

	if m != nil && m.Library != "" {
		p := m.Library
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); err == nil {
			return p, "prebuilt", true
		}
	}

	if m != nil && m.Source != "" {
		p := m.Source
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); err == nil {
			return p, langFromExt(filepath.Ext(p)), true
		}
	}

	// Try sibling source files first (compile-from-source path).
	for _, ext := range exts {
		candidate := filepath.Join(dir, base+ext)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, langFromExt(ext), true
		}
	}

	// Then fall back to sibling pre-built libraries. We hard-code the
	// platform's shared-lib extensions here rather than asking the user
	// to list them in source_extensions — they're always meaningful for
	// kernel modules and never have a "compile from source" semantics.
	for _, ext := range []string{".so", ".dylib", ".dll"} {
		// Try both <basename>.so and lib<basename>.so for unix.
		for _, candidate := range []string{
			filepath.Join(dir, base+ext),
			filepath.Join(dir, "lib"+base+ext),
		} {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, "prebuilt", true
			}
		}
	}
	return "", "", false
}

// manifestBasename strips ".molt.toml" (or any configured manifest
// suffix) from a manifest path and returns the bare basename.
func manifestBasename(path string) string {
	name := filepath.Base(path)
	for _, sfx := range []string{".molt.toml", ".molt.json", ".molt.yaml", ".molt.yml"} {
		if strings.HasSuffix(name, sfx) {
			return strings.TrimSuffix(name, sfx)
		}
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func langFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".zig":
		return "zig"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".rs":
		return "rust"
	case ".odin":
		return "odin"
	case ".s":
		return "s"
	case ".asm":
		return "asm"
	}
	return "unknown"
}
