"""
demo.py — Python driving the C++ stats binary.
Same pattern as demo 15 but with C++: Python orchestrates a C++ binary
living in the same molt project. Run with: molt run demo
"""
import subprocess, sys, os
from pathlib import Path

HERE = Path(__file__).parent
BIN  = HERE / "bin" / "stats-cpp"

def ensure_built():
    if BIN.exists():
        return
    print("Building stats-cpp binary first...")
    zig = os.environ.get("ZIG", "zig")
    result = subprocess.run(
        [zig, "c++", "-O2", "-std=c++17",
         "-o", str(BIN),
         str(HERE / "src" / "main.cpp")],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        # Fall back to system c++ compiler
        result = subprocess.run(
            ["c++", "-O2", "-std=c++17",
             "-o", str(BIN),
             str(HERE / "src" / "main.cpp")],
            capture_output=True, text=True
        )
        if result.returncode != 0:
            print("Build failed:", result.stderr)
            sys.exit(1)
    print(f"✓ compiled {BIN.name}\n")

def run_stats(label, data):
    result = subprocess.run(
        [str(BIN)] + [str(x) for x in data],
        capture_output=True, text=True
    )
    print(f"── {label} {'─' * (40 - len(label))}")
    print(result.stdout.rstrip())
    print()

ensure_built()

run_stats("exam scores (out of 100)",
          [88, 92, 71, 95, 84, 76, 90, 63, 85, 78, 93, 67])

run_stats("API latency (ms)",
          [12.3, 8.7, 15.1, 9.4, 11.2, 7.8, 13.5, 21.0, 6.9, 10.1])

run_stats("Fibonacci numbers (1–20)",
          [1, 1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233, 377, 610])
