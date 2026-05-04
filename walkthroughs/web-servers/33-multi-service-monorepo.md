# Multi-Service Monorepo — API, Worker, and Admin CLI

This walkthrough builds a monorepo containing three services that share a common library: an HTTP API server (`services/api`), a background worker (`services/worker`), and an admin CLI (`services/admin`). The shared code lives in `packages/common` and is declared as an editable path dependency in each service. Each service has its own `pyproject.toml`, its own `molt sync`, its own `molt.yaml`, and its own binary. This is the recommended pattern for Python monorepos with molt: each service is an independent unit, but they all share code without publishing a package.

---

## 1. Repository Structure

```
monorepo/
├── packages/
│   └── common/
│       ├── pyproject.toml
│       └── common/
│           ├── __init__.py
│           ├── models.py      # Shared ORM models
│           ├── db.py          # Shared DB engine
│           ├── config.py      # Base settings
│           └── auth.py        # JWT utilities
└── services/
    ├── api/
    │   ├── pyproject.toml
    │   ├── molt.yaml
    │   └── api/
    │       └── ...
    ├── worker/
    │   ├── pyproject.toml
    │   ├── molt.yaml
    │   └── worker/
    │       └── ...
    └── admin/
        ├── pyproject.toml
        ├── molt.yaml
        └── admin/
            └── ...
```

---

## 2. The Common Package

```
$ cd packages/common
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)
```

**packages/common/pyproject.toml**
```toml
[project]
name = "common"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "sqlalchemy>=2.0",
    "psycopg2-binary>=2.9",
    "pydantic-settings>=2.2",
    "python-jose[cryptography]>=3.3",
]
```

**packages/common/common/models.py**
```python
from sqlalchemy import Column, Integer, String, DateTime, Boolean
from sqlalchemy.orm import declarative_base
from datetime import datetime

Base = declarative_base()

class User(Base):
    __tablename__ = "users"
    id         = Column(Integer, primary_key=True)
    email      = Column(String(256), unique=True, nullable=False, index=True)
    hashed_pw  = Column(String(256), nullable=False)
    plan       = Column(String(16), default="free")
    active     = Column(Boolean, default=True)
    created_at = Column(DateTime, default=datetime.utcnow)

class AuditLog(Base):
    __tablename__ = "audit_log"
    id         = Column(Integer, primary_key=True)
    user_id    = Column(Integer, nullable=True)
    action     = Column(String(128), nullable=False)
    resource   = Column(String(256))
    created_at = Column(DateTime, default=datetime.utcnow)
```

**packages/common/common/db.py**
```python
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker
from contextlib import contextmanager
from common.config import settings

engine = create_engine(settings.database_url, pool_size=5, max_overflow=10)
Session = sessionmaker(bind=engine)

@contextmanager
def get_session():
    session = Session()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()
```

The common package does not need its own `molt sync` — it has no dev tasks and no binary. Services declare it as a path dependency.

---

## 3. API Service

```
$ cd services/api
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  common (editable) @ ../../packages/common
  fastapi==0.111.0
  uvicorn[standard]==0.29.0
  ...
  Locked 34 packages.
  Created .venv
```

**services/api/pyproject.toml**
```toml
[project]
name = "api"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "common @ file:///${PROJECT_ROOT}/packages/common",
    "fastapi>=0.111",
    "uvicorn[standard]>=0.29",
    "python-multipart>=0.0.9",
    "structlog>=24.1",
]

[project.scripts]
api = "api.main:start"

[tool.molt.tasks]
dev     = "uvicorn api.main:app --reload --host 0.0.0.0 --port 8000"
test    = "pytest tests/ -v"
migrate = "python -m api.migrate"
```

**services/api/api/routes/users.py**
```python
from fastapi import APIRouter, Depends, HTTPException
from common.models import User
from common.db import get_session
from common.auth import verify_token

router = APIRouter(prefix="/users", tags=["users"])

@router.get("/{user_id}")
def get_user(user_id: int, token_data=Depends(verify_token)):
    with get_session() as db:
        user = db.query(User).filter_by(id=user_id, active=True).first()
    if user is None:
        raise HTTPException(status_code=404, detail="User not found")
    return {"id": user.id, "email": user.email, "plan": user.plan}
```

