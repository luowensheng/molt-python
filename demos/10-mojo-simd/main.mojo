# 10-mojo-simd / main.mojo
#
# Run with:  molt run main.mojo
#
# Demonstrates Mojo SIMD types, vectorized math, and performance.
# In Mojo 1.0b1 the hardware-width helper (simdwidthof) lives in an internal
# module; we use a compile-time constant that matches Apple Silicon's 128-bit
# NEON registers (4 × float32 or 2 × float64 per instruction).
#
# Tip: on x86 with AVX2 you can double the widths (8 × float32, 4 × float64)
# for another 2× speedup — Mojo emits the right native SIMD instructions.

from math import cos, exp, sqrt
from collections import List
from time import perf_counter_ns


# ── 1. SIMD construction and arithmetic ──────────────────────────────────────

def simd_basics():
    print("=== 1. SIMD basics ===")

    # Construct a width-4 uint8 SIMD vector
    var v = SIMD[DType.uint8, 4](10, 20, 30, 40)
    print("v       =", v)      # [10, 20, 30, 40]
    v[2] = 99
    print("v[2]=99 =", v)      # [10, 20, 99, 40]

    # Element-wise arithmetic
    var a = SIMD[DType.float32, 4](1.0, 2.0, 3.0, 4.0)
    var b = SIMD[DType.float32, 4](10.0, 10.0, 10.0, 10.0)
    print("a + b   =", a + b)  # [11. 12. 13. 14.]
    print("a * b   =", a * b)  # [10. 20. 30. 40.]
    print("a * a   =", a * a)  # [1.  4.  9. 16.]

    # On Apple Silicon (128-bit NEON): 4 × float32 or 2 × float64 per register
    print("\nNative register width on this CPU:")
    print("  float32: 4 values per SIMD instruction (128-bit / 32)")
    print("  float64: 2 values per SIMD instruction (128-bit / 64)")


# ── 2. Reduce operations ─────────────────────────────────────────────────────

def simd_reductions():
    print("\n=== 2. Reductions ===")

    var v = SIMD[DType.float32, 8](1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0)
    print("v    =", v)
    print("sum  =", v.reduce_add())   # 36.0
    print("max  =", v.reduce_max())   # 8.0
    print("min  =", v.reduce_min())   # 1.0

    # Dot product: element-wise multiply then horizontal add
    var u = SIMD[DType.float64, 4](1.0, 2.0, 3.0, 0.0)
    var dot = (u * u).reduce_add()    # 1+4+9+0 = 14
    print("||u||² =", dot)
    print("||u||  =", sqrt(dot))      # 3.741...


# ── 3. Shuffle / join / interleave ───────────────────────────────────────────

def simd_permutations():
    print("\n=== 3. Permutations ===")

    var a = SIMD[DType.float32, 4](1.0, 2.0, 3.0, 4.0)
    var b = SIMD[DType.float32, 4](5.0, 6.0, 7.0, 8.0)

    print("join:       ", a.join(b))       # [1,2,3,4, 5,6,7,8]
    print("interleave: ", a.interleave(b)) # [1,5,2,6,3,7,4,8]

    var c = SIMD[DType.int32, 4](10, 20, 30, 40)
    print("shuffle [3,1,0,2]:", c.shuffle[3, 1, 0, 2]())  # [40,20,10,30]


# ── 4. SIMD math: cos / sqrt / exp ───────────────────────────────────────────

def simd_math():
    print("\n=== 4. SIMD math ===")

    # cos on a width-4 float64 vector: [0, π/2, π, 3π/2]
    alias PI = 3.141592653589793
    var angles = SIMD[DType.float64, 4](0.0, PI/2.0, PI, 3.0*PI/2.0)
    print("angles:", angles)
    print("cos:   ", cos(angles))    # [1.0, ~0.0, -1.0, ~0.0]
    print("sqrt:  ", sqrt(SIMD[DType.float32, 4](1.0, 4.0, 9.0, 16.0)))

    # exp on float32
    var xs = SIMD[DType.float32, 4](-2.0, -1.0, 0.0, 1.0)
    print("exp:   ", exp(xs))        # [0.135, 0.368, 1.0, 2.718]


# ── 5. Benchmark: scalar vs SIMD sigmoid ─────────────────────────────────────
# ARM NEON: 4 × float32 per register; x86 AVX2: use width=8 for 2× more gain.

def benchmark_sigmoid():
    print("\n=== 5. Benchmark: scalar vs SIMD sigmoid (N=1M) ===")

    alias N = 1_000_000
    alias W = 4     # float32 SIMD width (128-bit NEON)

    # Build input data using List (heap-allocated, GC-free)
    var data = List[Float32](capacity=N)
    for i in range(N):
        data.append(Float32(i) / Float32(N) * 10.0 - 5.0)
    var ptr = data.unsafe_ptr()

    # --- Scalar ---
    var t0 = perf_counter_ns()
    var total: Float32 = 0.0
    for i in range(N):
        total += 1.0 / (1.0 + exp(-ptr[i]))
    var scalar_ns = perf_counter_ns() - t0

    # --- SIMD (width=W) ---
    var t1 = perf_counter_ns()
    var acc = SIMD[DType.float32, W](0.0)
    var i = 0
    while i + W <= N:
        var x = ptr.load[width=W](i)
        acc += 1.0 / (1.0 + exp(-x))
        i += W
    # scalar tail for any leftover elements
    var tail: Float32 = 0.0
    while i < N:
        tail += 1.0 / (1.0 + exp(-ptr[i]))
        i += 1
    var simd_ns = perf_counter_ns() - t1

    var scalar_ms = Float64(scalar_ns) / 1_000_000.0
    var simd_ms   = Float64(simd_ns)   / 1_000_000.0
    print("scalar:         ", scalar_ms, "ms  (sum:", total, ")")
    print("SIMD (width=4): ", simd_ms,   "ms  (sum:", acc.reduce_add() + tail, ")")
    print("speedup:        ", scalar_ms / simd_ms, "x")


def main():
    simd_basics()
    simd_reductions()
    simd_permutations()
    simd_math()
    benchmark_sigmoid()
