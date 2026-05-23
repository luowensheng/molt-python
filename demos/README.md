# molt demos

Thirteen runnable projects, each highlighting a different part of molt.

```
demos/
  01-hello-app/      molt init, tasks, script runner
  02-web-scraper/    molt add, global store cache
  03-fastapi-api/    dev server, tests, lint — all via molt run
  04-data-analyzer/  pandas + rich, shared packages across projects
  05-cli-tool/       Click CLI + molt tool install global shim
  06-binary-dist/    molt build → self-installing hermetic binary
  07-asm-kernel/     assembly kernel module via molt's native build system
  08-mojo-hello/     Mojo basics: structs, traits, generics, comptime
  09-mojo-numpy/     Mojo calling numpy/pandas via PythonObject
  10-mojo-simd/      SIMD vectorization + benchmark vs Python/numpy
  11-mojo-matmul/    Matrix multiply: naive → SIMD → tiled + parallel
  12-mojo-extension/ Mojo compiled to Python .so via PythonModuleBuilder
  13-c-extension/    C extension module from a .h header — no .molt.toml needed
```

---

## 01 — hello-app

**What it shows:** `molt init`, basic tasks, zero-setup run.

```bash
cd 01-hello-app
molt run greet        # → Hello, Claude! + Python/platform info
molt run info
molt task list
```

**Key point:** No `.venv`, no `pip install`, no `source activate`. `molt run` builds
`PYTHONPATH` from the global store and execs the interpreter directly.

---

## 02 — web-scraper

**What it shows:** `molt add` pulls packages into `~/.molt/pkg/` (shared). A second
project that needs `requests` or `rich` hits the cache instantly — no re-download, no
re-install.

```bash
cd 02-web-scraper
molt run posts         # fetch + display 5 posts via requests, formatted with rich
molt run users         # fetch all users in a rich table
molt run top10         # top 10 posts
```

**Key point:** `molt add requests rich` showed 7 packages "✓ cached" immediately
because 01-hello-app had already populated the store.

---

## 03 — fastapi-api

**What it shows:** Full dev workflow — dev server, test suite, linting — all through
`molt run`. No venv activation needed between tasks.

```bash
cd 03-fastapi-api
molt run test          # pytest: 4 tests pass
molt run lint          # ruff check
molt run dev           # uvicorn --reload on :8000
```

REST endpoints:

```
GET  /tasks          list tasks
POST /tasks          create task  {"title": "...", "done": false}
GET  /tasks/{id}     get task
PATCH /tasks/{id}    update task
DELETE /tasks/{id}   delete task
```

**Key point:** `test`, `lint`, `dev` are just strings in `[tool.molt.tasks]`. The
store handles the environment; no activation script ever runs.

---

## 04 — data-analyzer

**What it shows:** pandas + rich pulled from the global store. Demonstrates that
heavy scientific packages (pandas, numpy) are installed once and shared.

```bash
cd 04-data-analyzer
molt run generate      # write data/sales.csv (240 rows)
molt run summary       # totals panel
molt run top           # top 5 products by revenue (rich table)
molt run region        # revenue breakdown by region
molt run trend         # monthly trend with ASCII bar chart
```

**Key point:** `pandas 3.0.2` and `numpy 2.4.4` were already in `~/.molt/pkg/` from
previous installs — `molt add pandas rich` showed "✓ cached" instantly, no wheel
download.

---

## 05 — cli-tool

**What it shows:** A Click CLI registered as a global tool via `molt tool install`.
After install, `filetool` is available anywhere in your shell via `~/.molt/bin/`.

```bash
cd 05-cli-tool
molt run ls            # list files in current dir
molt run stats         # recursive file stats with extension breakdown
molt run python -m filetool checksum pyproject.toml

# Register globally:
molt tool install
# Now from anywhere:
filetool ls ~/Downloads
filetool stats ~/Documents --ext .pdf
filetool checksum some-file.zip --algo sha512
```

**Key point:** `molt tool install` writes a shim to `~/.molt/bin/filetool` that
resolves the correct interpreter and store path without a venv.

---

## 06 — binary-dist

**What it shows:** `molt build` packages source + deps + optional interpreter into a
single self-installing binary. `molt.yaml` defines multiple named commands.

```bash
cd 06-binary-dist
molt run info          # system info (dev mode)
molt run bench         # Python loop benchmark

# Build the binary:
molt build
# → ./06-binary-dist  (3.7 MB, SHA-256 verified)

# Inspect and verify:
molt inspect 06-binary-dist
molt verify-binary 06-binary-dist

# On a target machine (no Python needed):
./06-binary-dist --install
./06-binary-dist run          # default command: info
./06-binary-dist run bench
./06-binary-dist run version
```

**Key point:** The binary embeds source, dependencies, and an integrity trailer.
`molt verify-binary` checks the SHA-256 root hash before any execution.

---

## 07 — asm-kernel

**What it shows:** Hand-written GAS assembly callable from Python via molt's kernel
module system — zero C or Cython boilerplate. A `mymath.molt.toml` manifest
declares exported functions; `molt sync` compiles `mymath.S` to `mymath.so` via
`zig cc` and generates a ctypes shim.

```bash
cd 07-asm-kernel
molt sync              # compiles mymath.S → mymath.so, generates ctypes shim
molt run               # python main.py: add(3,4)=7  mul(6,7)=42
```

**Key point:** The same `.S` source works on both ARM64 (Apple Silicon) and x86-64
Linux via `#ifdef __aarch64__` / `#ifdef __x86_64__` — one file, two architectures.

---

## 08 — mojo-hello

