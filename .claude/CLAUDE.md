# molt — Claude Code integration

This project uses **molt** as its Python toolchain (not pip / poetry / venv directly).

## Key commands

| What you want | Command |
|---|---|
| Add a dependency | `molt add numpy` |
| Remove a dependency | `molt remove numpy` |
| Install everything from lockfile | `molt sync` |
| Run a named task | `molt run <task>` (tasks defined in `pyproject.toml`) |
| Run a Python script | `molt run main.py` |
| Run a Mojo script | `molt run main.mojo` (if mojo is in deps) |
| Run arbitrary command in project env | `molt exec python -c "import sys; print(sys.path)"` |
| Build a self-contained binary | `molt build` |
| Show project summary | `molt info` |
| List Python versions | `molt python list` |

## Project configuration

Dependencies and tasks live in `pyproject.toml`:

```toml
[project]
name = "my-app"
dependencies = ["requests", "numpy"]

[tool.molt.tasks]
dev  = "python -m uvicorn app:app --reload"
test = "python -m pytest"
lint = "ruff check ."
```

## MCP server

`molt mcp` starts a stdio MCP server. Copy `.claude/mcp.json` from the molt
repo into any molt-managed project directory, then open that project in Claude Code —
the `molt_*` tools will appear in the tool list, letting you add packages, run tasks,
and build binaries without leaving the chat.

See [docs/mcp-server.md](../docs/mcp-server.md) for full configuration details.

## Where things live

```
~/.molt/pkg/           # global package store (shared across all projects)
~/.molt/projects/<id>/ # per-project state: Python shim, syspath.json, bin/
~/.molt/bin/           # global CLI tools (molt tool install)
~/.molt/env.yaml       # global environment variables
```

## Troubleshooting

- **"project not synced"** → run `molt sync`
- **wrong Python version** → run `molt python use 3.12`
- **can't find a package** → run `molt add <package>` then `molt sync`
- **full diagnostics** → run `molt doctor`
