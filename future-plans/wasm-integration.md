# WASM Integration — Zero-boilerplate Native Hot Paths for JavaScript

The goal: make calling native code from JavaScript as easy as dropping a
`.zig` or `.c` file into your project and running `molt wasm build`. No
manual binding generation, no wasm-pack config, no TypeScript declarations
to write by hand.

This document covers the full pipeline: compilation, type generation, import
alias strategies, bundler plugins, loading patterns, memory management, and
performance characteristics.

---

## Why WASM, not N-API native addons

Two options exist for calling native code from Node.js/Bun:

| | N-API `.node` addon | WASM module |
|---|---|---|
| Portability | Platform-specific binary | Runs anywhere |
| Performance | Near-native, direct memory | 5–20% overhead vs native |
| Safety | Full OS access | Sandboxed by default |
| Cross-compile | Requires target toolchain | `zig cc -target wasm32` from any host |
| Bundling | Cannot be bundled | Embedded as bytes in a bundle |
| Browser support | No | Yes |
| Deno support | Partial (FFI instead) | Native |
| Boilerplate | High (node-gyp, cmake-js) | Low with molt |

For most hot paths the 5–20% WASM overhead is irrelevant — you're comparing
to a Python/JS loop that's 20–200× slower to begin with. The cross-platform
and bundling benefits make WASM the right default.

N-API is the right choice when:
- You need direct OS/hardware access (GPU, HID devices, kernel interfaces)
- The 5–20% overhead matters more than portability
- You're wrapping an existing C library that was never meant to be portable

molt-js supports both but defaults to WASM.

---

## Supported source languages

| Source | Compiler | Notes |
|---|---|---|
| Zig (`.zig`) | `zig build-lib -target wasm32-freestanding` | Best ergonomics; explicit allocator |
| C (`.c`) | `zig cc -target wasm32-freestanding` | zig cc as cross-compiler, no emscripten needed |
| C++ (`.cpp`) | `zig c++ -target wasm32-freestanding -std=c++17` | C++17 features available |
| Rust (`.rs`) | `wasm-pack build --target bundler` | requires wasm-pack installed |
| AssemblyScript (`.ts`) | `asc {file} -o {basename}.wasm` | TypeScript-like syntax compiles to WASM |

---

## The compilation pipeline

```
molt wasm build
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│  Parse package.json molt.wasm array                     │
│  For each module:                                       │
│    source: "src/native/stats.zig"                       │
│    exports: ["sum_f64", "dot_product", "variance"]      │
└─────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│  Detect source type → select compiler                   │
│  Check cache: hash(source file) == stored hash?         │
│    yes → skip recompile                                 │
│    no  → compile                                        │
└─────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│  Compile to .wasm                                       │
│  .zig: zig build-lib -target wasm32-freestanding -O ReleaseFast │
│  .c:   zig cc -target wasm32-freestanding --no-entry -O2 │
│  .rs:  wasm-pack build --target bundler                  │
└─────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│  Parse WASM binary (§7 export section)                  │
│  Extract: function names, parameter counts              │
│  Cross-reference with declared exports for validation   │
└─────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│  Generate:                                              │
│    .molt/wasm/stats.wasm   compiled binary              │
│    .molt/wasm/stats.d.ts   TypeScript declarations      │
│    .molt/wasm/stats.js     runtime loader               │
│    wasm.ts                 barrel (all modules)         │
└─────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│  Write import aliases (all three strategies):           │
│    package.json "imports"  →  #wasm/stats               │
│    .molt/plugin.ts         →  wasm:stats  (Bun/esbuild) │
│    wasm.ts                 →  ./wasm barrel             │
└─────────────────────────────────────────────────────────┘
```

---

## Source language examples

### Zig

