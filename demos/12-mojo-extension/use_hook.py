"""use_hook.py — use the mojo.importer hook (no manual build step).

The hook compiles fast_math.mojo on first import and caches the .so in
__mojocache__/. Subsequent imports skip the build.

Run:  molt run hook
"""

import mojo.importer    # installs the .mojo import hook
import fast_math        # compiled from fast_math.mojo on first import

print("factorial(10) =", fast_math.factorial(10))
print("dot([1,2,3],[4,5,6]) =", fast_math.dot([1.0, 2.0, 3.0], [4.0, 5.0, 6.0]))

values = list(range(-5, 6))
print("sigmoid([-5..5]) =", [round(x, 3) for x in fast_math.sigmoid(values)])
