"""
Polyglot showcase — runs each hello.* script via subprocess so the output
can be captured and displayed in a table. This is what `molt run all` calls.
To dispatch directly, use: molt run hello.rb  /  molt run hello.js  / etc.
"""
import subprocess, sys, shutil, os, time
from pathlib import Path

HERE = Path(__file__).parent

SCRIPTS = [
    ("Python",  "python",  None,              "hello.py"),
    ("Ruby",    "ruby",    "hello.rb",        "hello.rb"),
    ("Node.js", "node",    "hello.js",        "hello.js"),
    ("Lua",     "lua",     "hello.lua",       "hello.lua"),
    ("Perl",    "perl",    "hello.pl",        "hello.pl"),
    ("Bash",    "bash",    "hello.sh",        "hello.sh"),
    ("Go",      "go",      "hello.go",        "hello.go"),
    ("Swift",   "swift",   "hello.swift",     "hello.swift"),
    ("Julia",   "julia",   "hello.jl",        "hello.jl"),
    ("Elixir",  "elixir",  "hello.exs",       "hello.exs"),
]

# hello.py lives inline
HELLO_PY = HERE / "hello.py"
if not HELLO_PY.exists():
    HELLO_PY.write_text('import sys\nname = sys.argv[1] if len(sys.argv) > 1 else "World"\nprint(f"Hello from Python {sys.version.split()[0]}! 👋 {name}")\n')

filter_lang = sys.argv[1] if len(sys.argv) > 1 else None

WIDTH = 72
print("─" * WIDTH)
print(f"  {'Language':<10}  {'Status':<8}  Output")
print("─" * WIDTH)

for lang, binary, script_name, _ in SCRIPTS:
    if filter_lang and filter_lang.lower() not in lang.lower():
        continue

    script = HERE / (script_name or "hello.py")
    if not script.exists():
        print(f"  {lang:<10}  {'SKIP':<8}  (file not found)")
        continue

    if not shutil.which(binary if binary != "python" else sys.executable):
        if binary == "python":
            binary = sys.executable
        else:
            print(f"  {lang:<10}  {'SKIP':<8}  ({binary} not on PATH)")
            continue

    if binary == "python":
        cmd = [sys.executable, str(script), "molt"]
    elif binary == "go":
        cmd = ["go", "run", str(script), "molt"]
    else:
        cmd = [binary, str(script), "molt"]

    t0 = time.perf_counter()
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        elapsed = (time.perf_counter() - t0) * 1000
        out = (r.stdout or r.stderr or "").strip().split("\n")[0][:50]
        status = "OK" if r.returncode == 0 else "FAIL"
        print(f"  {lang:<10}  {status:<8}  {out}  ({elapsed:.0f}ms)")
    except subprocess.TimeoutExpired:
        print(f"  {lang:<10}  {'TIMEOUT':<8}")
    except Exception as e:
        print(f"  {lang:<10}  {'ERROR':<8}  {e}")

print("─" * WIDTH)