Running the API:

```
$ cd services/api
$ molt run dev
INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)
INFO:     Started reloader process
INFO:     Application startup complete.

$ curl -s http://localhost:8000/users/1 \
    -H "Authorization: Bearer eyJ..."
{"id": 1, "email": "alice@example.com", "plan": "pro"}
```

**services/api/molt.yaml**
```yaml
build:
  name: api
  entry: api.main:start
  python: "3.12"
  target: linux/amd64

  commands:
    - name: serve
      args: ["serve"]
      description: "Start the API server"
    - name: migrate
      args: ["migrate"]
      description: "Run database migrations"

  env:
    PORT: "8000"
    WORKERS: "4"
    LOG_FORMAT: "json"
```

---

## 4. Worker Service

```
$ cd services/worker
$ molt init && molt sync
  Resolving dependencies...
  common (editable) @ ../../packages/common
  celery[redis]==5.3.6
  structlog==24.1.0
  boto3==1.34.84
  ...
  Locked 41 packages.
  Created .venv
```

**services/worker/pyproject.toml**
```toml
[project]
name = "worker"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "common @ file:///${PROJECT_ROOT}/packages/common",
    "celery[redis]>=5.3",
    "structlog>=24.1",
    "boto3>=1.34",
]

[project.scripts]
worker = "worker.cli:main"

[tool.molt.tasks]
worker = "celery -A worker.celery_app worker --loglevel=info"
beat   = "celery -A worker.celery_app beat --loglevel=info"
```

**services/worker/worker/tasks/users.py**
```python
from celery import shared_task
from common.models import User, AuditLog
from common.db import get_session
import structlog

log = structlog.get_logger(__name__)

@shared_task(bind=True, max_retries=3)
def send_welcome_email(self, user_id: int):
    with get_session() as db:
        user = db.query(User).filter_by(id=user_id).first()
        if user is None:
            log.warning("user_not_found", user_id=user_id)
            return
        # ... send email
        db.add(AuditLog(user_id=user_id, action="welcome_email_sent"))
    log.info("welcome_email_sent", user_id=user_id, email=user.email)
```

Running the worker:

```
$ cd services/worker
$ molt run worker
[2024-05-04 09:00:00,000: INFO/MainProcess] celery@dev-mac ready.
[2024-05-04 09:00:12,411: INFO/ForkPoolWorker-1] welcome_email_sent user_id=1 email=alice@example.com
```

**services/worker/molt.yaml**
```yaml
build:
  name: worker
  entry: worker.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: worker
      args: ["worker"]
      description: "Start the Celery worker"
    - name: beat
      args: ["beat"]
      description: "Start the Celery beat scheduler"

  env:
    CELERY_BROKER_URL: "redis://redis:6379/0"
    LOG_FORMAT: "json"
```

---

## 5. Admin CLI Service

```
$ cd services/admin
$ molt init && molt sync
  Resolving dependencies...
  common (editable) @ ../../packages/common
  click==8.1.7
  rich==13.7.1
  ...
  Locked 22 packages.
  Created .venv
```

**services/admin/pyproject.toml**
```toml
[project]
name = "admin"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "common @ file:///${PROJECT_ROOT}/packages/common",
    "click>=8.1",
    "rich>=13.7",
]

[project.scripts]
admin = "admin.cli:main"

[tool.molt.tasks]
run  = "python -m admin.cli"
test = "pytest tests/ -v"
```

