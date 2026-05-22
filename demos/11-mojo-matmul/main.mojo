# 11-mojo-matmul / main.mojo
#
# Run with:  molt run main.mojo
#
# Three levels of matrix multiply, each faster than the last:
#   1. Naive O(n³) triple-loop
#   2. SIMD-vectorized inner loop via `vectorize`
#   3. Tiled + vectorized (cache-friendly block access)
#
# Source: adapted from docs.modular.com/mojo/notebooks/Matmul/

from algorithm import vectorize, parallelize
from benchmark import run, keep
from memory import UnsafePointer
from sys.info import simdwidthof


# ── Matrix type ───────────────────────────────────────────────────────────────

struct Matrix:
    var data: UnsafePointer[Float32]
    var rows: Int
    var cols: Int

    def __init__(out self, rows: Int, cols: Int):
        self.rows = rows
        self.cols = cols
        self.data = UnsafePointer[Float32].alloc(rows * cols)
        for i in range(rows * cols):
            self.data[i] = 0.0

    def __del__(owned self):
        self.data.free()

    def __getitem__(self, r: Int, c: Int) -> Float32:
        return self.data[r * self.cols + c]

    def __setitem__(mut self, r: Int, c: Int, val: Float32):
        self.data[r * self.cols + c] = val

    def load[width: Int](self, r: Int, c: Int) -> SIMD[DType.float32, width]:
        return self.data.load[width=width](r * self.cols + c)

    def store[width: Int](mut self, r: Int, c: Int, val: SIMD[DType.float32, width]):
        self.data.store(r * self.cols + c, val)

    def fill_seq(mut self):
        """Fill with A[i,j] = (i*cols+j) / (rows*cols) — small floats."""
        var n = Float32(self.rows * self.cols)
        for i in range(self.rows * self.cols):
            self.data[i] = Float32(i) / n

    def fill_identity(mut self):
        """Fill as identity matrix."""
        for i in range(self.rows):
            for j in range(self.cols):
                self[i, j] = Float32(1 if i == j else 0)


# ── 1. Naive matmul ───────────────────────────────────────────────────────────

def matmul_naive(C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            for n in range(C.cols):
                C[m, n] += A[m, k] * B[k, n]


# ── 2. SIMD-vectorized inner loop ─────────────────────────────────────────────

def matmul_vectorized(C: Matrix, A: Matrix, B: Matrix):
    alias nelts = simdwidthof[DType.float32]()

    for m in range(C.rows):
        for k in range(A.cols):
            @parameter
            fn dot[nelts: Int](n: Int):
                C.store[nelts](
                    m, n,
                    C.load[nelts](m, n) + A[m, k] * B.load[nelts](k, n),
                )
            vectorize[dot, nelts](C.cols)


# ── 3. Tiled + vectorized (cache-friendly) ────────────────────────────────────

alias TILE = 4   # tune to your cache line / SIMD width

def matmul_tiled(C: Matrix, A: Matrix, B: Matrix):
    alias nelts = simdwidthof[DType.float32]()

    @parameter
    fn calc_row(m: Int):
        @parameter
        fn calc_tile[tile_j: Int, tile_k: Int](jo: Int, ko: Int):
            for k in range(ko, ko + tile_k):
                @parameter
                fn dot[nelts: Int](n: Int):
                    C.store[nelts](
                        m, n + jo,
                        C.load[nelts](m, n + jo) + A[m, k] * B.load[nelts](k, n + jo),
                    )
                vectorize[dot, nelts](tile_j)

        alias T = TILE * nelts
        tile[calc_tile[T, TILE], T, TILE](C.cols, B.rows)

    parallelize[calc_row](C.rows)


# ── correctness check ─────────────────────────────────────────────────────────

def check_equal(A: Matrix, B: Matrix, tol: Float32 = 1e-4) -> Bool:
    for i in range(A.rows):
        for j in range(A.cols):
            if abs(A[i, j] - B[i, j]) > tol:
                return False
    return True


# ── main ──────────────────────────────────────────────────────────────────────

def main():
    alias N = 128   # matrix size — increase to 512+ for visible speedup

    print("Matrix size:", N, "x", N)
    print("SIMD width (float32):", simdwidthof[DType.float32]())

    # Build input matrices
    var A = Matrix(N, N)
    var B = Matrix(N, N)
    A.fill_seq()
    B.fill_seq()

    # --- Naive ---
    var C_naive = Matrix(N, N)
    matmul_naive(C_naive, A, B)
    print("\nC_naive[0,0] =", C_naive[0, 0])
    print("C_naive[1,1] =", C_naive[1, 1])

    # --- Vectorized ---
    var C_vec = Matrix(N, N)
    matmul_vectorized(C_vec, A, B)
    print("\nC_vec[0,0]   =", C_vec[0, 0])
    print("results match naive:", check_equal(C_naive, C_vec))

    # --- Tiled ---
    var C_tiled = Matrix(N, N)
    matmul_tiled(C_tiled, A, B)
    print("C_tiled[0,0] =", C_tiled[0, 0])
    print("results match naive:", check_equal(C_naive, C_tiled))

    # --- Benchmarks ---
    print("\n=== Benchmarks (N=" + String(N) + ") ===")

    var A2 = Matrix(N, N)
    var B2 = Matrix(N, N)
    A2.fill_seq()
    B2.fill_seq()

    fn bench_naive():
        var C = Matrix(N, N)
        matmul_naive(C, A2, B2)
        keep(C[0, 0])

    fn bench_vec():
        var C = Matrix(N, N)
        matmul_vectorized(C, A2, B2)
        keep(C[0, 0])

    fn bench_tiled():
        var C = Matrix(N, N)
        matmul_tiled(C, A2, B2)
        keep(C[0, 0])

    print("naive:      ")
    run[bench_naive](max_runtime_secs=1.0).print()
    print("vectorized: ")
    run[bench_vec](max_runtime_secs=1.0).print()
    print("tiled:      ")
    run[bench_tiled](max_runtime_secs=1.0).print()
