# FastAPI REST API — orders-api

This walkthrough builds a production-ready orders API with FastAPI, SQLAlchemy, Alembic migrations, and PostgreSQL. You will initialise the project with molt, run the server locally under the global package store, and ship a single self-verifying binary to a Linux server — no Python or pip required on the target.

---

## 1. Project initialisation

```bash
molt init orders-api
cd orders-api
```

```
Initialized orders-api (uv-based project)
  pyproject.toml   ← project metadata + tasks
  uv.lock          ← auto-generated, commit this
  .molt/
    syspath.json   ← interpreter path + ordered store dirs
    sitecustomize.py
    bin/            ← console-script shims
```

Add runtime and dev dependencies:

```bash
molt add fastapi "uvicorn[standard]" sqlalchemy alembic psycopg2-binary pydantic-settings httpx
molt add --dev pytest pytest-asyncio ruff
```

```
Resolving dependencies...
  + fastapi 0.115.5
  + uvicorn 0.32.1 [standard]
  + sqlalchemy 2.0.36
  + alembic 1.14.0
  + psycopg2-binary 2.9.10
  + pydantic-settings 2.6.1
  + httpx 0.28.0
  + pytest 8.3.4 [dev]
  + pytest-asyncio 0.24.0 [dev]
  + ruff 0.8.2 [dev]
Syncing global store...
  11 packages installed  →  ~/.molt/pkg/  (first time: 38.2 MB)
  1 package cached       (httpx already present from another project)
Wrote .molt/syspath.json
Wrote .molt/bin/uvicorn, .molt/bin/alembic, .molt/bin/ruff, .molt/bin/pytest
```

---

## 2. Project layout

```
orders-api/
├── pyproject.toml
├── uv.lock
├── alembic.ini
├── alembic/
│   ├── env.py
│   └── versions/
├── orders_api/
│   ├── __init__.py
│   ├── main.py
│   ├── config.py
│   ├── database.py
│   ├── models.py
│   ├── schemas.py
│   └── routers/
│       ├── orders.py
│       └── health.py
└── tests/
    ├── conftest.py
    └── test_orders.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "orders-api"
version = "1.2.0"
requires-python = ">=3.12"
dependencies = [
  "fastapi>=0.115",
  "uvicorn[standard]>=0.32",
  "sqlalchemy>=2.0",
  "alembic>=1.14",
  "psycopg2-binary>=2.9",
  "pydantic-settings>=2.6",
  "httpx>=0.28",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "pytest-asyncio>=0.24",
  "ruff>=0.8",
]

[tool.molt.tasks]
dev      = "uvicorn orders_api.main:app --reload --port 8080"
test     = "pytest tests/ -v --tb=short -x"
lint     = "ruff check ."
format   = "ruff format ."
migrate  = "alembic upgrade head"

[tool.pytest.ini_options]
asyncio_mode = "auto"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Application code

**`orders_api/config.py`**

```python
from pydantic_settings import BaseSettings

class Settings(BaseSettings):
    database_url: str = "postgresql://orders:secret@localhost:5432/orders"
    api_prefix: str = "/api/v1"
    debug: bool = False

    class Config:
        env_file = ".env"

settings = Settings()
```

**`orders_api/main.py`**

```python
from fastapi import FastAPI
from orders_api.routers import orders, health

app = FastAPI(title="Orders API", version="1.2.0")
app.include_router(health.router)
app.include_router(orders.router, prefix="/api/v1/orders")
```

---

## 5. Running in development

```bash
molt run dev
```

```
INFO:     Will watch for changes in these directories: ['/home/dev/orders-api']
INFO:     Uvicorn running on http://127.0.0.1:8080 (Press CTRL+C to quit)
INFO:     Started reloader process [47821] using WatchFiles
INFO:     Started server process [47824]
INFO:     Application startup complete.
```

Run the test suite:

```bash
molt run test
```

```
============================= test session starts ==============================
platform linux -- Python 3.12.8
collected 14 items

tests/test_orders.py::test_create_order PASSED
tests/test_orders.py::test_list_orders PASSED
tests/test_orders.py::test_get_order PASSED
tests/test_orders.py::test_cancel_order PASSED
tests/test_orders.py::test_order_not_found PASSED
...
============================== 14 passed in 1.83s ==============================
```

Run Alembic migrations:

```bash
molt run migrate
```

```
INFO  [alembic.runtime.migration] Context impl PostgresqlImpl.
INFO  [alembic.runtime.migration] Will assume transactional DDL.
INFO  [alembic.runtime.migration] Running upgrade  -> a1b2c3d4e5f6, create orders table
INFO  [alembic.runtime.migration] Running upgrade a1b2c3d4e5f6 -> b2c3d4e5f6a7, add line_items
```

Lint the code:

```bash
molt run lint
```

```
All checks passed.
```

---

## 6. molt.yaml — building the distributable binary

After development, adopt the project and refine the generated config:

```bash
molt adopt --non-interactive
```

```
Detected: FastAPI project with Alembic migrations
Generated molt.yaml  ← review before building
```

**`molt.yaml`**

```yaml
version: 1

project:
  name: orders-api
  version: 1.2.0
  python: "3.12"
  description: "Orders REST API with PostgreSQL + Alembic"

deps:
  strategy: pyproject

include:
  - "orders_api/**/*.py"
  - "alembic/**/*.py"
  - "alembic.ini"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"

