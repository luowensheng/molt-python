# Changelog

All notable changes to molt are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions follow [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

---

## [0.2.0] — 2026-05-23

### Transport Glue — call Go/Rust functions from Python over IPC

The biggest new feature: declare `[[tool.molt.glue]]` blocks in your project config and
`molt sync` auto-generates a typed Python module that calls your Go or Rust code through
a zero-configuration IPC server. No `.so`, no cgo, no ABI constraints — any type that
survives JSON round-trip works.

- **Go support** — local packages (`src = "./mylib"`), Go stdlib (`src = "crypto/sha256"`),
  and third-party modules (`src = "golang.org/x/crypto/sha3"`). Third-party: `go get` runs
  automatically. Stdlib: clean go.mod, no require entries needed.
- **Rust support** — thin adapter `.rs` file (`src = "compress_glue.rs"`) included directly
  into the generated server via `include!`. Extra crates added to the generated `Cargo.toml`
  via `crates = [...]`.
- **Three transports**: `stdio` (subprocess per Python process, default), `unix_socket`
  (persistent daemon, concurrent threads), `tcp` (network-addressable daemon).
- **Type system**: `i32/i64/f32/f64/bool/string/bytes/[]i32/[]f64/[]string/json` — full
  bidirectional mapping between Go, Rust, Python, and JSON wire format. `bytes` args/returns
  are base64-encoded on the wire, transparent in Python.
- **Python shim fix**: re-writes `python`/`python3` shims after glue build so the glue
  directory is included in the hardcoded `PYTHONPATH` inside the shim.
- **Typed stubs**: `.pyi` files generated alongside `.py` clients for full IDE completion.
- **Glue driver system**: `~/.molt/glue-drivers.yaml` for custom language drivers; built-in
  drivers for Go and Rust; extensible to Nim, Zig, Crystal, etc.
- **CLI**: `molt glue list/show/regen/start/stop` and `molt glue-driver list/show/add`.
- **3 new demos**: 19 (Go stdio), 20 (Go library imports), 21 (Rust unix_socket).
- **Site**: 21 demos, Transport Glue feature card, demos 19–21 on GitHub Pages.

---

## [0.1.0] — 2026-05-23

First public release of molt — a hermetic Python toolchain built on top of [uv](https://github.com/astral-sh/uv).

### Core features

- **Hermetic project isolation** — every project gets its own Python interpreter, virtual environment, and binary shims; no cross-project pollution
- **Global package store** — packages are downloaded once and hard-linked into projects; `molt add numpy` in 10 projects costs one download
- **`molt init`** — scaffold a new project from built-in or custom templates
- **`molt add / remove`** — manage dependencies backed by `uv` for fast resolution
- **`molt sync`** — install the full lockfile in one command
- **`molt run`** — run Python scripts, `.mojo` files, or named tasks from `pyproject.toml`
- **Task runner** — `[tool.molt.tasks]` in `pyproject.toml`; supports shell commands, env vars, depends-on chains
- **Python version management** — `molt python list/install/use/remove`; multiple versions co-exist
- **Named environments** — `molt envs create/add/list` for shared dev environments
- **Global tools** — `molt tool install` puts CLI tools on PATH without polluting any project

### Native code (five paths)

- **Cython** — `[[tool.molt.native]]` with `type = "cython"`; auto-builds `.so` on `molt sync`
- **Rust + PyO3** — `type = "rust"`; calls `cargo build` and installs the wheel
- **Kernel modules** — Zig, C, C++ inline or from files; `molt.toml` manifest; ctypes shim generated automatically
- **Pre-built `.so`** — drop-in placement with `type = "prebuilt"`
- **External recipes** — `[[tool.molt.native]]` with `preset` key; reusable build recipes stored in the global config

### Single-binary distribution

- **`molt build`** — embed the Python project + dependencies into a single self-contained binary (no Python required on the target machine)
- **`molt capture / assemble`** — reproducible builds from environment manifests
- **`molt verify-binary`** — SHA-256 integrity check of embedded payloads

### Mojo support

- `molt add mojo` — installs Mojo via pip; no separate toolchain download
- `molt run main.mojo` — auto-dispatches `.mojo` files to the Mojo interpreter
- `molt mojo <args>` — direct passthrough to `mojo build`, `mojo doc`, etc.
- Automatic `MOJO_PYTHON_LIBRARY` + `PYTHONPATH` wiring so Mojo finds all project packages
- 5 runnable demos (08–12): hello, NumPy interop, SIMD, matmul, Python extension

### MCP server (AI integration)

- `molt mcp` — starts a stdio MCP server exposing 9 molt operations as AI tools
- Works with Claude Code, Cursor, and any MCP-compatible client
- See `docs/mcp-server.md` and `.claude/mcp.json`

### Multi-project management

- `molt project list/info/where/purge` — track and manage all molt projects on the machine
- `molt where` — print project root from any subdirectory

### Developer experience

- `molt doctor` — diagnose common configuration problems
- `molt info` — human-readable project summary
- `molt env get/set/unset` — per-project and global environment variables
- `molt editor` — configure IDE integration
- `molt gc` — garbage-collect orphaned store entries
- `molt cache info/libs` — show disk usage

### Platforms

- macOS (arm64, amd64)
- Linux (amd64, arm64)
- Windows (amd64) — experimental

---

[Unreleased]: https://github.com/luowensheng/molt-python/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/luowensheng/molt-python/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/luowensheng/molt-python/releases/tag/v0.1.0
