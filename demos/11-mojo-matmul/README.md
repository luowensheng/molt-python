# 11-mojo-matmul

Matrix multiplication in three increasingly optimised forms — naive triple-loop,
SIMD-vectorized inner loop, and cache-friendly tiled + parallelized — benchmarked
against each other and numpy.

## What this demo shows

- A `Matrix` struct backed by `List[Float32]` with SIMD `load` / `store` methods
- **Naive** O(n³) matmul as a correctness baseline
- **Vectorized** inner loop using manual SIMD while-loops — processes `NELTS=4`
  columns per instruction on ARM NEON (double on AVX2)
- **Tiled** access pattern for L1 cache locality, combined with `parallelize[fn](rows)`
  for multi-core execution
- `perf_counter_ns()` for accurate wall-clock benchmarks
- `bench_numpy.py` gives a numpy baseline for direct comparison

## Project layout

```
11-mojo-matmul/
  main.mojo         Matrix struct + 3 matmul implementations + benchmarks
  bench_numpy.py    numpy @ operator baseline
  pyproject.toml
  README.md
```

## Running

```bash
molt sync

# Mojo implementation + benchmark
molt run main.mojo

# numpy baseline
molt run bench-np
```

## The three levels

### Level 1 — Naive

```mojo
def matmul_naive(mut C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            for n in range(C.cols):
                C[m, n] += A[m, k] * B[k, n]
```

Straightforward but slow: one scalar multiply per loop iteration.

### Level 2 — Vectorized

```mojo
comptime NELTS = 4   # 4 × float32 per NEON register (128-bit ARM)

def matmul_vectorized(mut C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            var a_mk = A[m, k]
            var n = 0
            while n + NELTS <= C.cols:
                C.store[NELTS](m, n,
                    C.load[NELTS](m, n) + a_mk * B.load[NELTS](k, n))
                n += NELTS
            while n < C.cols:           # scalar tail
                C[m, n] += a_mk * B[k, n]
                n += 1
```

Each inner iteration issues one SIMD FMA, processing `NELTS` columns at once.
On AVX2 set `NELTS = 8` for another 2× speedup.

### Level 3 — Tiled + parallelized

Adds a 2-D tile over the `k` dimension (tile size = 4) to keep the B tile in L1
cache during the innermost SIMD loop, then uses `parallelize[calc_row](C.rows)`
to spread rows across CPU cores.

## Typical results (N=128, Apple Silicon M-series)

| Implementation | Time | Speedup vs naive |
|---|---|---|
| Mojo naive | ~7.6 ms | 1× |
| Mojo vectorized | ~0.27 ms | ~29× |
| Mojo tiled + parallel | ~0.09 ms | ~80× |
| numpy `@` | ~0.007 ms | (BLAS-optimised) |

Run `molt run bench-np` to get the numpy number on your machine.
The tiled Mojo implementation approaches numpy on pure scalar compute; numpy uses
BLAS which adds assembly-optimised block algorithms on top.

## Mojo 1.0b1 notes

The pre-1.0 `vectorize[fn, width](size)` and `simdwidthof[T]()` APIs are not
available in 1.0b1. This demo uses manual SIMD while-loops with a hardcoded
`NELTS = 4` (128-bit NEON) instead. See [docs/mojo.md](../../docs/mojo.md) for
the full API migration table.
