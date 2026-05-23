import time
import fastmath

N = 5_000_000

# Python loop: pure Python lerp approximation
t0 = time.perf_counter()
for i in range(N):
    _ = i / N  # pure python lerp approx
python_ms = (time.perf_counter() - t0) * 1000

# C extension: same via fastmath.lerp
t0 = time.perf_counter()
for i in range(N):
    _ = fastmath.lerp(0.0, 1.0, i / N)
c_ms = (time.perf_counter() - t0) * 1000

print(f"Python  : {python_ms:7.1f} ms")
print(f"C ext   : {c_ms:7.1f} ms")
print(f"Ratio   : {c_ms/python_ms:.2f}x (call overhead dominates at this grain)")
