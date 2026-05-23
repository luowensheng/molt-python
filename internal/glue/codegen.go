package glue

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

//go:embed templates/server_go.tmpl
var serverGoTmpl string

//go:embed templates/server_rust.tmpl
var serverRustTmpl string

//go:embed templates/client_stdio.py.tmpl
var clientStdioTmpl string

//go:embed templates/client_socket.py.tmpl
var clientSocketTmpl string

// serverTemplateData is passed to the Go/Rust server templates.
type serverTemplateData struct {
	Module     string
	Lang       string
	Transport  string
	ImportPath string // non-empty for library imports
	SrcFile    string // for Rust: absolute path to include!() source file
	Fns        []GlueFn
}

// clientTemplateData is passed to the Python client templates.
type clientTemplateData struct {
	Module    string
	Lang      string
	Transport string
	ExeSuffix string // "" on Unix, ".exe" on Windows
	Fns       []GlueFn
}

// goTypeMap maps glue type names to Go type names.
var goTypeMap = map[string]string{
	"i32":      "int32",
	"i64":      "int64",
	"f32":      "float32",
	"f64":      "float64",
	"bool":     "bool",
	"string":   "string",
	"bytes":    "string", // base64-encoded on the wire
	"[]i32":    "[]int32",
	"[]i64":    "[]int64",
	"[]f32":    "[]float32",
	"[]f64":    "[]float64",
	"[]string": "[]string",
	"json":     "interface{}",
}

// rustTypeMap maps glue type names to Rust type names.
var rustTypeMap = map[string]string{
	"i32":      "i32",
	"i64":      "i64",
	"f32":      "f32",
	"f64":      "f64",
	"bool":     "bool",
	"string":   "String",
	"bytes":    "Vec<u8>",
	"[]i32":    "Vec<i32>",
	"[]i64":    "Vec<i64>",
	"[]f32":    "Vec<f32>",
	"[]f64":    "Vec<f64>",
	"[]string": "Vec<String>",
	"json":     "serde_json::Value",
}

// pyTypeMap maps glue type names to Python type annotations.
var pyTypeMap = map[string]string{
	"i32":      "int",
	"i64":      "int",
	"f32":      "float",
	"f64":      "float",
	"bool":     "bool",
	"string":   "str",
	"bytes":    "bytes",
	"[]i32":    "list[int]",
	"[]i64":    "list[int]",
	"[]f32":    "list[float]",
	"[]f64":    "list[float]",
	"[]string": "list[str]",
	"json":     "Any",
	"":         "None",
}

// pyCastMap maps glue types to Python cast expressions.
var pyCastMap = map[string]string{
	"i32":    "int",
	"i64":    "int",
	"f32":    "float",
	"f64":    "float",
	"bool":   "bool",
	"string": "str",
	"json":   "",
}

func goType(t string) string {
	if v, ok := goTypeMap[t]; ok {
		return v
	}
	return "interface{}"
}

func isBytesType(t string) bool {
	return t == "bytes"
}

// callExpr returns the Go call expression for a function, handling the
// user. import alias for local packages and direct calls for library imports.
func callExpr(fn GlueFn) string {
	name := fn.Call
	if name == "" {
		// Capitalise first letter — Go exported names start with uppercase.
		if fn.Name != "" {
			name = strings.ToUpper(fn.Name[:1]) + fn.Name[1:]
		}
	}
	return "user." + name
}

// argList returns the Go argument list for a dispatch case, e.g. "a.Data, a.Buckets".
func argList(args []GlueArg) string {
	parts := make([]string, len(args))
	for i, a := range args {
		fieldName := strings.ToUpper(a.Name[:1]) + a.Name[1:]
		if a.Type == "bytes" {
			// bytes args arrive as base64 strings; decode before passing.
			parts[i] = fmt.Sprintf("func() []byte { b, _ := base64.StdEncoding.DecodeString(a.%s); return b }()", fieldName)
		} else {
			parts[i] = "a." + fieldName
		}
	}
	return strings.Join(parts, ", ")
}

// bytesExpr returns the expression to convert a Go return value to base64.
func bytesExpr(retType, varName string) string {
	if retType == "bytes" {
		return varName
	}
	return varName
}