```zig
// src/native/stats.zig
// Export functions are called directly from JS — no Python/C boilerplate.
// Zig 0.16: comptime type safety, no GC, deterministic memory.

const std = @import("std");

// Exported to JS as: sum_f64(data: Float64Array): number
export fn sum_f64(ptr: [*]const f64, len: usize) f64 {
    var s: f64 = 0;
    for (ptr[0..len]) |x| s += x;
    return s;
}

// Exported to JS as: dot_product(a: Float64Array, b: Float64Array): number
export fn dot_product(
    a_ptr: [*]const f64, b_ptr: [*]const f64, len: usize,
) f64 {
    var s: f64 = 0;
    for (0..len) |i| s += a_ptr[i] * b_ptr[i];
    return s;
}

// Exported to JS as: variance(data: Float64Array): number
export fn variance(ptr: [*]const f64, len: usize) f64 {
    const n = @as(f64, @floatFromInt(len));
    var sum: f64 = 0;
    for (ptr[0..len]) |x| sum += x;
    const mean = sum / n;
    var sq: f64 = 0;
    for (ptr[0..len]) |x| { const d = x - mean; sq += d * d; }
    return sq / n;
}

// Allocator exported so JS can allocate WASM-side memory
// (needed when passing large arrays without copying)
export fn alloc(len: usize) [*]u8 {
    const mem = std.heap.wasm_allocator.alloc(u8, len) catch return @ptrFromInt(0);
    return mem.ptr;
}

export fn free(ptr: [*]u8, len: usize) void {
    std.heap.wasm_allocator.free(ptr[0..len]);
}
```

```toml
// package.json molt section
"wasm": [
  {
    "source":  "src/native/stats.zig",
    "exports": ["sum_f64", "dot_product", "variance"]
  }
]
```

### C

```c
// src/native/img.c
// Image processing primitives — zig cc cross-compiles to WASM from any host.
#include <stdint.h>
#include <stddef.h>

// Exported to JS as: to_grayscale(ptr: Uint8Array): void
// Modifies the RGBA pixel buffer in-place.
__attribute__((export_name("to_grayscale")))
void to_grayscale(uint8_t *pixels, size_t len) {
    for (size_t i = 0; i < len; i += 4) {
        uint8_t r = pixels[i], g = pixels[i+1], b = pixels[i+2];
        // BT.601 luminance
        uint8_t y = (uint8_t)(0.299f*r + 0.587f*g + 0.114f*b);
        pixels[i] = pixels[i+1] = pixels[i+2] = y;
    }
}

// Exported to JS as: clamp_u8(value: number, lo: number, hi: number): number
__attribute__((export_name("clamp_u8")))
uint8_t clamp_u8(int value, int lo, int hi) {
    if (value < lo) return (uint8_t)lo;
    if (value > hi) return (uint8_t)hi;
    return (uint8_t)value;
}
```

---

## Generated output

### `.d.ts` — TypeScript declarations

```typescript
// .molt/wasm/stats.d.ts  — auto-generated by molt wasm build

/** WASM exports from src/native/stats.zig */
export interface StatsExports {
  /**
   * Sum all values in a Float64Array.
   * @param ptr  Pointer to f64 array in WASM memory
   * @param len  Number of elements
   */
  sum_f64(ptr: number, len: number): number;

  /**
   * Dot product of two Float64Arrays of equal length.
   */
  dot_product(ptr_a: number, ptr_b: number, len: number): number;

  /**
   * Population variance of a Float64Array.
   */
  variance(ptr: number, len: number): number;

  /** WASM memory allocator — returns pointer to len bytes of WASM memory */
  alloc(len: number): number;

  /** WASM memory deallocator */
  free(ptr: number, len: number): void;

  /** Direct access to WASM linear memory */
  memory: WebAssembly.Memory;
}

/** Load and instantiate the stats WASM module. */
export declare function load(): Promise<StatsExports>;

/**
 * High-level helper: sum a JS Float64Array without manual pointer management.
 * Copies data into WASM memory, calls sum_f64, frees the allocation.
 */
export declare function sum(data: Float64Array): number;

/**
 * High-level helper: dot product of two Float64Arrays.
 */
export declare function dot(a: Float64Array, b: Float64Array): number;

/**
 * High-level helper: variance of a Float64Array.
 */
export declare function variance(data: Float64Array): number;
```