**services/admin/admin/cli.py**
```python
import click
from rich.console import Console
from rich.table import Table
from common.models import User
from common.db import get_session

console = Console()

@click.group()
def main():
    pass

@main.command("list-users")
@click.option("--plan", default=None, help="Filter by plan")
@click.option("--limit", default=20, show_default=True)
def list_users(plan, limit):
    """List users in the database."""
    with get_session() as db:
        q = db.query(User)
        if plan:
            q = q.filter_by(plan=plan)
        users = q.order_by(User.created_at.desc()).limit(limit).all()

    table = Table(title=f"Users (plan={plan or 'all'})")
    table.add_column("ID", style="dim", width=6)
    table.add_column("Email")
    table.add_column("Plan", style="cyan")
    table.add_column("Active")
    for u in users:
        table.add_row(str(u.id), u.email, u.plan, "✓" if u.active else "✗")
    console.print(table)

@main.command("deactivate")
@click.argument("user_id", type=int)
def deactivate(user_id):
    """Deactivate a user account."""
    with get_session() as db:
        user = db.query(User).filter_by(id=user_id).first()
        if user is None:
            console.print(f"[red]User {user_id} not found[/red]")
            return
        user.active = False
    console.print(f"[green]User {user_id} ({user.email}) deactivated.[/green]")
```

```
$ cd services/admin
$ molt run run list-users --plan pro
Users (plan=pro)
┌──────┬──────────────────────┬──────┬────────┐
│ ID   │ Email                │ Plan │ Active │
├──────┼──────────────────────┼──────┼────────┤
│ 1    │ alice@example.com    │ pro  │ ✓      │
│ 4    │ bob@example.com      │ pro  │ ✓      │
│ 9    │ charlie@example.com  │ pro  │ ✗      │
└──────┴──────────────────────┴──────┴────────┘
```

**services/admin/molt.yaml**
```yaml
build:
  name: admin
  entry: admin.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: run
      args: []
      description: "Admin CLI — list-users, deactivate, etc."

  env:
    LOG_LEVEL: "WARNING"
```

---

## 6. Building All Three Services

From the repo root:

```
$ cd services/api    && molt build && echo "api: OK"
  Output: dist/api         (19.4 MB)
api: OK

$ cd services/worker && molt build && echo "worker: OK"
  Output: dist/worker      (28.1 MB)
worker: OK

$ cd services/admin  && molt build && echo "admin: OK"
  Output: dist/admin       (13.2 MB)
admin: OK
```

Or add a root-level Makefile:

```makefile
.PHONY: build
build:
	cd services/api    && molt build
	cd services/worker && molt build
	cd services/admin  && molt build
```

---

## 7. Deploy to Another Machine

```
$ scp services/api/dist/api       deploy@api-01.prod:/opt/api/
$ scp services/worker/dist/worker deploy@worker-01.prod:/opt/worker/
$ scp services/admin/dist/admin   deploy@mgmt-01.prod:/usr/local/bin/

# Verify — all three binaries read the same DATABASE_URL from env
deploy@api-01$ export DATABASE_URL=postgresql+psycopg2://app:secret@db.internal/app
deploy@api-01$ /opt/api/api serve
INFO:     Uvicorn running on http://0.0.0.0:8000

deploy@worker-01$ export DATABASE_URL=postgresql+psycopg2://app:secret@db.internal/app
deploy@worker-01$ /opt/worker/worker worker
[INFO] celery@worker-01 ready.

deploy@mgmt-01$ export DATABASE_URL=postgresql+psycopg2://app:secret@db.internal/app
deploy@mgmt-01$ admin list-users --plan pro
```

---

## Tips

- **Editable path dep**: `common @ file:///${PROJECT_ROOT}/packages/common` resolves relative to the service directory. Changes to `packages/common` are reflected immediately — no reinstall needed during development.
- **Shared migrations**: Keep Alembic in `packages/common` and run migrations from the `api` service (`services/api/molt run migrate`). The worker and admin CLI read the same schema.
- **Lock files per service**: Each service has its own `uv.lock`. This means `api` and `worker` can pin different versions of non-shared deps without conflict. They share only `common`'s transitive deps.
- **CI per service**: Run each service's tests independently in CI. Only rebuild a service's binary when its own code or `packages/common` changes — use path filters in GitHub Actions.
- **Common package versioning**: While developing, use the editable path dep. When you need to publish `common` to a private PyPI registry (for external consumers), bump its version and add it as a normal versioned dep.
- **Monorepo tooling**: Consider adding a root-level `pyproject.toml` (no `[project]`) with `[tool.ruff]` and `[tool.mypy]` configured for the entire repo. The `quality` binary (walkthrough 32) can then run a single check across all services.