// rustDecode returns a Rust expression to decode a JSON value as rustType.
func rustDecode(glueType, jsonExpr string) string {
	switch glueType {
	case "bytes":
		return fmt.Sprintf("general_purpose::STANDARD.decode(%s.as_str().unwrap_or(\"\")).unwrap_or_default()", jsonExpr)
	case "string":
		return fmt.Sprintf("%s.as_str().unwrap_or(\"\").to_string()", jsonExpr)
	case "bool":
		return fmt.Sprintf("%s.as_bool().unwrap_or(false)", jsonExpr)
	case "f32", "f64":
		return fmt.Sprintf("%s.as_f64().unwrap_or(0.0) as %s", jsonExpr, glueType)
	case "i32", "i64":
		return fmt.Sprintf("%s.as_i64().unwrap_or(0) as %s", jsonExpr, glueType)
	case "[]f64":
		return fmt.Sprintf("%s.as_array().unwrap_or(&vec![]).iter().map(|v| v.as_f64().unwrap_or(0.0)).collect::<Vec<f64>>()", jsonExpr)
	case "[]i32":
		return fmt.Sprintf("%s.as_array().unwrap_or(&vec![]).iter().map(|v| v.as_i64().unwrap_or(0) as i32).collect::<Vec<i32>>()", jsonExpr)
	case "[]string":
		return fmt.Sprintf("%s.as_array().unwrap_or(&vec![]).iter().map(|v| v.as_str().unwrap_or(\"\").to_string()).collect::<Vec<String>>()", jsonExpr)
	default:
		return fmt.Sprintf("%s.clone()", jsonExpr)
	}
}

// rustEncode returns a Rust expression to encode a value as JSON.
func rustEncode(glueType, varName string) string {
	switch glueType {
	case "bytes":
		return fmt.Sprintf("general_purpose::STANDARD.encode(&%s)", varName)
	default:
		return varName
	}
}

// callExprRust returns the Rust call expression.
func callExprRust(fn GlueFn) string {
	if fn.Call != "" {
		return fn.Call
	}
	return fn.Name
}

// argListRust returns the Rust argument list.
func argListRust(args []GlueArg) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.Name
	}
	return strings.Join(parts, ", ")
}

// pyArgs returns Python function argument list with type annotations.
func pyArgs(args []GlueArg) string {
	parts := make([]string, len(args))
	for i, a := range args {
		pyT, ok := pyTypeMap[a.Type]
		if !ok {
			pyT = "Any"
		}
		parts[i] = a.Name + ": " + pyT
	}
	return strings.Join(parts, ", ")
}

// pyKwargs returns keyword argument call to _call, e.g. ", data=data, buckets=buckets".
func pyKwargs(args []GlueArg) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, len(args))
	for i, a := range args {
		if a.Type == "bytes" {
			parts[i] = fmt.Sprintf("%s=base64.b64encode(%s).decode()", a.Name, a.Name)
		} else {
			parts[i] = fmt.Sprintf("%s=%s", a.Name, a.Name)
		}
	}
	return ", " + strings.Join(parts, ", ")
}

// pyReturnType returns the Python return type annotation.
func pyReturnType(t string) string {
	if v, ok := pyTypeMap[t]; ok {
		return v
	}
	return "Any"
}

// pyCast returns the Python cast expression for a return type.
func pyCast(t string) string {
	if v, ok := pyCastMap[t]; ok {
		if v == "" {
			return ""
		}
		return v
	}
	return ""
}

