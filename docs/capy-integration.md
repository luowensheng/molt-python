# Capy × molt — Multi-Target Code Generation

Capy is a **configurable transpiler engine**: you write a small DSL source file once,
point it at a library that defines the grammar, and get clean output in any text format
— Python, TypeScript, Go, SQL, HTML, Blender scripts, Kubernetes YAML, and anything
else you can express as a template.

molt pairs with Capy in two complementary ways:

1. **Run target system** — molt's task runner defines *how to execute* each target's
   output (run a Blender script, serve an HTML page, exec a CLI binary, etc.)
2. **Transport Glue** — the Capy Go library is callable from Python via a `capy.molt.toml`
   glue module, so your Python code can transpile at runtime without shelling out

---

## What Capy Does

```
your DSL source                 your library (YAML or .capy)
──────────────────              ────────────────────────────
say "hello"           ──────►  function say
if x > 0                         args: [capture msg any]
    add x 1                      template: "print({{ .msg }})\n"
end
                                file_template: "{{ .body }}"
          ▼
    output (any language)
          ▼
    print("hello")
    if x > 0:
        x += 1
```

Key properties relevant to molt:

- **Same source, many targets** — swap the library, get a different language. One
  `.capy` file → Python, JS, SQL, HTML, Blender, whatever you define.
- **Sandboxed by construction** — only patterns in the library can appear in output.
  Great for AI-generated code that must stay in bounds.
- **Go library** — `github.com/luowensheng/capy` — one function call, no subprocess.
- **No built-in runner** — Capy writes text; something else must execute it.

That last point is exactly where molt steps in.

---

## Pattern 1 — Task-based targets

The simplest integration: use Capy as a code generator, molt tasks as the runner.

```
project/
  dsl/
    app.capy           ← your single source of truth
  targets/
    blender/
      lib.yaml         ← Blender Python API grammar
    frontend/
      lib.yaml         ← HTML + vanilla JS grammar
    cli/
      lib.yaml         ← Python Click grammar
  moltproject.toml
```

```toml
# moltproject.toml
[project]
name = "my-project"
lang = "python"

[tool.molt.tasks]
# ── Blender ────────────────────────────────────────────────────────────────────
blender-gen  = "capy run targets/blender/lib.yaml dsl/app.capy --out=.out/blender_script.py"
blender-run  = "blender --background --python .out/blender_script.py"
blender      = { depends = ["blender-gen", "blender-run"] }

# ── Frontend ───────────────────────────────────────────────────────────────────
frontend-gen = "capy run targets/frontend/lib.yaml dsl/app.capy --out=.out/index.html"
frontend-run = "python -m http.server 8000 --directory .out"
frontend     = { depends = ["frontend-gen", "frontend-run"] }

# ── CLI ────────────────────────────────────────────────────────────────────────
cli-gen      = "capy run targets/cli/lib.yaml dsl/app.capy --out=.out/cli.py"
cli-run      = "python .out/cli.py"
cli          = { depends = ["cli-gen", "cli-run"] }

# ── All targets ────────────────────────────────────────────────────────────────
gen-all = { depends = ["blender-gen", "frontend-gen", "cli-gen"] }
```

```bash
molt run blender     # generate Blender script → open Blender
molt run frontend    # generate HTML → serve locally
molt run cli         # generate CLI → run it
molt run gen-all     # generate all targets at once
```

### Installing Capy in the project

```bash
molt add capy-bin    # if a Python wrapper exists
# or add the binary globally:
molt tool install github.com/luowensheng/capy/cmd/capy@latest
```

Since Capy is a Go binary you can also pin it with a task:

```toml
[tool.molt.tasks]
install-capy = "go install github.com/luowensheng/capy/cmd/capy@latest"
```

---

## Pattern 2 — Transport Glue: call Capy from Python

Capy exposes a Go library (`github.com/luowensheng/capy`). With molt's Transport Glue
you can call it from Python with a single import — no subprocess, no shell, no temp files.

### The glue manifest

Create `capy.molt.toml` in your project:

```toml
# capy.molt.toml
lang      = "go"
src       = "github.com/luowensheng/capy"   # third-party Go module
transport = "stdio"

[[fn]]
name    = "run_file"
call    = "RunFile"        # orchestrator.Run wraps this; see adapter below
args    = [{ name = "library_path", type = "string" },
           { name = "script_path",  type = "string" }]
returns = "string"

[[fn]]
name    = "run_strings"
call    = "RunStrings"
args    = [{ name = "library_yaml", type = "string" },
           { name = "library_path", type = "string" },
           { name = "script_src",   type = "string" }]
returns = "string"
```