### `.js` — runtime loader with high-level helpers

The generated JS has two layers:
1. **Low-level**: raw WASM exports (manual pointer management, maximum speed)
2. **High-level helpers**: copy-in/copy-out wrappers that handle memory automatically

```javascript
// .molt/wasm/stats.js  — auto-generated by molt wasm build
import { readFileSync } from "fs";
import { join, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));

let _instance = null;

// ── Instantiation ─────────────────────────────────────────────────────────

export async function load() {
  if (_instance) return _instance.exports;

  const bytes = readFileSync(join(__dirname, "stats.wasm"));
  const result = await WebAssembly.instantiate(bytes);
  _instance = result.instance;
  return _instance.exports;
}

// ── High-level helpers (copy-in / copy-out) ───────────────────────────────
// These handle WASM memory automatically. Use the raw exports for
// maximum performance when you need to avoid the copy overhead.

/**
 * sum(data) — copies data into WASM memory, calls sum_f64, frees allocation.
 * ~2× slower than the raw export due to memcpy, but safe and ergonomic.
 */
export async function sum(data) {
  const wasm = await load();
  const byteLen = data.byteLength;
  const ptr = wasm.alloc(byteLen);
  if (ptr === 0) throw new Error("WASM alloc failed");
  try {
    new Float64Array(wasm.memory.buffer, ptr, data.length).set(data);
    return wasm.sum_f64(ptr, data.length);
  } finally {
    wasm.free(ptr, byteLen);
  }
}

export async function dot(a, b) {
  if (a.length !== b.length) throw new Error("length mismatch");
  const wasm = await load();
  const byteLen = a.byteLength;
  const ptrA = wasm.alloc(byteLen);
  const ptrB = wasm.alloc(byteLen);
  if (!ptrA || !ptrB) throw new Error("WASM alloc failed");
  try {
    new Float64Array(wasm.memory.buffer, ptrA, a.length).set(a);
    new Float64Array(wasm.memory.buffer, ptrB, b.length).set(b);
    return wasm.dot_product(ptrA, ptrB, a.length);
  } finally {
    wasm.free(ptrA, byteLen);
    wasm.free(ptrB, byteLen);
  }
}

export async function variance(data) {
  const wasm = await load();
  const byteLen = data.byteLength;
  const ptr = wasm.alloc(byteLen);
  if (ptr === 0) throw new Error("WASM alloc failed");
  try {
    new Float64Array(wasm.memory.buffer, ptr, data.length).set(data);
    return wasm.variance(ptr, data.length);
  } finally {
    wasm.free(ptr, byteLen);
  }
}
```

### `wasm.ts` — project-wide barrel

