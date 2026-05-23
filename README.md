# molt — hermetic Python toolchain

[![CI](https://github.com/luowensheng/molt-python/actions/workflows/ci.yml/badge.svg)](https://github.com/luowensheng/molt-python/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/luowensheng/molt-python)](https://github.com/luowensheng/molt-python/releases)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)

---

## Installation

Download the latest binary for your platform from [GitHub Releases](https://github.com/luowensheng/molt-python/releases/latest):

```bash
# macOS (Apple Silicon)
curl -Lo molt https://github.com/luowensheng/molt-python/releases/latest/download/molt-darwin-arm64
chmod +x molt && sudo mv molt /usr/local/bin/

# macOS (Intel)
curl -Lo molt https://github.com/luowensheng/molt-python/releases/latest/download/molt-darwin-amd64
chmod +x molt && sudo mv molt /usr/local/bin/

# Linux (x86-64)
curl -Lo molt https://github.com/luowensheng/molt-python/releases/latest/download/molt-linux-amd64
chmod +x molt && sudo mv molt /usr/local/bin/

# Linux (ARM64)
curl -Lo molt https://github.com/luowensheng/molt-python/releases/latest/download/molt-linux-arm64
chmod +x molt && sudo mv molt /usr/local/bin/

# Verify checksum (optional)
curl -Lo molt.sha256 https://github.com/luowensheng/molt-python/releases/latest/download/molt-darwin-arm64.sha256
sha256sum -c molt.sha256
```

**From source** (requires Go 1.22+):
```bash
git clone https://github.com/luowensheng/molt-python.git
cd molt-python && go build -o molt . && sudo mv molt /usr/local/bin/
```

---

## What it is

A complete, opinionated tour of every feature molt ships, with examples
and rationale. This is the document you read once to know what molt
*does*; for command-by-command precision use [`docs/MANUAL.md`](docs/MANUAL.md);
for runnable example projects see [`demos/`](demos/).

## Table of contents

- [What molt is and why it exists](#what-molt-is-and-why-it-exists)
- [The core model: hermetic projects, shared store](#the-core-model-hermetic-projects-shared-store)
- [Quick start](#quick-start)
- [Project lifecycle](#project-lifecycle)
- [Dependency management](#dependency-management)
- [The global package store](#the-global-package-store)
- [Python version management](#python-version-management)
- [Tasks](#tasks)
  - [`moltproject.toml` — tasks for non-Python projects](#moltprojecttoml--tasks-for-non-python-projects)
- [Templates](#templates)
- [Multi-project tracking](#multi-project-tracking)
- [Tool registry: global CLI shims](#tool-registry-global-cli-shims)
- [Native modules — overview](#native-modules--overview)
- [Cython modules](#cython-modules)
- [Rust + PyO3 modules](#rust--pyo3-modules)
- [Kernel modules: any C-ABI language](#kernel-modules-any-c-abi-language)
- [Kernel builders: per-language compile recipes](#kernel-builders-per-language-compile-recipes)
- [Pre-built `.so` binding](#pre-built-so-binding)
- [`[[tool.molt.native]]` — external module recipes](#tool-moltnative--external-module-recipes)
- [Runtime environment: `extra_paths`](#runtime-environment-extra_paths)
- [Environment variables](#environment-variables)
- [Build & distribution: single-binary release](#build--distribution-single-binary-release)
- [Integrity & verification](#integrity--verification)
- [Editor integration](#editor-integration)
- [Diagnostics](#diagnostics)
- [Why this matters: developers and enterprise](#why-this-matters-developers-and-enterprise)
- [Comparison to other tools](#comparison-to-other-tools)
- [Roadmap](#roadmap)

---

## What molt is and why it exists

**molt is a hermetic Python toolchain.** It manages Python versions,
dependencies, native code compilation, runtime environments, and binary
distribution under a single CLI, with three guiding principles:

1. **Share what's shareable.** Packages live once on disk in
   `~/.molt/pkg/`, indexed by `(name, version, abi-tag)` and reused
   across every project. Two projects depending on `numpy 1.26.4` for
   `cp312-darwin-arm64` get the same files; the second project pays
   zero install cost.
2. **Isolate what's project-specific.** A project's environment is a
   single small JSON file (`.molt/syspath.json`) that lists the store
   paths to put on `PYTHONPATH`. There's no per-project `.venv`, no
   activation, no `source` — `molt run` just sets the env and execs.
3. **Native code is a first-class citizen — in both directions.** molt
   has six distinct pipelines for native integration:
   - **Header-driven C extensions** (`[[tool.molt.c.modules]]`): point at a
     `.h` file and molt auto-parses the function signatures, generates the
     entire CPython `PyInit_` / `PyMethodDef` glue, and compiles a real
     `.so` via `zig cc` — no Cython, no cffi, no manual binding.
   - **Cython** (`.pyx`): hot loops and C-API integration with Python syntax.
   - **Rust + PyO3** (`.rs`): type-safe complex APIs with full async support.
   - **Kernel modules** (`.molt.toml` + any C-ABI source): Zig, C, C++,
     Odin, Nim, Assembly — drop a file, get a Python module.
   - **Pre-built kernels**: bind vendor `.so` libraries with a manifest only.
   - **External native recipes** (`[[tool.molt.native]]`): wrap an existing
     Cargo / CMake / autotools project.

   The relationship is **bidirectional**: Python calling C extensions, but
   also C and Mojo code calling back into Python packages via the project's
   `PYTHONPATH` — `PyImport_ImportModule("numpy")` in C, or
   `Python.import_module("numpy")` in Mojo, and the packages are there.

The implication: the same molt project source supports the entire
lifecycle from **`molt init`** (start a new project) through
**`molt run`** (develop and execute) through **`molt build`** (produce
a single self-installing binary that runs on any matching machine
without Python installed).

---

## The core model: hermetic projects, shared store

```
~/.molt/                                    ← one global location
├── python/3.12.3/                           ← Python interpreters managed by uv
├── python/3.13.1/
├── pkg/                                     ← shared package store
│   ├── numpy/1.26.4/cp312-darwin-arm64/     ← unpacked once, used everywhere
│   ├── numpy/1.26.4/cp313-darwin-arm64/
│   ├── requests/2.32.3/py3-none-any/        ← pure-Python: same files for all ABIs
│   └── ...
├── native/<hash>/<module>.so                ← compiled native extensions, content-keyed
├── toolchains/zig/0.14.1/zig                ← auto-installed compiler toolchains
├── projects/myapp-abc123/                   ← per-project state mirrored centrally
├── kernel-builders.yaml                     ← per-extension kernel build recipes
├── native-presets.yaml                      ← named [[tool.molt.native]] presets
└── bin/                                     ← global tool shims (~/.molt/bin on PATH)

myproject/                                   ← one per project
├── pyproject.toml                            ← stays unchanged; standard PEP 621 metadata
├── uv.lock                                   ← deterministic dependency resolution
└── .molt/
    ├── syspath.json                          ← {"interpreter": "...", "syspath": [".../numpy/...", ...]}
    ├── sitecustomize.py                       ← processes .pth files in each store dir
    └── bin/<console-scripts>                  ← ruff, pytest, etc. — absolute paths baked in
```

Two facts follow:

1. **A `molt sync` is a graph operation, not a download fest.** It
   reads `uv.lock`, looks up every package in `~/.molt/pkg/`, downloads
   missing ones once, and writes a 30-line JSON file. Adding a fifth
   FastAPI project to your machine takes seconds; the wheels are
   already there.
2. **Switching Python versions is fast.** `molt python use 3.13`
   re-resolves the syspath against the new ABI and writes a new
   syspath.json. No venv to recreate.

---

## Quick start

```sh
$ molt init myapp
$ cd myapp
$ molt add fastapi uvicorn[standard]
$ molt run python -c 'import fastapi; print(fastapi.__version__)'
0.115.0
$ molt task add dev "uvicorn myapp.main:app --reload"
$ molt run dev
```

That's everything. No venv created, no shell to activate, no PATH
manipulation. `molt run dev` execs `uvicorn` from `~/.molt/bin` with
the project's syspath set as env, and exits when uvicorn does.

---

## Project lifecycle

| Command | What it does |
|---|---|
| `molt init [name]` | Scaffolds a new project via `uv init`. Optional `--template` to apply a saved template. |
| `molt init --python 3.13 myapp` | Pin Python version at creation. |
| `molt adopt` | Generate `molt.yaml` for an existing project that already has `pyproject.toml`. |
| `molt sync` | Materialise `uv.lock` into `~/.molt/pkg/`, write `.molt/syspath.json`. |
| `molt sync --frozen` | Use existing lock as-is, no resolution. |
| `molt sync --refresh` | Force-reinstall every package, even those in cache. |
| `molt info` | Project + environment summary. |

`molt sync` is the spine. Almost every other command implicitly runs
it when needed. Manual invocation matters mostly when you want to
re-resolve after editing `pyproject.toml` outside molt, or in CI.

---

## Dependency management

molt delegates resolution to [`uv`](https://github.com/astral-sh/uv) —
which is fast, correct, and the same code path that materialises the
store. Adding deps is a thin wrapper:

```sh
$ molt add pydantic 'sqlalchemy>=2.0' pytest --dev
$ molt remove pydantic
$ molt lock                # regenerate uv.lock without installing
$ molt tree                # print the resolved dep graph
$ molt uv pip list         # pass-through to uv for advanced cases
```

`molt add` does three things:
1. Edits `pyproject.toml` (delegates to `uv add`).
2. Re-runs `uv lock`.
3. Re-syncs into `~/.molt/pkg/` and updates `.molt/syspath.json`.

You can do those steps manually with `molt uv` if you need fine-grained
control.

---

## The global package store

Wheels go in once per `(name, version, abi)`. The hash key is the
**ABI tag** computed from your chosen Python — so `cp311-darwin-arm64`
and `cp313-darwin-arm64` get separate copies of `numpy`, but `requests`
(`py3-none-any`) is one set of files shared across every project on
the machine, regardless of Python version.

### Layout

```
~/.molt/pkg/
└── numpy/
    └── 1.26.4/
        └── cp312-cp312-macosx_11_0_arm64/
            ├── numpy/                 ← the actual package
            ├── numpy-1.26.4.dist-info/
            └── .molt/installed.json    ← version, files, dist-info
```

Per-project `.molt/syspath.json`:

```json
{
  "interpreter": "/Users/me/.molt/python/3.12.3/bin/python",
  "syspath": [
    "/Users/me/.molt/pkg/numpy/1.26.4/cp312-cp312-macosx_11_0_arm64",
    "/Users/me/.molt/pkg/requests/2.32.3/py3-none-any",
    "..."
  ]
}
```

When `molt run` executes Python, it:
1. Sets `PYTHONPATH` to those dirs joined.
2. Strips `VIRTUAL_ENV`, `PYTHONHOME`, any inherited `PYTHONPATH`.
3. Prepends `.molt/bin` (console scripts) to `PATH`.
4. `exec()`s the interpreter — no fork, no venv lookup.

### Garbage collection

```sh
$ molt gc --dry-run         # preview
$ molt gc                   # remove store dirs no project references
```

molt tracks every project that's been synced (in `~/.molt/projects/`).
GC walks `~/.molt/pkg/` and removes any directory whose
`(name, version, abi)` is referenced by zero registered projects. Safe
even if the project's source dir was deleted — GC will then offer to
purge the project's metadata too.

---

## Python version management

```sh
$ molt python list               # installed + system Pythons
$ molt python install 3.13       # download via uv
$ molt python use 3.13           # pin in current project
$ molt python use 3.13 --global  # set global default
$ molt python which              # active interpreter path
$ molt python audit              # find every Python on the machine
$ molt python isolation-check    # verify project syspath has no leaks
```

`molt python use` rewrites `.python-version` AND triggers a re-sync —
the new ABI tag means different store paths. Switching Python versions
is one command.

`molt python audit` is uniquely useful for debugging "why is `python`
on PATH the wrong one." It prints every Python it can find with
version, interpreter type, ABI tag, and source.

`molt python isolation-check` validates that nothing on `sys.path` in
`molt run python` is pointed at a system site-packages or stale venv.

---

## Tasks

```toml
# pyproject.toml
[tool.molt.tasks]
dev      = "uvicorn myapp.main:app --reload"
test     = "pytest -v --tb=short"
lint     = "ruff check ."
fmt      = "ruff format ."
migrate  = "python manage.py migrate"
```

```sh
$ molt run dev                # executes the task command
$ molt run test -- -k auth    # passes -k auth to pytest
$ molt task list              # show all defined tasks
$ molt task add ci "pytest -x && ruff check ."
$ molt task remove ci
```

Tasks run under the same hermetic environment as `molt run python` —
`PATH` includes `.molt/bin` so console scripts work, `PYTHONPATH` is
the store, no venv activation.

### `moltproject.toml` — tasks for non-Python projects

C, C++, Zig, and other compiled-language projects that don't need a
Python runtime can use `moltproject.toml` instead of `pyproject.toml`.
It supports the same `[tool.molt.tasks]` format but without the
Python-specific `[project]` fields (`requires-python`, `dependencies`):

```toml
# moltproject.toml — no requires-python, no dependencies needed
[project]
name = "stats-engine"
version = "0.1.0"

[tool.molt.tasks]
build = "zig c++ -O2 -std=c++17 -o bin/stats src/main.cpp"
run   = "bin/stats"
clean = "rm -f bin/stats"
```

```sh
$ molt run build    # compiles the C++ binary
$ molt run run      # executes bin/stats
$ molt task list    # shows all tasks, same as pyproject.toml
```

molt searches for `moltproject.toml` first, then falls back to
`pyproject.toml` — mixed projects (Python + native orchestration)
can keep both files. When no `syspath.json` exists (project has never
been synced as a Python project), task `env` falls back gracefully to
the current shell environment, so `zig`, `clang`, and other tools on
`PATH` are available without any Python setup step.

If the first arg to `molt run` isn't a task name, it's treated as a
binary:

```sh
$ molt run black --check src/
$ molt run python -c 'print(1)'
```

---

## Templates

```sh
$ molt template list                       # built-in + user templates
$ molt template show fastapi                # render the file tree
$ molt template add my-template ./scaffold  # register a user template
$ molt init --template my-template myapp   # apply on init
```

Templates are directory snapshots applied at `molt init` time. Built-in
ones cover common scaffolds (CLI, FastAPI service, library). User
templates are arbitrary directories you've registered.

Use cases:
- A team can ship one template that bakes in their lint config, CI
  workflow, README scaffolding, default tasks.
- Quick experiments: `molt init --template scratchbook` for a
  pre-configured Jupyter setup.

---

## Multi-project tracking

```sh
$ molt project list                            # every project molt has touched
$ molt project info myapp                       # detailed view
$ molt project where myapp syspath              # like `molt where`, for a remote project
$ molt project cd myapp                         # print path (for shell `cd $(molt project cd ...)`)
$ molt project purge --older-than 30d           # bulk cleanup
$ molt project purge --unused                   # purge projects whose source dir is gone
$ molt project reinit ./previously-purged       # re-register
```

The `--project / -p <name>` global flag makes most commands
accept-from-anywhere:

```sh
$ molt -p myapp run test                        # from any directory
$ molt -p myapp where bin                       # without cd-ing into it
```

Useful for monorepos and for IDE / editor integrations that need to
operate on a project from another working directory.

---

## Tool registry: global CLI shims

```sh
$ molt tool install ./myapp                     # register myapp's CLI globally
$ molt tool list
$ molt tool show myapp
$ molt tool uninstall myapp
$ molt tool path                                 # print ~/.molt/bin (add to PATH)
```

`molt tool install` writes a thin shim into `~/.molt/bin/` that, when
invoked, runs the target project's default task (or a named one via
`--task`). The shim uses absolute paths — no PATH manipulation needed
on the user side beyond having `~/.molt/bin` on `PATH`.

This is how you turn a Python project into a globally-callable command
without `pipx install`, `pip install --user`, or shell-specific setup.

---

## Native modules — overview

This is where molt diverges sharply from other Python toolchains.
Python is famously good as glue and bad as a number-cruncher. molt
makes adding native code in any language a one- or two-file change.
There are **six** distinct paths, each suited to a different use case:

| Path | What you write | Best for |
|---|---|---|
| `[[tool.molt.c.modules]]` (header-driven C) | A `.h` header + `.c` source | Zero-boilerplate C extensions; molt auto-generates all CPython glue from the header |
| `.pyx` (Cython) | Python-flavoured DSL | Numerical kernels, custom C-API integration, hot loops with type hints |
| `.rs` (Rust + PyO3) | Rust + `#[pyfunction]` macros | Type-safe complex APIs, async I/O, anything where Rust's safety pays off |
| `<name>.molt.toml + <name>.zig/.c/.cpp/...` (kernel modules) | Plain native source + manifest | Drop-a-file native functions in any C-ABI language; primitives only |
| `<name>.molt.toml + <name>.so` (pre-built kernel) | Manifest only | Binding to vendor-supplied or pre-compiled native libraries |
| `[[tool.molt.native]]` (external module) | A directory + a build command | Wrapping an existing Cargo / autotools / Make project |

Pick by use case, not by aesthetics:

- "I have a `.h` header I want to call from Python" → `[[tool.molt.c.modules]]`.
- "I have one tight inner loop in Zig or C" → kernel module.
- "I want to take a Python list and return a dict" → `.pyx` or `.rs`.
- "I want to bind libsodium from Homebrew" → pre-built kernel.
- "I have a multi-file Rust crate" → `[[tool.molt.native]]` with the
  rust preset.

All six compile to ABI-tagged `.so` files cached in `~/.molt/native/`
and staged into the project's view at sync time. They appear in
Python as ordinary modules.

---

## First-class C extensions (header-driven)

The **simplest** path to a CPython extension module: no Cython, no cffi,
no manual `PyArg_ParseTuple` wrangling. Declare `[[tool.molt.c.modules]]`
in `pyproject.toml`, point at a `.h` header, and `molt sync` does the rest.

### Python calling C

```c
/* fastmath.h — molt parses this automatically */
double add(double a, double b);
double lerp(double a, double b, double t);
double clamp(double value, double lo, double hi);
int    gcd(int a, int b);
double mean3(double a, double b, double c);
```

```c
/* fastmath.c — plain C, zero Python-awareness */
double add(double a, double b)   { return a + b; }
double lerp(double a, double b, double t) { return a + t * (b - a); }
double clamp(double v, double lo, double hi) { return v < lo ? lo : v > hi ? hi : v; }
int    gcd(int a, int b) { while (b) { int t = b; b = a % b; a = t; } return a; }
double mean3(double a, double b, double c) { return (a + b + c) / 3.0; }
```

```toml
# pyproject.toml — that's the entire configuration
[[tool.molt.c.modules]]
name = "fastmath"
src  = ["fastmath.c"]
# headers defaults to ["fastmath.h"] — auto-discovered from src directory
```

```sh
$ molt sync          # parses fastmath.h → generates glue.c → compiles fastmath.so
→ c: 1 module(s)
✓ fastmath  (cpython-312-darwin-arm64)
```

```python
import fastmath
print(fastmath.lerp(0.0, 100.0, 0.25))   # → 25.0
print(fastmath.gcd(48, 18))               # → 6
print(fastmath.clamp(-5.0, 0.0, 1.0))    # → 0.0
```

What molt generates automatically from the header:
- `PyInit_fastmath` — the CPython module entry point
- `PyMethodDef` table — one entry per parsed function
- `PyArg_ParseTuple` call for each function's arguments
- `__doc__` strings from function signatures
- `fastmath.pyi` stub for IDE / mypy / pyright integration

### C calling Python

Because molt compiles your C extension with the project's Python include
directory and sets `PYTHONPATH` to the full package store, **C code can
call back into Python packages at runtime**:

```c
/* ml_glue.c — C calling numpy and returning results to Python */
#define PY_SSIZE_T_CLEAN
#include <Python.h>

static PyObject* numpy_mean(PyObject* self, PyObject* args) {
    PyObject* list;
    if (!PyArg_ParseTuple(args, "O", &list)) return NULL;

    /* Import numpy from the project's molt-managed store */
    PyObject* np = PyImport_ImportModule("numpy");
    PyObject* arr = PyObject_CallMethod(np, "array", "O", list);
    PyObject* mean = PyObject_CallMethod(arr, "mean", NULL);

    Py_DECREF(arr); Py_DECREF(np);
    return mean;  /* a Python float back to the caller */
}
```

This works because `molt sync` writes the full `PYTHONPATH` into the
process environment before your extension is loaded. `numpy`, `pandas`,
`torch` — any managed dependency is importable from C.

### Cross-platform compilation

```sh
# Compile for Linux from macOS — no Linux toolchain install needed
$ molt c build --target linux_amd64
$ molt c build --target linux_arm64

# Explicit rebuild (skips cache)
$ molt c build

# List discovered modules
$ molt c list
```

The `zig cc` cross-compiler handles the platform ABI differences
automatically. The same `.c` source builds correctly for every target.

### Extra compiler flags

```toml
[[tool.molt.c.modules]]
name  = "fastmath"
src   = ["fastmath.c", "mathutil.c"]
flags = ["-O3", "-march=native", "-DNDEBUG"]
```

---

## Cython modules

Drop a `.pyx` in your project root or under `src/`:

```cython
# fastmath.pyx
cdef extern from "math.h":
    double sin(double)
    double cos(double)

def vec_dot(double[:] a, double[:] b):
    cdef double s = 0.0
    cdef Py_ssize_t i, n = a.shape[0]
    for i in range(n):
        s += a[i] * b[i]
    return s

def rotate(double x, double y, double angle):
    return (x * cos(angle) - y * sin(angle),
            x * sin(angle) + y * cos(angle))
```

```sh
$ molt run python -c 'import fastmath; print(fastmath.rotate(1, 0, 1.5708))'
(-3.673e-06, 0.999999...)
```

Configuration in `[tool.molt.cython]`:

```toml
[tool.molt.cython]
paths              = [".", "src"]
extra_compile_args = ["-O3", "-march=native"]
include_c          = true                            # auto-include .c/.h alongside
include_dirs       = ["vendor/**/include"]            # glob-aware
sources            = ["src/native/**/*.c"]            # bundled .c files
libraries          = ["sodium", "z"]                   # linker -l flags
library_dirs       = ["/opt/homebrew/lib"]
defines            = { NDEBUG = "1", VERSION = "1" }   # → -DNDEBUG=1 -DVERSION=1
std                = "c++17"                           # only used if .cpp source detected
language           = "c++"                             # explicit override (auto-detected otherwise)
pkg_config         = ["libsodium", "openssl"]          # runs pkg-config at build time
compiler           = "zig"                              # use zig cc/c++ as the C compiler
```

`pkg_config` is the killer feature here: `pkg_config = ["opencv4"]`
expands at build time into the dozens of `-I`, `-L`, `-l` flags
needed for OpenCV. No hand-editing.

`include_c = true` auto-discovers and compiles every `.c` in your
source tree alongside the generated Cython output. Mixed C+Cython
projects need only one config line.

C++ mode is auto-detected from `.cpp` sources or a
`# distutils: language = c++` directive in your `.pyx`; molt switches
the toolchain to `clang++`/`g++` automatically.

---

## Rust + PyO3 modules

Drop a `.rs` file with PyO3 bindings:

```rust
// crypto.rs
#[pyfunction]
fn xor_bytes(data: Vec<u8>, key: u8) -> Vec<u8> {
    data.iter().map(|b| b ^ key).collect()
}

#[pyfunction]
fn mul(a: f32, b: f32) -> f32 { a * b }

#[pymodule]
fn crypto(m: &Bound<'_, PyModule>) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(xor_bytes, m)?)?;
    m.add_function(wrap_pyfunction!(mul, m)?)?;
    Ok(())
}
```

```sh
$ molt run python -c 'import crypto; print(crypto.mul(2.5, 4))'
10.0
```

molt:
1. Auto-installs zig (used as the linker for cross-host portability).
2. Generates a `Cargo.toml` + `build.rs` in `~/.molt/native/rust-build/`.
3. Pins `pyo3` to your venv's Python (avoids the
   `_PyErr_GetRaisedException` and `dlopen dynamic_lookup` traps).
4. Builds, caches, stages.

Configuration in `[tool.molt.rust]`:

```toml
[tool.molt.rust]
pyo3_version  = "0.24"                                  # default
pyo3_features = ["extension-module"]                     # default
auto_prelude  = true                                     # inject `use pyo3::prelude::*;` if missing
auto_attrs    = true                                     # auto-add #[pyfunction] / #[pymodule]
```

`auto_attrs` lets you write the most concise valid form of a
single-file PyO3 module:

```rust
fn xor_bytes(data: Vec<u8>, key: u8) -> Vec<u8> {
    data.iter().map(|b| b ^ key).collect()
}

fn crypto(m: &Bound<'_, PyModule>) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(xor_bytes, m)?)?;
    Ok(())
}
```

molt's pre-build pass detects which `fn` is the module (matches the
file basename) and prepends `#[pyfunction]` / `#[pymodule]`
automatically. Functions that already carry any `#[…]` attribute are
left alone.

---

## Kernel modules: any C-ABI language

The newest pipeline. Manifest-driven, language-agnostic at the
discovery layer, with full IDE support via auto-generated `.pyi`
stubs.

```
project/
├── pyproject.toml
├── mathx.molt.toml
├── mathx.zig             # or .c, .cpp, .odin, .nim, ...
└── main.py
```

```toml
# mathx.molt.toml
[[fn]]
name        = "add"
description = "Sum two i32s."
args        = [
    { name = "a", type = "i32", description = "First addend." },
    { name = "b", type = "i32", description = "Second addend." },
]
returns     = { type = "i32", description = "The sum a + b." }

[[fn]]
name    = "scale"
args    = ["f64", "f64"]
returns = "f64"

[[fn]]
name        = "fib"
description = "Iterative Fibonacci."
args        = [{ name = "n", type = "i32" }]
returns     = "i64"
```

```zig
// mathx.zig — pure Zig, no Python bindings
export fn add(a: i32, b: i32) i32 { return a + b; }
export fn scale(x: f64, k: f64) f64 { return x * k; }
export fn fib(n: i32) i64 {
    if (n < 2) return @intCast(n);
    var a: i64 = 0; var b: i64 = 1;
    var i: i32 = 2;
    while (i <= n) : (i += 1) { const t = a + b; a = b; b = t; }
    return b;
}
```

```python
import mathx
mathx.add(2, 3)        # 5 — types matched, runtime-checked
mathx.scale(2.5, 4)    # 10.0
```

In your editor (VS Code, PyCharm, anything pyright-aware):
- Typed signatures via the auto-generated `mathx.pyi`.
- Per-arg descriptions surface as hover docs.
- `mathx.add("oops", 2)` flagged at type-check time.

### How it composes

The same manifest also feeds:
- **The C glue layer** — `PyInit_mathx`, `PyMethodDef[]`, per-fn
  `PyArg_ParseTuple` wrappers — no hand-written boilerplate.
- **`__doc__` strings inside the `.so`** so `help(mathx.add)` works
  at runtime.
- **The `.pyi` stub** — IDE / mypy / pyright integration.

Configuration in `[tool.molt.native_kernel]`:

```toml
[tool.molt.native_kernel]
paths             = [".", "src"]              # where to look for manifests
manifest_dir      = "manifests"                # optional centralised manifest dir
source_extensions = [".zig", ".c", ".cpp"]     # auto-extended by configured builders

[tool.molt.native_kernel.build.c]              # per-project builder override
command = "{zig} cc -c -O3 -DDEBUG=1 -fPIC -o {output} {source}"
```

### Supported types (MVP)

| Manifest | Python type |
|---|---|
| `i8` `i16` `i32` `i64` `u8` `u16` `u32` `u64` `usize` `isize` | `int` |
| `f32` `f64` | `float` |
| `bool` | `bool` |
| `void` (return) | `None` |

Unsupported types (pointers, strings, structs, callbacks) cause the
function to be dropped from the binding with a warning; the rest of
the module still builds. Pointer / string / struct support is on the
roadmap.

---

## Kernel builders: per-language compile recipes

A "kernel builder" is the recipe for compiling one source-file
extension to a position-independent object file. molt looks them up
in priority order:

1. **Per-project**: `[tool.molt.native_kernel.build.<ext>]` in
   pyproject.toml.
2. **Global**: `~/.molt/kernel-builders.yaml` (auto-seeded with
   defaults on first use).
3. **Built-in**: hardcoded fallbacks for `.zig`, `.c`, `.cpp`.

```sh
$ molt kernel-builder list
ext     source                  command
zig     global                  {zig} build-obj {source} -O ReleaseFast -fPIC -femit-bin={output}
c       global                  {cc} -c -O2 -fPIC -o {output} {source}
cpp     global                  {cxx} -c -O2 -fPIC -o {output} {source}

$ molt kernel-builder add odin --from-template
✓ added builder for .odin → ~/.molt/kernel-builders.yaml
  command: odin build {source} -file -build-mode:obj -reloc-mode:pic -o:speed -out:{output}

$ molt kernel-builder add nim 'nim c --app:staticlib --noMain --out:{output} {source}'
$ molt kernel-builder add c '{zig} cc -c -O3 -DDEBUG=1 -fPIC -o {output} {source}' --local
$ molt kernel-builder show odin
$ molt kernel-builder remove odin
$ molt kernel-builder edit             # opens ~/.molt/kernel-builders.yaml in $EDITOR
$ molt kernel-builder reset            # restore seeded defaults
$ molt kernel-builder path             # print global YAML path
```

Token vocabulary in commands:

| Token | Expands to |
|---|---|
| `{source}` | absolute path to the user's source file |
| `{output}` | absolute path molt expects the `.o` at |
| `{zig}` | absolute path to the auto-installed zig binary |
| `{cc}` | resolved system C compiler |
| `{cxx}` | resolved system C++ compiler |
| `{include_dir}` | Python include directory |

Adding a new C-ABI language is a one-line config change. No molt code
edit required.

`--from-template` uses molt's curated commands for `zig`, `c`, `cpp`,
`odin`, `nim`, `rs` (rustc without PyO3), `f90`, `f95`. Any not in
that table requires the user to provide a command explicitly.

### Auto-watching

When you add a builder for `.odin`, molt automatically extends the
list of watched source extensions to include `.odin`. No
`source_extensions = [".zig", ".c", ".cpp", ".odin"]` edit needed —
the builder registry IS the source of truth.

---

## Pre-built `.so` binding

Drop a `<name>.so` (or `lib<name>.so`) next to a `<name>.molt.toml`
and molt generates a **dlopen-style wrapper**:

```
project/
├── crypto.molt.toml
└── libcrypto.so          # vendor-supplied or downloaded release
```

```toml
# crypto.molt.toml — same schema as source-built kernels

[[fn]]
name    = "encrypt"
args    = [{ name = "key", type = "u32" }, { name = "data", type = "u32" }]
returns = "u32"
```

```python
import crypto
crypto.encrypt(0xdeadbeef, 42)
```

molt detects there's no source file, generates a `glue.c` that:
1. `dlopen()`s the library at `PyInit_crypto` time.
2. `dlsym()`s each declared function into a static fn-pointer slot.
3. Forwards Python calls through the pointers.

Three resolution modes for the library file (priority order):

| Manifest | Resolution |
|---|---|
| `library = "/opt/homebrew/lib/libsodium.dylib"` | Explicit absolute path — used as-is. |
| `library = "vendor/libsodium.dylib"` | Relative to manifest dir. |
| (no `library` field) | Sibling `<name>.so` / `lib<name>.so` / `.dylib` / `.dll`. |

The wrapper has no link-time dependency on the impl `.so` — it's
loaded at import time. Vendor updates trigger a rebuild via the cache
hash (which mixes the impl's content) but no manual cleanup.

### Use cases

- Bind to a brew/Nix-installed system library: `library = "/opt/homebrew/lib/libsodium.dylib"`.
- Bind to a Rust crate built outside molt: `library = "../rust-crate/target/release/libfoo.so"`.
- Distribute a pre-compiled vendor SDK alongside Python source: ship
  `vendor/libfoo.so` + `foo.molt.toml`; users get `import foo`.
- Glue around system libraries (zlib, libcurl, libcrypto): one
  manifest binds primitive subsets without needing a Cython wrapper.

---

## `[[tool.molt.native]]` — external module recipes

For projects bigger than "one source file + manifest" — e.g. a Rust
Cargo workspace, a CMake-built C++ library, an autotools project —
use the external-module recipe pattern:

```toml
[[tool.molt.native]]
module = "myrustlib"
src    = "rust/myrustlib"
preset = "rust"

[[tool.molt.native]]
module = "fastmath"
src    = "c/fastmath"
preset = "c"
build  = "cc -O3 -shared -fPIC -I{python_include} -o {output} src/*.c"
output = "fastmath.{ext}"
src_patterns = ["src/**/*.c", "src/**/*.h"]
```

molt:
1. Looks up the named preset (built-in: `rust`, `c`, `cpp`; or your own
   in `~/.molt/native-presets.yaml`).
2. Hashes all matching files in `src` for cache invalidation.
3. Runs the build command in the source dir.
4. Links the resulting `.so` into the project view.

Manage presets globally with:

```sh
$ molt native-preset list
$ molt native-preset show rust
$ molt native-preset add --name myrust --build "cargo build --release" --output "target/release/lib{module}.{ext}"
$ molt native-preset remove myrust
```

This is the "wrap an existing build system" path. The kernel-module
system is the "drop a single file in your project" path. Use whichever
fits.

---

## Runtime environment: `extra_paths`

```toml
[tool.molt.runtime]
extra_paths = ["vendor/lib", "vendor/bin", "vendor/Frameworks"]
```

Each entry is prepended to all relevant runtime path env vars when
molt spawns Python (or any subprocess via `molt run`):

| Env var | Used for |
|---|---|
| `PATH` | exec lookup; on Windows also DLL lookup |
| `LD_LIBRARY_PATH` | shared-library lookup on Linux |
| `DYLD_FALLBACK_LIBRARY_PATH` | shared-library lookup on macOS (SIP-safe) |
| `DYLD_FALLBACK_FRAMEWORK_PATH` | framework lookup on macOS |

Drop a binary, a `.so`, or a `Qt.framework` into a directory listed
in `extra_paths`, and it's findable from Python — for `subprocess`,
`ctypes`, `dlopen`, anything — without any shell setup.

```
project/
├── vendor/
│   ├── bin/ffmpeg                  # discovered via PATH
│   ├── lib/libcrypto.so            # discovered via dynamic-linker var
│   └── Frameworks/Qt.framework      # discovered on macOS
└── main.py
```

```python
# main.py
import subprocess, ctypes
subprocess.check_output(["ffmpeg", "-version"])     # finds vendor/bin/ffmpeg
ctypes.CDLL("libcrypto.so")                          # finds vendor/lib/libcrypto.so
```

This is genuinely useful even outside the kernel-module system — any
project that vendors a `.so` for ctypes use, or ships helper binaries,
can drop them in and have them discoverable on any developer's
machine without per-shell `LD_LIBRARY_PATH` exports.

---

## Environment variables

Two complementary layers, both opt-in, both injected into every
process molt spawns (`molt run`, tasks, builds, the project's Python).

### Global — `~/.molt/env.yaml`

Variables you want in every project on the machine: corporate proxy,
internal package mirrors, machine-wide secrets, debug flags.

```sh
$ molt env set HTTPS_PROXY http://proxy.corp.com:8080
✓ set HTTPS_PROXY in /Users/me/.molt/env.yaml

$ molt env set PIP_INDEX_URL https://pypi.corp.com/simple
$ molt env set GITHUB_TOKEN ghp_xxx

$ molt env list
GITHUB_TOKEN                    ghp_xxx
HTTPS_PROXY                     http://proxy.corp.com:8080
PIP_INDEX_URL                   https://pypi.corp.com/simple

$ molt env get HTTPS_PROXY
http://proxy.corp.com:8080

$ molt env unset GITHUB_TOKEN
$ molt env edit                 # opens ~/.molt/env.yaml in $EDITOR
$ molt env path                 # /Users/me/.molt/env.yaml
```

The file is `0600` since it can hold credentials. Saves are atomic
(write-temp + rename). Sorted YAML output for clean diffs.

### Per-project — `[tool.molt.runtime.env]`

Variables bound to a specific project: `DATABASE_URL`, `LOG_LEVEL`,
`PYTHONUNBUFFERED`, anything project-specific.

```toml
# pyproject.toml
[tool.molt.runtime.env]
DATABASE_URL     = "postgresql://localhost/dev"
LOG_LEVEL        = "DEBUG"
PYTHONUNBUFFERED = "1"
```

…or via CLI with `--local`:

```sh
$ molt env set DATABASE_URL postgresql://localhost/dev --local
✓ set DATABASE_URL in pyproject.toml [tool.molt.runtime.env]

$ molt env unset DATABASE_URL --local
```

### Resolution priority

```
1. project [tool.molt.runtime.env]    ← always wins (explicit project intent)
2. parent shell env (export FOO=…)     ← shell-level explicit beats global default
3. global ~/.molt/env.yaml              ← only sets vars not already in shell
```

So `export FOO=bar` in your shell still wins over a global default —
explicit shell intent isn't silently overridden — but a project that
declares `FOO = "..."` always wins, since project intent is even more
explicit.

### What's reserved

Three names are molt-managed and **can't be set** through env config:

| Name | Why molt manages it |
|---|---|
| `PYTHONPATH` | Computed from the project's syspath; manual override breaks store isolation |
| `VIRTUAL_ENV` | Stripped on every spawn so a stale venv doesn't leak in |
| `PYTHONHOME` | Same — molt always points at the project's pinned interpreter |

`molt env set PYTHONPATH ...` errors loudly. The same names in
`[tool.molt.runtime.env]` are silently dropped at parse time.

### Use cases

| Scenario | Where to put it |
|---|---|
| Corporate proxy | global — applies everywhere |
| Internal PyPI mirror | global — `PIP_INDEX_URL`, `UV_INDEX_URL` |
| Per-developer credentials | global — never in committed files |
| `RUSTFLAGS=-C target-cpu=native` | global if you want it on every build |
| `DATABASE_URL`, `LOG_LEVEL` | project — bound to the project |
| `PYTHONUNBUFFERED=1` | global if you always want it; project for one app |
| Test-only flags | project, scoped to the tests task |

---

## Build & distribution: single-binary release

```sh
$ molt build                                     # → myapp-v1.0.0
$ ls -la myapp-v1.0.0
-rwxr-xr-x 42M  myapp-v1.0.0
$ scp myapp-v1.0.0 prod:/usr/local/bin/
$ ssh prod './myapp-v1.0.0 install && ./myapp-v1.0.0 run'
```

The output binary contains:

```
┌────────────────────────┐
│ launcher (Go)          │  install / run / verify / uninstall
├────────────────────────┤
│ payload (tar.gz)       │  source + deps + Python interpreter + native .so files
├────────────────────────┤
│ trailer                │  [payload offset][SHA-256 root hash][magic]
└────────────────────────┘
```

On the target machine:
- `./myapp-v1.0.0 install` extracts payload to `~/.molt/installed/myapp-v1.0.0/`,
  verifies the SHA-256 trailer against the embedded integrity manifest,
  runs `post_install` hooks.
- `./myapp-v1.0.0 run` execs the default command in the hermetic env.
- `./myapp-v1.0.0 run worker` runs a named command.
- **No Python installation required on the target.**

### `molt.yaml`

The deployment artifact is described separately from `pyproject.toml`:

```yaml
version: 1

project:
  name:    myapp
  version: 2.3.1
  python:  "3.12"

deps:
  strategy: pyproject       # reads pyproject.toml + uv.lock

include:
  - "src/**/*.py"
  - "templates/"
  - "static/"

assets:
  - source: data/cities.json
    dest:   data/

commands:
  default: web
  web:     { exec: ["gunicorn", "myapp:app", "--bind", "0.0.0.0:8000"] }
  worker:  { exec: ["celery", "-A", "myapp", "worker"] }
  migrate: { exec: ["python", "manage.py", "migrate"] }

env:
  default:
    LOG_LEVEL: INFO
  worker:
    CELERY_CONCURRENCY: "8"

hooks:
  post_install:
    - "python manage.py migrate --noinput"
    - "python -c 'import myapp; myapp.warmup()'"

integrity:
  verify_on_install: true
```

### Cross-compile

```sh
$ molt build --os linux --arch amd64        # build a Linux binary on a Mac
$ molt build --os linux --arch arm64
$ molt build --output dist/myapp.glibc      # custom output path
```

`molt build` uses the same uv resolution under the hood; cross-builds
pull wheels for the target ABI, package them, and produce an
artifact runnable on the target without further toolchain.

### Wheel + sdist (PyPI)

```sh
$ molt package                              # build both wheel and sdist
$ molt package --wheel                      # wheel only
$ molt package --output dist/
```

Standard PEP 517 build via uv — for users who want to publish to PyPI
in addition to (or instead of) the single-binary path.

---

## Integrity & verification

```sh
$ molt verify-binary myapp-v1.0.0           # validates trailer ↔ manifest
$ molt verify-binary myapp-v1.0.0 --deep     # also re-hashes every file in payload
$ molt inspect myapp-v1.0.0                  # print embedded manifest
$ molt inspect myapp-v1.0.0 --files          # list every file with hash + size
$ molt diff old.bin new.bin                  # side-by-side manifest diff
```

Each `molt build` embeds an integrity manifest containing the SHA-256
of every file in the payload plus a single root hash that's also stored
in the binary's trailer. `verify-binary` checks both layers; `--deep`
re-hashes every file from the extracted tar to catch corruption.

This isn't optional security theatre — `verify_on_install: true` in
`molt.yaml` blocks the install step if any hash mismatches, which
catches transit corruption (cosmic rays, `scp` interruption, partial
writes) and tampering.

`molt diff` is for change auditing: comparing two release artifacts
shows exactly what's new, removed, or modified, by hash.

---

## Editor integration

```sh
$ molt editor              # auto-detect (vscode, pyright, etc.)
$ molt editor vscode       # write/refresh .vscode/settings.json
$ molt editor pyright      # write/refresh pyrightconfig.json
```

Generates the right config files so VS Code, pyright, mypy, etc. can
find the project's Python interpreter and the syspath entries from
`.molt/syspath.json`. After running, IntelliSense, go-to-definition,
and type-checking all work the same as if you'd activated a venv.

For the kernel-module system specifically, the auto-generated `.pyi`
stubs are picked up automatically — no extra editor config needed.

---

## Diagnostics

```sh
$ molt doctor
✓ uv          0.5.7  /usr/local/bin/uv
✓ python      3.12.3 /Users/me/.molt/python/3.12.3/bin/python
✓ git         2.39.1 /usr/bin/git
✓ go          1.22.0 /usr/local/go/bin/go (for `molt build`)
✓ cargo       1.83.0 (for .rs auto-compile)
✓ zig         0.14.1 /Users/me/.molt/toolchains/zig/0.14.1/zig (auto-installed)

$ molt info
Project:         myapp
Source:          /Users/me/work/myapp
Python:          3.12.3
Sync hash:       abc123...
Store dirs:      24
Native modules:  3 (1 cython, 1 rust, 1 kernel)
Tasks:           dev, test, lint
```

```sh
$ molt where syspath
/Users/me/work/myapp/.molt
/Users/me/.molt/pkg/numpy/1.26.4/cp312-cp312-macosx_11_0_arm64
/Users/me/.molt/pkg/...

$ molt where python
/Users/me/.molt/python/3.12.3/bin/python

$ molt where store
/Users/me/.molt/pkg

$ molt where bin
/Users/me/work/myapp/.molt/bin
```

`molt where` is the universal "show me where X actually is" tool. The
keys (`syspath`, `python`, `bin`, `store`, `state`, `sitecustomize`,
`uv-env`) cover everything a user might want to script around.

---

## Why this matters: developers and enterprise

### For individual developers

| Pain point | molt's answer |
|---|---|
| Activating a venv every time you switch projects | No venv. `molt run` sets env and execs. |
| Disk bloat from duplicate `.venv` installations | Wheels are unpacked once, shared. A 50-project laptop saves 10–20 GB. |
| Switching Python versions takes minutes | `molt python use 3.13` re-resolves syspath in seconds. No venv rebuild. |
| Distributing your CLI to non-technical users | `molt build` gives one binary they can `scp` and run. |
| Adding native code is a build-system odyssey | Drop a `.zig` / `.c` / `.rs` / `.pyx` file. Done. |
| IDE doesn't see your project's Python | `molt editor vscode`, then it does. |
| `pkg-config` flags vary by machine | `pkg_config = ["opencv4"]` resolves per-machine at build time. |
| Deciding between PyInstaller, Docker, Nuitka | Single-binary build with integrity verification, native `import`, no Python on target. |

### For enterprise teams

| Concern | molt's answer |
|---|---|
| Reproducible builds across team machines | The store key includes the ABI tag — same `(name, version, abi)` ⇒ same files on every dev's machine. |
| Auditing what's in a release | `molt inspect` + `molt diff` show every file by hash. |
| Tampering / corruption detection | SHA-256 root hash in the trailer; `verify-binary --deep` re-hashes the payload. |
| Cross-platform deploys | `molt build --os linux --arch amd64` from a developer's Mac. |
| Internal tooling distribution | `molt tool install` per-developer, OR build a binary and distribute via standard package management. |
| Vendored native dependencies (HSM SDKs, proprietary libs) | `[tool.molt.runtime] extra_paths`, manifest with `library = "..."`, or `[[tool.molt.native]]` with a custom build command. |
| Hot-path performance on Python services | Cython for numerical, Rust for type-safe complex APIs, kernel modules for primitives. All staged into the project view automatically. |
| CI/CD: provisioning Python toolchains | One install (`curl install.sh \| sh`), then `molt sync` + `molt build`. No system Python required if `--bundle-python` is set. |
| Long-term project archive | The single-binary output is a standalone artifact — no need to recreate the build environment to run it 5 years later. |

### Where molt is *not* the right tool

- **You're already happy with `uv` + `pip` + `venv` for a small
  hobby project.** molt's value compounds with project count and
  team size; for one solo project, the win is smaller.
- **You're shipping a library that users `pip install`.** Use
  `uv build` / `hatch` / `flit` for that. molt's `package` command
  produces wheels but the build-binary path is for *applications*,
  not libraries.
- **You need fully-managed Python infrastructure** (e.g. Lambda
  layers, Cloud Run images). molt's binary works in those, but if
  you're committed to the AWS/GCP-native build path, the
  integration cost may not be worth it for a single project.

---

## Comparison to other tools

| Capability | molt | pip + venv | poetry | pdm | uv | pyinstaller | docker |
|---|---|---|---|---|---|---|---|
| Dep management | ✓ (via uv) | ✓ | ✓ | ✓ | ✓ | – | ✓ (Dockerfile) |
| Shared package store | ✓ | ✗ | ✗ | ✓ (PEP 582 at one point) | ✗ | ✗ | ✗ |
| No `.venv` per project | ✓ | ✗ | ✗ | ✗ | ✗ | N/A | N/A |
| Python version mgmt | ✓ | ✗ (needs pyenv) | partial | ✓ | ✓ | – | – |
| Single-binary distribution | ✓ | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ (image) |
| No Python required on target | ✓ | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ (Docker) |
| Cython auto-compile | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Rust+PyO3 single-file | ✓ | ✗ (use maturin) | ✗ | ✗ | ✗ | ✗ | ✗ |
| Manifest-driven kernel modules | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Pre-built `.so` auto-binding | ✓ | partial (ctypes manual) | ✗ | ✗ | ✗ | ✗ | ✗ |
| Multi-language native (Zig, C, C++, Odin, …) | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Integrity-verified artifact | ✓ | ✗ | ✗ | ✗ | ✗ | ✗ | ✓ (image digest) |
| Hot-reload tool registry | ✓ | – | ✗ | ✗ | ✓ (uvx) | – | – |

molt's unique surface is the **combination**: nobody else gives you
the global package store + Python version management + native
auto-compile across multiple languages + single-binary distribution
in one tool. Each of those individually has alternatives; the value
is having them coherent and shareable.

---

## Roadmap

**Shipping:** kernel modules (zig/c/cpp), pre-built `.so` binding,
extra_paths, `kernel-builder` CLI, Cython rich config (pkg_config,
include_c, libraries, defines, std, language overrides), Rust+PyO3
auto-prelude / auto-attrs, multi-project tracking, integrity
verification, cross-compile builds.

**Near-term:**
- Pointer types (`*T`) in kernel manifests — unlocks buffer/array
  args and bulk-array returns.
- `cstring` ↔ `str` mapping for native strings.
- Keyword-argument support in kernel wrappers (real
  `inspect.signature` for native fns).
- `molt kernel-builder add --from-template` for more languages
  (currently zig/c/cpp/odin/nim/rs/f90).

**Medium-term:**
- Lazy iterator support: kernel-side `init/next/drop` triple wrapped
  as a Python iterator class.
- Struct types in manifests (`[[type]]` blocks) for richer FFI.
- `.molt.json` / `.molt.yaml` manifest formats (parser hooks plumbed,
  parsers not yet written).
- Auto-inference of kernel signatures from C/Zig source for
  manifest-less single-file workflows.

**Speculative:**
- Synchronous Python callbacks in kernels via opaque-handle
  trampolines.
- Asyncio integration via `fd` return type for non-blocking native
  workers.
- IPC patterns for in-process bidirectional Python ↔ native
  communication.

---

## Universal script launcher (run-handler system)

`molt run` is not limited to Python. Any file with a registered
extension is dispatched directly to the appropriate runtime — no tasks,
no configuration, no activation:

```sh
$ molt run hello.rb        # → ruby hello.rb
$ molt run hello.js Alice  # → node hello.js Alice  (args forwarded)
$ molt run hello.go        # → go run hello.go
$ molt run hello.jl        # → julia hello.jl
$ molt run hello.exs       # → elixir hello.exs
```

28 runtimes ship built-in (including `.rs` → `rustc`). Add your own in one command:

```sh
$ molt run-handler add deno "deno run {file} {args}"
$ molt run server.ts       # → deno run server.ts

# Per-OS overrides
$ molt run-handler add ts "npx ts-node {file} {args}" --windows "npx.cmd ts-node {file} {args}"

# Inspect and manage
$ molt run-handler list    # all 27 built-ins + user handlers
$ molt run-handler show rb # Extension: .rb  Command: ruby {file} {args}
$ molt run-handler reset   # restore factory defaults
```

**Dispatch order inside `molt run <arg>`:**

1. Flag-shaped arg → project's default entry point
2. No arg → `main.py` or `python -m <pkg>`
3. Ends in `.py` and file exists → project Python interpreter
4. Ends in `.mojo` / `.🔥` and file exists → `mojo run`
5. **Has any extension, file exists, handler registered → `runWithHandler`**
6. Matches a task in `[tool.molt.tasks]` → task runner
7. Fallthrough → exec binary on PATH

**Token reference:**

| Token | Expands to |
|---|---|
| `{file}` | Absolute path to the script |
| `{dir}` | Directory containing the script |
| `{basename}` | Filename without extension |
| `{args}` | Extra arguments, space-joined |
| `{tmp}` | Stable per-file temp dir (`$TMPDIR/molt-run/<hash>/`) |
| `{python}` | Project's pinned Python interpreter |
| `{zig}` | Auto-installed zig binary |

The built-in `.c` and `.cpp` handlers use `{tmp}` so compiled binaries
never appear in your working directory. Custom compile-then-run handlers
can use it the same way:

```sh
$ molt run-handler add odin \
    "sh -c \"odin build {file} -file -out:{tmp}/{basename} && {tmp}/{basename} {args}\""
$ molt run main.odin          # compiled to $TMPDIR/molt-run/<hash>/main — not CWD
```

Handlers are stored in `~/.molt/run-handlers.yaml`. User entries take
precedence over built-ins of the same extension. The project's full
molt environment (`PYTHONPATH`, `.molt/bin/` on `PATH`) is applied
before exec — so `{python}` resolves to the pinned interpreter and any
installed packages are available to child processes.

---

## Polyglot package management (pkg-backend system)

`molt add`, `molt remove`, and `molt sync` are language-agnostic interfaces.
For a **Python** project they delegate to `uv`; for any other language they
route to the ecosystem's native tool via a configurable **pkg-backend**.

```sh
# Rust project (lang = "rust" in moltproject.toml)
$ molt add serde --features derive   # → cargo add serde --features derive
$ molt add tokio --version "^1"      # → cargo add tokio --version ^1
$ molt sync                          # → cargo fetch
$ molt remove serde                  # → cargo remove serde

# Go project
$ molt add github.com/spf13/cobra    # → go get github.com/spf13/cobra
$ molt sync                          # → go mod download

# Node project
$ molt add express@4                 # → npm install express@4
$ molt sync                          # → npm install

# Force a specific language in a mixed or ambiguous project
$ molt add openssl --lang rust       # explicit --lang flag
```

molt infers the target language from the **package name structure**
(e.g. `github.com/…` → Go, `@scope/pkg` → Node, `lib*` → C, `*-sys` → Rust)
and from the project's `lang` field. When inference is ambiguous, pass `--lang`.

**Per-ecosystem version formatting** is handled automatically:

| Language | `molt add pkg 1.6` becomes |
|---|---|
| python | `uv add pkg==1.6` |
| rust | `cargo add pkg --version 1.6` |
| go | `go get pkg@v1.6` |
| node/bun | `npm install pkg@1.6` |
| swift | `swift package add pkg --exact 1.6` |

**Backend token vocabulary** (used in custom command templates):

| Token | Expands to |
|---|---|
| `{package}` | Package name as typed |
| `{version_flag}` | Formatted version argument for the ecosystem's CLI |
| `{flags}` | Extra flags forwarded from the CLI |
| `{manifest}` | Absolute path to the project manifest |
| `{project}` | Absolute path to the project root |

**Inspect and customise backends:**

```sh
$ molt pkg-backend list            # show all langs (built-in + user)
$ molt pkg-backend show rust       # print the rust backend commands
$ molt pkg-backend add nim \
    --add "nimble install {package}{version_flag}" \
    --remove "nimble uninstall {package}" \
    --sync "nimble install"
$ molt pkg-backend reset           # restore factory defaults
```

**Project-level override** — swap to Bun without changing the global backend:

```toml
# moltproject.toml
[tool.molt.backend]
add  = "bun add {package}{version_flag}"
sync = "bun install"
# remove, list, upgrade fall back to the global node backend
```

9 built-in backends: `python` `rust` `go` `node` `bun` `zig` `c` `swift` `nim`.
Backends are stored in `~/.molt/pkg-backends.yaml`.

---

## Speeding up Python with native hot paths

Python is an excellent orchestration and glue language, but CPython pays a
price for dynamism: every attribute access goes through a dict lookup, every
arithmetic operation boxes and unboxes objects, and the GIL prevents true
CPU-level parallelism across threads. For most code that price is invisible.
For a numerical inner loop or a tight data-processing kernel, it is the
difference between seconds and milliseconds.

molt makes native code the path of least resistance — not a build-system
adventure.

### Where Python is slow (and by how much)

| Scenario | Pure Python | Native equivalent | Typical speedup |
|---|---|---|---|
| Summing 10M floats in a loop | ~800 ms | C `for` loop | **20–40×** |
| Sorting 1M structs | ~400 ms | C++ `std::sort` | **5–15×** |
| 8-wide SIMD dot product | ~1200 ms | Zig / Mojo SIMD | **50–200×** |
| Calling a math primitive 10M times | ~900 ms | C extension (no GIL) | **30–100×** |
| Threaded parallel sum | GIL-serialised | C with `Py_BEGIN_ALLOW_THREADS` | **4–8× on 8-core** |
| AES-128 block cipher, 1 GB | ~20 s | AES-NI via Zig or C | **200–500×** |

These are representative orders of magnitude, not guarantees. The actual gain
depends on whether your bottleneck is CPU, memory bandwidth, or latency.
Profile first; optimise only what's hot.

### The gradient of effort

molt supports a spectrum of native-code strategies. Pick the one whose
complexity matches the speedup you need:

```
Python pure        →  Cython (typed)  →  C extension  →  Zig / Mojo SIMD
 1×                    2–10×              5–100×           50–500×
 no build step         .pyx file          .h header         .zig or .mojo file
 easy to read          mostly Python      zero boilerplate  maximum hardware use
                                          in molt
```

**1. Cython** — annotate types in a `.pyx` file, drop it next to your Python.
molt compiles it automatically during `molt sync`.

```toml
# pyproject.toml
[[tool.molt.native]]
source = "fastsum.pyx"       # Cython auto-detected by extension
```

**2. Header-driven C extension (demo 13)** — write a `.h` file with your
function signatures, implement in `.c`. molt parses the header, generates all
CPython boilerplate, and compiles a real `.so`.

```toml
[[tool.molt.c.modules]]
header = "fastmath.h"        # molt reads every scalar signature
sources = ["fastmath.c"]
```

```python
import fastmath
print(fastmath.sum_f64(data))   # direct C call, no marshal overhead
```

Speedup for a simple float summation: **30–50× vs a Python loop** because
the C function processes values without boxing them into `float` objects.

**3. C++ templates (demo 16)** — for data structures, sorting, or STL
algorithms that need C++17 features. Compile via molt tasks and call the
binary from Python, or expose as a C extension via `extern "C"`.

**4. Zig with SIMD (demo 17)** — for true vectorised computation. A Zig
`@Vector(8, f32)` processes 8 floats per CPU instruction. On modern x86-64
this means a vectorised sum is **50–200× faster** than a Python loop and
**2–4× faster than scalar C**, because the compiler maps the vector directly
to AVX2 registers.

```zig
// Zig: 8 floats processed in parallel by a single CPU instruction
const v: @Vector(8, f32) = data[i..][0..8].*;
acc += @reduce(.Add, v);
```

**5. Mojo SIMD (demos 10–11)** — for Python scientists who want near-metal
performance with a Python-like syntax and full access to numpy/pandas.
Demo 10 shows a **47× speedup** over a pure Python loop; demo 11 reaches
near-numpy-MKL matmul performance at N=128.

### Calling Python packages from C

The interop is bidirectional. C and Zig code can call back into Python
packages — numpy, pandas, scipy — because molt sets `PYTHONPATH` to the
full package store before executing any native code:

```c
// In your C extension: call numpy from C
PyObject *np = PyImport_ImportModule("numpy");
PyObject *arr = PyObject_CallMethod(np, "array", "O", py_list);
PyObject *mean = PyObject_CallMethod(arr, "mean", NULL);
// numpy's BLAS-backed mean, called from C, result returned to Python
```

This lets you write a C or Zig outer loop for parallelism or SIMD, while
still delegating to numpy for linear algebra — the best of both worlds.

### Cross-platform native builds

`zig cc` and `zig c++` are drop-in cross-compilers. From a single developer
machine (say, macOS arm64) you can produce native binaries for every target:

```bash
# Compile a C extension for Linux amd64 — from macOS, no Docker
GOOS=linux GOARCH=amd64 molt build              # full molt binary, cross-compiled

# Cross-compile a C++ binary for Linux
zig c++ -target x86_64-linux-gnu -O2 -std=c++17 -o bin/stats-linux src/main.cpp

# Build and package the entire molt project for Linux from macOS
molt build --os linux --arch amd64              # cross-compiles Python + native code
```

**Why this matters for CI/CD:** your Mac development machine can produce
Linux release artifacts without a Linux VM, remote SSH, or Docker. The Zig
toolchain is downloaded once by molt into `~/.molt/toolchains/zig/` and
shared across all projects.

**ABI compatibility:** the store key includes the ABI tag, so a wheel built
for `cp313-cp313-linux_x86_64` is never confused with one built for
`cp313-cp313-macosx_arm64`. Cross-compiled artifacts land in the correct
per-platform slot automatically.

### Workflow: profile → identify → drop in native code

```bash
# 1. Profile your Python to find the bottleneck
molt exec python -m cProfile -s cumulative main.py | head -20

# 2. Write the hot function in C (drop a .h + .c next to your Python)
echo '[[tool.molt.c.modules]]' >> pyproject.toml
echo 'header = "fastsum.h"'  >> pyproject.toml
echo 'sources = ["fastsum.c"]' >> pyproject.toml

# 3. Re-sync: molt compiles the extension automatically
molt sync

# 4. Import and call — no Python restart needed
python -c "import fastsum; print(fastsum.sum_f64([1.0, 2.0, 3.0]))"

# 5. Profile again to confirm
molt exec python -m cProfile -s cumulative main.py | head -20
```

No Makefile, no `setup.py`, no `pip install .`. The extension is compiled,
staged, and visible to Python in one `molt sync` invocation.

---

## Where to go next

- [`demos/`](demos/) — eighteen runnable example projects:
  - `01–06` core toolchain (init, deps, FastAPI, data, CLI, binary dist)
  - `07` assembly kernel module
  - `08–12` Mojo (hello world, numpy interop, SIMD, matmul, Python extension)
  - `13` header-driven C extension (`[[tool.molt.c.modules]]`)
  - `14` universal script launcher (Ruby, Go, Julia, Elixir, Node, …)
  - `15` C as primary language (`molt run hello.c` + multi-file project)
  - `16` C++17 as primary language (`molt run hello.cpp` + multi-file project)
  - `17` Zig 0.16 as primary language (`molt run hello.zig` + `zig build-exe`)
  - `18` Rust as primary language (`molt add` → Cargo, `molt sync` → `cargo fetch`)
- [`docs/MANUAL.md`](docs/MANUAL.md) — exhaustive command reference
- [`docs/global-store.md`](docs/global-store.md) — store architecture deep dive
- [`docs/kernel-modules.md`](docs/kernel-modules.md) — kernel-module reference
- [`docs/zig-odin-kernels.md`](docs/zig-odin-kernels.md) — design rationale for
  the manifest-driven kernel pipeline
- [`docs/native-modules.md`](docs/native-modules.md) — `[[tool.molt.native]]`
  recipes and presets
- [`docs/mcp-server.md`](docs/mcp-server.md) — MCP server for Claude Code / Cursor

For each individual command, `molt <cmd> --help` is built-in.
