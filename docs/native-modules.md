# Native Modules in Molt

Molt lets you write performance-critical code in Cython, Rust, C, or C++ and import it from Python as if it were a normal `.py` file — no `setup.py`, no `pip install -e .`, no manual compilation step. Drop a `.pyx` or `.rs` file next to your `main.py`, run `molt sync`, and `import mathx` just works.

---

## How it works

When you run `molt sync`, Molt:

1. **Discovers** native source files (`.pyx`, `.rs` with PyO3, and any `[[tool.molt.native]]` entries in `pyproject.toml`)
2. **Compiles** each one into a shared library (`.so` / `.dylib` / `.pyd`) if its content has changed since the last build
3. **Caches** the output in `~/.molt/native/<hash>/`, keyed by a hash of the source content, Python ABI, platform, and compiler flags — so cache hits are instant and cross-project
4. **Places a view** by symlinking compiled libraries into the project's state directory and prepending it to `sys.path` before your Python process starts

On subsequent `molt run python` / `molt run python3` calls, Molt automatically checks whether any source file is newer than the last compiled output. If anything changed, it recompiles before executing — no manual `molt sync` needed.

---

## Supported source types

| Source | How it's detected | What compiles it |
|---|---|---|
| `*.pyx` | Auto-discovered in project paths | Cython → C → shared library |
| `*.rs` with `#[pymodule]` / `use pyo3` | Auto-discovered in project paths | `cargo build --release` via auto-generated `Cargo.toml` |
| Rust project folder (has `Cargo.toml`) | `[[tool.molt.native]]` in `pyproject.toml` | `cargo build --release` in that folder |
| Any language with a shell build command | `[[tool.molt.native]]` with `build =` | User-defined shell command |

---

## Quick start

### Cython

```
project/
├── mathx.pyx
├── main.py
└── pyproject.toml
```

```toml
# pyproject.toml
[project]
name = "my-project"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = ["cython"]
```

```python
# mathx.pyx
def add(int a, int b):
    return a + b

def greet(str name):
    return f"Hello from Cython, {name}!"
```

```python
# main.py
import mathx

print(mathx.add(3, 4))      # → 7
print(mathx.greet("world")) # → Hello from Cython, world!
```

```
$ molt sync
→ cython: 1 source(s)
  ↻ cython  mathx
✓ done

$ molt run python main.py
7
Hello from Cython, world!
```

Edit `mathx.pyx` and run again — Molt recompiles automatically:

```
$ molt run python main.py
→ native sources changed, recompiling…
  ↻ cython  mathx
7
Hello from Cython, world!
```

### Rust (single file, PyO3)

```
project/
├── crypto.rs
├── main.py
└── pyproject.toml
```

```toml
# pyproject.toml
[project]
name = "my-project"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = []
```

```rust
// crypto.rs
use pyo3::prelude::*;

#[pyfunction]
fn xor_bytes(data: Vec<u8>, key: u8) -> Vec<u8> {
    data.iter().map(|b| b ^ key).collect()
}

#[pymodule]
fn crypto(_py: Python, m: &PyModule) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(xor_bytes, m)?)?;
    Ok(())
}
```

```python
# main.py
import crypto

data = b"hello"
encrypted = bytes(crypto.xor_bytes(list(data), 0xFF))
print(encrypted)  # → b'\x97\x9a\x93\x93\x90'
```

```
$ molt sync
→ rust: 1 source(s)
  ↻ rust  crypto
✓ done

$ molt run python main.py
b'\x97\x9a\x93\x93\x90'
```

Molt auto-generates a `Cargo.toml` (stored in `~/.molt/native/rust-build/`) — you never see it. The `.rs` file itself is the only thing you manage.

---

## Project layouts

### Flat layout (default)

The simplest case: everything at the project root. No configuration needed.

```
project/
├── main.py
├── mathx.pyx       ← compiled, importable as `mathx`
├── crypto.rs       ← compiled, importable as `crypto`
└── pyproject.toml
```

### Nested packages