Because Capy's library API has richer types than the glue type system supports directly,
add a thin Go adapter file alongside the manifest:

```go
// capy_adapter.go  (sits next to capy.molt.toml)
package main

import (
    "github.com/luowensheng/capy/orchestrator"
)

// RunFile transpiles scriptPath using the library at libraryPath.
func RunFile(libraryPath, scriptPath string) (string, error) {
    return orchestrator.Run(libraryPath, scriptPath)
}

// RunStrings transpiles scriptSrc using libraryYAML.
// libraryPath is used only for resolving relative paths inside the library.
func RunStrings(libraryYAML, libraryPath, scriptSrc string) (string, error) {
    return orchestrator.RunStrings(libraryYAML, libraryPath, scriptSrc)
}
```

Update the manifest `src` to point at the adapter:

```toml
# capy.molt.toml
lang      = "go"
src       = "./capy_adapter.go"
transport = "stdio"
# (keep [[fn]] blocks as above)
```

After `molt sync`:

```python
# main.py — call Capy transpiler from Python with no subprocess
import capy

# Transpile from files on disk
output = capy.run_file("targets/blender/lib.yaml", "dsl/app.capy")

# Or inline library YAML + source string
output = capy.run_strings(LIBRARY_YAML, ".", SOURCE)

# Use the output however you like
with open(".out/blender_script.py", "w") as f:
    f.write(output)
```

### Why this matters

| Without glue | With glue |
|---|---|
| `subprocess.run(["capy", "run", ...])` | `capy.run_file(lib, src)` |
| Parses stdout, checks exit code | Returns string or raises `RuntimeError` |
| Binary must be on PATH | Binary embedded in project state |
| One process per call | Lazy-start subprocess reused for lifetime |

---

## Pattern 3 — Runtime multi-target generation

Combine both patterns: Python drives Capy through the glue module and then dispatches
to the right runner based on a config or CLI flag.

```python
# build.py
import capy
import subprocess
import sys
import os

TARGETS = {
    "blender": {
        "lib": "targets/blender/lib.yaml",
        "out": ".out/blender_script.py",
        "run": lambda f: ["blender", "--background", "--python", f],
    },
    "frontend": {
        "lib": "targets/frontend/lib.yaml",
        "out": ".out/index.html",
        "run": lambda f: ["python", "-m", "http.server", "8000", "--directory", ".out"],
    },
    "cli": {
        "lib": "targets/cli/lib.yaml",
        "out": ".out/cli.py",
        "run": lambda f: ["python", f],
    },
}

def build(target: str, run: bool = False):
    cfg = TARGETS[target]
    output = capy.run_file(cfg["lib"], "dsl/app.capy")
    os.makedirs(".out", exist_ok=True)
    with open(cfg["out"], "w") as f:
        f.write(output)
    print(f"✓ {target} → {cfg['out']}")
    if run:
        subprocess.run(cfg["run"](cfg["out"]))

if __name__ == "__main__":
    target = sys.argv[1] if len(sys.argv) > 1 else "cli"
    build(target, run=True)
```

```toml
[tool.molt.tasks]
blender  = "python build.py blender"
frontend = "python build.py frontend"
cli      = "python build.py cli"
all      = "python build.py blender && python build.py frontend && python build.py cli"
```

---

## Defining a Capy library for each target

A Capy library is a YAML file (or `.capy` file) that defines the grammar for your DSL
and how each statement maps to the target language. You write this once per target,
then your source DSL stays the same forever.

### Example: Blender target

Your DSL describes scene objects, materials, and render settings. The library maps
these to Blender Python API calls:

