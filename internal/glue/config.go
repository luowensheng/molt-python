package glue

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// GlueConfig holds all [[tool.molt.glue]] blocks from the project config.
type GlueConfig struct {
	Modules []GlueModuleConfig
}

// GlueModuleConfig is one [[tool.molt.glue]] stanza.
type GlueModuleConfig struct {
	// Module is the generated Python module name (required).
	Module string

	// Lang is the language: "go" or "rust" (required).
	Lang string

	// Src is either a local file/directory path or a library import path.
	//   Go local:   "./stats"  or  "stats/stats.go"
	//   Go lib:     "crypto/sha256"  or  "golang.org/x/crypto/sha3"
	//   Rust local: "compress_glue.rs"  or  "./rust-lib"
	Src string

	// PkgModule is the Go module path for a user-written package whose
	// go.mod already declares a real module path (e.g. github.com/you/lib).
	// When absent molt uses "user/<Module>" as the synthetic module path.
	PkgModule string

	// Crates is a list of extra crate dependencies to add to the generated
	// Cargo.toml (Rust only). E.g. ["flate2 = '1.0'", "sha2 = '0.10'"].
	Crates []string

	// Transport is "stdio" (default), "unix_socket", or "tcp".
	Transport string

	// Fns is the list of exported functions to expose.
	Fns []GlueFn
}

// SrcIsLibrary returns true when Src is a package/module import path rather
// than a local file or directory.
//
// Rules:
//
//	Go:   contains "/" with no file extension  → library import path
//	      known stdlib prefixes (net/, crypto/, …) → stdlib
//	Rust: Src not ending in ".rs" and not starting with "./" or "/" → crate name
func (m GlueModuleConfig) SrcIsLibrary() bool {
	s := m.Src
	switch strings.ToLower(m.Lang) {
	case "go":
		// Local directory: starts with ./ or / or has no slash
		if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "/") {
			return false
		}
		// Has a slash but no file extension → import path
		if strings.Contains(s, "/") {
			ext := filepath.Ext(s)
			return ext == ""
		}
		// Known stdlib top-level packages without a slash
		stdlibPrefixes := []string{
			"net", "crypto", "math", "encoding", "compress", "archive",
			"bufio", "bytes", "context", "database", "debug", "errors",
			"expvar", "flag", "fmt", "go", "hash", "html", "http",
			"image", "io", "log", "maps", "mime", "net", "os", "path",
			"plugin", "reflect", "regexp", "runtime", "slices", "sort",
			"strconv", "strings", "sync", "syscall", "testing", "text",
			"time", "unicode", "unsafe",
		}
		for _, p := range stdlibPrefixes {
			if s == p {
				return true
			}
		}
		return false
	case "rust":
		// Local .rs file
		if strings.HasSuffix(s, ".rs") {
			return false
		}
		// Local directory
		if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "/") {
			return false
		}
		// Otherwise treat as crate name
		return true
	}
	return false
}

// Transport returns the transport string defaulting to "stdio".
func (m GlueModuleConfig) EffectiveTransport() string {
	if m.Transport == "" {
		return "stdio"
	}
	return m.Transport
}

// GlueFn is one exported function declaration.
type GlueFn struct {
	// Name is the function name as it appears in the Go/Rust source.
	Name string

	// Call is an optional fully-qualified symbol override, e.g. "sha3.Sum256".
	// When absent the function is called as Name directly.
	Call string

	// Args is the list of function arguments.
	Args []GlueArg

	// Returns is the glue type returned by the function, or "" for void.
	Returns string
}

// GlueArg is one argument in a GlueFn.
type GlueArg struct {
	Name string
	Type string
}

