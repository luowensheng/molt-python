# Mojo with molt

[Mojo](https://www.modular.com/mojo) is a high-performance systems language that compiles to native code and interoperates fully with Python packages. Since `pip install mojo` works (v1.0.0b1+), molt treats Mojo as a normal Python dependency — no separate toolchain installation needed.

---

## Quick start

```bash
# 1. Allow prerelease packages (required — Mojo is still in beta)
#    Add this to your pyproject.toml before running molt add:
#
#    [tool.uv]
#    prerelease = "allow"

# 2. Add Mojo as a dependency
molt add mojo

# 3. Sync — installs the compiler and wires MOJO_PYTHON_LIBRARY automatically
molt sync
```

```bash
# Run a Mojo script (auto-dispatched by extension)
molt run main.mojo

# Direct mojo passthrough — build, doc, package, etc.
molt mojo build fast_math.mojo --emit shared-lib -o fast_math.so
molt mojo --help
```

---

## What molt wires for you

When Mojo is present in a project's dependencies, `molt sync` automatically:

1. Locates the Mojo binary inside the installed wheel (`mojo-compiler` package)
2. Derives `libpython` for the project's Python version
3. Writes a `mojo` shim to the project's `bin/` directory — sets `MOJO_PYTHON_LIBRARY`, `MODULAR_*`, and `PYTHONPATH` correctly
4. Injects all variables into every `molt run` / `molt exec` subprocess

You never have to set `MOJO_PYTHON_LIBRARY` or `PYTHONPATH` manually.

---

## Running `.mojo` files

`molt run` dispatches on extension:

| Command | Result |
|---|---|
| `molt run main.py` | Python interpreter |
| `molt run main.mojo` | `mojo run main.mojo` via project shim |
| `molt run main.🔥` | same (Mojo emoji extension) |

---

## Building Python extensions

Mojo can compile to a Python `.so` extension:

```bash
molt mojo build fast_math.mojo --emit shared-lib -o fast_math.so
python -c "import fast_math; print(fast_math.factorial(10))"
```

See **demo 12** for a complete example with SIMD sigmoid, dot product, and a `RunningStats` struct exposed to Python.

---

## Python interop

All packages installed via `molt add` are automatically available inside Mojo:

```mojo
# main.mojo
from std.python import Python

def main() raises:
    var np = Python.import_module("numpy")
    var arr = np.array(Python.list(1.0, 2.0, 3.0))
    print("mean:", arr.mean())   # → 2.0
```

---

## Mojo 1.0b1 API notes

Several stdlib APIs changed in Mojo 1.0b1. Key differences from older tutorials:

| Old | New |
|---|---|
| `from sys import simdwidthof` | Not available — hardcode SIMD width (4×f32, 2×f64 on ARM) |
| `UnsafePointer[T].alloc(N)` | Use `List[T]` + `unsafe_ptr()` |
| `from time import now` | `from std.time import perf_counter_ns` (returns `UInt`) |
| `vectorize[fn, width](N)` | Manual `while i + W <= N` SIMD loop |
| `def method(mut self, args, kwargs)` in `def_method` | `@staticmethod def method(py_self, ...)` + `py_self.downcast_value_ptr[T]()` |

---

## Demos

Five progressive demos show Mojo integration in action:

| Demo | What it shows |
|---|---|
| [08-mojo-hello](../demos/08-mojo-hello/) | Basic Mojo syntax, `molt run main.mojo` dispatch |
| [09-mojo-numpy](../demos/09-mojo-numpy/) | Python/NumPy interop from Mojo |
| [10-mojo-simd](../demos/10-mojo-simd/) | SIMD types, vectorization, benchmarks (~3.4× speedup) |
| [11-mojo-matmul](../demos/11-mojo-matmul/) | Tiled + parallelized matmul (~79× speedup on N=128) |
| [12-mojo-extension](../demos/12-mojo-extension/) | Compiling Mojo to a Python `.so` extension |

Run any demo:

```bash
cd demos/10-mojo-simd
molt sync
molt run main.mojo
```
