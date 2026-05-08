# 01-hello-app

The simplest possible molt project. No dependencies, no build config — just a
`main.py` and tasks defined in `pyproject.toml`.

## What this demo shows

- `molt init --template app` scaffolds a project in seconds
- `[tool.molt.tasks]` replaces shell scripts / Makefiles for common dev commands
- `molt run` executes under the correct Python with no `.venv` activation
- Second and subsequent runs skip the sync entirely — sub-millisecond startup

## Project layout

```
01-hello-app/
  main.py            application entry point
  pyproject.toml     project metadata + molt tasks
  uv.lock            pinned dependency graph (just Python itself here)
  .python-version    pinned interpreter version
```

## Running

```bash
# Run with default greeting
molt run run

# Run with a custom name
molt run greet           # → Hello, Claude!

# Print Python + platform info
molt run info

# List all available tasks
molt task list
```

## Tasks defined

| Task | Command |
|---|---|
| `run` | `python main.py` |
| `greet` | `python main.py Claude` |
| `info` | `python main.py` |

## Key things to notice

**No `.venv`.** After `molt run` there is no `.venv/` directory anywhere in this
project. Run `ls -la` to confirm. The interpreter is resolved from `~/.molt/` and
`PYTHONPATH` is built at exec time.

**Instant second run.** The first invocation prints `→ first run, syncing project…`.
Every subsequent `molt run` skips straight to execution because all store entries
already have `.ok` markers.

**Pinned Python.** The `.python-version` file records the exact interpreter. Run
`molt python which` to see the full path. Change it with `molt python use 3.12`.

## What's in pyproject.toml

```toml
[tool.molt.tasks]
run   = { script = "main.py" }
greet = "python main.py Claude"
info  = "python main.py"
```

Tasks are plain strings (shell commands) or `{ script = "..." }` shorthand. They
run under the project's pinned interpreter and store `PYTHONPATH` — no activation
needed.
