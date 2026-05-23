// Package native — C header parser.
//
// ParseCHeader extracts function declarations from a .h file and converts
// them into []ManifestFn entries using the supported primitive type table.
// Functions using types that cannot be mapped (structs, non-void pointers,
// arrays, etc.) are silently skipped with a warning to stderrSink.
//
// The parser is intentionally simple: it strips comments and preprocessor
// lines, then matches function declarations with a regex. It does not aim
// to handle every valid C declaration — it only needs to cover the scalar
// types that the kernel codegen already supports.
package native

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// cTypeTable maps C type spellings to molt primitive names.
// Keys are lower-cased and have extra whitespace collapsed to a single space
// before lookup. "const" qualifier is stripped before the lookup.
var cTypeTable = map[string]string{
	"void":               "void",
	"bool":               "bool",
	"_bool":              "bool",
	"char":               "i8",
	"int8_t":             "i8",
	"unsigned char":      "u8",
	"uint8_t":            "u8",
	"short":              "i16",
	"short int":          "i16",
	"int16_t":            "i16",
	"unsigned short":     "u16",
	"unsigned short int": "u16",
	"uint16_t":           "u16",
	"int":                "i32",
	"int32_t":            "i32",
	"unsigned int":       "u32",
	"uint32_t":           "u32",
	"long long":          "i64",
	"long long int":      "i64",
	"int64_t":            "i64",
	"long":               "i64",
	"long int":           "i64",
	"unsigned long long": "u64",
	"unsigned long long int": "u64",
	"uint64_t":           "u64",
	"unsigned long":      "u64",
	"unsigned long int":  "u64",
	"float":              "f32",
	"double":             "f64",
	"size_t":             "usize",
	"ssize_t":            "isize",
}

// fnDeclRe matches a C function declaration of the form:
//
//	<return-type> <name>(<params>);
//
// The return type group is non-greedy and may include spaces and *,
// but must end before the function name. The param group captures
// everything inside the outermost parentheses.
var fnDeclRe = regexp.MustCompile(`(?m)^\s*([\w][\w\s\*]*?)\s+(\w+)\s*\(([^)]*)\)\s*;`)

// blockCommentRe matches /* ... */ block comments, including multi-line.
var blockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

// ParseCHeader parses a C header file and returns ManifestFn entries
// for every function declaration it can resolve to supported primitives.
// Functions using unsupported types (structs, pointers beyond void*, etc.)
// are silently skipped with a warning to stderrSink. Comments (// and /* */)
// and preprocessor lines (#...) are stripped before parsing.
func ParseCHeader(path string) ([]ManifestFn, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ParseCHeader: read %s: %w", path, err)
	}

	src := string(data)

	// Strip /* ... */ block comments first (may be multi-line).
	src = blockCommentRe.ReplaceAllString(src, " ")

	// Strip // line comments and # preprocessor lines line by line.
	var filtered strings.Builder
	for _, line := range strings.Split(src, "\n") {
		// Strip // line comments.
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		// Skip preprocessor lines.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			filtered.WriteByte('\n')
			continue
		}
		filtered.WriteString(line)
		filtered.WriteByte('\n')
	}
	src = filtered.String()

	var out []ManifestFn
	matches := fnDeclRe.FindAllStringSubmatch(src, -1)
	for _, m := range matches {
		retTypeRaw := strings.TrimSpace(m[1])
		fnName := strings.TrimSpace(m[2])
		paramsRaw := strings.TrimSpace(m[3])

		retPrim, ok := mapCType(retTypeRaw)
		if !ok {
			fmt.Fprintf(stderrSink, "warn: ParseCHeader: skipping %s — unsupported return type %q\n", fnName, retTypeRaw)
			continue
		}

		args, allOK := parseCParams(fnName, paramsRaw)
		if !allOK {
			continue
		}

		out = append(out, ManifestFn{
			Name: fnName,
			Args: args,
			Returns: ManifestReturn{
				Type: retPrim,
			},
		})
	}
	return out, nil
}