**What it shows:** Mojo 1.0 language basics run through molt. `molt add mojo` installs
the compiler as an ordinary Python dependency; `molt run main.mojo` dispatches to
`mojo run` automatically by file extension.

```bash
cd 08-mojo-hello
molt sync              # installs the mojo compiler
molt run main.mojo     # auto-dispatched by .mojo extension
```

**Key point:** No `MOJO_PYTHON_LIBRARY` setup, no PATH editing. `molt sync` derives
the libpython path and writes a self-contained `mojo` shim to the project's `bin/`.

---

## 09 — mojo-numpy

**What it shows:** Calling numpy and pandas from Mojo via `Python.import_module`.
Because molt sets `PYTHONPATH` to all managed packages, every `molt add`'d package
is automatically visible to Mojo's embedded CPython — zero extra configuration.

```bash
cd 09-mojo-numpy
molt sync              # installs mojo, numpy, pandas
molt run main.mojo
```

**Key point:** `PYTHONPATH` set by molt at sync time covers both Python *and* Mojo.
No `sys.path` manipulation needed in your Mojo code.

---

## 10 — mojo-simd

**What it shows:** Mojo SIMD types, hardware-width auto-detection, vectorized cosine,
and a sigmoid benchmark against pure Python and numpy.

```bash
cd 10-mojo-simd
molt sync
molt run main.mojo     # Mojo SIMD demos + benchmark
molt run bench-py      # Python/numpy baselines for comparison
```

**Key point:** `ptr.load[width=W](i)` reads W floats per instruction. The manual SIMD
while-loop with a scalar tail achieves ~3.4× speedup vs scalar on Apple Silicon NEON.

---

## 11 — mojo-matmul

**What it shows:** Matrix multiply in three levels — naive O(n³), SIMD-vectorized
inner loop, and cache-friendly tiled + parallelized — benchmarked against numpy.

```bash
cd 11-mojo-matmul
molt sync
molt run main.mojo     # all three implementations + Mojo benchmark
molt run bench-np      # numpy baseline
```

**Key point:** The tiled + `parallelize` implementation reaches numpy-level performance
without calling BLAS, using only Mojo's standard library.

---

## 12 — mojo-extension

**What it shows:** Compile a `.mojo` file to a Python-importable `.so` extension via
`molt mojo build --emit shared-lib`. Once built, `import fast_math` works from any
Python file — the same as a Cython or Rust/PyO3 extension.

```bash
cd 12-mojo-extension
molt sync
molt run build         # molt mojo build fast_math.mojo --emit shared-lib -o fast_math.so
molt run               # python use_extension.py — imports the .so
molt run hook          # alternative: mojo.importer auto-compile (no build step)
molt run bench         # sigmoid: Python vs numpy vs Mojo extension
```

**Key point:** `PythonModuleBuilder` + `@export def PyInit_<name>()` is all it takes
to make Mojo functions and structs callable from Python. The `.so` is a standard
CPython extension module.

---

## Global store: before vs after

After running the demos you can see what's been shared:

```bash
molt gc --dry-run       # show what GC could remove
molt info               # show current project's store entries
ls ~/.molt/pkg/ | head  # packages shared across all projects
```

Packages like `rich`, `click`, `requests` appear once in `~/.molt/pkg/` regardless
of how many projects use them.

---

## Running all demos in sequence

```bash
# From the demos/ directory:
for d in [0-9][0-9]-*/; do
  echo "=== $d ===" && cd "$d" && molt sync --frozen && cd ..
done
```

---

## 13 — c-extension

**What it shows:** First-class C extension modules with zero boilerplate — no `.molt.toml` manifest, no Cython, no cffi. Declare `[[tool.molt.c.modules]]` in `pyproject.toml`, point at a `.h` header, and `molt sync` (or `molt c build`) auto-parses the function signatures and compiles a proper CPython extension module via `zig cc`.

**What's in the demo:**

```
13-c-extension/
  fastmath.h        function declarations (parsed automatically)
  fastmath.c        implementations: add, mul, clamp, lerp, gcd, ipow, mean3, variance3
  pyproject.toml    [[tool.molt.c.modules]] entry — just name + src
  main.py           import fastmath and call all functions
  bench.py          microbenchmark: C extension call overhead vs pure Python
```

**Running:**

```bash
cd demos/13-c-extension
molt sync             # parses fastmath.h → compiles fastmath.so via zig cc
molt run demo         # import fastmath; call all 8 functions
molt run bench        # benchmark C extension vs Python
```

**How it works:**

1. `molt sync` reads `[[tool.molt.c.modules]]` from `pyproject.toml`
2. Parses `fastmath.h` — extracts all scalar function signatures automatically
3. Synthesises a manifest from the parsed signatures
4. Generates `glue.c` (CPython `PyInit_fastmath`, `PyMethodDef` table, argument parsing)
5. Compiles `fastmath.c + glue.c → fastmath.cpython-3XX-platform.so` via `zig cc`
6. Caches the result in `~/.molt/native/<hash>/`; subsequent syncs are instant

**Cross-compilation:**

```bash
molt c build --target linux_amd64   # compile for Linux x86-64 from macOS
molt c build --target linux_arm64   # compile for Linux ARM64
```

The `zig cc` toolchain handles the cross-compile without any separate toolchain install.

**pyproject.toml:**

```toml
[[tool.molt.c.modules]]
name = "fastmath"
src  = ["fastmath.c"]
# headers defaults to fastmath.h — auto-discovered from src directory
```

That's the entire configuration. Compare to Cython (`.pyx` + `setup.py` + compiler) or cffi (manual `ffi.cdef()` with copy-pasted signatures).
