# 10-mojo-simd

Mojo SIMD types, vectorized operations, hardware-width detection, and benchmarking
against pure Python. Demonstrates where Mojo's speed advantage over Python comes
from: SIMD processes N values per CPU instruction without GIL overhead.

## What this demo shows

- `SIMD[DType, width]` — construct, index, arithmetic, reduce
- `simdwidthof[dtype]()` — query how wide your CPU's SIMD registers are at compile time
- `iota` — fill a SIMD vector with sequential values
- `vectorize[fn, width](N)` — auto-tile a buffer operation to match hardware width
- `shuffle`, `join`, `interleave` — lane permutation operations
- `benchmark.run` / `keep` — accurate microbenchmarking inside Mojo
- Side-by-side comparison with pure Python and numpy baselines

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
from memory import UnsafePointer
from algorithm import vectorize
from sys.info import simdwidthof

alias dtype  = DType.float32
alias simd_w = simdwidthof[dtype]()   # e.g. 8 on AVX2, 16 on AVX-512

var buf = UnsafePointer[dtype].alloc(N)

@parameter
fn sigmoid_lane[w: Int](i: Int):
    var x = buf.load[width=w](i)
    buf.store(i, 1.0 / (1.0 + exp(-x)))

vectorize[sigmoid_lane, simd_w](N)    # processes simd_w elements per call
```

The `@parameter` annotation means `w` is a compile-time constant — the compiler
specialises the function for each possible width and emits native SIMD instructions.

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
