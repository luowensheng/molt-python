# The molt Handbook

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
3. **Native code is a first-class citizen.** Python is fast to write,
   slow to run. molt has built-in pipelines for Cython (`.pyx`), Rust
   (`.rs` via PyO3), and a manifest-driven "kernel module" system that
   accepts any C-ABI language (Zig, C, C++, Odin, Nim, …) or pre-built
   shared library. Drop a file in your project, get an importable
   Python module.

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
There are five distinct paths, each suited to a different use case:

| Path | What you write | Best for |
|---|---|---|
| `.pyx` (Cython) | Python-flavoured DSL | Numerical kernels, custom C-API integration, hot loops with type hints |
| `.rs` (Rust + PyO3) | Rust + `#[pyfunction]` macros | Type-safe complex APIs, async I/O, anything where Rust's safety pays off |
| `<name>.molt.toml + <name>.zig/.c/.cpp/...` (kernel modules) | Plain native source + manifest | Drop-a-file native functions in any C-ABI language; primitives only |
| `<name>.molt.toml + <name>.so` (pre-built kernel) | Manifest only | Binding to vendor-supplied or pre-compiled native libraries |
| `[[tool.molt.native]]` (external module) | A directory + a build command | Wrapping an existing Cargo / autotools / Make project |

Pick by use case, not by aesthetics:

- "I have one tight inner loop" → kernel module (zig or C).
- "I want to take a Python list and return a dict" → `.pyx` or `.rs`.
- "I want to bind libsodium from Homebrew" → pre-built kernel.
- "I have a multi-file Rust crate" → `[[tool.molt.native]]` with the
  rust preset.

All five compile to ABI-tagged `.so` files cached in `~/.molt/native/`
and staged into the project's view at sync time. They appear in
Python as ordinary modules.

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

## Where to go next

- [`demos/`](demos/) — six runnable example projects
- [`docs/MANUAL.md`](docs/MANUAL.md) — exhaustive command reference
- [`docs/global-store.md`](docs/global-store.md) — store architecture deep dive
- [`docs/kernel-modules.md`](docs/kernel-modules.md) — kernel-module reference
- [`docs/zig-odin-kernels.md`](docs/zig-odin-kernels.md) — design rationale for
  the manifest-driven kernel pipeline
- [`docs/native-modules.md`](docs/native-modules.md) — `[[tool.molt.native]]`
  recipes and presets

For each individual command, `molt <cmd> --help` is built-in.
