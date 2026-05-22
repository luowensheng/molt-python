# 11-mojo-matmul / main.mojo
#
# Run with:  molt run main.mojo
#
# Three levels of matrix multiply, each faster than the last:
#   1. Naive O(n³) triple-loop
#   2. SIMD-vectorized inner loop (manual SIMD while-loop)
#   3. Tiled + vectorized (cache-friendly block access) with parallelize

from std.collections import List
from std.algorithm import parallelize
from std.time import perf_counter_ns


# ── SIMD width constants ──────────────────────────────────────────────────────
# Apple Silicon (128-bit NEON): 4 × float32 per register.
# On x86 with AVX2 you can double this to 8 for another 2× speedup.
comptime NELTS = 4


# ── Matrix type ───────────────────────────────────────────────────────────────

struct Matrix(Movable):
    var data: List[Float32]
    var rows: Int
    var cols: Int

    def __init__(out self, rows: Int, cols: Int):
        self.rows = rows
        self.cols = cols
        self.data = List[Float32](capacity=rows * cols)
        for _ in range(rows * cols):
            self.data.append(0.0)

    def __getitem__(self, r: Int, c: Int) -> Float32:
        return self.data[r * self.cols + c]

    def __setitem__(mut self, r: Int, c: Int, val: Float32):
        self.data[r * self.cols + c] = val

    def load[width: Int](self, r: Int, c: Int) -> SIMD[DType.float32, width]:
        return self.data.unsafe_ptr().load[width=width](r * self.cols + c)

    def store[width: Int](mut self, r: Int, c: Int, val: SIMD[DType.float32, width]):
        self.data.unsafe_ptr().store[width=width](r * self.cols + c, val)

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

def matmul_naive(mut C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            for n in range(C.cols):
                C[m, n] += A[m, k] * B[k, n]


# ── 2. SIMD-vectorized inner loop ─────────────────────────────────────────────

def matmul_vectorized(mut C: Matrix, A: Matrix, B: Matrix):
    for m in range(C.rows):
        for k in range(A.cols):
            var a_mk = A[m, k]
            var n = 0
            while n + NELTS <= C.cols:
                var cv = C.load[NELTS](m, n)
                var bv = B.load[NELTS](k, n)
                C.store[NELTS](m, n, cv + a_mk * bv)
                n += NELTS
            while n < C.cols:
                C[m, n] += a_mk * B[k, n]
                n += 1


# ── 3. Tiled + vectorized (cache-friendly) ────────────────────────────────────
# Manual 2-D tiling: iterate over tiles of TILE rows to keep B tiles
# in L1 cache during the innermost vectorized loop over n.

comptime TILE = 4   # tune to your cache line / SIMD width

def matmul_tiled(mut C: Matrix, A: Matrix, B: Matrix):
    @parameter
    def calc_row(m: Int):
        for ko in range(0, A.cols, TILE):
            var k_end = ko + TILE if ko + TILE < A.cols else A.cols
            for k in range(ko, k_end):
                var a_mk = A[m, k]
                var n = 0
                while n + NELTS <= C.cols:
                    var cv = C.load[NELTS](m, n)
                    var bv = B.load[NELTS](k, n)
                    C.store[NELTS](m, n, cv + a_mk * bv)
                    n += NELTS
                while n < C.cols:
                    C[m, n] += a_mk * B[k, n]
                    n += 1

    parallelize[calc_row](C.rows)


# ── correctness check ─────────────────────────────────────────────────────────

def check_equal(A: Matrix, B: Matrix, tol: Float32 = 1e-3) -> Bool:
    for i in range(A.rows):
        for j in range(A.cols):
            if abs(A[i, j] - B[i, j]) > tol:
                return False
    return True


# ── main ──────────────────────────────────────────────────────────────────────

def main():
    comptime N = 128   # matrix size — increase to 256+ for visible speedup

    print("Matrix size:", N, "x", N)
    print("SIMD width (float32):", NELTS, "(hardcoded for 128-bit NEON / Apple Silicon)")

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
    print("\n=== Benchmarks (N=" + String(N) + ", 3 runs each) ===")

    var A2 = Matrix(N, N)
    var B2 = Matrix(N, N)
    A2.fill_seq()
    B2.fill_seq()

    # Naive timing (3 runs, take minimum)
    var best_naive = UInt(9_000_000_000_000_000_000)
    for _ in range(3):
        var C = Matrix(N, N)
        var t0 = perf_counter_ns()
        matmul_naive(C, A2, B2)
        var elapsed = perf_counter_ns() - t0
        if elapsed < best_naive:
            best_naive = elapsed

    # Vectorized timing
    var best_vec = UInt(9_000_000_000_000_000_000)
    for _ in range(3):
        var C = Matrix(N, N)
        var t0 = perf_counter_ns()
        matmul_vectorized(C, A2, B2)
        var elapsed = perf_counter_ns() - t0
        if elapsed < best_vec:
            best_vec = elapsed

    # Tiled timing
    var best_tiled = UInt(9_000_000_000_000_000_000)
    for _ in range(3):
        var C = Matrix(N, N)
        var t0 = perf_counter_ns()
        matmul_tiled(C, A2, B2)
        var elapsed = perf_counter_ns() - t0
        if elapsed < best_tiled:
            best_tiled = elapsed

    var t_naive = Float64(Int(best_naive)) / 1_000_000.0
    var t_vec   = Float64(Int(best_vec))   / 1_000_000.0
    var t_tiled = Float64(Int(best_tiled)) / 1_000_000.0

    print("naive:      ", t_naive, "ms")
    print("vectorized: ", t_vec,   "ms  (", t_naive / t_vec,   "x faster)")
    print("tiled:      ", t_tiled, "ms  (", t_naive / t_tiled, "x faster)")
