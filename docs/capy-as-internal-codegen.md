# Using Capy as molt's Internal Code Generator

Capy can replace (or sit alongside) molt's current `text/template`-based code
generation infrastructure. The pay-off is concentrated in the **glue driver system**:
instead of shipping raw Go template files that require custom `FuncMap` helpers baked
into molt's binary, driver authors write a YAML Capy library that molt evaluates at
runtime — no recompilation, no Go knowledge required.

---

## Where code generation lives today

| Location | What it generates | How |
|---|---|---|
| `internal/glue/templates/server_go.tmpl` | Go JSON-RPC server binary | `text/template` + custom FuncMap |
| `internal/glue/templates/server_rust.tmpl` | Rust JSON-RPC server binary | `text/template` + custom FuncMap |
| `internal/glue/templates/client_stdio.py.tmpl` | Python stdio client module | `text/template` + custom FuncMap |
| `internal/glue/templates/client_socket.py.tmpl` | Python socket client module | `text/template` + custom FuncMap |
| `internal/native/kernel_codegen.go` | C PyInit/PyMethodDef glue + `.pyi` stubs | `strings.Builder` + `fmt.Fprintf` |
| `internal/glue/codegen.go` | `.pyi` stubs for glue modules | `fmt.Sprintf` |

The templates are powerful but the FuncMap helpers (`goType`, `argList`, `callExpr`,
`rustDecode`, `pyArgs`, `pyKwargs`, `isBytesType`, …) are Go code compiled into the
binary. Adding a new language driver today means sending a PR to molt. The driver YAML
can point to a custom `server_template` file — but that file is still a raw Go
`text/template`, which requires knowing Go's template syntax and registering helpers
inside molt's source.

---

## What Capy changes

Capy's Go API is three lines:

```go
import "github.com/luowensheng/capy"

lib, err := capy.NewLibraryYAML(librarySrc)  // compile library once; reuse forever
output, err := lib.Run(capySrc)               // thread-safe; each call gets fresh context
```

`*Library` is safe to cache across builds. `Run` is safe for concurrent callers.

The driver's `server_template` (or a new `server_library` field) points to a `.yaml`
Capy library file instead of a `.tmpl` Go template. The library file contains both the
grammar (what statements are valid) and the templates (what code they emit). No Go
FuncMap, no binary change.

---

## The input serialization bridge

Go templates receive a struct directly. Capy receives a text string. The bridge is a
simple serializer that turns `GlueModuleConfig` into a Capy DSL source:

```go
// internal/glue/codegen.go

func moduleToCapySrc(m GlueModuleConfig) string {
    var b strings.Builder
    fmt.Fprintf(&b, "module %q\n", m.Module)
    fmt.Fprintf(&b, "transport %q\n", transport(m)) // "stdio", "unix_socket", "tcp"
    if m.ImportPath != "" {
        fmt.Fprintf(&b, "import_path %q\n", m.ImportPath)
    }
    if m.SrcFile != "" {
        fmt.Fprintf(&b, "src_file %q\n", filepath.Base(m.SrcFile))
    }
    for _, fn := range m.Fns {
        fmt.Fprintf(&b, "fn %s", fn.Name)
        if fn.Call != "" {
            fmt.Fprintf(&b, " call %q", fn.Call)
        }
        for _, arg := range fn.Args {
            fmt.Fprintf(&b, " %s:%s", arg.Name, arg.Type)
        }
        if fn.Returns != "" {
            fmt.Fprintf(&b, " -> %s", fn.Returns)
        }
        b.WriteString("\n")
    }
    return b.String()
}
```

Resulting source (for `stats.molt.toml`):
```
module "stats"
transport "stdio"
fn mean data:[]f64 -> f64
fn stddev data:[]f64 -> f64
fn histogram data:[]f64 buckets:i32 -> []i32
fn compress payload:bytes -> bytes
```

This is the only Go code that knows the struct shape. Everything after this point is
defined by the library YAML.

---

## What the library looks like

Here is the current `server_go.tmpl` rewritten as a Capy library. Compare the two:

### Current: `server_go.tmpl` (text/template, requires FuncMap in Go)

```go
// In codegen.go — Go code, compiled into binary
funcMap["goType"] = func(t string) string { return goTypeMap[t] }
funcMap["isBytesType"] = func(t string) bool { return t == "bytes" || t == "[]byte" }
funcMap["callExpr"] = func(fn GlueFn) string {
    if m.ImportPath != "" { return "user." + fn.Name }
    return fn.Name
}
funcMap["argList"] = func(args []GlueArg) string { /* complex base64 handling */ }
```

