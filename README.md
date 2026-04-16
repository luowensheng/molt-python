# molt

**The hermetic Python project toolchain.** One tool for the entire Python project lifecycle — from scaffolding to reproducible builds to dependency forensics.

```
molt new project billing --type api   # scaffold a production-ready project in seconds
molt deps tree                        # see every dep, every .so, every system lib — hashed
molt hash lock                        # cryptographic proof of your entire build surface
molt build                            # ship a self-contained binary
molt_INSTALL_BASE=/opt ./billing-v1.0.0 install
```

---

## Why molt

Python packaging is **fragmented**. You need `pyenv` for Python versions, `uv` or `pip` for packages, `cookiecutter` for templates, `pip-audit` for CVEs, `pipdeptree` for dep trees, `pytest` + `coverage` for tests, `make` for task running, and a hand-rolled CI matrix for cross-platform builds. None of these tools talk to each other.

molt unifies all of it under one mental model, one config file (`pyproject.toml`), and one lock file (`.molt-deps.lock`) that covers your entire runtime surface — not just package versions, but Python itself, every native extension `.so`, and every system library they link against.

---

## Install

```bash
# From source (requires Go 1.22+)
git clone https://github.com/yourorg/molt
cd molt
go build -o molt .
mv molt /usr/local/bin/

# Verify
molt version
```

---

## Quick Start

```bash
# Create a new project
molt new project myapp --type cli

# Enter it and set up the environment
cd myapp
molt sync              # creates .venv and installs deps

# Run tasks
molt run dev           # python -m myapp
molt run test          # pytest tests/ -v
molt run lint          # ruff check .

# Understand what you depend on
molt deps tree         # full tree: packages → .so files → system libs
molt deps conflicts    # detect version conflicts before they bite
molt imports missing   # find imports not declared in pyproject.toml

# Lock everything for reproducibility
molt hash lock         # hash every file at every layer

# Build and ship
molt build             # self-contained binary for current platform
molt_INSTALL_BASE=/opt ./myapp-v0.1.0 install
```

---

## What It Does

| Area | Commands |
|------|----------|
| **Project scaffolding** | `new project`, `new package`, `new module`, `new cli`, `new model`, ... |
| **Python version management** | `python list`, `python install`, `python use`, `python remove` |
| **Dependency management** | `add`, `remove`, `sync`, `lock` (wraps uv) |
| **Dependency analysis** | `deps tree`, `deps conflicts`, `deps why`, `deps pinned-by`, `deps minimal` |
| **Import analysis** | `imports graph`, `imports unused`, `imports missing`, `imports cycles` |
| **Environment** | `env validate`, `env snapshot`, `env restore`, `env reset` |
| **Reproducibility** | `hash lock`, `hash verify`, `hash diff` |
| **Templates** | `create --from-template`, `template list/show/add/export/new` |
| **Task runner** | `run <task>`, `task list/add/remove` |
| **Distribution** | `build`, `capture`, `assemble`, `install` |
| **Verification** | `check`, `verify`, `sbom`, `doctor` |

---

## Project Layout

Every project created with molt follows the same structure:

```
myapp/
├── pyproject.toml          ← single source of truth (deps, tasks, config)
├── .python-version         ← exact pinned Python version
├── .molt-deps.lock       ← full hash manifest (all layers)
├── uv.lock                 ← package lockfile
├── .env.example            ← committed env var template
├── .env                    ← local overrides (gitignored)
│
├── myapp/
│   ├── __init__.py         ← version exposed here
│   ├── __main__.py         ← python -m myapp always works
│   ├── main.py             ← entry point
│   ├── config.py           ← typed config + .env loading
│   ├── logging.py          ← structured logging setup
│   └── exceptions.py       ← exception hierarchy
│
├── tests/
│   ├── conftest.py         ← shared fixtures
│   └── test_main.py
│
└── scripts/
    └── seed.py             ← molt run seed
```

---

## Configuration

All molt config lives in `pyproject.toml`:

```toml
[project]
name = "myapp"
version = "1.0.0"
requires-python = ">=3.12"
dependencies = [
    "fastapi>=0.100",
    "uvicorn[standard]>=0.23",
    "python-dotenv>=1.0",
]

[tool.molt.tasks]
dev      = "python -m myapp"
test     = "pytest tests/ -v --tb=short --cov"
lint     = "ruff check ."
format   = "ruff format ."
typecheck = "mypy myapp/"
seed     = "python scripts/seed.py"
migrate  = "alembic upgrade head"
```

---

## Documentation

See [USAGE.md](USAGE.md) for comprehensive examples covering every command.

---

## License

MIT
