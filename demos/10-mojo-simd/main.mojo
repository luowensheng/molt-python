# 10-mojo-simd / main.mojo
#
# Run with:  molt run main.mojo
#
# Demonstrates Mojo SIMD types, auto-width detection, vectorized operations,
# and benchmarking. SIMD lets Mojo process N values in a single CPU instruction,
# matching what C/C++ compilers emit for hot numerical loops.

from math import cos, iota, pi, sqrt, exp
from memory import UnsafePointer
from algorithm import vectorize
from benchmark import run, keep
from sys.info import simdwidthof, simdbitwidth


# ── 1. SIMD construction and arithmetic ──────────────────────────────────────

def simd_basics():
    print("=== 1. SIMD basics ===")

    # Construct a width-4 uint8 SIMD vector
    var v = SIMD[DType.uint8, 4](10, 20, 30, 40)
    print("v      =", v)          # [10, 20, 30, 40]
    v[2] = 99
    print("v[2]=99:", v)          # [10, 20, 99, 40]

    # Arithmetic is element-wise
    var a = SIMD[DType.float32, 4](1.0, 2.0, 3.0, 4.0)
    var b = SIMD[DType.float32, 4](10.0, 10.0, 10.0, 10.0)
    print("a + b  =", a + b)      # [11. 12. 13. 14.]
    print("a * b  =", a * b)      # [10. 20. 30. 40.]
    print("a ** 2 =", a * a)      # [1.  4.  9. 16.]

    # iota fills with sequential values starting at offset
    var seq = iota[DType.uint8, 8](0)
    print("iota   =", seq)        # [0, 1, 2, 3, 4, 5, 6, 7]

    # Hardware width — how many float64 fit in one SIMD register on this CPU
    alias w = simdwidthof[DType.float64]()
    print("\nCPU SIMD bit-width:", simdbitwidth())
    print("float64 SIMD width:", w, "(process", w, "f64 values per instruction)")


# ── 2. Reduce operations ─────────────────────────────────────────────────────

def simd_reductions():
    print("\n=== 2. Reductions ===")

    var v = SIMD[DType.float32, 8](1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0)
    print("sum  =", v.reduce_add())    # 36.0
    print("max  =", v.reduce_max())    # 8.0
    print("min  =", v.reduce_min())    # 1.0

    # Dot product: element-wise multiply then horizontal add
    var u = SIMD[DType.float64, 4](1.0, 2.0, 3.0, 0.0)
    var dot = (u * u).reduce_add()     # 1+4+9+0 = 14
    print("||u||² =", dot)
    print("||u||  =", sqrt(dot))


# ── 3. Shuffle / join / interleave ───────────────────────────────────────────

def simd_permutations():
    print("\n=== 3. Permutations ===")

    var a = SIMD[DType.float32, 4](1.0, 2.0, 3.0, 4.0)
    var b = SIMD[DType.float32, 4](5.0, 6.0, 7.0, 8.0)

    print("join:       ", a.join(b))        # [1,2,3,4, 5,6,7,8]
    print("interleave: ", a.interleave(b))  # [1,5,2,6,3,7,4,8]

    var c = SIMD[DType.int32, 4](10, 20, 30, 40)
    print("shuffle [3,1,0,2]:", c.shuffle[3, 1, 0, 2]())  # [40,20,10,30]


# ── 4. Vectorized cosine over a buffer ───────────────────────────────────────

def vectorized_cosine():
    print("\n=== 4. Vectorized cosine (256 elements) ===")

    alias N = 256
    alias dtype = DType.float64
    alias simd_w = simdwidthof[dtype]()

    var buf = UnsafePointer[dtype].alloc(N)

    @parameter
    fn compute_cos[w: Int](i: Int):
        var x = iota[dtype, w](i) * (pi * 2.0 / Float64(N))
        buf.store(i, cos(x))

    vectorize[compute_cos, simd_w](N)

    print("cos(0)   =", buf.load(0))    # 1.0
    print("cos(π/2) =", buf.load(64))   # ~0.0
    print("cos(π)   =", buf.load(128))  # -1.0
    print("cos(3π/2)=", buf.load(192))  # ~0.0

    buf.free()


# ── 5. Benchmark: scalar vs SIMD sigmoid ─────────────────────────────────────

def benchmark_sigmoid():
    print("\n=== 5. Benchmark: scalar vs SIMD sigmoid (N=1M) ===")

    alias N = 1_000_000
    alias dtype = DType.float32
    alias simd_w = simdwidthof[dtype]()

    var data = UnsafePointer[dtype].alloc(N)
    for i in range(N):
        data[i] = Float32(i) / Float32(N) * 10.0 - 5.0

    fn scalar_sigmoid():
        var total: Float32 = 0.0
        for i in range(N):
            total += 1.0 / (1.0 + exp(-data[i]))
        keep(total)

    fn simd_sigmoid():
        var acc = SIMD[dtype, simd_w](0.0)
        var i = 0
        while i + simd_w <= N:
            var x = data.load[width=simd_w](i)
            acc += 1.0 / (1.0 + exp(-x))
            i += simd_w
        keep(acc.reduce_add())

    print("scalar:")
    run[scalar_sigmoid](max_runtime_secs=1.0).print()
    print("SIMD (width=" + String(simd_w) + "):")
    run[simd_sigmoid](max_runtime_secs=1.0).print()

    data.free()


def main():
    simd_basics()
    simd_reductions()
    simd_permutations()
    vectorized_cosine()
    benchmark_sigmoid()
