# 15-c-project

**What this shows:** molt as a complete C project manager. Two distinct capabilities:

1. **`molt run file.c`** — single-file compile-and-run, zero config. The built-in `.c`
   handler compiles with `zig cc` and executes in one command.
2. **Multi-file C project via tasks** — `pyproject.toml` defines `build`, `stats`, `demo`,
   and `clean` tasks so a multi-file C project is managed with the exact same workflow as
   any Python project.

Compare with **demo 13** (`13-c-extension`): that demo compiles C as a *Python extension
module* (callable from Python). This demo treats C as the *primary language* — molt just
manages the build.

---

## What's in this demo

```
15-c-project/
  hello.c        single-file C program (for molt run hello.c)
  pyproject.toml tasks: build, stats, demo, clean
  src/
    main.c       statistics CLI: argc-based, prints mean/std/min/max + histogram
    stats.c      statistics implementation (mean, std, min, max, sum)
    stats.h      header
  bin/           created by molt run build (gitignored)
  demo.py        Python script that drives the C binary with three datasets
```

---

## Running

```bash
cd demos/15-c-project

# ── Single-file C: compile and run in one command ──────────────────
molt run hello.c              # Hello from C! 👋 World
molt run hello.c Alice        # Hello from C! 👋 Alice

# ── Multi-file C project via tasks ────────────────────────────────
molt run build                # cc -O2 -Wall → bin/stats
molt run stats 88 92 71 95    # run the binary directly with args
molt run demo                 # Python drives C binary: 3 datasets + histograms
molt run clean                # rm bin/stats

# ── Run-handler inspect ───────────────────────────────────────────
molt run-handler show c       # Extension: .c  unix: sh -c "{zig} cc ..."
molt run-handler list         # all 27 built-in handlers
```

---

## How single-file C dispatch works

`molt run hello.c` hits the generic extension-handler path in `molt run`:

```
1. Sees file hello.c exists
2. Looks up ext "c" in ~/.molt/run-handlers.yaml
3. Resolves command template for current OS:
     Unix:    sh -c "{zig} cc -O2 -o {dir}/{basename} {file} && {dir}/{basename} {args}"
     Windows: cmd /c "{zig} cc -O2 -o {dir}\{basename}.exe {file} && {dir}\{basename}.exe {args}"
4. Substitutes tokens:
     {zig}      → /Users/.../.molt/toolchains/zig/0.14.1/zig
     {dir}      → /path/to/15-c-project
     {basename} → hello
     {file}     → /path/to/15-c-project/hello.c
     {args}     → Alice
5. exec: sh -c "/path/zig cc -O2 -o /proj/hello /proj/hello.c && /proj/hello Alice"
```

- `zig cc` is a **drop-in replacement for clang/gcc** — cross-platform, no toolchain install.
- The project's full molt environment (`PYTHONPATH`, `.molt/bin/` on `PATH`) is applied
  before exec, so any managed Python packages are available to child processes.

---

## How multi-file project tasks work

```toml
# pyproject.toml
[tool.molt.tasks]
build = "cc -O2 -Wall -o bin/stats src/main.c src/stats.c -lm && echo '✓ compiled bin/stats'"
stats = "bin/stats"
demo  = "python demo.py"
clean = "rm -f bin/stats bin/stats.exe"
```

Tasks are executed under the project's molt environment — no shell activation needed,
no `source` required. Swap `cc` for `zig cc` for guaranteed cross-platform compilation.

---

## Python orchestrating C

`demo.py` shows Python driving the compiled C binary via `subprocess`:

```python
import subprocess
from pathlib import Path

BIN = Path(__file__).parent / "bin" / "stats"

result = subprocess.run(
    [str(BIN), "88", "92", "71", "95", "84"],
    capture_output=True, text=True
)
print(result.stdout)
```

The Python code and the C binary live in the same molt project. Both are managed,
built, and run with `molt run <task>` — no separate toolchain, no separate shell setup.

---

## Customise the C run-handler

```bash
# Use system clang instead of zig cc
molt run-handler add c "sh -c \"clang -O2 -o {dir}/{basename} {file} && {dir}/{basename} {args}\""

# Add C++ support (already built-in as .cpp, but you can override)
molt run-handler add cpp "sh -c \"{zig} c++ -O2 -std=c++17 -o {dir}/{basename} {file} && {dir}/{basename} {args}\""

# Restore defaults
molt run-handler reset
```

---

## Tested output

```
── Single-file run ───────────────────────────────────────────────
  molt run hello.c          →  Hello from C! 👋 World
  molt run hello.c molt     →  Hello from C! 👋 molt

── Multi-file build + run ────────────────────────────────────────
  molt run build            →  ✓ compiled bin/stats
  molt run demo             →  3 datasets × mean/std/min/max/histogram
─────────────────────────────────────────────────────────────────
```
