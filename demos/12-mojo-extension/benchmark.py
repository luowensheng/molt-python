"""benchmark.py — compare Mojo extension vs pure Python vs numpy sigmoid.

Run:  molt run bench
(Requires fast_math.so to be built first: molt run build)
"""

import time
import math
import numpy as np
import fast_math

N = 1_000_000
data = [i / N * 10.0 - 5.0 for i in range(N)]
data_np = np.array(data, dtype=np.float32)

REPS = 5

def timeit(label, fn):
    # warm-up
    fn()
    t0 = time.perf_counter()
    for _ in range(REPS):
        fn()
    ms = (time.perf_counter() - t0) / REPS * 1000
    print(f"  {label:<30} {ms:8.1f} ms/call")

print(f"sigmoid(N={N:,})  ({REPS} reps each)\n")

timeit("pure Python",            lambda: [1/(1+math.exp(-x)) for x in data])
timeit("numpy",                  lambda: (1 / (1 + np.exp(-data_np))).tolist())
timeit("Mojo SIMD extension",    lambda: fast_math.sigmoid(data))

print()
print("Note: Mojo extension includes Python↔Mojo list conversion overhead.")
print("For maximum speed, keep data on the Mojo side (see 10-mojo-simd).")
