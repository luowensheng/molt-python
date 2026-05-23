# demo 13 — header-driven C extensions

This demo shows molt's manifest-free C extension pipeline: drop a `.h` file
next to your `.c` implementation, declare `[[tool.molt.c.modules]]` in
`pyproject.toml`, and molt auto-generates all CPython glue code, compiles it
with `zig cc`, and caches the result — no `.molt.toml`, no Cython, no cffi.

## How it works

```
fastmath.h      ──► ParseCHeader ──► []ManifestFn
fastmath.c  ┐                            │
            │   emitGlueC ◄──────────────┘
            │       │
            └───────┴──► zig cc ──► fastmath.cpython-3XX-<plat>.so
                                 └──► fastmath.pyi  (IDE stub)
```

1. `ParseCHeader` strips comments and preprocessor lines, then regex-matches
   function declarations.  Each C scalar type (`int`, `double`, `size_t`, …)
   maps to a molt primitive (`i32`, `f64`, `usize`, …).  Functions with
   unsupported types (pointers, structs) are silently skipped.

2. `emitGlueC` (shared with the kernel-module pipeline) writes a complete
   `glue.c` with `PyInit_<name>`, `PyMethodDef`, and per-function wrappers
   that call `PyArg_ParseTuple` / return a `PyObject*`.

3. `zig cc` compiles `fastmath.c` + `glue.c` into a shared library.  On
   macOS `-undefined dynamic_lookup` is added automatically; on Linux
   nothing extra is needed.

4. The result lands in `~/.molt/native/<hash>/fastmath.<ext_suffix>` (global
   content-addressed cache, shared across all projects).

## Quick start

```sh
molt sync          # installs Python, registers project
molt c build       # compile fastmath.c → fastmath.so
molt run demo      # runs main.py — imports fastmath
molt run bench     # shows C-extension call overhead vs pure Python
```

## Configuration

```toml
[[tool.molt.c.modules]]
name    = "fastmath"      # Python module name
src     = ["fastmath.c"]  # one or more .c files
# headers = ["fastmath.h"]  # optional; defaults to <name>.h if it exists
# flags   = ["-lm"]          # extra zig cc flags (-D, -I, -l, ...)
```

Headers default to `<name>.h` in the same directory as the first source
file.  If you have no header, specify function signatures in a `.molt.toml`
manifest instead (the original kernel-module path).

## Cross-compilation

```sh
molt c build --target linux_amd64    # cross-compile from macOS/arm64
```

The `--target` flag is forwarded to `zig cc` via the ZigConfig target-flags
mechanism.  No separate toolchain is needed — zig ships all libc headers for
every supported target.

## Comparison with other approaches

| Approach | Requires | Type mapping | Build tool |
|---|---|---|---|
| **molt c modules** | `.h` file | auto from header | `zig cc` (auto-installed) |
| molt kernel module | `.molt.toml` manifest | explicit | `zig cc` / any |
| Cython | `.pyx` file | Cython DSL | C compiler on PATH |
| cffi | inline C string | manual | C compiler on PATH |
| ctypes | none | manual at runtime | none |

The C-modules path is the fastest path from "I have a C function" to
"I can call it from Python" — no DSL, no hand-written marshalling.