Cython files inside packages are discovered automatically and their module names reflect the package hierarchy:

```
project/
├── main.py
├── pyproject.toml
└── src/
    └── myapp/
        ├── __init__.py
        ├── fast/
        │   ├── _inner.pyx   ← importable as `myapp.fast._inner`
        │   └── utils.pyx    ← importable as `myapp.fast.utils`
        └── math.pyx         ← importable as `myapp.math`
```

```python
# main.py
from myapp.fast._inner import process
from myapp.math import dot_product
```

### Controlling which paths are scanned

By default Molt scans `.` and `src/`. Override with:

```toml
[tool.molt.cython]
paths = ["src", "extensions", "vendor/fast"]
```

---

## Cython configuration

### Compiler directives

Cython directives tune language behaviour and performance. Set them project-wide:

```toml
[tool.molt.cython]
directives = { boundscheck = "False", wraparound = "False", cdivision = "True" }
```

These are equivalent to `# cython: boundscheck=False` at the top of every `.pyx` file. Common directives:

| Directive | Default | Effect |
|---|---|---|
| `boundscheck` | `True` | Disable index bounds checking — faster array access, unsafe if indices can be out of range |
| `wraparound` | `True` | Disable negative index support — small speedup for tight loops |
| `cdivision` | `False` | Use C integer division instead of Python's — avoids `ZeroDivisionError` check |
| `nonecheck` | `False` | Enable `None` checks on typed extension type attributes |
| `language_level` | `"3str"` | Python 3 string literals (Molt default) |

### Extra compiler flags

Pass arbitrary flags to the C compiler:

```toml
[tool.molt.cython]
extra_compile_args = ["-O3", "-march=native", "-ffast-math"]
```

Useful for: architecture-specific optimisations (`-march=native`), floating-point relaxation (`-ffast-math`), link-time optimisation (`-flto`).

### Full Cython config example

```toml
[tool.molt.cython]
paths = ["src", "extensions"]
directives = { boundscheck = "False", wraparound = "False", cdivision = "True", nonecheck = "False" }
extra_compile_args = ["-O3", "-march=native"]
```

---

## Rust configuration

### Single `.rs` file (PyO3)

Auto-discovered — no configuration needed. Requirements:
- The file must contain `use pyo3`, `pyo3::`, or `#[pymodule]`
- `cargo` must be on `PATH`

The module name is the filename without the `.rs` extension: `crypto.rs` → `import crypto`.

### Rust cargo project

For larger Rust modules that need multiple files, a `Cargo.toml`, or specific dependencies:

```
project/
├── main.py
├── pyproject.toml
└── rust_imgproc/
    ├── Cargo.toml
    └── src/
        ├── lib.rs
        ├── filters.rs
        └── transform.rs
```

```toml
# pyproject.toml
[[tool.molt.native]]
module = "imgproc"
src    = "rust_imgproc/"
preset = "rust"
```

```toml
# rust_imgproc/Cargo.toml
[package]
name    = "imgproc"
version = "0.1.0"
edition = "2021"

[lib]
crate-type = ["cdylib"]

[dependencies]
pyo3 = { version = "0.21", features = ["extension-module"] }
image = "0.25"
```

```python
# main.py
import imgproc

result = imgproc.gaussian_blur(pixels, sigma=1.5)
```

Molt hashes all `.rs` files, `Cargo.toml`, and `Cargo.lock` together — the entire project is a single cache unit, rebuilt only when any of those files change.

---

## Generic build commands (`[[tool.molt.native]]`)

For languages and toolchains beyond Cython and Rust, use `[[tool.molt.native]]` with a custom build command. Each entry produces one importable module.

### Syntax

```toml
[[tool.molt.native]]
module  = "<import name>"          # required: what you'll `import`
src     = "<path>"                 # required: source folder (for hashing + $PWD)
build   = "<shell command>"        # required (or use preset =)
output  = "<path to .so output>"   # required (or use preset =)
```

The `build` and `output` fields support token substitution:

