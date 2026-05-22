# 09-mojo-numpy / main.mojo
#
# Run with:  molt run main.mojo
#
# Demonstrates calling Python packages (numpy, pandas) from Mojo.
# Because molt sets PYTHONPATH to all managed packages, every package
# installed via `molt add` is automatically visible to Mojo's embedded CPython —
# no PYTHONPATH manual configuration needed.

from std.python import Python, PythonObject


def numpy_basics(np: PythonObject) raises:
    print("--- numpy basics ---")

    # Create arrays
    var arr = np.arange(12).reshape(3, 4)
    print("shape:", arr.shape)          # (3, 4)
    print("dtype:", arr.dtype)          # int64

    # Arithmetic — fully delegated to CPython/numpy
    var f = arr.astype(np.float64)
    var squared = f ** 2
    print("mean of squares:", squared.mean())

    # Create from Mojo list via Python.list()
    var mojo_data = Python.list(1.0, 4.0, 9.0, 16.0, 25.0)
    var vec = np.array(mojo_data)
    print("sqrt([1,4,9,16,25]):", np.sqrt(vec))   # [1. 2. 3. 4. 5.]

    # Linear algebra
    var A = np.array(Python.list(
        Python.list(2.0, 1.0),
        Python.list(5.0, 3.0),
    ))
    var b = np.array(Python.list(4.0, 7.0))
    var x = np.linalg.solve(A, b)
    print("solution to Ax=b:", x)    # [5. -6.]

    # Statistics
    var data = np.random.randn(1000)
    print("random sample — mean:", np.round(data.mean(), 3),
          "std:", np.round(data.std(), 3))


def pandas_basics(pd: PythonObject, np: PythonObject) raises:
    print("\n--- pandas basics ---")

    # Build a DataFrame from a numpy array
    var matrix = np.array(Python.list(
        Python.list(10, 20, 30),
        Python.list(40, 50, 60),
        Python.list(70, 80, 90),
    ))
    var columns = Python.list("A", "B", "C")
    var df = pd.DataFrame(matrix, columns=columns)
    print(df)
    print("column sums:\n", df.sum())

    # Simple time-series
    var dates = pd.date_range("2024-01-01", periods=5, freq="D")
    var values = np.array(Python.list(10.5, 11.2, 9.8, 12.1, 10.9))
    var series = pd.Series(values, index=dates, name="price")
    print("\nprice series:\n", series)
    print("rolling mean (3d):\n", series.rolling(3).mean())


def python_builtins_demo() raises:
    print("\n--- Python builtins from Mojo ---")

    var builtins = Python.import_module("builtins")

    # Build and manipulate a Python list
    var foods = Python.list("apple", "banana", "cherry")
    print("foods:", foods)
    foods.append("date")
    print("after append:", foods)
    print("sorted:", builtins.sorted(foods))
    print("type:", Python.type(foods))

    # Python dict
    var scores = Python.dict()
    scores["alice"] = 95
    scores["bob"] = 87
    scores["carol"] = 92
    print("scores:", scores)
    print("alice's score:", scores["alice"])

    # String formatting via Python
    var fmt = builtins.format
    print("formatted:", fmt(3.14159265, ".4f"))   # 3.1416


def local_python_module_demo() raises:
    print("\n--- local Python module ---")
    # Tell Mojo where to find local .py files.
    # molt already sets PYTHONPATH to all managed package dirs; adding "." covers
    # files in the project root.
    Python.add_to_path(".")
    var helper = Python.import_module("helpers")
    var result = helper.normalize_scores(Python.list(65, 78, 90, 55, 100))
    print("normalized scores:", result)


def main() raises:
    var np = Python.import_module("numpy")
    var pd = Python.import_module("pandas")

    numpy_basics(np)
    pandas_basics(pd, np)
    python_builtins_demo()
    local_python_module_demo()
