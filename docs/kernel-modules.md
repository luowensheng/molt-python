# Kernel modules

A **kernel module** is a Python extension built from two files:

- `<name>.molt.toml` — a manifest describing the module's exported functions
- `<name>.<ext>` — a sibling source file in any C-ABI language (Zig, C, C++ today; more easy to add)

molt finds the manifest, generates C glue + a `.pyi` stub from it, compiles your source per its language, links them into a single `.so` that Python's `import` machinery finds directly, and stages everything into the project view.

This document is the user-facing reference. For the design rationale, see [zig-odin-kernels.md](zig-odin-kernels.md).

---

## Table of contents

- [Quick start](#quick-start)
- [How it works](#how-it-works)
- [Manifest schema](#manifest-schema)
- [Configuration in `pyproject.toml`](#configuration-in-pyprojecttoml)
- [Examples](#examples)
- [Pre-built `.so` binding](#pre-built-so-binding)
- [Kernel builders](#kernel-builders)
- [Runtime path setup with `[tool.molt.runtime]`](#runtime-path-setup-with-toolmoltruntime)
- [Type reference](#type-reference)
- [What gets generated](#what-gets-generated)
- [Cache behaviour](#cache-behaviour)
- [Errors and warnings](#errors-and-warnings)
- [Limitations](#limitations)
- [How this compares to `.pyx` and `.rs`](#how-this-compares-to-pyx-and-rs)
- [Roadmap](#roadmap)

---

## Quick start

In an existing molt project, drop two files anywhere in `.` or `src/`:

```toml
# math.molt.toml
[[fn]]
name    = "add"
args    = ["i32", "i32"]
returns = "i32"

[[fn]]
name    = "mul"
args    = ["f32", "f32"]
returns = "f32"
```

```zig
// math.zig — pure Zig, no Python bindings
export fn add(a: i32, b: i32) i32 { return a + b; }
export fn mul(a: f32, b: f32) f32 { return a * b; }
```

```python
# main.py
import math
print(math.add(2, 3))      # 5
print(math.mul(2.5, 4.0))  # 10.0
```

```sh
molt run python main.py
```

That's it. molt downloads zig if needed (one-time, into `~/.molt/toolchains/zig/`), compiles the kernel, and Python imports the result directly.

---

## How it works

```
┌────────────────┐                    ┌────────────────┐
│ math.molt.toml │  ←  the manifest   │ math.zig       │
│   [[fn]] add   │                    │ export fn add  │
└────────────────┘                    └────────────────┘
        │                                       │
        │  ParseManifest                        │  zig build-obj
        ▼                                       ▼
   ModuleDescription                       math.zig.o
        │                                       │
        ├────────► emitGlueC ──► glue.c ────┐   │
        │                                    │   │
        ├────────► emitPyiStub ──► math.pyi  │   │
        │                          (sibling) │   │
        │                                    ▼   ▼
        │                              zig cc -shared
        │                                    │
        │                                    ▼
        │                            ┌──────────────┐
        │                            │ math.so      │
        │                            │  PyInit_math │
        │                            │  add(), mul()│
        │                            └──────────────┘
        │
        └─► (cached + staged into syspath view)
```

Three things matter:

1. **The manifest is the discovery key.** molt walks for `*.molt.toml` files; the source file's extension determines which compiler runs but doesn't drive discovery.
2. **The `.so` is a real CPython extension.** The generated glue contains `PyInit_<name>`, a `PyMethodDef` table, and a per-fn wrapper that does `PyArg_ParseTuple` → native call → `PyLong_FromLong`/etc. No `ctypes` overhead.
3. **The `.pyi` stub is generated from the manifest.** IDEs, mypy, pyright all see typed signatures and per-function docstrings without you writing them twice.

---

## Manifest schema

### Top-level fields

```toml
module = "math"           # optional; defaults to filename basename
                          # (math.molt.toml → "math")

source = "src/math.zig"   # optional; defaults to a sibling file matching
                          # any of the configured source_extensions
```

### `[[fn]]` blocks

Each `[[fn]]` declares one exported function:

```toml
[[fn]]
name        = "add"                  # required; matches the C-ABI symbol
description = "Sum two integers."    # optional; becomes runtime docstring + .pyi
args        = [...]                   # required; see two forms below
returns     = ...                     # required; see two forms below
```

### Argument forms

**String array** (terse — for simple primitive functions):

```toml
args = ["i32", "i32", "f32"]
```

→ `_arg0: int, _arg1: int, _arg2: float` in the generated `.pyi`.

**Inline-table array** (verbose — for named/documented args):

```toml
args = [
    { name = "a", type = "i32", description = "First addend." },
    { name = "b", type = "i32", description = "Second addend." },
]
```

→ `a: int, b: int` with per-arg docstrings.

You can mix forms across functions in the same manifest — each `[[fn]]` chooses what it needs.

### Return forms

**String form** (no description):

```toml
returns = "i32"
```

**Inline-table form** (with description):

```toml
returns = { type = "i32", description = "The sum a + b." }
```

Use `"void"` (or omit `type`) for functions that return `None` to Python:

```toml
returns = "void"
```

### Supported types (MVP)

The manifest's `type` field accepts these primitives:

| Manifest | C type | Python (`.pyi`) |
|---|---|---|
| `void` | `void` | `None` (return only) |
| `bool` | `_Bool` | `bool` |
| `i8` `i16` `i32` `i64` | `int8_t` … `int64_t` | `int` |
| `u8` `u16` `u32` `u64` | `uint8_t` … `uint64_t` | `int` |
| `isize` | `ssize_t` | `int` |
| `usize` | `size_t` | `int` |
| `f32` | `float` | `float` |
| `f64` | `double` | `float` |

Anything outside this list — pointers, structs, slices, generics, strings, bytes — causes the function to be skipped at build time with a warning. The rest of the module still builds.

---

## Configuration in `pyproject.toml`

All under `[tool.molt.native_kernel]`. Every field has a sensible default; a project with no `[tool.molt.native_kernel]` block at all still works.

```toml
[tool.molt.native_kernel]
# Project-relative directories scanned for manifest files.
# Manifests in deeper paths take priority on basename collision.
paths = [".", "src"]

# Optional centralised directory for manifests, in addition to `paths`.
# Useful when you want manifests version-controlled separately from sources.
manifest_dir = "manifests"

# Sibling source-file extensions tried when pairing a manifest to its source.
# First match wins. Add ".odin", ".nim", etc. when more languages are wired.
source_extensions = [".zig", ".c", ".cpp", ".cc", ".cxx"]

# Manifest filename suffixes recognised during discovery.
# Future-proof for ".molt.json" / ".molt.yaml" formats.
manifest_suffixes = [".molt.toml"]
```

### Resolving a manifest to its source file

For a manifest at `<dir>/foo.molt.toml`, molt looks for:

1. The path in the manifest's optional `source = "..."` field, if set.
2. `<dir>/foo<ext>` for each `<ext>` in `source_extensions`, in order.

The first one that exists wins.

---

## Examples

### Example 1 — Zig kernel, sibling layout (the simplest case)

```
project/
├── pyproject.toml
├── math.molt.toml
├── math.zig
└── main.py
```

```toml
# math.molt.toml
[[fn]]
name = "add"
args = ["i32", "i32"]
returns = "i32"

[[fn]]
name = "dot3"
args = ["f32", "f32", "f32", "f32", "f32", "f32"]
returns = "f32"
description = "Dot product of two 3-vectors flattened into 6 floats."
```

```zig
// math.zig
export fn add(a: i32, b: i32) i32 { return a + b; }

export fn dot3(ax: f32, ay: f32, az: f32, bx: f32, by: f32, bz: f32) f32 {
    return ax * bx + ay * by + az * bz;
}
```

```python
# main.py
import math
print(math.add(2, 3))                          # 5
print(math.dot3(1, 0, 0, 0, 1, 0))             # 0.0
print(math.dot3(1.0, 2.0, 3.0, 4.0, 5.0, 6.0)) # 32.0
```

```sh
molt run python main.py
```

Output:
```
→ kernel: 1 module(s)
  ↻ kernel  math  [zig]
5
0.0
32.0
```

Subsequent runs hit the cache:
```
  ✓ kernel  math  (cached)
```

### Example 2 — Plain C kernel

```
project/
├── pyproject.toml
├── crypto.molt.toml
└── crypto.c
```

```toml
# crypto.molt.toml
[[fn]]
name        = "rot13"
description = "Rotate a single ASCII character by 13."
args        = [{ name = "c", type = "u8" }]
returns     = "u8"

[[fn]]
name    = "checksum_byte"
args    = [
    { name = "seed", type = "u32", description = "Starting hash value." },
    { name = "byte", type = "u8",  description = "Next byte to mix in." },
]
returns = { type = "u32", description = "Updated hash." }
```

```c
/* crypto.c */
#include <stdint.h>

uint8_t rot13(uint8_t c) {
    if (c >= 'a' && c <= 'z') return ((c - 'a' + 13) % 26) + 'a';
    if (c >= 'A' && c <= 'Z') return ((c - 'A' + 13) % 26) + 'A';
    return c;
}

uint32_t checksum_byte(uint32_t seed, uint8_t byte) {
    return seed * 31 + (uint32_t)byte;
}
```

```python
import crypto
print(chr(crypto.rot13(ord('A'))))   # N
print(crypto.checksum_byte(0, 65))   # 65
```

molt detects the sibling `.c`, compiles with `cc`, links via `zig cc`, and the import works.

### Example 3 — Centralised manifest directory

For larger projects you may want to keep manifests separate from source:

```
project/
├── pyproject.toml
├── manifests/
│   ├── crypto.molt.toml
│   ├── physics.molt.toml
│   └── vision.molt.toml
└── src/
    ├── crypto.zig
    ├── physics.zig
    └── vision.cpp
```

```toml
# pyproject.toml
[tool.molt.native_kernel]
paths        = ["src"]
manifest_dir = "manifests"
```

Each manifest in `manifests/` is paired by basename to its source under `src/`. Editing either file forces a rebuild on next `molt run`.

### Example 4 — Mix of manual `[[fn]]` blocks with descriptions

The descriptions feed three things at once: the runtime `__doc__`, the `.pyi` stub's docstring, and (eventually) generated reference docs.

```toml
# physics.molt.toml
module = "physics"

[[fn]]
name = "kinetic_energy"
description = """
Compute kinetic energy of a point mass.

KE = 0.5 * m * v^2 in SI units (kg, m/s, J).
"""
args = [
    { name = "mass",     type = "f64", description = "Mass in kg." },
    { name = "velocity", type = "f64", description = "Velocity in m/s." },
]
returns = { type = "f64", description = "Kinetic energy in joules." }

[[fn]]
name        = "free_fall_distance"
description = "Distance covered in free fall during `t` seconds at Earth gravity."
args        = [{ name = "t", type = "f64" }]
returns     = "f64"
```

```zig
// physics.zig
export fn kinetic_energy(mass: f64, velocity: f64) f64 {
    return 0.5 * mass * velocity * velocity;
}

export fn free_fall_distance(t: f64) f64 {
    return 0.5 * 9.81 * t * t;
}
```

The Python user gets typed completion:

```python
import physics
help(physics.kinetic_energy)
```
```
kinetic_energy(mass: float, velocity: float) -> float

Compute kinetic energy of a point mass.

KE = 0.5 * m * v^2 in SI units (kg, m/s, J).

Args:
    mass: Mass in kg.
    velocity: Velocity in m/s.
```

### Example 5 — Multiple modules in one project

molt finds all manifests under the configured roots and compiles each independently:

```
project/
├── pyproject.toml
├── physics.molt.toml
├── physics.zig
└── src/
    ├── crypto.molt.toml
    ├── crypto.c
    ├── audio/
    │   ├── mixer.molt.toml
    │   └── mixer.zig
```

```python
import physics
import crypto
import src.audio.mixer  # nested package path is preserved
```

Each compiles to its own `.so` + `.pyi`. Caches are independent — editing `crypto.c` rebuilds `crypto`, leaves `physics` and `mixer` untouched.

### Example 6 — Overriding the source path

When the source file isn't a sibling, set `source` explicitly:

```toml
# fast.molt.toml
source = "vendor/zigmath/main.zig"

[[fn]]
name = "matmul"
args = ["i32", "i32", "i32"]
returns = "f32"
```

Path is relative to the manifest's directory.

---

## Pre-built `.so` binding

If you already have a compiled shared library — vendor-supplied,
downloaded as a release asset, or built outside molt — you don't need
a source file. Drop a `<name>.so` (or `lib<name>.so` / `.dylib` /
`.dll`) next to a `<name>.molt.toml` and molt generates a
**dlopen-style wrapper** around it.

```
project/
├── crypto.molt.toml
└── libcrypto.so          # pre-built — vendor / download / external build
```

```toml
# crypto.molt.toml
[[fn]]
name    = "encrypt"
args    = [{ name = "key", type = "u32" }, { name = "data", type = "u32" }]
returns = "u32"
```

```python
import crypto
crypto.encrypt(0xdeadbeef, 42)
```

molt detects the lack of a source file, generates a `glue.c` that
`dlopen`s the lib at `PyInit_crypto` time, `dlsym`s each declared
function into a static fn-pointer slot, and forwards Python calls
through the pointers. Same `.pyi` stub generation as for source-built
kernels.

Three resolution modes (priority order):

| Manifest field / file | Resolution |
|---|---|
| `library = "/abs/path/libfoo.so"` | Explicit absolute path. |
| `library = "vendor/libfoo.so"` | Relative to manifest dir. |
| (no `library`, no source sibling) | Sibling `<name>.so` / `lib<name>.so` / `.dylib` / `.dll`. |

The wrapper has no link-time dependency on the impl `.so` — the impl
is loaded at import time. Vendor updates trigger a wrapper rebuild
via the cache hash (which mixes the impl's content) but no manual
cleanup.

### When to use this

- Bind a brew/Nix-installed system library:
  `library = "/opt/homebrew/lib/libsodium.dylib"`.
- Bind a Rust crate built outside molt:
  `library = "../rust-crate/target/release/libfoo.so"`.
- Distribute a pre-compiled vendor SDK alongside Python source: ship
  `vendor/libfoo.so` + `foo.molt.toml`; users get `import foo`.
- Glue around system libs (zlib, libcurl, libcrypto) without writing
  Cython.

## Kernel builders

A "kernel builder" is the recipe for compiling one source-file
extension to a position-independent `.o`. Resolution order:

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

Token vocabulary in commands: `{source}`, `{output}`, `{zig}`, `{cc}`,
`{cxx}`, `{include_dir}`. Unknown tokens pass through unchanged.

`--from-template` uses molt's curated commands for `zig`, `c`, `cpp`,
`odin`, `nim`, `rs` (rustc no-PyO3), `f90`, `f95`. Languages outside
that table require an explicit `<command>` argument.

### Auto-watching

Adding a builder for `.odin` (or any extension) **automatically** adds
that extension to molt's source-extension watch list. You do NOT need
to also edit `[tool.molt.native_kernel] source_extensions = [...]` —
the builder registry IS the source of truth.

```sh
$ molt kernel-builder add odin --from-template
# Now: any project with mathx.odin + mathx.molt.toml just works.
```

## Runtime path setup with `[tool.molt.runtime]`

Kernel modules that depend on shared libraries at *runtime* (e.g. a
pre-built `.so` whose impl in turn dlopens `libomp.so` or
`libcrypto.so`) need those deps findable when Python loads the
wrapper. The dedicated mechanism:

```toml
[tool.molt.runtime]
extra_paths = ["vendor/lib", "vendor/bin", "vendor/Frameworks"]
```

Each entry is prepended to `PATH`, plus the platform-appropriate
dynamic-linker var (`LD_LIBRARY_PATH` on Linux,
`DYLD_FALLBACK_LIBRARY_PATH` and `DYLD_FALLBACK_FRAMEWORK_PATH` on
macOS) when molt spawns Python. Drop a `.so` or a binary or a
framework into one of those dirs and it's findable from any code in
the Python process — your kernel wrapper, your other modules,
`subprocess.run("foo")`, `ctypes.CDLL("...")`.

**Note:** This is distinct from the older `[tool.molt] extra_paths`
field, which adds *Python source* directories to `PYTHONPATH` (for
sharing pure-Python helper modules across projects). The new
`[tool.molt.runtime] extra_paths` is for native binaries and shared
libraries.

## Type reference

The same primitive table from above, expanded with the C-glue runtime conversions:

| Manifest | C declaration | `PyArg_ParseTuple` fmt | Python wrap |
|---|---|---|---|
| `void` | `void` | (n/a — return only) | `Py_RETURN_NONE` |
| `bool` | `_Bool` | `p` | `PyBool_FromLong` |
| `i8` `i16` `i32` | `int8/16/32_t` | `i` | `PyLong_FromLong` |
| `i64` `isize` | `int64_t` / `ssize_t` | `L` | `PyLong_FromLongLong` |
| `u8` `u16` `u32` | `uint8/16/32_t` | `I` | `PyLong_FromUnsignedLong` |
| `u64` `usize` | `uint64_t` / `size_t` | `K` | `PyLong_FromUnsignedLongLong` |
| `f32` | `float` | `f` | `PyFloat_FromDouble` |
| `f64` | `double` | `d` | `PyFloat_FromDouble` |

Width-correct casts happen at the call boundary so that, e.g., a Python `int` parsed via `i` (C `int`) is cast back down to `uint8_t` cleanly before calling your function.

### What about pointers and strings?

Out of scope for the MVP. If your function takes pointers or buffers, the manifest skips it with a warning at build time and the rest of the module still builds. The roadmap section below lists what's planned.

---

## What gets generated

Every kernel build produces three files:

### `<name>.so` — the importable extension

A real CPython extension module. Has `PyInit_<name>`, a `PyMethodDef` table, and per-function wrappers that:
1. Parse Python args via `PyArg_ParseTuple` (with width-correct types).
2. Call your C-ABI symbol with width-correct casts.
3. Wrap the result back into a `PyObject*`.

You can `import <name>` directly.

### `<name>.pyi` — the type stub

A typed stub for IDEs, mypy, and pyright. Looks like:

```python
"""Auto-generated by molt. Source: /abs/path/to/math.zig"""

def add(a: int, b: int) -> int:
    """Sum two integers.

    Args:
        a: First addend.
        b: Second addend.
    """
    ...

def mul(a: float, b: float) -> float: ...
```

molt symlinks both `.so` and `.pyi` into the same directory in the project view, so editors find them naturally.

### `glue.c` — the generated wrapper (in cache)

Lives in `~/.molt/native/kernel-build/<module>-<hash>/glue.c`. You'd only look at it if debugging. Sample fragment:

```c
/* Auto-generated by molt — do not edit. */
#define PY_SSIZE_T_CLEAN
#include <Python.h>
#include <stdint.h>

extern int32_t add(int32_t, int32_t);

static PyObject* py_add(PyObject* self, PyObject* args) {
    int _arg0;
    int _arg1;
    if (!PyArg_ParseTuple(args, "ii", &_arg0, &_arg1)) return NULL;
    int32_t _ret = add((int32_t)_arg0, (int32_t)_arg1);
    return PyLong_FromLong((long)(_ret));
}

static PyMethodDef _molt_methods[] = {
    {"add", (PyCFunction)py_add, METH_VARARGS, NULL},
    {NULL, NULL, 0, NULL}
};

static struct PyModuleDef _molt_moddef = {
    PyModuleDef_HEAD_INIT, "math", NULL, -1, _molt_methods,
};

PyMODINIT_FUNC PyInit_math(void) {
    return PyModule_Create(&_molt_moddef);
}
```

---

## Cache behaviour

The cache key for a kernel mixes:

| Input | Why |
|---|---|
| Manifest content | New `[[fn]]` or changed types → rebuild |
| Source file content | Editing `.zig` / `.c` → rebuild |
| Python ABI tag (e.g. `cp311`) | Switching venvs from 3.11 to 3.12 → rebuild |
| Platform (e.g. `darwin-arm64`) | Cross-compile or new machine → fresh build |
| Python include directory | Encodes Python's exact patch version |
| Recipe version (`v1`) | Future molt build-pipeline changes invalidate old caches automatically |

Hits print `✓ kernel  <module>  (cached)`; misses print `↻ kernel  <module>  [<lang>]`.

The cache lives under `~/.molt/native/<hash>/` (one dir per build). Deleting the directory forces a rebuild; nothing else is needed.

---

## Errors and warnings

| What you see | What it means |
|---|---|
| `warn: skipping `foo` — unsupported return type "Color"` | Function uses a type outside the supported primitive set (struct, pointer, etc.). The function is dropped from the binding; the rest of the module still builds. |
| `warn: skipping `foo` — unsupported arg 0 type "*const u8"` | Same, but for an argument. |
| `<path>: no source file found (looked for foo.{zig,c,cpp,cc,cxx} alongside)` | The manifest doesn't have a sibling source matching the configured `source_extensions`. Set the `source = "..."` field explicitly or rename the source file. |
| `zig build-obj <source>: error: ...` | The Zig compiler rejected your source. molt forwards stdout/stderr verbatim. |
| `link kernel .so: error: ...` | The link step failed (usually missing `Python.h` or symbol mismatch). Check the manifest's signatures against the source's actual `export fn` declarations. |
| `expected an array, got "…"` | Manifest TOML syntax error in an `args = …` line. Use either the string-array form or the inline-table-array form. |

---

## Limitations

These are explicit gaps in the MVP. Each is straightforward to add when needed.

- **No pointer / buffer types.** Arguments and returns must be primitives. Functions that need byte buffers or numeric arrays are dropped from the binding.
- **No struct types.** No `[[type]]` table support yet; manifests can't define record-shaped values.
- **No string conversions.** No `cstring` ↔ `str` mapping.
- **No keyword-argument support.** Generated wrappers use `METH_VARARGS` only; `kwargs` are accepted but ignored at the C level. (The `.pyi` still lists names so kwarg-style calls work in the type checker.)
- **No `[[type]]` definitions.** Types are limited to the primitive list; no user-defined types.
- **TOML only.** `manifest_suffixes` is plumbed for `.molt.json` / `.molt.yaml` but the parser only handles `.molt.toml`.
- **Wired languages out of the box: Zig, C, C++.** Adding Odin, Nim, Rust-without-PyO3, Fortran, etc. is one entry in `~/.molt/kernel-builders.yaml` (see [Kernel builders](#kernel-builders) below) — no molt code change.

---

## How this compares to `.pyx` and `.rs`

| | `.pyx` (Cython) | `.rs` (PyO3) | `.molt.toml` (kernel) |
|---|---|---|---|
| Discovery | walk for `.pyx` | walk for `.rs` w/ pyo3 | walk for `.molt.toml` |
| Source language | Python-flavoured DSL | Rust + pyo3 macros | any C-ABI language |
| Boilerplate | generated by Cython | generated by pyo3 macros | generated by molt from manifest |
| Object types (Python list/dict/str) | yes | yes | no (primitives only, MVP) |
| Auto-installs toolchain | Cython via uv | cargo expected on PATH | zig auto-installed; cc expected for C/C++ |
| `.pyi` for IDE | not generated | not generated | yes (from manifest) |
| Add a new language | n/a (Cython only) | n/a (Rust only) | one switch arm in `compileKernelSource` |

Kernel modules are not a replacement for `.pyx` or PyO3 — when you need to take a Python list as input or return a dict, those tools have decades of polish you should use instead. Kernels are for the case where you want a fast native function with a primitive signature, written in whatever language you're already using, with zero per-language framework on top.

---

## Roadmap

In rough priority order:

1. **Pointer / buffer types** — `*f32`, `*const u8`, `bytes` → `Buffer` (PEP 688) at the manifest layer, mapped to C pointers and Python `Buffer` annotations. Unlocks numerical kernels.
2. **String types** — `cstring` ↔ Python `str` with UTF-8 encode/decode in the glue.
3. **Keyword-argument support** — emit `METH_VARARGS | METH_KEYWORDS` wrappers when args have explicit names. Makes `inspect.signature` work at runtime.
4. **`[[type]]` blocks** — user-defined struct types referenced from `[[fn]]` args/returns. Maps to `PyArg_ParseTuple` with `O&` converters and a generated ctypes-shaped Python class in the `.pyi`.
5. **`.molt.json` / `.molt.yaml`** — plug-in parsers behind `manifest_suffixes`. ~30 lines per format.
6. **Pre-built `.so` path** — for binding to existing native libraries via dlopen-style glue. Architecture supports it; just an alternative `compileKernelSource` arm.
7. **Odin and other languages** — one switch arm each in `compileKernelSource`. Trivial once the language has a binary release distribution.
8. **`.pyi` for `.pyx` / `.rs`** — same emitter pipeline, fed by source-parsing instead of manifest. Brings IDE support to existing Cython and PyO3 modules.
9. **Markdown reference output** — optional `<name>.md` next to the `.so` from the same manifest, for docs sites.

---

## Cheat sheet

```toml
# pyproject.toml — fully optional; defaults are sensible
[tool.molt.native_kernel]
paths             = [".", "src"]
manifest_dir      = "manifests"            # optional centralised location
source_extensions = [".zig", ".c", ".cpp"]
manifest_suffixes = [".molt.toml"]
```

```toml
# <name>.molt.toml — minimal manifest
[[fn]]
name    = "add"
args    = ["i32", "i32"]
returns = "i32"
```

```zig
// <name>.zig — primitives only, no Python bindings
export fn add(a: i32, b: i32) i32 { return a + b; }
```

```python
import name
name.add(2, 3)
```

```sh
molt run python main.py     # finds, compiles, runs
```

That's the whole loop.