| Token | Expands to |
|---|---|
| `{module}` | The value of `module =` |
| `{ext}` | Platform extension: `dylib` (macOS), `so` (Linux), `pyd` (Windows) |
| `{python_include}` | Path to the directory containing `Python.h` |
| `{src_files}` | Space-separated list of all source files found under `src` |
| `{output}` | The resolved output path (only in `build =`) |

### C example

```
project/
├── main.py
├── pyproject.toml
└── cpp_utils/
    ├── utils.c
    └── utils.h
```

```toml
[[tool.molt.native]]
module = "utils"
src    = "cpp_utils/"
build  = "cc -O2 -shared -fPIC -I {python_include} -o {output} {src_files}"
output = "{module}.{ext}"
```

```c
// cpp_utils/utils.c
#define PY_SSIZE_T_CLEAN
#include <Python.h>

static PyObject* py_add(PyObject* self, PyObject* args) {
    int a, b;
    if (!PyArg_ParseTuple(args, "ii", &a, &b)) return NULL;
    return PyLong_FromLong(a + b);
}

static PyMethodDef methods[] = {
    {"add", py_add, METH_VARARGS, "Add two integers"},
    {NULL, NULL, 0, NULL}
};

static struct PyModuleDef module = {
    PyModuleDef_HEAD_INIT, "utils", NULL, -1, methods
};

PyMODINIT_FUNC PyInit_utils(void) { return PyModule_Create(&module); }
```

```python
import utils
print(utils.add(10, 20))  # → 30
```

### C++ example

```toml
[[tool.molt.native]]
module = "fastjson"
src    = "fastjson_src/"
build  = "c++ -O2 -std=c++17 -shared -fPIC -I {python_include} -o {output} {src_files}"
output = "{module}.{ext}"
```

### Zig example

```toml
[[tool.molt.native]]
module = "zigsort"
src    = "zig_src/"
build  = "zig build-lib -dynamic -O ReleaseFast {src_files} -lc -o {output}"
output = "libzigsort.{ext}"
```

### Meson / CMake example (custom build script)

```toml
[[tool.molt.native]]
module  = "mlext"
src     = "mlext_src/"
build   = "bash mlext_src/build.sh {python_include} {output}"
output  = "{module}.{ext}"
```

```bash
# mlext_src/build.sh
PYTHON_INCLUDE=$1
OUTPUT=$2
cmake -B build -S . -DPYTHON_INCLUDE=$PYTHON_INCLUDE
cmake --build build --config Release
cp build/libmlext.so "$OUTPUT"
```

---

## Multiple native modules in one project

Any combination of sources works. All compiled modules land in the same view directory and are importable with plain `import`:

```
project/
├── main.py
├── pyproject.toml
├── mathx.pyx          ← Cython
├── crypto.rs          ← Rust single file (PyO3)
├── rust_imgproc/      ← Rust cargo project
│   ├── Cargo.toml
│   └── src/lib.rs
└── cpp_utils/         ← C with custom build
    ├── utils.c
    └── utils.h
```

```toml
# pyproject.toml
[project]
name = "my-project"
version = "0.1.0"
requires-python = ">=3.11"
dependencies = ["cython"]

[tool.molt.cython]
directives = { boundscheck = "False", wraparound = "False" }

[[tool.molt.native]]
module = "imgproc"
src    = "rust_imgproc/"
preset = "rust"

[[tool.molt.native]]
module = "utils"
src    = "cpp_utils/"
build  = "cc -O2 -shared -fPIC -I {python_include} -o {output} {src_files}"
output = "{module}.{ext}"
```

```python
# main.py — all four import statements just work
import mathx    # from mathx.pyx
import crypto   # from crypto.rs (PyO3)
import imgproc  # from rust_imgproc/ (cargo project)
import utils    # from cpp_utils/ (C with custom build)

print(mathx.add(3, 4))
print(crypto.xor_bytes([0x41, 0x42], 0xFF))
print(imgproc.resize([...], 224, 224))
print(utils.add(10, 20))
```

