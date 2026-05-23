# molt demos

Twenty-one runnable projects, each highlighting a different part of molt.

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
  13-c-extension/    C as a Python extension module (header-driven, no boilerplate)
  14-polyglot/       molt run as universal launcher: Ruby, Node, Go, Julia, Elixir…
  15-c-project/      C as the primary language: molt run hello.c + multi-file project
  16-cpp-project/    C++17 as the primary language: molt run hello.cpp + multi-file
  17-zig-project/    Zig 0.16 as the primary language: molt run hello.zig + build-exe
  18-rust-project/   Rust via pkg-backend: molt add → cargo, molt sync → cargo fetch
  19-glue-go/        Transport Glue: Go functions called from Python (stdio)
  20-glue-lib/       Transport Glue: Go stdlib + third-party library imports
  21-glue-rust/      Transport Glue: Rust + flate2, unix_socket transport
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

---

## 14 — polyglot

**What it shows:** `molt run` as a universal script launcher. Any file whose extension is registered in `~/.molt/run-handlers.yaml` is dispatched directly — no tasks, no configuration, no activation. Ships with 27 built-in handlers and lets you add your own.

**What's in the demo:**

```
14-polyglot/
  hello.rb     Ruby greeting
  hello.js     Node.js greeting
  hello.lua    Lua greeting
  hello.pl     Perl greeting
  hello.sh     Bash greeting
  hello.go     Go greeting
  hello.swift  Swift greeting
  hello.jl     Julia greeting
  hello.exs    Elixir greeting
  showcase.py  Python runner that executes and times each script
  pyproject.toml
```

**Running:**

```bash
cd demos/14-polyglot

# Run any individual script directly — no config needed
molt run hello.rb               # Hello from Ruby 2.6.10! 👋 World
molt run hello.rb Alice         # Hello from Ruby 2.6.10! 👋 Alice
molt run hello.js Bob
molt run hello.go Charlie
molt run hello.jl               # Hello from Julia 1.11.4! 👋 World

# Run the polyglot showcase (all languages in a table with timing)
molt run all

# Or filter to one language
molt run julia
```

**Managing handlers:**

```bash
# See all built-in and user handlers
molt run-handler list

# Show command for a specific extension
molt run-handler show rb
# Extension : .rb
# Command   : ruby {file} {args}

# Register a custom handler globally
molt run-handler add deno "deno run {file} {args}"

# Cross-platform handler
molt run-handler add ts "npx ts-node {file} {args}" --windows "npx.cmd ts-node {file} {args}"

# Remove a custom handler (built-ins cannot be removed, only overridden)
molt run-handler remove deno

# Restore factory defaults
molt run-handler reset
```

**Token reference:**

| Token | Value |
|---|---|
| `{file}` | Absolute path to the file |
| `{dir}` | Directory containing the file |
| `{basename}` | Filename without extension |
| `{args}` | Extra arguments space-joined |
| `{python}` | Project's pinned Python interpreter |
| `{zig}` | Auto-installed zig binary |

Handlers are stored in `~/.molt/run-handlers.yaml`. User-added entries take precedence over built-ins of the same extension. 27 runtimes ship out of the box: Ruby, Node, TypeScript, Lua, Bash, Perl, R, PHP, Swift, Go, Java, Kotlin, Groovy, PowerShell, Nim, Crystal, Julia, Elixir, Haskell, Clojure, Dart, V, Odin, and more.

---

## 15 — c-project