```yaml
# targets/blender/lib.yaml
extension: py
context: { imports: [], scene_objects: [] }

functions:
  scene:
    args:
      - { kind: literal, value: "scene" }
      - { kind: capture, name: name, type: string }
    block: { closer: end }
    template: |
      # Scene: {{ .name }}
      {{ .body }}
    run: |
      set context.current_scene name

  mesh:
    args:
      - { kind: literal, value: "mesh" }
      - { kind: capture, name: name, type: string }
      - { kind: literal, value: "type" }
      - { kind: capture, name: shape, type: string }
    template: |
      bpy.ops.mesh.primitive_{{ .shape | lower }}_add(location=(0,0,0))
      bpy.context.object.name = {{ .name | toQuoted }}

  material:
    args:
      - { kind: literal, value: "material" }
      - { kind: capture, name: obj, type: string }
      - { kind: capture, name: r, type: float }
      - { kind: capture, name: g, type: float }
      - { kind: capture, name: b, type: float }
    template: |
      mat = bpy.data.materials.new(name={{ .obj | toQuoted }})
      mat.diffuse_color = ({{ .r }}, {{ .g }}, {{ .b }}, 1.0)
      bpy.data.objects[{{ .obj | toQuoted }}].data.materials.append(mat)

  render:
    args:
      - { kind: literal, value: "render" }
      - { kind: literal, value: "to" }
      - { kind: capture, name: path, type: string }
    template: |
      bpy.context.scene.render.filepath = {{ .path | toQuoted }}
      bpy.ops.render.render(write_still=True)

file_template: |
  import bpy
  bpy.ops.object.select_all(action='SELECT')
  bpy.ops.object.delete()
  {{ .body -}}
```

**DSL source** (`dsl/app.capy`):

```
scene "my_render"
    mesh "Cube" type "cube"
    material "Cube" 0.8 0.3 0.1
    render to "/tmp/render.png"
end
```

**Generated output** (`→ blender --background --python`):

```python
import bpy
bpy.ops.object.select_all(action='SELECT')
bpy.ops.object.delete()
# Scene: my_render
bpy.ops.mesh.primitive_cube_add(location=(0,0,0))
bpy.context.object.name = "Cube"
mat = bpy.data.materials.new(name="Cube")
mat.diffuse_color = (0.8, 0.3, 0.1, 1.0)
bpy.data.objects["Cube"].data.materials.append(mat)
bpy.context.scene.render.filepath = "/tmp/render.png"
bpy.ops.render.render(write_still=True)
```

---

### Example: HTML/JS frontend target

```yaml
# targets/frontend/lib.yaml
extension: html
context: { title: "App", components: [], styles: [] }

functions:
  title:
    args:
      - { kind: capture, name: t, type: string }
    run: |
      set context.title t
    template: ""

  hero:
    args:
      - { kind: literal, value: "hero" }
      - { kind: capture, name: heading, type: string }
      - { kind: capture, name: sub, type: string }
    template: |
      <section class="hero">
        <h1>{{ .heading | unquote }}</h1>
        <p>{{ .sub | unquote }}</p>
      </section>

  button:
    args:
      - { kind: literal, value: "button" }
      - { kind: capture, name: label, type: string }
      - { kind: literal, value: "onclick" }
      - { kind: capture, name: fn, type: ident }
    template: |
      <button onclick="{{ .fn }}()">{{ .label | unquote }}</button>

file_template: |
  <!DOCTYPE html>
  <html><head><title>{{ .context.title }}</title></head>
  <body>
  {{ .body -}}
  </body></html>
```

---

### Example: CLI target (Python + Click)

```yaml
# targets/cli/lib.yaml
extension: py
context: { commands: [], imports: ["click"] }

functions:
  command:
    args:
      - { kind: literal, value: "command" }
      - { kind: capture, name: name, type: ident }
      - { kind: literal, value: "help" }
      - { kind: capture, name: doc, type: string }
    block: { closer: end }
    template: |
      @cli.command()
      def {{ .name }}():
          """{{ .doc | unquote }}"""
      {{ .body | indent 4 }}

  option:
    args:
      - { kind: literal, value: "option" }
      - { kind: capture, name: flag, type: string }
      - { kind: capture, name: type_, type: ident }
    template: |
      @click.option({{ .flag }}, type={{ .type_ }})

  print_:
    args:
      - { kind: literal, value: "print" }
      - { kind: capture, name: msg, type: any }
    template: "    click.echo({{ .msg }})\n"

file_template: |
  import click
  {{ range .context.imports }}import {{ . }}
  {{ end }}
  @click.group()
  def cli(): pass

  {{ .body }}
  if __name__ == "__main__":
      cli()
```

---

## One DSL, three targets

The real power: write your program logic once, let the target library define how it runs.

**Shared source** (`dsl/app.capy`):

```
# This exact file is fed to all three target libraries
command greet help "Print a greeting"
    option "--name" str
    print "Hello, {name}"
end
```

```bash
molt run gen-all   # generates .out/blender_script.py + .out/index.html + .out/cli.py
molt run cli       # runs the CLI: python .out/cli.py greet --name world
molt run frontend  # serves the HTML page
molt run blender   # opens Blender with the generated script
```

---