Each module is compiled and cached independently. Editing `crypto.rs` rebuilds only `crypto`; `mathx`, `imgproc`, and `utils` are served from cache.

---

## Content-addressed cache

All compiled outputs live in `~/.molt/native/<hash>/`, where `<hash>` is derived from:

- The source file content (or all source files for multi-file projects)
- The Python ABI tag (e.g. `cp311`)
- The platform tag (e.g. `macosx_14_0_arm64`, `linux_x86_64`)
- For Cython: compiler directives and extra compile args

This means:
- **Cross-project sharing**: Two projects using identical `mathx.pyx` with the same Python version share the same cached `.so`
- **ABI safety**: Switching Python versions (e.g. 3.11 → 3.12) produces a different hash and a separate cache entry — no stale `.so` files
- **Instant cache hits**: If content hasn't changed, compilation is skipped regardless of file timestamps
- **Safe parallel builds**: Each module's cache slot is unique; concurrent `molt sync` calls don't corrupt each other

The per-project view lives at `~/.molt/projects/<project-hash>/cython/` and contains symlinks (or copies on Windows) pointing into the global cache.

---

## Change detection and auto-sync

`molt run python` and `molt run python3` automatically detect changes before executing:

```
$ molt run python main.py           # first run — compiles
→ native sources changed, recompiling…
  ↻ cython  mathx
7

$ molt run python main.py           # no changes — instant
7

$ vim mathx.pyx                     # edit the file

$ molt run python main.py           # auto-detects change
→ native sources changed, recompiling…
  ↻ cython  mathx
7
```

Detection is mtime-based: Molt compares the modification time of each source file against the modification time of the compiled output manifest. Only changed modules are recompiled — unchanged ones are served from cache.

For external modules (`[[tool.molt.native]]`), Molt walks the entire `src` directory looking for any file newer than the last build.

---

## Presets

Presets are named, reusable build configurations stored globally at `~/.molt/native-presets.yaml`. They reduce repetition when you use the same build pattern across projects.

### Built-in presets

Three presets are seeded on first use:

| Name | Build command | Output |
|---|---|---|
| `rust` | `cargo build --release` | `target/release/lib{module}.{ext}` |
| `c` | `cc -O2 -shared -fPIC -I {python_include} -o {output} {src_files}` | `{module}.{ext}` |
| `cpp` | `c++ -O2 -shared -fPIC -I {python_include} -o {output} {src_files}` | `{module}.{ext}` |

### Using a preset

```toml
[[tool.molt.native]]
module = "imgproc"
src    = "rust_imgproc/"
preset = "rust"
```

```toml
[[tool.molt.native]]
module = "mylib"
src    = "mylib_src/"
preset = "cpp"
```

### Managing presets

```bash
# List all presets
molt native-preset list

# Show details of one preset
molt native-preset show rust

# Add a new preset
molt native-preset add \
  --name zig \
  --build "zig build-lib -dynamic -O ReleaseFast {src_files} -lc -o {output}" \
  --output "lib{module}.{ext}" \
  --src-patterns "**/*.zig,build.zig"

# Remove a preset
molt native-preset remove zig
```

### Overriding preset fields per-project

A preset provides defaults. Any field in `[[tool.molt.native]]` overrides the preset value:

```toml
[[tool.molt.native]]
module = "imgproc"
src    = "rust_imgproc/"
preset = "rust"
# Override: add extra cargo flags via a wrapper script
build  = "cargo build --release --features simd"
```

---

## Use cases

### 1. Speeding up a numerical hotspot

You have a Python function that's too slow. Rewrite just the hot inner loop in Cython, keep everything else in Python:

```python
# Before: pure Python, slow on large arrays
def moving_average(data, window):
    result = []
    for i in range(len(data) - window + 1):
        result.append(sum(data[i:i+window]) / window)
    return result
```

