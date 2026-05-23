# molt — Future Plans

Design documents for features and extensions that are not yet implemented.
These are architecture-level specs, not proposals — they are written to the
level of detail where implementation could begin directly.

---

## Documents

| Document | Summary |
|---|---|
| [pkg-backend-interface.md](pkg-backend-interface.md) | `molt add` / `molt sync` as a language-agnostic interface — configurable package-manager backends stored in `~/.molt/pkg-backends.yaml`, same token-substitution pattern as run-handlers, built-in backends for Python/Rust/Go/Node/Zig/C, user-extensible to any package manager |
| [polyglot-packages.md](polyglot-packages.md) | Polyglot package management — global native store (`~/.molt/pkg-native/`), per-language resolver strategies (zig-build, pkg-config, Cargo, go mod), lock file format, `{cflags}` / `{ldflags}` token injection, cross-language projects, phased plan |
| [molt-js.md](molt-js.md) | molt for JavaScript — swappable runtime abstraction (bun / pnpm / npm / deno / yarn) with unified CLI, task runner, single-binary distribution, and native WASM hot paths |
| [wasm-integration.md](wasm-integration.md) | Zero-boilerplate WASM from C / Zig / Rust — auto-generated TypeScript types, bundler plugins, import alias strategies, eager/lazy loading, memory management |

---

## Design principles carried over from Python molt

1. **One tool, swappable internals.** The runtime (bun vs pnpm vs deno), the
   resolver (zig-build vs pkg-config vs Cargo), and the compiler (zig cc vs
   clang vs gcc) are implementation details. Everything the user types is
   `molt <cmd>`.

2. **No boilerplate for native code.** Point at a source file or a dependency
   name; get a working build. No manual `-I` / `-L` flags, no wasm-pack
   config, no N-API scaffolding.

3. **The path of least resistance is the fast path.** Adding a C library
   should be no harder than `molt add libpng`. Native hot paths should be
   easier to add than a Python dependency, not harder.

4. **Cross-platform from one machine.** `zig cc` as universal cross-compiler.
   Build Linux binaries from macOS. No Docker, no CI matrix for simple cases.

5. **Progressive.** Start with Python/JS pure; add native only where the
   profiler says to. molt supports the full gradient without forcing a rewrite.

6. **Delegate where delegation is correct.** Cargo, go mod, and Zig's package
   manager are excellent at what they do. molt wraps them for a unified CLI
   experience — it does not replace them with inferior reimplementations.

7. **Commands are interfaces; configs are implementations.** `molt add`,
   `molt sync`, `molt run` are not Python commands — they are contracts. The
   implementation is a shell command template in a YAML file. Built-in
   defaults cover the common cases; `~/.molt/pkg-backends.yaml` and
   `~/.molt/run-handlers.yaml` let users teach molt any package manager or
   runtime without touching Go source.