// title capitalises the first letter of s.
func title(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// buildGoFuncMap returns the template.FuncMap for Go server templates.
func buildGoFuncMap() template.FuncMap {
	return template.FuncMap{
		"goType":      goType,
		"isBytesType": isBytesType,
		"callExpr":    callExpr,
		"argList":     argList,
		"bytesExpr":   bytesExpr,
		"title":       title,
	}
}

// buildRustFuncMap returns the template.FuncMap for Rust server templates.
func buildRustFuncMap() template.FuncMap {
	return template.FuncMap{
		"rustDecode":   rustDecode,
		"rustEncode":   rustEncode,
		"callExprRust": callExprRust,
		"argListRust":  argListRust,
		"isBytesType":  isBytesType,
		"printf":       fmt.Sprintf,
	}
}

// buildPyFuncMap returns the template.FuncMap for Python client templates.
func buildPyFuncMap() template.FuncMap {
	return template.FuncMap{
		"pyArgs":       pyArgs,
		"pyKwargs":     pyKwargs,
		"pyReturnType": pyReturnType,
		"pyCast":       pyCast,
		"isBytesType":  isBytesType,
	}
}

// GenerateGoServer renders the Go server source into buildDir/server.go.
// importPath is the Go import path to use in the generated module (for library
// imports). For local package imports, importPath is the synthetic "user/<module>".
func GenerateGoServer(m GlueModuleConfig, buildDir string, importPath string) error {
	tmplSrc := serverGoTmpl

	// Load custom template if driver specifies one.
	if driver, ok, _ := Find(m.Lang); ok && driver.ServerTemplate != "" {
		data, err := os.ReadFile(driver.ServerTemplate)
		if err != nil {
			return fmt.Errorf("load server template %s: %w", driver.ServerTemplate, err)
		}
		tmplSrc = string(data)
	}

	tmpl, err := template.New("server_go").Funcs(buildGoFuncMap()).Parse(tmplSrc)
	if err != nil {
		return fmt.Errorf("parse Go server template: %w", err)
	}

	data := serverTemplateData{
		Module:     m.Module,
		Lang:       "go",
		Transport:  m.EffectiveTransport(),
		ImportPath: importPath,
		Fns:        m.Fns,
	}

	outPath := filepath.Join(buildDir, "server.go")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	return tmpl.Execute(f, data)
}

// GenerateRustServer renders the Rust server source into buildDir/src/main.rs.
// srcAbsPath is the absolute path to the user's .rs source file (for include!).
// The file is copied next to main.rs so the include! path is just the basename.
// Leave empty when the user's source is a crate directory (path dependency).
func GenerateRustServer(m GlueModuleConfig, buildDir string, srcAbsPath string) error {
	tmplSrc := serverRustTmpl

	if driver, ok, _ := Find(m.Lang); ok && driver.ServerTemplate != "" {
		data, err := os.ReadFile(driver.ServerTemplate)
		if err != nil {
			return fmt.Errorf("load server template %s: %w", driver.ServerTemplate, err)
		}
		tmplSrc = string(data)
	}

	tmpl, err := template.New("server_rust").Funcs(buildRustFuncMap()).Parse(tmplSrc)
	if err != nil {
		return fmt.Errorf("parse Rust server template: %w", err)
	}

	srcDir := filepath.Join(buildDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}

	// Copy the user's .rs file into buildDir/src/ so include! uses a simple local path.
	localSrcName := ""
	if srcAbsPath != "" {
		localSrcName = filepath.Base(srcAbsPath)
		destPath := filepath.Join(srcDir, localSrcName)
		data, err := os.ReadFile(srcAbsPath)
		if err != nil {
			return fmt.Errorf("read user source %s: %w", srcAbsPath, err)
		}
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("copy user source to build dir: %w", err)
		}
	}

	td := serverTemplateData{
		Module:    m.Module,
		Lang:      "rust",
		Transport: m.EffectiveTransport(),
		SrcFile:   localSrcName, // just the filename, same dir as main.rs
		Fns:       m.Fns,
	}

	outPath := filepath.Join(srcDir, "main.rs")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	return tmpl.Execute(f, td)
}

// GeneratePythonClient renders the Python client module into outDir/<module>.py
// and outDir/<module>.pyi.
func GeneratePythonClient(m GlueModuleConfig, outDir string) error {
	transport := m.EffectiveTransport()

	var tmplSrc string
	switch transport {
	case "unix_socket", "tcp":
		tmplSrc = clientSocketTmpl
	default:
		tmplSrc = clientStdioTmpl
	}

	// Load custom client template if driver specifies one.
	if driver, ok, _ := Find(m.Lang); ok && driver.ClientTemplate != "" {
		data, err := os.ReadFile(driver.ClientTemplate)
		if err != nil {
			return fmt.Errorf("load client template %s: %w", driver.ClientTemplate, err)
		}
		tmplSrc = string(data)
	}

	tmpl, err := template.New("client_py").Funcs(buildPyFuncMap()).Parse(tmplSrc)
	if err != nil {
		return fmt.Errorf("parse Python client template: %w", err)
	}

	exeSuffix := ""
	if runtime.GOOS == "windows" {
		exeSuffix = ".exe"
	}

	td := clientTemplateData{
		Module:    m.Module,
		Lang:      m.Lang,
		Transport: transport,
		ExeSuffix: exeSuffix,
		Fns:       m.Fns,
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	// Write .py
	pyPath := filepath.Join(outDir, m.Module+".py")
	f, err := os.Create(pyPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", pyPath, err)
	}
	if err := tmpl.Execute(f, td); err != nil {
		f.Close()
		return err
	}
	f.Close()

	// Write .pyi stub.
	return generatePyiStub(m, outDir)
}

// generatePyiStub writes a .pyi type stub alongside the .py client.
func generatePyiStub(m GlueModuleConfig, outDir string) error {
	var sb strings.Builder
	sb.WriteString("# Type stubs generated by molt glue. DO NOT EDIT.\n")
	sb.WriteString("from typing import Any\n\n")

	for _, fn := range m.Fns {
		retType := pyReturnType(fn.Returns)
		sb.WriteString(fmt.Sprintf("def %s(%s) -> %s: ...\n", fn.Name, pyArgs(fn.Args), retType))
	}

	pyiPath := filepath.Join(outDir, m.Module+".pyi")
	return os.WriteFile(pyiPath, []byte(sb.String()), 0o644)
}