```python
# fastmath.pyx — drop-in replacement, typed for speed
import numpy as np
cimport numpy as np

def moving_average(np.ndarray[double, ndim=1] data, int window):
    cdef int n = len(data) - window + 1
    cdef np.ndarray[double, ndim=1] result = np.empty(n)
    cdef double s = sum(data[:window])
    result[0] = s / window
    for i in range(1, n):
        s += data[i + window - 1] - data[i - 1]
        result[i] = s / window
    return result
```

```python
# main.py — unchanged import style
import fastmath
import numpy as np

data = np.random.rand(1_000_000)
avg = fastmath.moving_average(data, 50)
```

```toml
[project]
name = "myapp"
requires-python = ">=3.11"
dependencies = ["cython", "numpy"]

[tool.molt.cython]
directives = { boundscheck = "False", wraparound = "False" }
```

### 2. Wrapping a C library

You have a C library (`libsodium`, `zlib`, etc.) and want to call it from Python without pulling in `cffi` or `ctypes`:

```c
// sodium_wrap.c — thin PyO3-style wrapper using Python's C API
#define PY_SSIZE_T_CLEAN
#include <Python.h>
#include <sodium.h>

static PyObject* py_randombytes(PyObject* self, PyObject* args) {
    int n;
    if (!PyArg_ParseTuple(args, "i", &n)) return NULL;
    unsigned char* buf = malloc(n);
    randombytes_buf(buf, n);
    PyObject* result = PyBytes_FromStringAndSize((char*)buf, n);
    free(buf);
    return result;
}

static PyMethodDef methods[] = {
    {"randombytes", py_randombytes, METH_VARARGS, NULL},
    {NULL, NULL, 0, NULL}
};
static struct PyModuleDef moddef = { PyModuleDef_HEAD_INIT, "sodium_wrap", NULL, -1, methods };
PyMODINIT_FUNC PyInit_sodium_wrap(void) { return PyModule_Create(&moddef); }
```

```toml
[[tool.molt.native]]
module = "sodium_wrap"
src    = "sodium_wrap_src/"
build  = "cc -O2 -shared -fPIC -I {python_include} -lsodium -o {output} {src_files}"
output = "{module}.{ext}"
```

```python
import sodium_wrap
key = sodium_wrap.randombytes(32)
```

### 3. Rust for safe concurrency

Use Rust's thread safety guarantees for a CPU-bound parallel workload:

```rust
// parallel_sum.rs
use pyo3::prelude::*;
use rayon::prelude::*;

#[pyfunction]
fn parallel_sum(data: Vec<f64>) -> f64 {
    data.par_iter().sum()
}

#[pyfunction]
fn parallel_map_square(data: Vec<f64>) -> Vec<f64> {
    data.par_iter().map(|x| x * x).collect()
}

#[pymodule]
fn parallel_sum(_py: Python, m: &PyModule) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(parallel_sum, m)?)?;
    m.add_function(wrap_pyfunction!(parallel_map_square, m)?)?;
    Ok(())
}
```

```python
import parallel_sum

data = list(range(10_000_000))
total = parallel_sum.parallel_sum(data)
squares = parallel_sum.parallel_map_square(data)
```

### 4. Machine learning inference in C++

Wrap a native ML inference engine (e.g. ONNX Runtime, TensorRT) for zero-copy tensor passing:

```toml
[[tool.molt.native]]
module  = "onnx_infer"
src     = "onnx_infer_src/"
build   = "c++ -O3 -std=c++17 -shared -fPIC -I {python_include} -I/usr/local/include/onnxruntime -lonnxruntime -o {output} {src_files}"
output  = "{module}.{ext}"
```

```python
import onnx_infer

session = onnx_infer.Session("model.onnx")
result = session.run(input_tensor)
```

### 5. Gradual migration from Python to Cython

You want to migrate a module incrementally. Start with pure Python, add type annotations as `.pxd` hints, then rewrite the inner loop in Cython — the `import` line in your code never changes:

```python
# Phase 1: pure Python
# parser.py
def parse_csv(path):
    ...
```

```python
# Phase 2: Cython with Python fallback types
# parser.pyx
def parse_csv(str path):
    cdef list rows = []
    ...
```

