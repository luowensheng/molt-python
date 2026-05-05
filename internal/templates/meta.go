package templates

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// parseMeta is a hand-rolled mini TOML parser tailored to template.toml.
// It supports:
//   - bare keys at the top level (no [section] headers used by templates)
//   - basic strings ("…")
//   - triple-quoted basic strings (\"\"\"…\"\"\") spanning multiple lines
//   - bools (true/false)
//   - string arrays ([\"a\", \"b\"])
//   - line and trailing # comments
//
// Anything else is ignored — templates are user-authored config, so be
// permissive on unknown keys, strict on syntax of recognised ones.
func parseMeta(data []byte) (Meta, error) {
	m := Meta{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		line := stripComment(strings.TrimSpace(raw))
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])

		// Triple-quoted multiline string?
		if strings.HasPrefix(val, `"""`) {
			body, err := readTripleQuoted(val, sc, &lineNo)
			if err != nil {
				return m, fmt.Errorf("line %d: %w", lineNo, err)
			}
			assignMeta(&m, key, body)
			continue
		}
		v, err := parseValue(val)
		if err != nil {
			return m, fmt.Errorf("line %d (%s): %w", lineNo, key, err)
		}
		assignMeta(&m, key, v)
	}
	return m, sc.Err()
}

func assignMeta(m *Meta, key string, v any) {
	switch key {
	case "description":
		if s, ok := v.(string); ok {
			m.Description = s
		}
	case "use_uv_lib":
		if b, ok := v.(bool); ok {
			m.UseUvLib = b
		}
	case "keep_hello":
		if b, ok := v.(bool); ok {
			m.KeepHello = b
		}
	case "add":
		if a, ok := v.([]string); ok {
			m.Add = a
		}
	case "add_dev":
		if a, ok := v.([]string); ok {
			m.AddDev = a
		}
	case "tasks":
		if s, ok := v.(string); ok {
			m.Tasks = s
		}
	}
}

// parseValue handles single-line scalars: bool, string, string array.
func parseValue(s string) (any, error) {
	if s == "true" {
		return true, nil
	}
	if s == "false" {
		return false, nil
	}
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		return unquote(s[1 : len(s)-1]), nil
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		body := strings.TrimSpace(s[1 : len(s)-1])
		if body == "" {
			return []string{}, nil
		}
		var out []string
		for _, raw := range strings.Split(body, ",") {
			t := strings.TrimSpace(raw)
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`) && len(t) >= 2 {
				out = append(out, unquote(t[1:len(t)-1]))
			} else {
				return nil, fmt.Errorf("array element %q is not a quoted string", t)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unrecognised value %q", s)
}

// readTripleQuoted reads a """…""" string starting from `first` (the line
// after the `=`). Handles same-line termination and multi-line bodies.
func readTripleQuoted(first string, sc *bufio.Scanner, lineNo *int) (string, error) {
	body := strings.TrimPrefix(first, `"""`)
	if i := strings.Index(body, `"""`); i >= 0 {
		return body[:i], nil
	}
	var b strings.Builder
	if body != "" {
		b.WriteString(body)
		b.WriteByte('\n')
	}
	for sc.Scan() {
		*lineNo++
		ln := sc.Text()
		if i := strings.Index(ln, `"""`); i >= 0 {
			b.WriteString(ln[:i])
			return b.String(), nil
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return "", fmt.Errorf(`unterminated """ string`)
}

// unquote handles minimal escapes (\\, \", \n, \t, \r) in a basic string.
func unquote(s string) string {
	r := strings.NewReplacer(
		`\\`, `\`,
		`\"`, `"`,
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
	)
	return r.Replace(s)
}

// stripComment removes a trailing # comment that is not inside quotes.
func stripComment(s string) string {
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
		if c == '#' {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}
