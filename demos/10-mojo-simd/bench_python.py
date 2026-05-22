"""bench_python.py — pure Python and numpy sigmoid baselines.

Run with: molt run bench-py
Compare against: molt run main.mojo (SIMD section)
"""

import time
import math
import numpy as np

N = 1_000_000
data = [i / N * 10.0 - 5.0 for i in range(N)]
data_np = np.array(data, dtype=np.float32)


def sigmoid(x: float) -> float:
    return 1.0 / (1.0 + math.exp(-x))


# --- pure Python loop ---
t0 = time.perf_counter()
total = sum(sigmoid(x) for x in data)
elapsed_py = time.perf_counter() - t0
print(f"pure Python:  {elapsed_py*1000:.1f} ms  (total={total:.2f})")

# --- numpy vectorized ---
t0 = time.perf_counter()
total_np = float((1.0 / (1.0 + np.exp(-data_np))).sum())
elapsed_np = time.perf_counter() - t0
print(f"numpy:        {elapsed_np*1000:.1f} ms  (total={total_np:.2f})")

print()
print("Compare with `molt run main.mojo` to see Mojo SIMD timing.")
