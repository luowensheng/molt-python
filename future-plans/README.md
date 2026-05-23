# molt — Future Plans

Design documents for features and extensions that are not yet implemented.
These are architecture-level specs, not proposals — they are written to the
level of detail where implementation could begin directly.

---

## Documents

| Document | Summary |
|---|---|
| [molt-js.md](molt-js.md) | molt for JavaScript — swappable runtime abstraction (bun / pnpm / npm / deno / yarn) with unified CLI, task runner, single-binary distribution, and native WASM hot paths |
| [wasm-integration.md](wasm-integration.md) | Zero-boilerplate WASM from C / Zig / Rust — auto-generated TypeScript types, bundler plugins, import alias strategies, eager/lazy loading, memory management |

---

## Design principles carried over from Python molt

1. **One tool, swappable internals.** The runtime (bun vs pnpm vs deno) is an
   implementation detail. Everything the user types is `molt <cmd>`.

2. **No boilerplate for native code.** Point at a source file; get a typed,
   importable module. No manual binding generation, no wasm-pack config,
   no N-API scaffolding.

3. **The path of least resistance is the fast path.** Native hot paths should
   be easier to add than a Python dependency, not harder.

4. **Cross-platform from one machine.** `zig cc` as universal cross-compiler.
   Build Linux binaries from macOS. No Docker, no CI matrix for simple cases.

5. **Progressive.** Start with Python/JS pure; add native only where the
   profiler says to. molt supports the full gradient without forcing a rewrite.
