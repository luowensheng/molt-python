# 12-mojo-extension

Compile a Mojo source file to a Python-importable `.so` extension module — the
same kind of artifact Cython produces. Once built, `import fast_math` works in any
Python file with no special setup.

## What this demo shows

- `mojo build --emit shared-lib` produces a standard CPython extension (`.so`)
- `PythonModuleBuilder` registers Mojo functions and structs for Python
- `@export def PyInit_<name>()` is the C extension entry point Python looks for
- Converting between Mojo and Python types: `Int(py=obj)`, `Float64(...)`, `PythonObject(...)`
- Exporting a Mojo struct (`RunningStats`) as a Python class with methods
- Two usage patterns: pre-compiled `.so` and `mojo.importer` auto-compile hook
- `molt run build` uses `molt mojo build` as a build step in a task

## Project layout

```
12-mojo-extension/
  fast_math.mojo       Mojo source: factorial, sigmoid, dot, RunningStats
  use_extension.py     Python caller — uses pre-built fast_math.so
  use_hook.py          Python caller — uses mojo.importer (no manual build)
  benchmark.py         Python vs numpy vs Mojo extension speed comparison
  pyproject.toml
  README.md
```

## Running

```bash
molt sync

# Option A: pre-compile to .so, then run Python
molt run build         # → fast_math.so
molt run               # → python use_extension.py

# Option B: let mojo.importer compile on first import (dev workflow)
molt run hook          # compiles + runs, caches in __mojocache__/

# Benchmark
molt run build
molt run bench
```

## The Mojo side

```mojo
from std.python import PythonObject
from std.python.bindings import PythonModuleBuilder
from std.os import abort

def factorial(py_n: PythonObject) raises -> PythonObject:
    var n = Int(py=py_n)          # convert Python int → Mojo Int
    var result = 1
    for i in range(2, n + 1):
        result *= i
    return PythonObject(result)   # convert Mojo Int → Python int

@export
def PyInit_fast_math() -> PythonObject:
    try:
        var m = PythonModuleBuilder("fast_math")
        m.def_function[factorial]("factorial", docstring="Compute n!")
        return m.finalize()
    except e:
        abort(String("error: ", e))
```

## The Python side

```python
import fast_math          # works after `mojo build --emit shared-lib`

print(fast_math.factorial(10))              # 3628800
print(fast_math.dot([1,2,3], [4,5,6]))     # 32.0

stats = fast_math.RunningStats()
for x in [2, 4, 4, 4, 5, 5, 7, 9]:
    stats.update(x)
print(stats.mean(), stats.variance())       # 5.0  4.571...
```

## PythonModuleBuilder API limits (v1.0.0b1)

| Limitation | Details |
|---|---|
| Max 6 args per exported function | Use `*args` / `OwnedKwargsDict` as workaround |
| No keyword-only arguments | Pass as positional or via kwargs dict |
| No computed properties on types | Use explicit getter methods |
| Import hook (`mojo.importer`) | Does not support `-I` for extra Mojo package paths |
| API stability | `PythonModuleBuilder` is marked unstable; will change before 1.0 GA |

## How this fits into molt's native module system

`molt sync` detects `.mojo` files in your project the same way it detects `.pyx`
(Cython) and `.rs` (Rust/PyO3) files, and compiles them to `.so` artefacts stored
in `~/.molt/native/`. The compiled `.so` is then symlinked into the project state
dir so it appears on `PYTHONPATH` and is importable immediately after `molt sync`.

This demo shows the manual compilation path (`molt mojo build`); the automated
path via `molt sync` will be supported in a future molt release.