**What it shows:** molt managing C as the *primary* project language — not as a Python extension (that's demo 13), but as a first-class C program compiled and run by molt.

Two capabilities in one demo:

1. **`molt run hello.c`** — single-file compile-and-run. The built-in `.c` handler compiles with `zig cc` (cross-platform, no toolchain install) and executes in one command.
2. **Multi-file C project via tasks** — `build`, `stats`, `demo`, `clean` tasks let you manage a real C project with the same `molt run <task>` workflow as any Python project.

**What's in the demo:**

```
15-c-project/
  hello.c        single-file C program (molt run hello.c)
  pyproject.toml tasks: build, stats, demo, clean
  src/
    main.c       statistics CLI: mean/std/min/max + ASCII histogram
    stats.c      statistics implementation
    stats.h      header
  demo.py        Python orchestrator — calls the C binary with 3 datasets
```

**Running:**

```bash
cd demos/15-c-project

# Single-file: compile and run in one command (no config)
molt run hello.c              # Hello from C! 👋 World
molt run hello.c Alice        # Hello from C! 👋 Alice

# Multi-file project: build then run
molt run build                # cc -O2 → bin/stats
molt run stats 88 92 71 95 84 # run the binary with args
molt run demo                 # Python drives C: 3 datasets + histograms
molt run clean                # rm compiled binaries
```

**How the `.c` handler works:**

```
molt run hello.c Alice
  → ext "c" → run-handler lookup
  → Unix: sh -c "{zig} cc -O2 -o {dir}/{basename} {file} && {dir}/{basename} {args}"
  → sh -c "/path/zig cc -O2 -o /proj/hello /proj/hello.c && /proj/hello Alice"
  → Hello from C! 👋 Alice
```

`zig cc` is a drop-in for `clang`/`gcc`. The handler works on macOS, Linux, and Windows without any additional toolchain setup.

**Customise the handler:**

```bash
molt run-handler add c "sh -c \"clang -O2 -o {dir}/{basename} {file} && {dir}/{basename} {args}\""
molt run-handler show c   # inspect the active command
molt run-handler reset    # restore zig cc default
```

---

## 16 — cpp-project

**What it shows:** molt managing **C++17 as the primary project language** using `zig c++` as a zero-install C++17 compiler. No Xcode, no system toolchain required.

Two capabilities in one demo:

1. **`molt run hello.cpp`** — single-file compile-and-run. The built-in `.cpp` handler compiles with `zig c++ -std=c++17` and executes in one command.
2. **Multi-file C++17 project via tasks** — `build`, `stats`, `demo`, `clean` tasks manage a real C++ project with templates and structured bindings.

**What's in the demo:**

```
16-cpp-project/
  hello.cpp      single-file C++17 program (molt run hello.cpp)
  pyproject.toml tasks: build, stats, demo, clean
  src/
    vec.hpp      generic Stats<T> template — C++17 structured bindings
    main.cpp     statistics CLI: mean/std/min/max + ASCII histogram
  demo.py        Python orchestrator — calls the C++ binary with 3 datasets
```

**Running:**

```bash
cd demos/16-cpp-project

# Single-file: compile and run in one command (no config)
molt run hello.cpp              # Hello from C++17! 👋 World
molt run hello.cpp Alice        # Hello from C++17! 👋 Alice

# Multi-file project: build then run
molt run build                  # zig c++ -O2 -std=c++17 → bin/stats-cpp
molt run stats 88 92 71 95 84   # run the binary with args
molt run demo                   # Python drives C++: 3 datasets + histograms
molt run clean                  # rm compiled binaries
```

**How the `.cpp` handler works:**

```
molt run hello.cpp Alice
  → ext "cpp" → run-handler lookup
  → Unix: sh -c "{zig} c++ -O2 -std=c++17 -o {dir}/{basename} {file} && {dir}/{basename} {args}"
  → Hello from C++17! 👋 Alice
```

---

## 17 — zig-project

**What it shows:** molt managing **Zig 0.16 as the primary project language**. molt auto-installs the Zig toolchain and resolves `{zig}` in all task commands.

Two capabilities in one demo:

1. **`molt run hello.zig`** — single-file run via `zig run` (no compile step).
2. **Multi-file Zig project via tasks** — `zig build-exe` compiles a stats CLI with separate modules.

**What's in the demo:**

```
17-zig-project/
  hello.zig      single-file Zig 0.16 program (molt run hello.zig)
  pyproject.toml tasks: build, stats, demo, clean
  src/
    stats.zig    statistics module: StatsResult + compute() with error union
    main.zig     statistics CLI: parse args, call stats, print histogram
  demo.py        Python orchestrator — calls the Zig binary with 3 datasets
```

**Running:**

```bash
cd demos/17-zig-project

# Single-file: zig run (no compile step)
molt run hello.zig              # Hello from Zig 0.16.0! 👋 World
molt run hello.zig Alice        # Hello from Zig 0.16.0! 👋 Alice

# Multi-file project: build then run
molt run build                  # zig build-exe → bin/stats-zig
molt run stats 88 92 71 95 84   # run the binary with args
molt run demo                   # Python drives Zig: 3 datasets + histograms
molt run clean                  # rm compiled binaries
```

**Zig 0.16 patterns used:**

```zig
pub fn main(init: std.process.Init) !void {
    const arena = init.arena.allocator();
    const args  = try init.minimal.args.toSlice(arena);

    var data: std.ArrayList(f64) = .empty;  // .empty sentinel — no init()
    try data.append(arena, v);               // allocator passed per-call

    try std.Io.File.stdout().writeStreamingAll(init.io, bytes);
}
```

## 18 — rust-project

**What it shows:** The **pkg-backend system** — `molt add`, `molt sync`, and `molt remove` as language-agnostic interfaces routing to the ecosystem's native tool. For a Rust project (`lang = "rust"` in `moltproject.toml`) every package operation delegates to `cargo`.

**Polyglot package routing:**

| molt command | routes to |
|---|---|
| `molt add serde` | `cargo add serde` |
| `molt add tokio --version "^1"` | `cargo add tokio --version ^1` |
| `molt sync` | `cargo fetch` |
| `molt remove serde` | `cargo remove serde` |

**What's in the demo:**

```
18-rust-project/
  moltproject.toml   lang = "rust" + tasks: build, stats, test, demo, clean
  Cargo.toml         standard Rust manifest, [dependencies] section
  src/
    main.rs          pure-std statistics CLI: mean, std-dev, min, max, histogram
```

**Running:**

```bash
cd demos/18-rust-project

molt run build                  # cargo build --release
molt run stats 88 92 71 95 84   # Rust binary: statistics + 8-bucket histogram
molt run test                   # cargo test (unit tests in #[cfg(test)])
molt run demo                   # preset dataset
molt run clean                  # cargo clean
```

**pkg-backend commands:**

```bash
# Inspect the rust backend
molt pkg-backend show rust
# add:     cargo add {package} {version_flag} {flags}
# remove:  cargo remove {package}
# sync:    cargo fetch
# upgrade: cargo update {package}

# List all 9 built-in backends
molt pkg-backend list

# Add a custom backend (Nim example)
molt pkg-backend add nim \
  --add "nimble install {package}{version_flag}" \
  --remove "nimble uninstall {package}" \
  --sync "nimble install"

# Per-project override in moltproject.toml (partial — unset fields fall back)
# [tool.molt.backend]
# add  = "bun add {package}{version_flag}"
# sync = "bun install"
```

---

## 19 — glue-go

**Transport Glue: Go functions called from Python over stdio.**

Calls a pure Go statistics package (`stats/stats.go`) from Python with no `.so`,
no cgo, and no ABI compatibility issues. The Go package is `package stats` (not
`package main`) — molt generates a server binary that imports it via a Go module
`replace` directive, then exposes its functions as a Python module.

### Layout

```
19-glue-go/
  gocode/
    go.mod      ← module user/stats
    stats.go    ← package stats: Mean, Stddev, Histogram, Compress
  moltproject.toml
  main.py
```

### moltproject.toml

```toml
[[tool.molt.glue]]
module    = "stats"
lang      = "go"
src       = "./stats"   # Go package directory — NOT package main
transport = "stdio"

  [[tool.molt.glue.fn]]
  name    = "mean"
  args    = [{ name = "data", type = "[]f64" }]
  returns = "f64"
```

### What molt generates

```
.molt/_build_stats/go.mod   — module molt-glue-stats; replace user/stats => ./stats
.molt/_build_stats/server.go — package main; import user "user/stats"; JSON dispatch
.molt/glue/stats_server      — compiled binary
.molt/glue/stats.py          — Python client (lazy subprocess start)
.molt/glue/stats.pyi         — type stubs for IDE completion
```

### Run

```sh
molt sync       # compile Go server + generate Python client
molt run demo   # python main.py — calls Go from Python over stdio
molt glue list  # show all glue modules
```

---

## 20 — glue-lib

**Transport Glue: import Go stdlib and third-party packages — zero local Go code.**

`src` is a Go import path, not a local file. molt detects this and generates a server
that imports the package directly. Third-party paths trigger `go get` automatically.

### moltproject.toml

```toml
[[tool.molt.glue]]
module    = "gosha256"
lang      = "go"
src       = "crypto/sha256"               # stdlib — no go get needed

[[tool.molt.glue]]
module    = "sha3"
lang      = "go"
src       = "golang.org/x/crypto/sha3"   # third-party; molt runs go get
```

### Run

```sh
molt sync       # go get golang.org/x/crypto, compile both servers
molt run demo   # python main.py — hash with stdlib and third-party Go crypto
```

---

## 21 — glue-rust

**Transport Glue: Rust (flate2 compression) over unix_socket.**

`compress_glue.rs` is a thin adapter file — not a full crate. molt uses Rust's
`include!` macro to pull it into a generated `main.rs`. The `flate2` crate is declared
in `crates = [...]` and added to the generated `Cargo.toml`.

Transport is `unix_socket`: the Rust server runs as a persistent daemon so multiple
Python threads can share it concurrently.

### Layout

```
21-glue-rust/
  compress_glue.rs   ← pub fn deflate / inflate / ratio (NOT a crate)
  moltproject.toml
  main.py
```

### moltproject.toml

```toml
[[tool.molt.glue]]
module    = "compress"
lang      = "rust"
src       = "compress_glue.rs"
crates    = ["flate2 = '1.0'"]
transport = "unix_socket"     # persistent daemon; concurrent callers
```

### Run

```sh
molt sync          # cargo build --release, generate compress.py
molt run demo      # python main.py — concurrent compression from 10 threads

# Optional: explicit daemon management
molt glue start compress   # start server before Python imports it
molt glue stop  compress   # stop it
```