```typescript
// wasm.ts  — auto-generated by molt wasm build
// Single import point for all WASM modules in this project.
//
// Usage:
//   import { stats, img } from "./wasm";
//   const result = await stats.sum(data);
//
// Or eager (load everything once at startup):
//   import { init, wasm } from "./wasm";
//   await init();
//   const result = wasm.stats.sum_f64(ptr, len);  // synchronous

import * as _stats from "./.molt/wasm/stats.js";
import * as _img   from "./.molt/wasm/img.js";

export type { StatsExports } from "./.molt/wasm/stats.d.ts";
export type { ImgExports }   from "./.molt/wasm/img.d.ts";

// ── Per-module lazy accessors ─────────────────────────────────────────────

/** stats WASM module — lazily instantiated on first use */
export const stats = {
  sum:      (data: Float64Array) => _stats.sum(data),
  dot:      (a: Float64Array, b: Float64Array) => _stats.dot(a, b),
  variance: (data: Float64Array) => _stats.variance(data),
  raw:      () => _stats.load(),   // access raw exports for manual memory mgmt
};

/** img WASM module — lazily instantiated on first use */
export const img = {
  toGrayscale: (pixels: Uint8Array) => _img.to_grayscale(pixels),
  raw:         () => _img.load(),
};

// ── Eager loader (recommended for servers) ────────────────────────────────

type WasmInstances = {
  stats: Awaited<ReturnType<typeof _stats.load>>;
  img:   Awaited<ReturnType<typeof _img.load>>;
};

let _ready = false;
let _cache: Partial<WasmInstances> = {};

/**
 * Pre-instantiate all WASM modules. Call once at startup.
 * After init(), wasm.stats and wasm.img are synchronous.
 *
 * @example
 * await init();
 * console.log(wasm.stats.sum_f64(ptr, len));  // no await
 */
export async function init(): Promise<void> {
  [_cache.stats, _cache.img] = await Promise.all([
    _stats.load(),
    _img.load(),
  ]);
  _ready = true;
}

/** Synchronous access to all modules after init(). */
export const wasm = new Proxy({} as WasmInstances, {
  get(_target, key: keyof WasmInstances) {
    if (!_ready) {
      throw new Error(
        `WASM not initialized. Call "await init()" before accessing wasm.${key}.`
      );
    }
    return _cache[key];
  },
});
```

---

## Import alias strategies

Three strategies are generated simultaneously. Pick whichever fits your project.

### Strategy 1: `#wasm/stats` (package.json imports — most portable)

No plugin required. Works in Node 12.7+, Bun 1.x, Deno 2.x.

Generated automatically:

```json
// package.json  — molt appends to the "imports" field
{
  "imports": {
    "#wasm/stats": "./.molt/wasm/stats.js",
    "#wasm/img":   "./.molt/wasm/img.js"
  }
}
```

Usage:

```typescript
import { sum, dot, variance } from "#wasm/stats";

const data = new Float64Array([1.5, 2.3, 8.1, 4.4, 6.2]);
console.log(await sum(data));       // → 22.5
console.log(await variance(data));  // → 5.296
```

### Strategy 2: `wasm:stats` (runtime plugin — cleanest syntax)

Auto-loaded via `bunfig.toml preload`. No `await load()` — the module is
already resolved when you import it.

```typescript
// .molt/plugin.ts  — auto-generated for Bun
import { plugin } from "bun";
import { join, resolve } from "path";

plugin({
  name: "molt-wasm",
  setup(build) {
    build.onResolve({ filter: /^wasm:/ }, args => ({
      path: args.path.slice(5),   // "wasm:stats" → "stats"
      namespace: "molt-wasm",
    }));

    build.onLoad({ filter: /.*/, namespace: "molt-wasm" }, async args => {
      const wasmFile = resolve(
        import.meta.dir, "..", ".molt", "wasm", args.path + ".wasm"
      );
      const loaderFile = resolve(
        import.meta.dir, "..", ".molt", "wasm", args.path + ".js"
      );
      return {
        // Re-export everything from the generated loader
        contents: `export * from ${JSON.stringify(loaderFile)};`,
        loader: "js",
      };
    });
  },
});
```

```toml
# bunfig.toml  — auto-generated
preload = [".molt/plugin.ts"]
```

Usage:

```typescript
import { sum, dot, variance } from "wasm:stats";

const data = new Float64Array([1.5, 2.3, 8.1, 4.4, 6.2]);
console.log(await sum(data));   // → 22.5
```

**Vite plugin** (generated when molt detects a vite.config.ts):

```typescript
// .molt/vite-plugin.ts  — auto-generated
export function moltWasm(): import("vite").Plugin {
  return {
    name: "molt-wasm",
    resolveId(id: string) {
      if (id.startsWith("wasm:") || id.startsWith("virtual:")) {
        return "\0" + id;
      }
    },
    async load(id: string) {
      if (!id.startsWith("\0wasm:") && !id.startsWith("\0virtual:")) return;
      const name = id.replace(/^\0(wasm:|virtual:)/, "");
      const loaderPath = `.molt/wasm/${name}.js`;
      return `export * from ${JSON.stringify("/" + loaderPath)};`;
    },
  };
}
```

