# 12-mojo-extension / fast_math.mojo
#
# Build as Python extension:
#   molt mojo build fast_math.mojo --emit shared-lib -o fast_math.so
#   python use_extension.py
#
# Or use the import hook (no manual build):
#   python use_hook.py
#
# Exports three functions and one struct to Python via PythonModuleBuilder.
# Once compiled, `import fast_math` works like any C extension module.
#
# Mojo 1.0b1 notes:
#   • def_method requires @staticmethod methods that take `py_self: PythonObject`
#     as the first argument; use py_self.downcast_value_ptr[T]() to access fields.
#   • Float64(py=obj) converts a PythonObject to Float64.
#   • UnsafePointer[T].alloc(N) is gone; use List[T] + unsafe_ptr() instead.
#   • simdwidthof is unavailable; SIMD widths are hardcoded (4×f32, 2×f64 on NEON).

from std.math import exp, sqrt
from std.collections import List
from std.os import abort
from std.python import Python, PythonObject
from std.python.bindings import PythonModuleBuilder

# SIMD widths for Apple Silicon (128-bit NEON).
# On x86 with AVX2: float32→8, float64→4.
comptime SIMD_F32 = 4   # 128-bit / 32-bit
comptime SIMD_F64 = 2   # 128-bit / 64-bit


# ── exported function 1: factorial ───────────────────────────────────────────

def factorial(py_n: PythonObject) raises -> PythonObject:
    """Compute n! (integer). Raises ValueError for n < 0."""
    var n = Int(py=py_n)
    if n < 0:
        raise Error("factorial of negative number")
    var result = 1
    for i in range(2, n + 1):
        result *= i
    return PythonObject(result)


# ── exported function 2: SIMD sigmoid over a Python list ────────────────────

def fast_sigmoid(py_values: PythonObject) raises -> PythonObject:
    """
    Compute sigmoid(x) = 1/(1+exp(-x)) for every element in a Python list.
    Returns a new Python list of floats. Uses Mojo SIMD internally.
    """
    var n = Int(py=py_values.__len__())

    # Copy Python list into native buffer via List[Float32]
    var buf = List[Float32](capacity=n)
    for i in range(n):
        buf.append(Float32(Float64(py=py_values[i])))

    var ptr = buf.unsafe_ptr()

    # SIMD sigmoid
    var i = 0
    while i + SIMD_F32 <= n:
        var x = ptr.load[width=SIMD_F32](i)
        ptr.store[width=SIMD_F32](i, 1.0 / (1.0 + exp(-x)))
        i += SIMD_F32
    # scalar tail
    while i < n:
        ptr[i] = 1.0 / (1.0 + exp(-ptr[i]))
        i += 1

    # Pack results back into a Python list
    var out = Python.list()
    for j in range(n):
        out.append(Float64(ptr[j]))
    return out


# ── exported function 3: dot product ─────────────────────────────────────────

def dot_product(py_a: PythonObject, py_b: PythonObject) raises -> PythonObject:
    """
    Compute the dot product of two Python lists of equal length.
    Returns a float.
    """
    var n = Int(py=py_a.__len__())
    if n != Int(py=py_b.__len__()):
        raise Error("dot_product: lists must have equal length")

    var a = List[Float64](capacity=n)
    var b = List[Float64](capacity=n)
    for i in range(n):
        a.append(Float64(py=py_a[i]))
        b.append(Float64(py=py_b[i]))

    var pa = a.unsafe_ptr()
    var pb = b.unsafe_ptr()

    var acc = SIMD[DType.float64, SIMD_F64](0.0)
    var i = 0
    while i + SIMD_F64 <= n:
        acc += pa.load[width=SIMD_F64](i) * pb.load[width=SIMD_F64](i)
        i += SIMD_F64
    var tail: Float64 = 0.0
    while i < n:
        tail += pa[i] * pb[i]
        i += 1

    return PythonObject(acc.reduce_add() + tail)


# ── exported struct: RunningStats ─────────────────────────────────────────────
# Tracks count/mean/M2 (Welford's online algorithm) — useful for streaming data.
#
# Mojo 1.0b1: def_method requires @staticmethod methods with the signature:
#   (py_self: PythonObject, args: PythonObject, kwargs: PythonObject)
# Use py_self.downcast_value_ptr[T]() to access (and mutate) the struct.
# The struct must also implement Writable for Python repr() to work.

struct RunningStats(Movable, Writable):
    var count: Int
    var mean: Float64
    var m2: Float64   # running sum of squared deviations

    def __init__(out self):
        self.count = 0
        self.mean  = 0.0
        self.m2    = 0.0

    def write_to[W: Writer](self, mut writer: W):
        writer.write("RunningStats(n=", self.count,
                     ", mean=", self.mean, ")")

    @staticmethod
    def py_init(out self: RunningStats,
                args: PythonObject, kwargs: PythonObject) raises:
        self = Self()

    @staticmethod
    def py_update(py_self: PythonObject, x_obj: PythonObject) raises -> PythonObject:
        """Welford online update: add one value x."""
        var x = Float64(py=x_obj)
        var p = py_self.downcast_value_ptr[RunningStats]()
        p[].count += 1
        var delta  = x - p[].mean
        p[].mean += delta / Float64(p[].count)
        var delta2 = x - p[].mean
        p[].m2  += delta * delta2
        return Python.none()

    @staticmethod
    def py_variance(py_self: PythonObject) raises -> PythonObject:
        """Sample variance (Bessel's correction, n-1)."""
        var p = py_self.downcast_value_ptr[RunningStats]()
        if p[].count < 2:
            return PythonObject(0.0)
        return PythonObject(p[].m2 / Float64(p[].count - 1))

    @staticmethod
    def py_mean(py_self: PythonObject) raises -> PythonObject:
        """Current running mean."""
        return PythonObject(py_self.downcast_value_ptr[RunningStats]()[].mean)

    @staticmethod
    def py_count(py_self: PythonObject) raises -> PythonObject:
        """Number of values seen so far."""
        return PythonObject(py_self.downcast_value_ptr[RunningStats]()[].count)


# ── module entry point ────────────────────────────────────────────────────────

@export
def PyInit_fast_math() -> PythonObject:
    try:
        var m = PythonModuleBuilder("fast_math")

        m.def_function[factorial]("factorial",
            docstring="Compute n! for non-negative integer n")
        m.def_function[fast_sigmoid]("sigmoid",
            docstring="SIMD sigmoid over a Python list of floats")
        m.def_function[dot_product]("dot",
            docstring="Dot product of two equal-length Python lists")

        _ = (m.add_type[RunningStats]("RunningStats")
               .def_py_init[RunningStats.py_init]()
               .def_method[RunningStats.py_update]("update")
               .def_method[RunningStats.py_variance]("variance")
               .def_method[RunningStats.py_mean]("mean")
               .def_method[RunningStats.py_count]("count"))

        return m.finalize()
    except e:
        abort(String("failed to create fast_math module: ", e))