// parseCParams parses a C parameter list like "int a, double b" or
// "int, double" (names optional) into []ManifestArg. Returns (args, true)
// on success, or (nil, false) if any parameter type is unsupported.
// An empty or "void" parameter list returns (nil, true).
func parseCParams(fnName, paramsRaw string) ([]ManifestArg, bool) {
	if strings.TrimSpace(paramsRaw) == "" {
		return nil, true
	}
	parts := strings.Split(paramsRaw, ",")
	var args []ManifestArg
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		typStr, argName := splitCTypeAndName(p)
		// Special case: a single "void" param means no parameters.
		if len(parts) == 1 {
			typStr2 := strings.TrimSpace(strings.ToLower(stripConst(typStr)))
			if typStr2 == "void" {
				return nil, true
			}
		}
		prim, ok := mapCType(typStr)
		if !ok {
			fmt.Fprintf(stderrSink, "warn: ParseCHeader: skipping %s — unsupported arg %d type %q\n", fnName, i, typStr)
			return nil, false
		}
		if argName == "" {
			argName = fmt.Sprintf("_arg%d", i)
		}
		args = append(args, ManifestArg{
			Name: argName,
			Type: prim,
		})
	}
	return args, true
}

// splitCTypeAndName separates "int a", "unsigned int foo", "double" (name-less)
// into a (typeStr, name) pair. The name is the last token when it looks like
// a plain identifier (no * or type keywords). If only type tokens are present,
// name is "".
func splitCTypeAndName(s string) (typStr, name string) {
	s = strings.TrimSpace(s)
	// Strip pointer-level stars — we don't support pointer types (except
	// we handle the case later in mapCType where void* is explicitly rejected).
	// We keep stars in the raw string so mapCType can detect and reject them.

	tokens := strings.Fields(s)
	if len(tokens) == 0 {
		return s, ""
	}

	// Keywords that are always part of a type, never a variable name.
	typeKeywords := map[string]bool{
		"const": true, "volatile": true, "restrict": true,
		"unsigned": true, "signed": true,
		"short": true, "long": true, "int": true,
		"char": true, "float": true, "double": true,
		"void": true, "_Bool": true, "bool": true,
		"struct": true, "union": true, "enum": true,
		// stdint typedefs — treated as type keywords
		"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
		"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
		"size_t": true, "ssize_t": true, "ptrdiff_t": true,
	}

	// The last token is a name candidate if it doesn't look like a type keyword
	// and doesn't contain a star.
	last := tokens[len(tokens)-1]
	if !typeKeywords[last] && !strings.Contains(last, "*") {
		typStr = strings.Join(tokens[:len(tokens)-1], " ")
		name = last
		return
	}
	// All tokens are type: no name provided.
	return s, ""
}

// mapCType maps a raw C type string to a molt primitive name.
// Returns ("", false) for unsupported types.
// Handles "const" stripping and whitespace normalisation.
func mapCType(raw string) (string, bool) {
	// Reject anything containing * (pointer) — except we don't support
	// void* either. Any pointer → skip.
	if strings.Contains(raw, "*") {
		return "", false
	}
	// Normalise: strip const/volatile/restrict, collapse spaces, lowercase.
	clean := stripConst(raw)
	clean = collapseSpaces(clean)
	clean = strings.ToLower(clean)
	// Strip trailing/leading spaces again after lowercase.
	clean = strings.TrimSpace(clean)

	if p, ok := cTypeTable[clean]; ok {
		return p, true
	}
	return "", false
}

// stripConst removes const, volatile, and restrict qualifiers from a type string.
func stripConst(s string) string {
	var out []string
	for _, tok := range strings.Fields(s) {
		switch strings.ToLower(tok) {
		case "const", "volatile", "restrict":
			// skip
		default:
			out = append(out, tok)
		}
	}
	return strings.Join(out, " ")
}

// collapseSpaces replaces runs of whitespace with a single space.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