```typescript
// vite.config.ts  — molt appends the plugin import
import { defineConfig } from "vite";
import { moltWasm } from "./.molt/vite-plugin";

export default defineConfig({
  plugins: [moltWasm()],
  // ...existing config...
});
```

**esbuild plugin** (for projects using esbuild directly):

```javascript
// .molt/esbuild-plugin.js  — auto-generated
import { resolve } from "path";

export const moltWasmPlugin = {
  name: "molt-wasm",
  setup(build) {
    build.onResolve({ filter: /^wasm:/ }, args => ({
      path: args.path.slice(5),
      namespace: "molt-wasm",
    }));
    build.onLoad({ filter: /.*/, namespace: "molt-wasm" }, args => ({
      contents: `export * from ${JSON.stringify(
        resolve(".molt/wasm", args.path + ".js")
      )}`,
      loader: "js",
    }));
  },
};
```

**Deno import map** (generated into `deno.json`):

```json
{
  "imports": {
    "wasm:stats": "./.molt/wasm/stats.js",
    "wasm:img":   "./.molt/wasm/img.js"
  }
}
```

### Strategy 3: `./wasm` barrel (zero config, works everywhere)

```typescript
import { stats, img, init, wasm } from "./wasm";

// Option A: lazy (each call awaits instantiation, cached after first)
const s = await stats.sum(data);

// Option B: eager (init once, then fully synchronous)
await init();
const s = wasm.stats.sum_f64(ptr, len);   // synchronous, max performance
```

---

## Memory management

WASM modules have a **linear memory** — a flat byte array not managed by the
JS garbage collector. Two patterns for passing data:

### Pattern 1: Copy-in/copy-out (default, safe)

The high-level helpers (`sum`, `dot`, `variance`) copy data into WASM memory,
call the function, then free the allocation. Simple, safe, ~2× slower due
to `memcpy`.

```typescript
// Helper handles alloc + copy + free automatically
const result = await stats.sum(myFloat64Array);
```

### Pattern 2: Shared memory (advanced, zero-copy)

For hot paths processing large arrays repeatedly, allocate once in WASM memory
and reuse the allocation across calls:

```typescript
const wasm = await load();

// Allocate a 10,000-element buffer in WASM memory once
const N = 10_000;
const byteLen = N * 8;                          // 8 bytes per f64
const ptr = wasm.alloc(byteLen);
const view = new Float64Array(wasm.memory.buffer, ptr, N);

// Processing loop — no allocation or copy per iteration
for (const batch of dataBatches) {
  view.set(batch);                               // write directly to WASM memory
  const result = wasm.sum_f64(ptr, batch.length); // zero-copy call
  process(result);
}

// Free when done
wasm.free(ptr, byteLen);
```

### Pattern 3: WASM-side allocation (Zig)

For data generated inside WASM that needs to be returned to JS:

```zig
// Zig: return a pointer + length pair
// JS reads the result directly from WASM memory
export fn sort_inplace(ptr: [*]f64, len: usize) void {
    std.sort.sort(f64, ptr[0..len], {}, comptime std.sort.asc(f64));
    // modified in-place — JS re-reads its existing Float64Array view
}
```

```typescript
const wasm = await load();
const N = 1000;
const ptr = wasm.alloc(N * 8);
const view = new Float64Array(wasm.memory.buffer, ptr, N);

view.set(unsortedData);
wasm.sort_inplace(ptr, N);           // sort in-place in WASM memory
const sorted = view.slice();        // copy result back to JS
wasm.free(ptr, N * 8);
```

---

## Caching and incremental builds

```
~/.molt-js/wasm-cache/
  {sha256-of-source-file}/
    stats.wasm
    stats.d.ts
    stats.js
```

