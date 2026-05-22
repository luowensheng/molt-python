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

from math import exp, sqrt, log
from memory import UnsafePointer
from algorithm import vectorize
from sys.info import simdwidthof
from std.os import abort
from std.python import PythonObject
from std.python.bindings import PythonModuleBuilder
from time import perf_counter


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
    alias dtype  = DType.float32
    alias simd_w = simdwidthof[dtype]()

    var n = Int(py=py_values.__len__())
    var buf = UnsafePointer[dtype].alloc(n)

    # Copy Python list into native buffer
    for i in range(n):
        buf[i] = Float32(py_values[i].__float__())

    # SIMD sigmoid
    var i = 0
    while i + simd_w <= n:
        var x = buf.load[width=simd_w](i)
        buf.store(i, 1.0 / (1.0 + exp(-x)))
        i += simd_w
    # scalar tail
    while i < n:
        buf[i] = 1.0 / (1.0 + exp(-buf[i]))
        i += 1

    # Pack results back into a Python list
    from std.python import Python
    var out = Python.list()
    for i in range(n):
        out.append(Float64(buf[i]))
    buf.free()
    return out


# ── exported function 3: dot product ─────────────────────────────────────────

def dot_product(py_a: PythonObject, py_b: PythonObject) raises -> PythonObject:
    """
    Compute the dot product of two Python lists of equal length.
    Returns a float.
    """
    alias dtype  = DType.float64
    alias simd_w = simdwidthof[dtype]()

    var n = Int(py=py_a.__len__())
    if n != Int(py=py_b.__len__()):
        raise Error("dot_product: lists must have equal length")

    var a = UnsafePointer[dtype].alloc(n)
    var b = UnsafePointer[dtype].alloc(n)
    for i in range(n):
        a[i] = Float64(py_a[i].__float__())
        b[i] = Float64(py_b[i].__float__())

    var acc = SIMD[dtype, simd_w](0.0)
    var i = 0
    while i + simd_w <= n:
        acc += a.load[width=simd_w](i) * b.load[width=simd_w](i)
        i += simd_w
    var tail: Float64 = 0.0
    while i < n:
        tail += a[i] * b[i]
        i += 1

    a.free()
    b.free()
    return PythonObject(acc.reduce_add() + tail)


# ── exported struct: RunningStats ─────────────────────────────────────────────
# Tracks count/mean/M2 (Welford's online algorithm) — useful for streaming data.

struct RunningStats(Movable):
    var count: Int
    var mean: Float64
    var m2: Float64   # running sum of squared deviations

    def __init__(out self):
        self.count = 0
        self.mean  = 0.0
        self.m2    = 0.0

    @staticmethod
    def py_init(out self: RunningStats,
                args: PythonObject, kwargs: PythonObject) raises:
        self = Self()   # zero-init; no constructor args

    def py_update(self: Reference[Self, _],
                  args: PythonObject, kwargs: PythonObject) raises -> PythonObject:
        var x = Float64(args[0].__float__())
        self[].count += 1
        var delta  = x - self[].mean
        self[].mean += delta / Float64(self[].count)
        var delta2 = x - self[].mean
        self[].m2  += delta * delta2
        from std.python import Python
        return Python.none()

    def py_variance(self: Reference[Self, _],
                    args: PythonObject, kwargs: PythonObject) raises -> PythonObject:
        if self[].count < 2:
            return PythonObject(0.0)
        return PythonObject(self[].m2 / Float64(self[].count - 1))

    def py_mean(self: Reference[Self, _],
                args: PythonObject, kwargs: PythonObject) raises -> PythonObject:
        return PythonObject(self[].mean)

    def py_count(self: Reference[Self, _],
                 args: PythonObject, kwargs: PythonObject) raises -> PythonObject:
        return PythonObject(self[].count)


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