commands:
  default: serve

  serve:
    exec: [uvicorn, "orders_api.main:app", "--host=0.0.0.0", "--port=8080", "--workers=2"]
    description: "Run the FastAPI ASGI server"
    env:
      PYTHONUNBUFFERED: "1"

  migrate:
    exec: [alembic, upgrade, head]
    description: "Run Alembic database migrations"

  shell:
    exec: [python, "-c", "import orders_api; print('orders-api shell ready')"]
    description: "Drop into a Python shell with the app imported"

  health:
    script: "curl -sf http://localhost:8080/health | python -m json.tool"
    description: "Quick health check against a running server"

env:
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "alembic upgrade head"

integrity:
  verify_on_install: true
  verify_on_launch: false
```

---

## 7. Building the binary

```bash
molt build
```

```
Building orders-api v1.2.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 31.4 MB (892 files)
  ✓ Integrity manifest: orders-api-v1.2.0.manifest.json
  ✓ Created: orders-api-v1.2.0 (51.7 MB)
    root_hash: 9c4a1f82d30e5b17c8ea2f4a991b3c56a70d84f21e65c3b9a8d2f14e7c6b0a3d
    files:     892    total: 31.4 MB
    build time: 4.2s
```

Inspect what shipped:

```bash
molt inspect ./orders-api-v1.2.0 --files | head -20
```

```
orders-api v1.2.0  root_hash 9c4a1f82…
  892 files  31.4 MB

PATH                                        SIZE     SOURCE
orders_api/__init__.py                       124 B   include
orders_api/main.py                          1.1 KB   include
orders_api/config.py                        512 B   include
orders_api/database.py                      2.3 KB   include
orders_api/models.py                        3.8 KB   include
orders_api/schemas.py                       4.1 KB   include
orders_api/routers/orders.py                6.2 KB   include
alembic.ini                                 1.8 KB   include
alembic/env.py                              2.1 KB   include
alembic/versions/a1b2c3d4e5f6_.py           1.4 KB   include
...
```

---

## 8. Deploying to a Linux server

Copy the binary and its manifest to the target server:

```bash
scp orders-api-v1.2.0 orders-api-v1.2.0.manifest.json deploy@prod-01:/tmp/
```

On the server (no Python, no pip required):

```bash
ssh deploy@prod-01
```

```bash
# Install — extracts payload, verifies root hash, runs post_install hooks
DATABASE_URL="postgresql://orders:$(cat /run/secrets/db_pass)@db.internal:5432/orders" \
  /tmp/orders-api-v1.2.0 install
```

```
Installing orders-api v1.2.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (892 files)...
  Setting up Python environment...
  Running post_install hooks:
    [1/1] alembic upgrade head
          INFO  Running upgrade  -> a1b2c3d4e5f6, create orders table
          INFO  Running upgrade a1b2c3d4e5f6 -> b2c3d4e5f6a7, add line_items
  ✓ Installed to /home/deploy/.molt/apps/orders-api/1.2.0/
```

Run migrations separately (e.g. as a pre-deploy step):

```bash
DATABASE_URL="postgresql://orders:secret@db.internal:5432/orders" \
  /tmp/orders-api-v1.2.0 run migrate
```

Start the server:

```bash
DATABASE_URL="postgresql://orders:secret@db.internal:5432/orders" \
  /tmp/orders-api-v1.2.0 run serve
```

```
INFO:     Started server process [12087]
INFO:     Application startup complete.
INFO:     Uvicorn running on http://0.0.0.0:8080
```

Or use the default command shorthand (`serve` is the default):

```bash
orders-api-v1.2.0 run
```

---

## 9. Upgrade workflow

Build a new version on your dev machine:

```bash
# bump version in pyproject.toml and molt.yaml, then:
molt build --version 1.3.0
scp orders-api-v1.3.0 orders-api-v1.3.0.manifest.json deploy@prod-01:/tmp/

ssh deploy@prod-01 '/tmp/orders-api-v1.3.0 install'
```

Diff the builds before deploying:

```bash
molt diff ./orders-api-v1.2.0 ./orders-api-v1.3.0
```

```
Comparing:
  a: orders-api v1.2.0  (root 9c4a1f82…)
  b: orders-api v1.3.0  (root 2d8f4c91…)

Added:   1 file(s)
  + alembic/versions/c3d4e5f6a7b8_.py (1.2 KB)

Changed: 3 file(s)
  ~ orders_api/routers/orders.py (+214 bytes)
  ~ orders_api/schemas.py (+88 bytes)
  ~ orders_api/models.py (+56 bytes)

Total size change: +1558 bytes (+1.5 KB)
```

---

## 10. Tips

- **Zero-downtime deploys**: install the new binary alongside the old one (`/tmp/orders-api-v1.3.0 install`), migrate, then swap the process supervisor to point to the new version. The old install remains intact under its own version directory until you uninstall it.
- **Multiple workers**: set `--workers=4` in the `serve` command exec or override at runtime with extra args: `orders-api-v1.2.0 run serve --workers=4`.
- **CI builds**: run `molt build` in your pipeline (`ubuntu-24.04` for Linux/amd64 targets). Upload both the binary and the `.manifest.json` as release artifacts. See the [USAGE.md cross-compile section](../../USAGE.md) for a full GitHub Actions matrix.
- **Secrets**: never embed secrets in the binary. Pass them via environment variables at runtime (`DATABASE_URL`, `SECRET_KEY`). Pydantic-settings reads them automatically.
- **Second sync is instant**: on a CI agent that has already built a similar project, `molt sync` hits the cache for every package already in `~/.molt/pkg/`. No re-download, sub-second.