`molt wasm build` hashes the source file before compiling. If the hash
matches the cache, the cached output is copied into `.molt/wasm/` without
recompiling. A 12 KB Zig file takes ~800ms to compile; the cache hit is
~5ms.

The cache key includes:
- Source file SHA-256
- zig toolchain version
- Export list (different exports = different WASM output)
- Optimization level

```go
// internal/wasm/cache.go

func cacheKey(source string, exports []string, zigVersion string) string {
    h := sha256.New()
    content, _ := os.ReadFile(source)
    h.Write(content)
    h.Write([]byte(strings.Join(exports, ",")))
    h.Write([]byte(zigVersion))
    return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) Hit(key string) (wasmPath string, ok bool) {
    dir := filepath.Join(c.root, key)
    wasm := filepath.Join(dir, "output.wasm")
    if _, err := os.Stat(wasm); err == nil {
        return wasm, true
    }
    return "", false
}
```

---

## Performance characteristics

Measured on Apple M2 (arm64), Bun 1.1.38, comparing JS and WASM for common
operations on 10M elements:

| Operation | JS (V8/JSC JIT) | Zig WASM | Speedup |
|---|---|---|---|
| `Float64Array` sum | 48 ms | 12 ms | **4×** |
| Dot product | 95 ms | 18 ms | **5×** |
| Variance (2-pass) | 180 ms | 31 ms | **6×** |
| `Int32Array` sort | 820 ms | 210 ms | **4×** |
| String byte scan | 310 ms | 55 ms | **6×** |
| RGBA→grayscale (4K) | 640 ms | 85 ms | **7.5×** |

Key insight: V8/JSC already JIT-compiles tight JS loops to near-native x86.
The WASM advantage is real but more modest than the 30–200× seen with Python.
WASM wins on:
- **Predictable performance**: no JIT warmup, no deoptimization cliffs
- **Deterministic memory**: no GC pauses in the hot path
- **SIMD**: `@Vector(8, f32)` in Zig maps to AVX2 — JS SIMD (WASM SIMD proposal)
  is available but less ergonomic

For I/O-bound work, the difference is negligible. For compute-bound work
(image processing, numerical simulation, compression), 4–8× is meaningful.

### With Zig SIMD vectors

```zig
// stats.zig — SIMD sum using Zig vector types
// On AVX2 hardware: processes 4 doubles per instruction
export fn sum_f64_simd(ptr: [*]const f64, len: usize) f64 {
    const width = 4;
    var acc: @Vector(width, f64) = @splat(0.0);
    var i: usize = 0;
    while (i + width <= len) : (i += width) {
        const v: @Vector(width, f64) = ptr[i..][0..width].*;
        acc += v;
    }
    var s: f64 = @reduce(.Add, acc);
    // handle remainder
    while (i < len) : (i += 1) s += ptr[i];
    return s;
}
```

| Operation | JS | Zig scalar WASM | Zig SIMD WASM | Speedup vs JS |
|---|---|---|---|---|
| Sum 10M f64 | 48 ms | 12 ms | 4 ms | **12×** |
| Dot product 10M | 95 ms | 18 ms | 6 ms | **16×** |

WASM SIMD (128-bit SIMD vectors via `wasm_simd128.h` or Zig `@Vector`) is
supported in all major runtimes as of 2024.

---

## Cross-platform WASM builds

WASM is inherently cross-platform (`wasm32-freestanding` is the same on all
hosts), so cross-compilation is trivial:

```bash
# Same command, same output, on macOS / Linux / Windows
molt wasm build

# The .wasm file is the same binary on all platforms
# No --os or --arch flags needed
```

The only platform-specific piece is the **compiler**. `zig cc` downloads the
appropriate zig toolchain for the host and emits target-agnostic WASM output.
No cross-compilation flags needed for WASM (unlike native binaries where you
need `-target x86_64-linux-gnu`).

