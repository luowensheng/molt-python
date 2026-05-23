"""
demo.py — Python driving the C stats binary.

Shows that molt projects can mix Python orchestration with C binaries
cleanly: the same project manages both the C build and the Python
scripts that call it. Run with: molt run demo
"""
import subprocess, sys, os
from pathlib import Path

HERE = Path(__file__).parent
BIN  = HERE / "bin" / "stats"

def ensure_built():
    if BIN.exists():
        return
    print("Building stats binary first...")
    cc = os.environ.get("CC", "cc")
    result = subprocess.run(
        [cc, "-O2", "-Wall",
         "-o", str(BIN),
         str(HERE / "src" / "main.c"),
         str(HERE / "src" / "stats.c"),
         "-lm"],
        capture_output=True, text=True
    )
    if result.returncode != 0:
        print("Build failed:", result.stderr)
        sys.exit(1)
    print("✓ compiled bin/stats\n")

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

run_stats("API latency samples (ms)",
          [12.3, 8.7, 15.1, 9.4, 11.2, 7.8, 13.5, 21.0, 6.9, 10.1, 8.3, 14.7])

run_stats("first 15 primes",
          [2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47])
