"""
demo.py — Python driving the Zig stats binary.
Zig binary compiled by molt's auto-installed zig toolchain.
Run with: molt run demo
"""
import subprocess, sys, os
from pathlib import Path

HERE = Path(__file__).parent
BIN  = HERE / "bin" / "stats-zig"

def ensure_built():
    if BIN.exists():
        return
    print("Building stats-zig binary first (zig build-exe)...")
    zig = os.environ.get("ZIG", "zig")
    result = subprocess.run(
        [zig, "build-exe",
         str(HERE / "src" / "main.zig"),
         f"-femit-bin={HERE / 'bin' / 'stats-zig'}"],
        capture_output=True, text=True, cwd=str(HERE)
    )
    if result.returncode != 0:
        print("Build failed:\n", result.stderr)
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

run_stats("powers of 2 (1–1024)",
          [1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024])