```
{{- range .Fns}}
case "{{.Name}}":
    {{- if .Args}}
    var a struct {
        {{- range .Args}}
        {{title .Name}} {{goType .Type}} `json:"{{.Name}}"`
        {{- end}}
    }
    ...
    {{- if isBytesType .Returns}}
    out := {{callExpr . }}({{argList .Args}})
    return _Resp{Result: base64.StdEncoding.EncodeToString(out[:])}
    {{- else}}
    return _Resp{Result: {{callExpr . }}({{argList .Args}})}
    {{- end}}
{{- end}}
```

### New: `libraries/server_go.yaml` (Capy library, pure YAML, zero Go code)

```yaml
# libraries/server_go.yaml  — Capy library for Go stdio/socket server generation
extension: go
context:
  module: ""
  transport: "stdio"
  import_path: ""
  src_file: ""
  fns: []
  use_import: false

types:
  GlueType:
    options:
      - i32
      - i64
      - f32
      - f64
      - bool
      - string
      - bytes
      - "[]i32"
      - "[]f64"
      - "[]string"
      - json

functions:
  module:
    args:
      - { kind: capture, name: name, type: string }
    run: |
      set context.module name
    template: ""

  transport:
    args:
      - { kind: capture, name: t, type: string }
    run: |
      set context.transport t
    template: ""

  import_path:
    args:
      - { kind: capture, name: p, type: string }
    run: |
      set context.import_path p
      set context.use_import true
    template: ""

  src_file:
    args:
      - { kind: capture, name: f, type: string }
    run: |
      set context.src_file f
    template: ""

  fn:
    priority: 0
    args:
      - { kind: capture, name: name,    type: ident }
      - { kind: capture, name: argspec, type: json }   # [{name,type},...] built by args parser
      - { kind: literal, value: "->" }
      - { kind: capture, name: returns, type: GlueType }
    run: |
      append context.fns { name: name, argspec: argspec, returns: returns }
    template: ""

  fn_void:
    # fn without -> return type
    priority: -1
    args:
      - { kind: capture, name: name,    type: ident }
      - { kind: capture, name: argspec, type: json }
    run: |
      append context.fns { name: name, argspec: argspec, returns: "" }
    template: ""

file_template: |
  // Code generated by molt glue. DO NOT EDIT.
  // Module: {{ .context.module }}  Transport: {{ .context.transport }}
  package main

  import (
  	"bufio"
  	"encoding/base64"
  	"encoding/json"
  	"os"
  {{- if ne .context.transport "stdio" }}
  	"fmt"
  	"net"
  {{- end }}
  {{- if .context.use_import }}
  	user "{{ .context.import_path }}"
  {{- end }}
  )

  type _Req  struct { Fn string `json:"fn"`; Args json.RawMessage `json:"args"` }
  type _Resp struct { Result interface{} `json:"result,omitempty"`; Error string `json:"error,omitempty"` }

  func _dispatch(req _Req) _Resp {
  	switch req.Fn {
  {{ range .context.fns -}}
  	case "{{ .name }}":
  		var a struct {
  		{{ range .argspec -}}
  			{{ .name | title }} {{ .type | goNativeType }} `json:"{{ .name }}"`
  		{{ end -}}
  		}
  		if err := json.Unmarshal(req.Args, &a); err != nil { return _Resp{Error: err.Error()} }
  		{{ if eq .returns "bytes" -}}
  		out := {{ if $.context.use_import }}user.{{ end }}{{ .name }}({{ .argspec | goArgList }})
  		return _Resp{Result: base64.StdEncoding.EncodeToString(out[:])}
  		{{- else if ne .returns "" -}}
  		return _Resp{Result: {{ if $.context.use_import }}user.{{ end }}{{ .name }}({{ .argspec | goArgList }})}
  		{{- else -}}
  		{{ if $.context.use_import }}user.{{ end }}{{ .name }}({{ .argspec | goArgList }})
  		return _Resp{}
  		{{- end }}
  {{ end -}}
  	default:
  		return _Resp{Error: "unknown function: " + req.Fn}
  	}
  }
  {{ .body -}}
```

Two things to note: the Go-level helpers `goNativeType` and `goArgList` would need to be
registered as Capy library helpers — either as built-in additions, or expressed in the
library's `run:` snippets building up a context map. This is the one rough edge
(detailed below).

---

## Integration into `codegen.go`

