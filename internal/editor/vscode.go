package editor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"molt/internal/syspath"
)

// WriteVSCode writes/merges molt-managed Python keys into the project's
// .vscode/settings.json. The molt-owned keys are:
//
//   - python.defaultInterpreterPath = spec.Python
//   - python.analysis.extraPaths    = spec.Syspath
//   - python.terminal.activateEnvironment = false
//
// Other keys in the file are preserved byte-for-byte where possible. If
// the existing file contains JSON-with-comments (jsonc), molt won't risk
// silently dropping the comments — it errors unless force is true.
func WriteVSCode(projectDir string, spec *syspath.Spec, force bool) error {
	if spec == nil {
		return fmt.Errorf("nil spec")
	}
	dir := filepath.Join(projectDir, ".vscode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	settingsPath := filepath.Join(dir, "settings.json")

	existing := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if hasJSONComments(data) && !force {
			return fmt.Errorf(
				"%s contains JSON comments (jsonc). Remove them or pass --force to overwrite",
				settingsPath)
		}
		// json.Unmarshal of an empty file fails — treat as empty object.
		stripped := bytes.TrimSpace(data)
		if len(stripped) > 0 {
			if err := json.Unmarshal(stripStandaloneComments(data), &existing); err != nil {
				return fmt.Errorf("parse %s: %w (try --force)", settingsPath, err)
			}
		}
	}

	existing["python.defaultInterpreterPath"] = spec.Python
	existing["python.analysis.extraPaths"] = spec.Syspath
	existing["python.terminal.activateEnvironment"] = false

	out, err := marshalIndented(existing)
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o644)
}

// marshalIndented produces a stable, human-friendly 2-space-indented JSON
// with a trailing newline. Map keys are sorted by encoding/json default.
func marshalIndented(v any) ([]byte, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// hasJSONComments reports whether data contains // or /* ... */ style
// comments outside of strings. Conservative: any unquoted occurrence of
// `//` or `/*` is treated as a comment. Good enough to refuse jsonc files
// where blind json.Unmarshal would lose information.
func hasJSONComments(data []byte) bool {
	inStr := false
	prevBackslash := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			if c == '"' && !prevBackslash {
				inStr = false
			}
			prevBackslash = c == '\\' && !prevBackslash
			continue
		}
		switch c {
		case '"':
			inStr = true
			prevBackslash = false
		case '/':
			if i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*') {
				return true
			}
		}
	}
	return false
}

// stripStandaloneComments is a tiny JSONC stripper used only when --force
// is in play. Removes // ... line comments and /* ... */ block comments
// outside of strings. Trailing commas inside objects/arrays are NOT
// handled — keep this minimal until a real need arises.
func stripStandaloneComments(data []byte) []byte {
	var out bytes.Buffer
	inStr := false
	prevBackslash := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out.WriteByte(c)
			if c == '"' && !prevBackslash {
				inStr = false
			}
			prevBackslash = c == '\\' && !prevBackslash
			continue
		}
		if c == '"' {
			inStr = true
			out.WriteByte(c)
			prevBackslash = false
			continue
		}
		if c == '/' && i+1 < len(data) {
			if data[i+1] == '/' {
				// skip until newline
				j := strings.IndexByte(string(data[i:]), '\n')
				if j < 0 {
					i = len(data)
				} else {
					i += j
					out.WriteByte('\n')
				}
				continue
			}
			if data[i+1] == '*' {
				// skip until */
				end := strings.Index(string(data[i+2:]), "*/")
				if end < 0 {
					i = len(data)
				} else {
					i += 2 + end + 1
				}
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}
