package glue

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// manifestSuffixes are the file suffixes recognised as glue manifest files.
// A glue manifest is a <name>.molt.toml that contains a top-level `lang` key.
// This distinguishes it from a native kernel manifest (which has no lang key).
var manifestSuffixes = []string{".molt.toml", ".molt.json", ".molt.yaml", ".molt.yml"}

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

// LoadGlueConfig loads glue module configurations for a project.
//
// It merges two sources (in this priority order):
//
//  1. <name>.molt.toml files in the project directory that contain a top-level
//     `lang` key. These are the preferred, per-module format — identical to the
//     native kernel manifest format but extended with glue-specific keys (lang,
//     src, transport, crates, pkg_module). The module name is the file basename
//     stripped of the .molt.toml suffix.
//
//  2. [[tool.molt.glue]] blocks in moltproject.toml or pyproject.toml (legacy /
//     backward-compatible form, kept for projects that prefer inline config).
//
// Returns an empty GlueConfig (no error) when neither source is present.
func LoadGlueConfig(projectDir string) (GlueConfig, error) {
	var cfg GlueConfig

	// 1. Scan project directory for <name>.molt.toml glue manifests.
	entries, _ := os.ReadDir(projectDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		isManifest := false
		for _, sfx := range manifestSuffixes {
			if strings.HasSuffix(name, sfx) {
				isManifest = true
				break
			}
		}
		if !isManifest {
			continue
		}
		fullPath := filepath.Join(projectDir, name)
		m, err := ParseGlueManifest(fullPath)
		if err != nil || m == nil {
			continue // not a glue manifest (no lang key) or parse error
		}
		cfg.Modules = append(cfg.Modules, *m)
	}

	// 2. Also read [[tool.molt.glue]] blocks from the project TOML file.
	data, err := readGlueConfigFile(projectDir)
	if err != nil {
		return cfg, nil // no TOML config — just return whatever manifests found
	}

	const header = "[[tool.molt.glue]]"
	const fnHeader = "[[tool.molt.glue.fn]]"

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

// ParseGlueManifest reads a <name>.molt.toml file and returns a GlueModuleConfig
// if the file contains a `lang` key (identifying it as a glue manifest rather
// than a native kernel manifest). Returns nil without error when the file has
// no lang key — this silently skips kernel manifests.
//
// The module name defaults to the file's basename stripped of the manifest
// suffix (e.g. "stats" from "stats.molt.toml").
//
// Supported top-level keys:
//
//	lang       = "go" | "rust"         (required — distinguishes glue from kernel)
//	src        = "./gocode"            (local path or library import path / crate name)
//	transport  = "stdio" | "unix_socket" | "tcp"  (default: "stdio")
//	crates     = ["flate2 = '1.0'"]   (Rust only)
//	pkg_module = "github.com/you/lib" (Go only — explicit module path)
//	module     = "stats"              (override derived module name)
//
// Function declarations use the same [[fn]] format as native kernel manifests:
//
//	[[fn]]
//	name    = "mean"
//	args    = [{ name = "data", type = "[]f64" }]
//	returns = "f64"
func ParseGlueManifest(path string) (*GlueModuleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Quick pre-scan: does the file have a `lang` key at the top level?
	// If not, it's a native kernel manifest — skip it.
	hasLang := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "lang") && strings.Contains(t, "=") {
			hasLang = true
			break
		}
		// Stop scanning once we hit the first [[fn]] — lang must come before fns.
		if t == "[[fn]]" {
			break
		}
	}
	if !hasLang {
		return nil, nil // kernel manifest — not a glue manifest
	}

	// Derive module name from filename.
	base := filepath.Base(path)
	for _, sfx := range manifestSuffixes {
		if strings.HasSuffix(base, sfx) {
			base = strings.TrimSuffix(base, sfx)
			break
		}
	}
	m := &GlueModuleConfig{Module: base}

	// Use splitGlueLogicalLines so multi-line `args = [...]` values are
	// joined before key/value parsing.
	lines := splitGlueLogicalLines(data)
	var curFn *GlueFn

	flushFn := func() {
		if curFn != nil && curFn.Name != "" {
			m.Fns = append(m.Fns, *curFn)
			curFn = nil
		}
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if line == "[[fn]]" {
			flushFn()
			curFn = &GlueFn{}
			continue
		}
		// Any other section header ends the current [[fn]] and top-level scope.
		if strings.HasPrefix(line, "[") {
			flushFn()
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if curFn != nil {
			switch key {
			case "name":
				curFn.Name = strings.Trim(val, `"'`)
			case "call":
				curFn.Call = strings.Trim(val, `"'`)
			case "returns":
				curFn.Returns = strings.Trim(strings.Trim(val, `"'`), " ")
			case "args":
				curFn.Args = parseGlueArgs(val)
			}
		} else {
			switch key {
			case "module":
				m.Module = strings.Trim(val, `"'`)
			case "lang":
				m.Lang = strings.ToLower(strings.Trim(val, `"'`))
			case "src":
				m.Src = strings.Trim(val, `"'`)
			case "transport":
				m.Transport = strings.Trim(val, `"'`)
			case "crates":
				m.Crates = parseTOMLStringArray(val)
			case "pkg_module":
				m.PkgModule = strings.Trim(val, `"'`)
			}
		}
	}
	flushFn()

	if m.Lang == "" {
		return nil, nil // safety: lang disappeared after pre-scan
	}
	return m, nil
}

// splitGlueLogicalLines joins multi-line TOML values (e.g. args = [\n{…}\n])
// into a single logical line so the key=value parser can handle them.
// Tracks bracket/brace/quote depth; only breaks on newline at depth 0.
func splitGlueLogicalLines(data []byte) []string {
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