```python
# main.py — unchanged across all phases
import parser
rows = parser.parse_csv("data.csv")
```

### 6. Shared native code across a monorepo

In a monorepo, multiple Python services can share compiled native code. The content-addressed cache ensures the same `.pyx` file compiled for the same Python version reuses one cached binary across all services:

```
monorepo/
├── service_a/
│   ├── pyproject.toml
│   └── main.py            # import shared_math
├── service_b/
│   ├── pyproject.toml
│   └── worker.py          # import shared_math
└── shared/
    └── shared_math.pyx    # compiled once, cached at ~/.molt/native/<hash>/
```

```toml
# service_a/pyproject.toml
[tool.molt.cython]
paths = [".", "../shared"]

# service_b/pyproject.toml
[tool.molt.cython]
paths = [".", "../shared"]
```

Both services see `~/.molt/native/<same-hash>/` — a single cache entry shared by all.

---

## Reference

### `[tool.molt.cython]` options

| Key | Type | Default | Description |
|---|---|---|---|
| `paths` | list of strings | `[".", "src"]` | Project-relative directories to scan for `.pyx` and `.rs` files |
| `directives` | table | `{}` | Cython compiler directives applied to all `.pyx` files |
| `extra_compile_args` | list of strings | `[]` | Additional flags passed to the C compiler |

### `[[tool.molt.native]]` options

| Key | Required | Description |
|---|---|---|
| `module` | yes | Python import name for the compiled extension |
| `src` | yes | Project-relative path to the source folder |
| `preset` | no | Name of a built-in or user-defined preset to use as defaults |
| `build` | yes (or from preset) | Shell command to build the extension |
| `output` | yes (or from preset) | Path to the built `.so` file (relative to `src`) |

### `molt native-preset` subcommands

```
molt native-preset list
molt native-preset show <name>
molt native-preset add --name <n> --build <cmd> --output <tmpl> [--src-patterns <globs>]
molt native-preset remove <name>
```

### Environment variables

| Variable | Effect |
|---|---|
| `CC` | Override the C compiler used by Cython and generic `c` preset builds |

### Cache locations

| Path | Contents |
|---|---|
| `~/.molt/native/<hash>/` | Compiled shared library for one source+ABI+platform combination |
| `~/.molt/native/rust-build/` | Auto-generated `Cargo.toml` files for single-file Rust modules |
| `~/.molt/native-presets.yaml` | Global preset registry |
| `~/.molt/projects/<hash>/cython/` | Per-project view: symlinks into the global cache |
| `~/.molt/projects/<hash>/native.json` | Manifest of all compiled modules for this project |
| `~/.molt/projects/<hash>/ext-hashes.json` | Change-detection state for `[[tool.molt.native]]` entries |

---

## Troubleshooting

**`cython: command not found` / `no module named cython`**

Add `cython` to your project dependencies:

```toml
dependencies = ["cython"]
```

Then `molt sync`.

**`cargo: command not found`**

Install Rust from [rustup.rs](https://rustup.rs). Cargo must be on `PATH`.

**`no C compiler on PATH`**

Install `clang` (macOS: `xcode-select --install`, Linux: `apt install clang`) or `gcc`. Set `CC=/path/to/compiler` to use a non-standard location.

**Module not found after editing**

If you edited a source file and `molt run python` didn't recompile, check that the file's mtime actually changed (some editors write files atomically with the same mtime). Force a recompile with `molt sync`.

**`ImportError: dynamic module does not define module export function (PyInit_<name>)`**

The compiled `.so` exists but doesn't export `PyInit_<module_name>`. For C/C++: ensure your `PyMODINIT_FUNC` name matches the `module =` value in `pyproject.toml`. For Rust: ensure your `#[pymodule]` function name matches.

**Stale cache after changing compiler flags**

Compiler directives and `extra_compile_args` are included in the cache hash for Cython. Changing them automatically busts the cache and triggers a recompile. For `[[tool.molt.native]]`, the `build` command string is included in the hash — changing it also triggers a rebuild.