```go
// internal/glue/codegen.go

//go:embed libraries/server_go.yaml
var serverGoLibSrc string

//go:embed libraries/server_rust.yaml
var serverRustLibSrc string

//go:embed libraries/client_stdio.yaml
var clientStdioLibSrc string

//go:embed libraries/client_socket.yaml
var clientSocketLibSrc string

// compiled once at package init; reused across all builds in the process
var (
    builtinLibs   sync.Once
    goServerLib   *capy.Library
    rustServerLib *capy.Library
    stdioClientLib *capy.Library
    socketClientLib *capy.Library
)

func initLibs() {
    builtinLibs.Do(func() {
        goServerLib, _   = capy.NewLibraryYAML(serverGoLibSrc)
        rustServerLib, _ = capy.NewLibraryYAML(serverRustLibSrc)
        stdioClientLib, _ = capy.NewLibraryYAML(clientStdioLibSrc)
        socketClientLib, _ = capy.NewLibraryYAML(clientSocketLibSrc)
    })
}

func generateServerSource(m GlueModuleConfig, driver GlueDriver, buildDir string) error {
    initLibs()

    var lib *capy.Library

    switch {
    case driver.ServerTemplate != "":
        // Driver-provided library (the hot-swappable path)
        data, err := os.ReadFile(driver.ServerTemplate)
        if err != nil { return err }
        lib, err = capy.NewLibraryYAML(string(data))
        if err != nil { return fmt.Errorf("load driver library %s: %w", driver.ServerTemplate, err) }
    case m.Lang == "rust":
        lib = rustServerLib
    default:
        lib = goServerLib
    }

    output, err := lib.Run(moduleToCapySrc(m))
    if err != nil { return fmt.Errorf("generate server %s: %w", m.Module, err) }

    outFile := filepath.Join(buildDir, m.Module+"_server_gen.go") // or .rs
    return os.WriteFile(outFile, []byte(output), 0o644)
}
```

The `sync.Once` means library compilation (YAML parse + grammar compile) happens once
per molt process, not once per module. Each `lib.Run()` call gets a fresh context.

---

## The glue driver system benefit

Today a user adding a Nim driver writes:

```yaml
# ~/.molt/glue-drivers.yaml
- lang: nim
  build_cmd: "nim c -d:release -o:{output} {server_file}"
  server_template: "~/.molt/glue-templates/nim-server.nim.tmpl"
```

That `.tmpl` file is a Go `text/template`. To use the `goType`-equivalent for Nim, the
author must either embed the mapping inline with verbose `{{if eq .Type "i32"}}int{{else if ...`
chains, or open a PR to molt to add Nim helpers to the FuncMap.

With Capy, the author writes:

```yaml
# ~/.molt/glue-drivers.yaml
- lang: nim
  build_cmd: "nim c -d:release -o:{output} {server_file}"
  server_template: "~/.molt/glue-templates/nim-server.yaml"  # Capy library
```

```yaml
# nim-server.yaml  (lives on disk, never touches molt source)
extension: nim
context:
  type_map:
    i32: int32
    i64: int64
    f32: float32
    f64: float64
    bool: bool
    string: string
    bytes: seq[byte]
    "[]i32": seq[int32]
    "[]f64": seq[float64]
    "[]string": seq[string]
    json: JsonNode
  fns: []
  module: ""

functions:
  module:
    args: [{ kind: capture, name: name, type: string }]
    run: set context.module name
    template: ""

  fn:
    args:
      - { kind: capture, name: name,    type: ident }
      - { kind: capture, name: argspec, type: json }
      - { kind: literal, value: "->" }
      - { kind: capture, name: returns, type: ident }
    run: append context.fns { name: name, argspec: argspec, returns: returns }
    template: ""

file_template: |
  import json, os, strutils
  {{ range .context.fns }}
  proc {{ .name }}({{ range .argspec }}{{ .name }}: {{ index $.context.type_map .type }}{{ end }}): {{ index .context.type_map .returns }} =
    discard  # user-implemented
  {{ end }}
```

**The author controls everything with YAML.** No Go knowledge required. No molt PR.

---

## The type mapping problem

One subtlety: `goType`, `rustDecode`, and `pyArgs` are Go functions that take a glue
type name and return target-language syntax. Capy's template engine doesn't have
arbitrary Go function calls.

Two solutions:

**Option A — Context lookup** (works today, no Capy changes needed):
Store the type map in `context.type_map` and use `index` in the template:

```yaml
context:
  type_map:
    i32: int32
    string: string
    bytes: string  # base64 on wire; Go type is string for transmission
    ...
```

```
{{ index .context.type_map .type }}
```

**Option B — Pre-expand types in the serializer** (cleanest):
Instead of emitting `fn mean data:[]f64 -> f64`, emit the target-language types
directly into the Capy source when using built-in libraries:

