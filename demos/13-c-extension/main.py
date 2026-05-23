import fastmath

print(f"add(3, 4)          = {fastmath.add(3.0, 4.0)}")
print(f"clamp(15, 0, 10)   = {fastmath.clamp(15.0, 0.0, 10.0)}")
print(f"lerp(0, 100, 0.25) = {fastmath.lerp(0.0, 100.0, 0.25)}")
print(f"gcd(48, 18)        = {fastmath.gcd(48, 18)}")
print(f"ipow(2, 10)        = {fastmath.ipow(2, 10)}")
print(f"mean3(1,2,3)       = {fastmath.mean3(1.0, 2.0, 3.0):.4f}")
print(f"variance3(2,4,6)   = {fastmath.variance3(2.0, 4.0, 6.0):.4f}")