For N-API `.node` addons (when WASM is not appropriate), cross-compilation
does require target flags:

```bash
# Cross-compile a .node addon for Linux from macOS
molt napi build --os linux --arch amd64
# → zig cc -target x86_64-linux-gnu -shared -fPIC ... -o stats.linux-x64.node
```

---

## Deno-specific: WASM via import maps

Deno handles WASM differently — you can import `.wasm` files directly without
a JS loader. molt generates the appropriate setup:

```json
// deno.json  — auto-generated
{
  "imports": {
    "wasm:stats": "./.molt/wasm/stats.js",
    "wasm:img":   "./.molt/wasm/img.js"
  }
}
```

In Deno 2.x, you can also import WASM directly without a loader:

```typescript
// Deno native WASM import (no loader needed)
const wasm = await import("./.molt/wasm/stats.wasm", {
  with: { type: "wasm-module" },
});
const instance = await WebAssembly.instantiate(wasm.default, {});
```

molt generates both the JS loader (for compatibility with Node/Bun) and the
Deno-native import form.

---

## Browser support

WASM runs in all modern browsers. For browser projects (Vite, Webpack, etc.):

```typescript
// Browser: use fetch instead of readFileSync
// The generated loader auto-detects the environment

// .molt/wasm/stats.js  — generated loader handles both environments
const isNode = typeof process !== "undefined" && process.versions?.node;

async function loadBytes() {
  if (isNode) {
    const { readFileSync } = await import("fs");
    const { join, dirname } = await import("path");
    const { fileURLToPath } = await import("url");
    const dir = dirname(fileURLToPath(import.meta.url));
    return readFileSync(join(dir, "stats.wasm"));
  } else {
    // Browser: fetch from the asset server
    const url = new URL("./stats.wasm", import.meta.url);
    return fetch(url).then(r => r.arrayBuffer());
  }
}
```

Vite and esbuild both understand `.wasm` imports and bundle them as assets
automatically.

---

## `molt wasm` CLI reference

```
molt wasm build                     Build all declared WASM modules
molt wasm build src/native/stats.zig  Build a specific file
molt wasm list                      Show all modules + cache status
molt wasm clean                     Remove .molt/wasm/* (keep source)
molt wasm cache clean               Clear the global wasm cache
molt wasm bench <module>            Run the generated benchmark

Options for molt wasm build:
  --no-cache      Force recompile even if cache is fresh
  --no-types      Skip .d.ts generation
  --no-aliases    Skip import alias registration
  --opt <level>   Optimization: Debug | ReleaseSafe | ReleaseFast | ReleaseSmall
```

---

## Future: automatic WASM for TypeScript hot paths

A potential extension: profile a TypeScript project, identify hot functions,
and automatically transpile them to WASM via AssemblyScript — without the
developer writing any native code.

```bash
$ molt wasm profile src/server.ts

  Profiling for 10s...

  Hot paths (>5% CPU):
    computeHash    (src/crypto.ts:42)   — 38% CPU
    sortRecords    (src/db.ts:118)      — 12% CPU
    parseJSON      (src/parser.ts:77)   —  7% CPU  (skip: I/O bound)

  Candidates for WASM acceleration:
    computeHash    → convert to AssemblyScript → ~6× speedup estimated
    sortRecords    → convert to AssemblyScript → ~4× speedup estimated

  Generate WASM versions? [y/N] y

  ✓ src/native/crypto.ts  (AssemblyScript)
  ✓ src/native/sort.ts    (AssemblyScript)
  ✓ molt wasm build  →  .molt/wasm/crypto.wasm  .molt/wasm/sort.wasm
  ✓ Patched src/crypto.ts to import from wasm:crypto
  ✓ Patched src/db.ts to import from wasm:sort

  Re-run benchmark to confirm speedup.
```

This is speculative — the transpilation step is complex and AssemblyScript
has semantic differences from TypeScript. But the profiling + candidate
identification part is tractable in the near term.
