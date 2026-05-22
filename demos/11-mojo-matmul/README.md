# 11-mojo-matmul

Matrix multiplication in three increasingly optimised forms — naive triple-loop,
SIMD-vectorized inner loop, and cache-friendly tiled + parallelized — benchmarked
against each other and against numpy.

## What this demo shows

- A `Matrix` struct backed by `UnsafePointer[Float32]` with SIMD load/store methods
- **Naive** O(n³) matmul as a correctness baseline
- **Vectorized** inner loop with `vectorize[fn, simd_width](cols)` — processes
  `simd_width` columns in one instruction
- **Tiled** access pattern with `tile[fn, tile_j, tile_k](cols, rows)` for cache
  locality, combined with `parallelize[fn](rows)` for multi-core execution
- `benchmark.run` / `keep` for accurate Mojo microbenchmarks
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
def matmul_naive(C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            for n in range(C.cols):
                C[m, n] += A[m, k] * B[k, n]
```

Straightforward but slow: one scalar multiply per loop iteration.

### Level 2 — Vectorized

```mojo
alias nelts = simdwidthof[DType.float32]()   # 8 on AVX2

def matmul_vectorized(C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            @parameter
            fn dot[nelts: Int](n: Int):
                C.store[nelts](m, n,
                    C.load[nelts](m, n) + A[m, k] * B.load[nelts](k, n))
            vectorize[dot, nelts](C.cols)
```

`vectorize` tiles the inner loop in chunks of `nelts`, issuing one SIMD FMA
instruction per chunk.

### Level 3 — Tiled + parallelized

Adds `tile[...]` for cache-friendly block access and `parallelize[calc_row]`
to use all CPU cores.

## Typical results (N=512, MacBook Pro M3)

| Implementation | Time/call |
|---|---|
| numpy `@` | ~0.3 ms |
| Mojo naive | ~90 ms |
| Mojo vectorized | ~4 ms |
| Mojo tiled + parallel | ~0.25 ms |

Mojo's tiled implementation reaches numpy-level performance on pure compute without
calling BLAS.
