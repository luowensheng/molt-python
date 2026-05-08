# 03-fastapi-api

A small FastAPI task-list REST API. Shows how molt handles a full dev workflow —
hot-reload server, test suite, and linter — all through `molt run` with no venv
activation.

## What this demo shows

- `molt add fastapi uvicorn httpx` + `molt add --dev pytest ruff` in one step
- Dev, test, lint, and format tasks in `[tool.molt.tasks]`
- `src/` layout (`src/taskapi/`) with pytest discovering the package correctly
- All tools (`pytest`, `ruff`, `uvicorn`) available via `.molt/bin/` shims

## Project layout

```
03-fastapi-api/
  src/taskapi/
    __init__.py        package version
    __main__.py        uvicorn entry point
    app.py             FastAPI app + routes
  tests/
    conftest.py
    test_tasks.py      4 async tests via httpx.AsyncClient
  pyproject.toml       deps + dev-deps + molt tasks
  uv.lock
```

## Running

```bash
# Run the test suite
molt run test

# Start the dev server (hot-reload on :8000)
molt run dev

# Lint with ruff
molt run lint

# Format source
molt run format

# Lint + test in one shot
molt run check
```

## Tasks defined

| Task | Command |
|---|---|
| `dev` | `uvicorn taskapi.app:app --reload` on `127.0.0.1:8000` |
| `test` | `pytest tests/ -v --tb=short` |
| `lint` | `ruff check src/ tests/` |
| `format` | `ruff format src/ tests/` |
| `check` | lint + test in sequence |

## API endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | health / version |
| `GET` | `/tasks` | list all tasks |
| `POST` | `/tasks` | create task `{"title": "...", "done": false}` |
| `GET` | `/tasks/{id}` | get one task |
| `PATCH` | `/tasks/{id}` | update task |
| `DELETE` | `/tasks/{id}` | delete task |

With the dev server running:

```bash
# Create a task
curl -s -X POST http://localhost:8000/tasks \
  -H "Content-Type: application/json" \
  -d '{"title": "Try molt"}' | python -m json.tool

# List tasks
curl -s http://localhost:8000/tasks | python -m json.tool

# Interactive docs
open http://localhost:8000/docs
```

## Dev-dependency isolation

Dev deps (`pytest`, `ruff`) are declared under `[tool.uv] dev-dependencies`. They
are included in the store during development (`molt sync`) but are excluded from
production builds (`molt build`). The `pyproject.toml` `dependencies` list stays
clean — only runtime deps.

## Why no conftest.py needed for sys.path

With a `src/` layout, pytest normally can't import your package without
`conftest.py` hacks or `pip install -e .`. Under molt, the project's `src/`
directory is already in `PYTHONPATH` (via `syspath.json`), so `from taskapi.app
import app` just works.
