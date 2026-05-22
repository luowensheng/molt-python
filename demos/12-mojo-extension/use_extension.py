"""use_extension.py — use the pre-compiled fast_math.so from Python.

Build first:
    molt run build     # molt mojo build fast_math.mojo --emit shared-lib -o fast_math.so
Then run:
    molt run           # python use_extension.py
"""

import fast_math   # works like any C extension once built

# ── factorial ────────────────────────────────────────────────────────────────
print("=== factorial ===")
for n in [0, 1, 5, 10, 20]:
    print(f"  {n}! = {fast_math.factorial(n)}")

# ── SIMD sigmoid ─────────────────────────────────────────────────────────────
print("\n=== SIMD sigmoid ===")
import math

values = [-5.0, -2.0, -1.0, 0.0, 1.0, 2.0, 5.0]
mojo_out = fast_math.sigmoid(values)
py_out   = [1 / (1 + math.exp(-x)) for x in values]

for x, m, p in zip(values, mojo_out, py_out):
    print(f"  sigmoid({x:5.1f}) = {m:.6f}  (python: {p:.6f})")

# ── dot product ──────────────────────────────────────────────────────────────
print("\n=== dot product ===")
a = list(range(8))         # [0, 1, 2, ..., 7]
b = list(range(8))
expected = sum(x * y for x, y in zip(a, b))   # 0+1+4+9+16+25+36+49 = 140
result = fast_math.dot(a, b)
print(f"  dot([0..7], [0..7]) = {result}  (expected {expected})")

# ── RunningStats struct ───────────────────────────────────────────────────────
print("\n=== RunningStats (Welford online algorithm) ===")
stats = fast_math.RunningStats()
data = [2.0, 4.0, 4.0, 4.0, 5.0, 5.0, 7.0, 9.0]
for x in data:
    stats.update(x)

print(f"  n        = {stats.count()}")
print(f"  mean     = {stats.mean():.4f}  (expected 5.0)")
print(f"  variance = {stats.variance():.4f}  (sample variance, Bessel-corrected)")
