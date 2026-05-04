// Package lockparse reads uv.lock. It implements only the subset of TOML
// that uv.lock uses (top-level scalars, [[package]] arrays of tables, inline
// tables, arrays of inline tables, basic strings/integers). It is NOT a
// general-purpose TOML parser; if uv changes its lock format substantially,
// this package will need updating.
package lockparse

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ResolvedPkg struct {
	Name         string
	Version      string
	Source       string // "registry", "git", "editable", "path", ...
	EditablePath string // for editable installs (absolute, resolved against lockDir)
	Dependencies []string
	Wheels       []Wheel
}

type Wheel struct {
	URL      string
	Hash     string // sha256 hex (no "sha256:" prefix)
	Filename string // derived from URL basename
	// Tags parsed from filename per PEP 427:
	PyTag, AbiTag, PlatformTag string
}

type pyABI interface {
	GetPyTag() string
	GetAbiTag() string
	GetPlatforms() []string
}

// Parse reads a uv.lock and returns the [[package]] entries.
func Parse(lockPath string) ([]ResolvedPkg, error) {
	f, err := os.Open(lockPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tables, err := readTables(bufio.NewScanner(f))
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Dir(lockPath)
	out := []ResolvedPkg{}
	for _, t := range tables {
		if t.header != "package" {
			continue
		}
		p := ResolvedPkg{
			Name:    strFromTable(t.fields, "name"),
			Version: strFromTable(t.fields, "version"),
		}
		if src, ok := t.fields["source"].(map[string]any); ok {
			for k, v := range src {
				if vs, ok := v.(string); ok {
					switch k {
					case "registry":
						p.Source = "registry"
					case "git":
						p.Source = "git"
					case "editable":
						p.Source = "editable"
						p.EditablePath = absPath(lockDir, vs)
					case "path":
						p.Source = "path"
						p.EditablePath = absPath(lockDir, vs)
					case "url":
						if p.Source == "" {
							p.Source = "url"
						}
					}
				}
			}
		}
		if deps, ok := t.fields["dependencies"].([]any); ok {
			for _, d := range deps {
				if dm, ok := d.(map[string]any); ok {
					if n, ok := dm["name"].(string); ok {
						p.Dependencies = append(p.Dependencies, n)
					}
				}
			}
		}
		if whs, ok := t.fields["wheels"].([]any); ok {
			for _, w := range whs {
				wm, ok := w.(map[string]any)
				if !ok {
					continue
				}
				url, _ := wm["url"].(string)
				hash, _ := wm["hash"].(string)
				hash = strings.TrimPrefix(hash, "sha256:")
				wh := Wheel{URL: url, Hash: hash, Filename: filepath.Base(url)}
				wh.PyTag, wh.AbiTag, wh.PlatformTag = parseWheelTags(wh.Filename)
				p.Wheels = append(p.Wheels, wh)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// SelectWheel returns the best wheel for the active interpreter ABI from pkg.Wheels.
// Order: exact (py_tag, abi_tag, plat) match > abi3 match > pure (py3-none-any).
// Returns an error if pkg has no wheels at all.
func SelectWheel(pkg ResolvedPkg, py pyABI) (Wheel, error) {
	if len(pkg.Wheels) == 0 {
		return Wheel{}, fmt.Errorf("%s %s: no wheels in lockfile (sdist-only?)", pkg.Name, pkg.Version)
	}
	platSet := map[string]bool{"any": true}
	for _, p := range py.GetPlatforms() {
		platSet[p] = true
	}
	pyTag := py.GetPyTag()
	abiTag := py.GetAbiTag()

	// Score each wheel; lower is better.
	type cand struct {
		w     Wheel
		score int
	}
	best := cand{score: 1 << 30}
	for _, w := range pkg.Wheels {
		// Platform must contain at least one of the interpreter's platforms.
		platOK := false
		for _, p := range strings.Split(w.PlatformTag, ".") {
			if platSet[p] {
				platOK = true
				break
			}
		}
		if !platOK {
			continue
		}
		var s int
		switch {
		case tagContains(w.AbiTag, abiTag) && tagContains(w.PyTag, pyTag):
			s = 0
		case tagContains(w.AbiTag, "abi3") && strings.HasPrefix(pyTag, "cp"):
			// abi3 wheel: any cp wheel with a py-tag <= our interpreter's tag works.
			minTag := ""
			for _, c := range strings.Split(w.PyTag, ".") {
				if strings.HasPrefix(c, "cp") && (minTag == "" || c < minTag) {
					minTag = c
				}
			}
			if minTag != "" && pyTag >= minTag {
				s = 10
			} else {
				continue
			}
		case tagContains(w.AbiTag, "none") && (tagContains(w.PyTag, "py3") || tagContains(w.PyTag, pyTag)):
			s = 20
		default:
			continue
		}
		if w.PlatformTag == "any" {
			s += 1
		}
		if s < best.score {
			best = cand{w: w, score: s}
		}
	}
	if best.score == 1<<30 {
		return Wheel{}, fmt.Errorf("%s %s: no wheel matches %s/%s on %v",
			pkg.Name, pkg.Version, pyTag, abiTag, py.GetPlatforms())
	}
	return best.w, nil
}

// ── tiny TOML subset reader ──────────────────────────────────────────────────

type table struct {
	header   string         // "package" for [[package]]; "" for the top-level table
	isArray  bool           // true for [[ ]]
	fields   map[string]any // values are: string, int64, bool, []any, map[string]any
}

func readTables(sc *bufio.Scanner) ([]*table, error) {
	root := &table{header: "", fields: map[string]any{}}
	tables := []*table{root}
	cur := root

	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		line := stripComment(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		// New table?
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			name := strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]")
			cur = &table{header: name, isArray: true, fields: map[string]any{}}
			tables = append(tables, cur)
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			cur = &table{header: name, fields: map[string]any{}}
			tables = append(tables, cur)
			continue
		}
		// key = value (value may continue across lines for multi-line arrays)
		eq := indexEquals(line)
		if eq < 0 {
			return nil, fmt.Errorf("uv.lock:%d: expected '=': %q", lineNo, raw)
		}
		key := strings.TrimSpace(line[:eq])
		valStr := strings.TrimSpace(line[eq+1:])
		// If value starts with '[' and isn't closed on this line, accumulate.
		if needsContinuation(valStr) {
			for sc.Scan() {
				lineNo++
				next := stripComment(strings.TrimSpace(sc.Text()))
				if next == "" {
					continue
				}
				valStr += " " + next
				if !needsContinuation(valStr) {
					break
				}
			}
		}
		v, err := parseValue(valStr)
		if err != nil {
			return nil, fmt.Errorf("uv.lock:%d: %w", lineNo, err)
		}
		cur.fields[key] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

// indexEquals returns the offset of the first '=' that's outside of any
// quotes or brackets.
func indexEquals(s string) int {
	inStr := false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '[' || c == '{' {
			depth++
		} else if c == ']' || c == '}' {
			depth--
		} else if c == '=' && depth == 0 {
			return i
		}
	}
	return -1
}

// needsContinuation: are we mid-array/inline-table? We say yes when the
// number of unmatched [ or { exceeds 0.
func needsContinuation(s string) bool {
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
	}
	return depth > 0
}

func stripComment(s string) string {
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if !inStr && c == '#' {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func parseValue(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '"':
		return parseString(s)
	case '[':
		return parseArray(s)
	case '{':
		return parseInlineTable(s)
	case 't':
		if s == "true" {
			return true, nil
		}
	case 'f':
		if s == "false" {
			return false, nil
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	// Fallback: treat as string-ish (uv.lock occasionally has bare values).
	return s, nil
}

func parseString(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' {
		return "", fmt.Errorf("not a string: %q", s)
	}
	end := -1
	for i := 1; i < len(s); i++ {
		if s[i] == '"' && s[i-1] != '\\' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", fmt.Errorf("unterminated string: %q", s)
	}
	body := s[1:end]
	body = strings.ReplaceAll(body, `\"`, `"`)
	body = strings.ReplaceAll(body, `\\`, `\`)
	return body, nil
}

func parseArray(s string) ([]any, error) {
	if s[0] != '[' {
		return nil, fmt.Errorf("not an array: %q", s)
	}
	// Strip outer brackets.
	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return []any{}, nil
	}
	parts, err := splitTopLevel(body, ',')
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := parseValue(p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func parseInlineTable(s string) (map[string]any, error) {
	if s[0] != '{' {
		return nil, fmt.Errorf("not an inline table: %q", s)
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	out := map[string]any{}
	if body == "" {
		return out, nil
	}
	parts, err := splitTopLevel(body, ',')
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eq := indexEquals(p)
		if eq < 0 {
			return nil, fmt.Errorf("inline table missing '=': %q", p)
		}
		k := strings.TrimSpace(p[:eq])
		v, err := parseValue(strings.TrimSpace(p[eq+1:]))
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// splitTopLevel splits s by sep where sep appears at depth 0 (outside strings,
// arrays, inline tables).
func splitTopLevel(s string, sep byte) ([]string, error) {
	out := []string{}
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out, nil
}

func strFromTable(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func absPath(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Clean(filepath.Join(base, p))
}

// parseWheelTags returns (py, abi, plat) parsed from a wheel filename per PEP 427.
// Compressed tags ("py2.py3", "cp311.cp312") are returned verbatim; SelectWheel
// uses tagContains to test membership.
func parseWheelTags(filename string) (string, string, string) {
	base := strings.TrimSuffix(filename, ".whl")
	parts := strings.Split(base, "-")
	if len(parts) != 5 && len(parts) != 6 {
		return "", "", ""
	}
	off := 0
	if len(parts) == 6 {
		off = 1
	}
	return parts[2+off], parts[3+off], parts[4+off]
}

// tagContains returns true if needle equals any '.'-separated component of haystack.
func tagContains(haystack, needle string) bool {
	for _, c := range strings.Split(haystack, ".") {
		if c == needle {
			return true
		}
	}
	return false
}