## Pattern 4 — AI-assisted DSL authoring

Capy's sandboxing property is especially useful with AI code generation: the model
can only emit patterns the library allows. Pair with molt's MCP server for a full
agentic workflow.

```python
# ai_codegen.py — molt MCP tool calls Capy on model output
import capy

def generate_from_prompt(prompt: str, target: str) -> str:
    """Call an LLM to write a Capy DSL source, then transpile it."""
    dsl_source = call_llm(prompt, context=read_library(target))
    return capy.run_strings(
        library_yaml=read_library(target),
        library_path=f"targets/{target}/lib.yaml",
        script_src=dsl_source,
    )
```

The model can only generate valid output for the target language — `DROP TABLE`,
`eval()`, `__import__()`, and other dangerous patterns simply don't parse.

---

## Project layout recommendation

```
my-project/
  dsl/
    app.capy              ← single source of truth (your DSL)
    app.test.capy         ← test cases
  targets/
    blender/
      lib.yaml            ← Blender Python API grammar
      README.md           ← what this target does, how to run
    frontend/
      lib.yaml            ← HTML + CSS + JS grammar
    cli/
      lib.yaml            ← Python Click grammar
    sql/
      lib.yaml            ← PostgreSQL DDL grammar
  capy.molt.toml          ← Transport Glue: Python → Capy Go library
  capy_adapter.go         ← thin adapter for glue
  build.py                ← runtime dispatcher (optional)
  moltproject.toml        ← tasks: gen/run per target
  pyproject.toml
```

```toml
# moltproject.toml (complete)
[project]
name = "my-project"
lang = "python"

[tool.molt.tasks]
# Code generation
gen-blender  = "capy run targets/blender/lib.yaml  dsl/app.capy --out=.out/blender_script.py"
gen-frontend = "capy run targets/frontend/lib.yaml dsl/app.capy --out=.out/index.html"
gen-cli      = "capy run targets/cli/lib.yaml      dsl/app.capy --out=.out/cli.py"
gen-sql      = "capy run targets/sql/lib.yaml      dsl/app.capy --out=.out/schema.sql"
gen-all      = { depends = ["gen-blender", "gen-frontend", "gen-cli", "gen-sql"] }

# Run targets
run-blender  = { depends = ["gen-blender"],  cmd = "blender --background --python .out/blender_script.py" }
run-frontend = { depends = ["gen-frontend"], cmd = "python -m http.server 8000 --directory .out" }
run-cli      = { depends = ["gen-cli"],      cmd = "python .out/cli.py" }
run-sql      = { depends = ["gen-sql"],      cmd = "psql $DATABASE_URL < .out/schema.sql" }

# Shorthands
blender  = "molt run run-blender"
frontend = "molt run run-frontend"
cli      = "molt run run-cli"
sql      = "molt run run-sql"
```

---

## Adding Capy to an existing molt project

```bash
# 1. Install Capy binary globally (available to all molt projects)
molt tool install github.com/luowensheng/capy/cmd/capy@latest

# 2. Verify
capy version

# 3. Add a library file and a DSL source file
mkdir -p targets/cli
# ...write targets/cli/lib.yaml...
# ...write dsl/app.capy...

# 4. Test transpilation
capy run targets/cli/lib.yaml dsl/app.capy

# 5. Add tasks to moltproject.toml and run
molt run gen-cli
molt run cli
```

For the Python library integration via Transport Glue:

```bash
# 5. Create capy.molt.toml + capy_adapter.go (see Pattern 2 above)
molt sync       # compiles the Capy Go server + generates capy.py
python -c "import capy; print(capy.run_file('targets/cli/lib.yaml', 'dsl/app.capy'))"
```

---

## Summary

| Capability | How molt provides it |
|---|---|
| Run Blender with generated script | `molt run blender` task → `blender --background --python` |
| Serve generated frontend | `molt run frontend` task → `python -m http.server` |
| Execute generated CLI | `molt run cli` task → `python .out/cli.py` |
| Apply generated SQL | `molt run sql` task → `psql < .out/schema.sql` |
| Call Capy from Python code | `capy.molt.toml` Transport Glue → `import capy; capy.run_file(...)` |
| Generate all targets | `molt run gen-all` (parallel task dependencies) |
| AI-safe code generation | Capy sandboxing + molt MCP server |
| One DSL → many outputs | Swap the `lib.yaml`, keep `dsl/app.capy` |

The combination is: **Capy defines what your DSL means, molt defines how to run it.**