// LoadGlueConfig reads all [[tool.molt.glue]] stanzas from the project's
// moltproject.toml or pyproject.toml. Returns an empty GlueConfig (no error)
// when no [[tool.molt.glue]] blocks are present.
func LoadGlueConfig(projectDir string) (GlueConfig, error) {
	data, err := readGlueConfigFile(projectDir)
	if err != nil {
		return GlueConfig{}, nil // no config file → zero modules, no error
	}

	const header = "[[tool.molt.glue]]"
	const fnHeader = "[[tool.molt.glue.fn]]"

	var cfg GlueConfig
	var curMod *GlueModuleConfig
	var curFn *GlueFn

	flushFn := func() {
		if curFn != nil && curFn.Name != "" && curMod != nil {
			curMod.Fns = append(curMod.Fns, *curFn)
			curFn = nil
		}
	}
	flushMod := func() {
		flushFn()
		if curMod != nil && curMod.Module != "" {
			cfg.Modules = append(cfg.Modules, *curMod)
			curMod = nil
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// Strip inline comments.
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}

		if line == header {
			flushMod()
			curMod = &GlueModuleConfig{}
			continue
		}

		if line == fnHeader {
			flushFn()
			if curMod != nil {
				curFn = &GlueFn{}
			}
			continue
		}

		// Any other section header ends the current glue block.
		if strings.HasPrefix(line, "[[") || (strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[[tool.molt.glue")) {
			flushMod()
			continue
		}

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			// Could be an inline table entry in args: handle below.
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if curFn != nil {
			// Inside a [[tool.molt.glue.fn]] block.
			switch key {
			case "name":
				curFn.Name = strings.Trim(val, `"'`)
			case "call":
				curFn.Call = strings.Trim(val, `"'`)
			case "returns":
				curFn.Returns = strings.Trim(val, `"'`)
			case "args":
				// Inline TOML array of inline tables:
				// [{name = "data", type = "[]f64"}, ...]
				curFn.Args = parseGlueArgs(val)
			}
		} else if curMod != nil {
			// Inside a [[tool.molt.glue]] block.
			switch key {
			case "module":
				curMod.Module = strings.Trim(val, `"'`)
			case "lang":
				curMod.Lang = strings.ToLower(strings.Trim(val, `"'`))
			case "src":
				curMod.Src = strings.Trim(val, `"'`)
			case "pkg_module":
				curMod.PkgModule = strings.Trim(val, `"'`)
			case "transport":
				curMod.Transport = strings.Trim(val, `"'`)
			case "crates":
				curMod.Crates = parseTOMLStringArray(val)
			}
		}
	}
	flushMod()

	return cfg, nil
}

// readGlueConfigFile reads the project configuration file.
func readGlueConfigFile(projectDir string) ([]byte, error) {
	for _, name := range []string{"moltproject.toml", "pyproject.toml"} {
		if data, err := os.ReadFile(filepath.Join(projectDir, name)); err == nil {
			return data, nil
		}
	}
	return nil, os.ErrNotExist
}

// parseGlueArgs parses an inline TOML array of inline tables:
//
//	[{name = "data", type = "[]f64"}, {name = "buckets", type = "i32"}]
func parseGlueArgs(s string) []GlueArg {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := s[1 : len(s)-1]
	// Split on "}, {" or "}," patterns to find individual inline tables.
	var args []GlueArg
	// Simple approach: find each {...} block.
	depth := 0
	start := -1
	for i, ch := range inner {
		if ch == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 && start >= 0 {
				block := inner[start+1 : i]
				arg := parseInlineTable(block)
				if arg.Name != "" && arg.Type != "" {
					args = append(args, arg)
				}
				start = -1
			}
		}
	}
	return args
}

// parseInlineTable parses key = "value" pairs from an inline table body
// (without the surrounding braces).
func parseInlineTable(s string) GlueArg {
	var arg GlueArg
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		idx := strings.IndexByte(p, '=')
		if idx < 0 {
			continue
		}
		k := strings.TrimSpace(p[:idx])
		v := strings.Trim(strings.TrimSpace(p[idx+1:]), `"'`)
		switch k {
		case "name":
			arg.Name = v
		case "type":
			arg.Type = v
		}
	}
	return arg
}

// parseTOMLStringArray parses ["a", "b", "c"] → ["a", "b", "c"].
// Returns nil when the value doesn't look like an array.
// Strips exactly one matching outer quote pair ("..." or '...') per element,
// preserving inner quotes (important for crate entries like "flate2 = '1.0'").
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
		// Strip exactly one outer matching quote pair.
		if len(p) >= 2 &&
			((p[0] == '"' && p[len(p)-1] == '"') ||
				(p[0] == '\'' && p[len(p)-1] == '\'')) {
			p = p[1 : len(p)-1]
		}
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
