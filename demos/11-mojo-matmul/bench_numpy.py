"""bench_numpy.py — numpy matmul baseline.

Run with: molt run bench-np
Compare against: molt run main.mojo (Mojo benchmark section)
"""

import time
import numpy as np

N = 128

A = np.random.rand(N, N).astype(np.float32)
B = np.random.rand(N, N).astype(np.float32)

# Warm-up
_ = A @ B

# Timed run (100 iterations)
REPS = 100
t0 = time.perf_counter()
for _ in range(REPS):
    C = A @ B
elapsed = (time.perf_counter() - t0) / REPS * 1000  # ms per call

print(f"numpy matmul ({N}x{N} float32): {elapsed:.3f} ms/call")
print(f"C[0,0] = {C[0,0]:.6f}")
print()
print("Compare with `molt run main.mojo` to see Mojo timings.")