```go
func moduleToCapySrcForGo(m GlueModuleConfig) string {
    // expand glue types to Go types before serializing
    ...
    fmt.Fprintf(&b, "fn %s %s:%s -> %s\n",
        fn.Name, arg.Name, goTypeMap[arg.Type], goTypeMap[fn.Returns])
}
```

The Capy library then just passes the type through without mapping. Custom driver
libraries get the glue types (Option A) because they define their own mapping.

Option B is the pragmatic path for built-in libraries; Option A is the extensible path
for user-defined driver libraries.

---

## Kernel codegen

The C glue generator in `internal/native/kernel_codegen.go` is pure `fmt.Fprintf`
today. It generates three kinds of output: `glue.c`, `glue-dlopen.c`, and `.pyi` stubs.

Each maps cleanly to a Capy library:

```
mymath.molt.toml  →  moduleToCapySrc()  →  Capy "c-kernel" library  →  glue.c
```

The DSL source would look like:

```
module "fastmath"
fn multiply a:i32 b:i32 -> i32
fn dot_product a:[]f64 b:[]f64 -> f64
fn norm v:[]f64 -> f64
```

The library generates the `PyInit_fastmath`, `PyMethodDef`, and `PyArg_ParseTuple`
boilerplate from the type table. The kernel `.pyi` library generates the Python stub.

This makes kernel codegen **fully overridable without recompiling molt**: a project
that needs exotic PyArg parsing can point at a custom kernel library.

---

## What stays as text/template

Not everything should move. `text/template` is the right tool when:

- The template is a simple string with a handful of `{{.Field}}` substitutions
  (e.g. the Python shim: one `fmt.Sprintf` is clearer than a library file)
- The input struct is too complex to serialize to a clean DSL
- The code path is on the critical hot path and every nanosecond counts

Good candidates to keep as `text/template` or `fmt.Sprintf`:
- Python/Mojo shim generation (4-line shell scripts)
- `go.mod` / `Cargo.toml` generation (2–3 key/value substitutions)
- Simple scaffold files in `molt init`

---

## `molt init` scaffolding

`molt init` is a clean new use case. Each template is a Capy library; the user's
answers to `molt init` prompts become the DSL source:

```go
// Build source from prompt answers
src := fmt.Sprintf(`
name %q
python %q
description %q
task test %q
task lint %q
`, answers.Name, answers.Python, answers.Desc, answers.TestCmd, answers.LintCmd)

// Load and run the scaffold library
lib, _ := capy.NewLibraryFromFile("~/.molt/templates/default/pyproject.yaml")
pyprojectTOML, _ := lib.Run(src)
```

Each scaffold (default, FastAPI, data-science, CLI) is a `.yaml` Capy library file
that users can customize without touching Go code. `molt init --template mytemplate`
loads from `~/.molt/templates/mytemplate/`.

---

## Adding Capy to molt's `go.mod`

```bash
# In the molt repo root
go get github.com/luowensheng/capy@latest
```

That's the only infrastructure change. The library compiles into the molt binary with
no external dependencies (Capy itself has none beyond Go stdlib).

**Binary size impact**: Capy is a pure-Go library. Estimate: +300–500 KB on the final
binary (Go's linker strips unused code).

---

## Migration order

1. **`go get github.com/luowensheng/capy`** — add dependency
2. **Driver custom templates** — detect `.yaml`/`.capy` extension on `server_template`;
   use `capy.NewLibraryYAML` instead of `text/template`. Zero breaking changes; `.tmpl`
   files keep working via the existing path.
3. **Built-in glue libraries** — convert `server_go.tmpl` → `libraries/server_go.yaml`,
   etc. Remove the FuncMap helpers from `codegen.go`. Tests stay green.
4. **Kernel codegen** — convert `emitGlueC`/`emitPyiStub` to Capy libraries.
5. **`molt init` scaffolding** — new Capy-native template system.

Steps 2 and 3 are independent and can land separately. Steps 4 and 5 are fully
additive (no existing code removed until the new path is tested).

---

## Summary

| | Today | With Capy |
|---|---|---|
| Custom language driver | Write a `.tmpl` Go template; register helpers via PR to molt | Write a `.yaml` Capy library; point `server_template` at it |
| Type mapping | Go `map[string]string` in `codegen.go` | `context.type_map` in the library YAML |
| New transport protocol | Fork the `.tmpl` file + add Go conditionals | Write a new library YAML |
| Kernel codegen extensibility | Hard-coded `fmt.Fprintf` | Overridable Capy library per project |
| `molt init` scaffolding | Compiled-in template strings | User-customizable YAML libraries |
| Dependency added | — | `github.com/luowensheng/capy` |
| Binary size increase | — | ~400 KB |
