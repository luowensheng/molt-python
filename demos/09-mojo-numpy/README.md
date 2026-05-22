# 09-mojo-numpy

Mojo calling Python packages (numpy, pandas) through its embedded CPython bridge.
Because molt sets `PYTHONPATH` to all managed packages at sync time, **every package
installed via `molt add` is automatically visible to Mojo** — no extra configuration.

## What this demo shows

- `Python.import_module("numpy")` imports any installed Python package into Mojo
- `PythonObject` wraps any Python value — arrays, DataFrames, dicts, functions
- `Python.list(...)` and `Python.dict()` construct Python builtins from Mojo
- `Python.add_to_path(".")` makes local `.py` helper files importable from Mojo
- numpy, pandas, and stdlib all work unchanged — 100% CPython compatibility
- molt's `PYTHONPATH` injection makes this zero-config

## Project layout

```
09-mojo-numpy/
  main.mojo        full interop demo (numpy + pandas + builtins + local module)
  helpers.py       local Python helper module imported from Mojo
  pyproject.toml
  README.md
```

## Running

```bash
molt sync          # installs mojo, numpy, pandas

molt run main.mojo
# or equivalently:
molt mojo run main.mojo
```

## Key interop patterns

```mojo
from std.python import Python, PythonObject

def main() raises:
    # Import any installed Python package
    var np = Python.import_module("numpy")

    # PythonObject wraps any Python value — fully duck-typed
    var arr = np.arange(12).reshape(3, 4)
    print(arr.shape)   # (3, 4)

    # Construct Python builtins from Mojo values
    var py_list = Python.list(1.0, 4.0, 9.0)
    var vec = np.array(py_list)
    print(np.sqrt(vec))   # [1. 2. 3.]

    # Import a local .py file
    Python.add_to_path(".")
    var helper = Python.import_module("helpers")
    print(helper.normalize_scores(Python.list(0, 50, 100)))
```

## Why PYTHONPATH is automatic

When you run `molt sync`, molt:
1. Installs all dependencies into the global store at `~/.molt/pkg/`
2. Writes `PYTHONPATH=<store-dirs>` into the project's `syspath.json`
3. When `mojo` runs, it loads `libpython`, which reads `PYTHONPATH` and finds all packages

No `.venv` activation, no `sys.path` manipulation in your code.

## Mojo ↔ Python bridge caveats

| Limitation | Workaround |
|---|---|
| `import_module()` must be inside a `def` (not file-level) | Wrap in `def main() raises` |
| No `from numpy import array` at Mojo top level | Import the full module, use `np.array(...)` |
| All Python calls hold the GIL | Keep Python-heavy code on the Python side |
| `Python.add_to_path(".")` needed for local `.py` files | Call once in `main()` |
