# 10-mojo-simd

Mojo SIMD types, vectorized operations, hardware-width detection, and benchmarking
against pure Python. Demonstrates where Mojo's speed advantage over Python comes
from: SIMD processes N values per CPU instruction without GIL overhead.

## What this demo shows

- `SIMD[DType, width]` — construct, index, arithmetic, reduce
- Hardcoded `alias W = 4` for 128-bit NEON (4 × float32); change to 8 on AVX2
- `shuffle`, `join`, `interleave` — lane permutation operations
- Manual SIMD while-loop with scalar tail for remainder elements
- Side-by-side scalar vs SIMD benchmark using `perf_counter_ns()`

> **Mojo 1.0b1 note:** `simdwidthof[T]()`, `vectorize[fn, width](N)`, and
> `benchmark.run` are not available in 1.0b1. This demo uses a hardcoded
> width constant and a manual while-loop instead.

## Project layout

```
10-mojo-simd/
  main.mojo         all SIMD demos + Mojo benchmark
  bench_python.py   pure Python and numpy baselines for comparison
  pyproject.toml
  README.md
```

## Running

```bash
molt sync

# Mojo demos + benchmark
molt run main.mojo

# Python/numpy baselines (compare manually)
molt run bench-py
```

## What SIMD looks like

```mojo
from math import exp
from collections import List
from time import perf_counter_ns

alias W = 4    # 4 × float32 = 128-bit NEON; use 8 on AVX2

var data = List[Float32](capacity=N)
# ... fill data ...
var ptr = data.unsafe_ptr()

var acc = SIMD[DType.float32, W](0.0)
var i = 0
while i + W <= N:
    var x = ptr.load[width=W](i)
    acc += 1.0 / (1.0 + exp(-x))   # one SIMD FMA per loop iteration
    i += W
# scalar tail for remaining elements
while i < N:
    acc[0] += 1.0 / (1.0 + exp(-ptr[i]))
    i += 1
```

`ptr.load[width=W](i)` reads W consecutive floats into a SIMD register in one
instruction. The compiler emits native SIMD FMA instructions for the whole loop.

## Typical speedup

On a modern x86 machine (AVX2):

| Implementation | Time (N=1M sigmoid) |
|---|---|
| Pure Python | ~400 ms |
| numpy | ~3 ms |
| Mojo scalar | ~2 ms |
| Mojo SIMD (width=8) | ~0.3 ms |

Mojo SIMD is typically 10–30× faster than numpy and 1000× faster than pure Python
for numerical loops, because it avoids Python object overhead and uses native
vector instructions.

## Key SIMD types

| Type | Description |
|---|---|
| `SIMD[DType.float32, 8]` | 8-wide float32 vector (256-bit AVX) |
| `SIMD[DType.float64, 4]` | 4-wide float64 vector (256-bit AVX) |
| `SIMD[DType.int32, 16]`  | 16-wide int32 (512-bit AVX-512) |
| `DType.uint8`, `.int64`, `.float16`, … | full set of numeric element types |
