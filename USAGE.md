# molt Usage Guide

Comprehensive examples for every command. Jump to a section:

- [Python Version Management](#python-version-management)
- [Project Scaffolding](#project-scaffolding)
- [Templates](#templates)
- [Task Runner](#task-runner)
- [Dependency Management](#dependency-management)
- [Dependency Analysis](#dependency-analysis)
- [Import Analysis](#import-analysis)
- [Environment Management](#environment-management)
- [Hash & Reproducibility](#hash--reproducibility)
- [Building & Distribution](#building--distribution)
- [Installation](#installation)
- [Verification & Health Checks](#verification--health-checks)
- [Real-World Workflows](#real-world-workflows)

---

## Python Version Management

molt manages Python versions independently of your system. Standalone builds from [python-build-standalone](https://github.com/indygreg/python-build-standalone) are stored in `~/.molt/python/` and are completely isolated from any system Python.

### List all available Pythons

```bash
molt python list
```
```
  3.12.3       system        /usr/bin/python3
  3.11.9       standalone    ~/.molt/python/3.11.9/bin/python3
  3.12.1       standalone    ~/.molt/python/3.12.1/bin/python3  ← active
```

List only installed versions:
```bash
molt python list --installed
```

### Install a Python version

```bash
molt python install 3.12.1
molt python install 3.11.9
molt python install 3.10.14
```

Downloads a standalone CPython build for your platform. Works on Linux, macOS, and Windows.

### Set the Python version for a project

```bash
# Set for the current project (writes .python-version + updates pyproject.toml)
molt python use 3.12.1

# Set globally (all new projects default to this version)
molt python use 3.12.1 --global
```

After changing the version:
```bash
molt env reset    # recreates .venv with the new Python
```

### Show active Python path

```bash
molt python which
# /home/user/.molt/python/3.12.1/bin/python3
```

### Remove a version

```bash
molt python remove 3.10.14
```

### Diagnose environment problems

```bash
# Find EVERY Python installation on your machine and where it came from
molt python audit
```
```
Python installations found on this machine:

  /usr/bin/python3
    version:     3.12.3
    site-packages:
      /usr/lib/python3/dist-packages
      /usr/local/lib/python3.12/dist-packages

  ~/.molt/python/3.12.1/bin/python3
    version:     3.12.1
    site-packages:
      ~/.molt/python/3.12.1/lib/python3.12/site-packages
```

```bash
# Detect PYTHONPATH pollution and sys.path surprises
molt python conflicts
```

```bash
# Verify your venv is genuinely isolated
molt python isolation-check
```
```
Checking venv isolation...
  ✓ PYTHONPATH not set
  ✓ sys.path is clean — no unexpected entries
```

---

## Project Scaffolding

### New project — interactive wizard

```bash
molt new
```
Prompts for: project type, name, Python version, optional deps.

### New project — direct

```bash
# CLI application (default)
molt new project myapp --type cli

# REST API with FastAPI
molt new project billing-api --type api

# Background worker
molt new project data-pipeline --type worker

# Reusable library
molt new project mylib --type lib

# Single-file utility script
molt new project db-migrator --type script

# Plugin-based application
molt new project plugin-host --type plugin

# Specific Python version
molt new project myapp --type cli --python 3.11

# Skip git init
molt new project myapp --no-git

# Minimal scaffold (no logging, config, exceptions)
molt new project myapp --minimal
```

### New subpackage inside an existing project

```bash
# Basic subpackage with core.py + tests
molt new package payments

# Nested under existing package
molt new package stripe --under payments

# With optional components
molt new package users --with-models --with-exceptions --with-cli
molt new package orders --with-config --with-models
```

Generated structure for `molt new package payments --with-models --with-exceptions`:
```
myapp/payments/
  __init__.py
  __main__.py
  core.py
  models.py
  exceptions.py
tests/payments/
  __init__.py
  conftest.py
  test_core.py
```

### New module

```bash
# Module + matching test file
molt new module validators

# In a specific directory
molt new module cache --in myapp/utils

# With a class scaffold
molt new module email-sender --class EmailSender

# Async module
molt new module stream-processor --async

# Dataclass-based
molt new module event --dataclass

# Skip test file
molt new module internal-helper --no-test
```

### New CLI entrypoint

```bash
# argparse (default) — zero extra deps
molt new cli myapp

# typer — adds typer as a dependency
molt new cli myapp --typer

# click — adds click as a dependency
molt new cli myapp --click
```

The entry point in `pyproject.toml` is wired automatically:
```toml
[project.scripts]
myapp = "myapp.cli:main"
```

### Config system

```bash
# Dataclass + python-dotenv (default)
molt new config

# Pydantic settings (adds pydantic-settings dep)
molt new config --pydantic

# Layered: .env.development, .env.staging, .env.production
molt new config --layered
```

Generated `config.py`:
```python
@dataclass(frozen=True)
class Config:
    env:       str  = os.getenv("ENV",       "development")
    debug:     bool = os.getenv("DEBUG",     "false").lower() == "true"
    log_level: str  = os.getenv("LOG_LEVEL", "INFO")

    def is_production(self) -> bool:
        return self.env == "production"

config = Config()
```

### Logging setup

```bash
# Standard structured logging (default)
molt new logging

# JSON output for production/log aggregators
molt new logging --json
```

### Models

```bash
# Frozen dataclass (default)
molt new model User

# With typed fields
molt new model Product --fields "name:str,price:float,active:bool,stock:int"

# Pydantic v2
molt new model Order --pydantic --fields "id:int,total:float,status:str"

# SQLAlchemy ORM
molt new model Customer --sqlalchemy --fields "email:str,name:str"
```

### Tests

```bash
# Unit test for a module
molt new test payments

# Async test
molt new test stream-processor --async

# Add a named fixture to conftest.py
molt new fixture db_session
molt new fixture mock_redis
```

### Scripts

```bash
# One-off utility script (added to [tool.molt.tasks])
molt new script seed-database
molt new script export-report

# Scheduled task
molt new script cleanup-old-jobs --scheduled
```

Run with:
```bash
molt run seed-database
```

### Deployment files

```bash
# Multi-stage Dockerfile (default)
molt new dockerfile

# Distroless production image (smaller attack surface)
molt new dockerfile --distroless

# Specific Python version
molt new dockerfile --python 3.12.1

# GitHub Actions CI workflow
molt new github-actions

# GitHub Actions release workflow (uploads binaries to GitHub releases)
molt new github-actions --release

# README.md
molt new readme
```

---

## Templates

Templates use Go's `text/template` syntax. Three tiers: built-in (shipped with molt), user (`~/.molt/templates/`), and project (`.molt/templates/`). Project templates override user, which override built-in.

### List all templates

```bash
molt template list                  # all tiers
molt template list --builtin        # built-ins only
molt template list --user           # your personal templates
molt template list --project        # project-local templates

# Filter by tag
molt template list --tag api
molt template list --tag test
molt template list --tag worker
```

**Built-in template tags:** `api`, `cli`, `worker`, `data`, `pattern`, `config`, `test`, `infra`, `scaffold`

### Inspect a template

```bash
# Full source + variable list
molt template show fastapi-router

# Just the variables
molt template vars fastapi-router
```

### Preview without writing

```bash
molt template preview fastapi-router --data '{"router_name": "payments", "prefix": "/payments"}'

molt template preview dataclass-model --data '{"model_name": "Invoice", "fields": "id:int,total:float,paid:bool"}'
```

### Create from template

```bash
# Specify output file
molt create --from-template fastapi-router myapp/routers/payments.py \
  --data '{"router_name": "payments", "prefix": "/payments"}'

# Interactive — molt asks for each variable
molt create --from-template sqlalchemy-model myapp/models/user.py --interactive

# Data from JSON file
molt create --from-template fastapi-server \
  --data @service-config.json \
  myapp/app.py

# Multi-file template — output is a directory
molt create --from-template fastapi-server myapp/ \
  --data '{"app_name": "billing", "with_auth": true, "with_db": true}'
```

### Manage templates

```bash
# Install a template from a file (to user templates)
molt template add ~/my-templates/internal-client.tmpl

# Install to project templates (shared with team via git)
molt template add ~/my-templates/team-service.tmpl --project

# Install from URL
molt template add https://raw.githubusercontent.com/org/templates/main/grpc-service.tmpl

# Remove a template
molt template remove my-http-client
molt template remove team-service --project

# Export a built-in to customize it
molt template export fastapi-server ~/.molt/templates/fastapi-server.tmpl
# Edit the file — your version now overrides the built-in

# Validate template syntax
molt template validate ~/.molt/templates/my-template.tmpl
```

### Create your own template

```bash
# Start from scratch
molt template new my-http-client

# Convert an existing file (molt detects common patterns and suggests variables)
molt template new payments-service --from-file myapp/services/payments.py
```

**Template format** — Go `text/template` with molt extensions:

```
{{/*
name: my-service
description: Internal service with retry logic
tags: [service, pattern]
requires: [tenacity]
vars:
  - name: service_name
    description: Service class name (e.g. PaymentsService)
    required: true
  - name: base_url_env
    description: Environment variable for the service base URL
    default: "SERVICE_BASE_URL"
*/}}
import os
from tenacity import retry, stop_after_attempt, wait_exponential
from {{.package_name}}.logging import get_logger

logger = get_logger(__name__)

class {{.service_name | camel}}:
    def __init__(self) -> None:
        self._base_url = os.environ["{{.base_url_env}}"]

    @retry(stop=stop_after_attempt(3), wait=wait_exponential(multiplier=1, min=1, max=10))
    def get(self, path: str) -> dict:
        logger.debug("GET %s%s", self._base_url, path)
        # TODO: implement
        raise NotImplementedError
```

**Auto-injected variables** (always available without `--data`):

| Variable | Value |
|----------|-------|
| `{{.project_name}}` | from `pyproject.toml` |
| `{{.project_version}}` | from `pyproject.toml` |
| `{{.package_name}}` | project_name with hyphens → underscores |
| `{{.python_version}}` | from `.python-version` |
| `{{.author}}` | from `pyproject.toml` or git config |
| `{{.year}}` | current year |
| `{{.date}}` | current date ISO format |

**Built-in template functions:**

| Function | Example | Result |
|----------|---------|--------|
| `camel` | `{{.name \| camel}}` | `paymentService` → `PaymentService` |
| `snake` | `{{.name \| snake}}` | `PaymentService` → `payment_service` |
| `kebab` | `{{.name \| kebab}}` | `payment_service` → `payment-service` |
| `upper` | `{{.name \| upper}}` | `hello` → `HELLO` |
| `lower` | `{{.name \| lower}}` | `HELLO` → `hello` |
| `title` | `{{.name \| title}}` | `hello world` → `Hello world` |
| `indent` | `{{indent 4 .body}}` | indents by 4 spaces |
| `year` | `{{year}}` | `2026` |
| `now` | `{{now}}` | `2026-04-15T10:23:00Z` |

---

## Task Runner

Tasks are defined in `pyproject.toml` under `[tool.molt.tasks]`. The task runner automatically activates `.venv`, loads `.env`, and sets `PYTHONPATH` correctly before running each command.

### Define tasks

```toml
[tool.molt.tasks]
dev      = "python -m myapp --debug"
test     = "pytest tests/ -v --tb=short --cov=myapp --cov-report=term-missing"
lint     = "ruff check ."
format   = "ruff format ."
typecheck = "mypy myapp/ --strict"
seed     = "python scripts/seed_database.py"
migrate  = "alembic upgrade head"
rollback = "alembic downgrade -1"
docs     = "mkdocs serve"
clean    = "find . -name '*.pyc' -delete && rm -rf .pytest_cache __pycache__"
```

### Run tasks

```bash
molt run dev
molt run test
molt run lint
molt run migrate

# Pass extra args after --
molt run test -- -k test_payments -x --pdb

# Re-run on file change (watches *.py files)
molt run test --watch
molt run lint --watch
```

### Manage tasks

```bash
# List all tasks
molt task list
```
```
Available tasks:

  dev                  python -m myapp --debug
  test                 pytest tests/ -v --tb=short --cov
  lint                 ruff check .
  format               ruff format .
  seed                 python scripts/seed_database.py
```

```bash
# Add a task
molt task add profile "python -m cProfile -s cumulative -m myapp"
molt task add check-deps "molt deps conflicts && molt imports missing"

# Remove a task
molt task remove old-task
```

### Shortcut: run tasks by name directly

```bash
# Any task name works as a top-level molt subcommand
molt test      # same as molt run test
molt lint      # same as molt run lint
molt migrate   # same as molt run migrate
```

---

## Dependency Management

molt wraps [uv](https://github.com/astral-sh/uv) for package operations.

### Add and remove packages

```bash
# Add production dependencies
molt add fastapi uvicorn pydantic

# Add development dependencies
molt add --dev pytest pytest-cov mypy ruff

# Remove a package
molt remove requests

# Remove a dev dependency
molt remove --dev black
```

### Sync and lock

```bash
# Install/update packages to match uv.lock (recreates .venv if needed)
molt sync

# Fail if lockfile needs updating (use in CI)
molt sync --frozen

# Regenerate uv.lock from pyproject.toml
molt lock

# Show dependency tree
molt tree
```

### Raw uv passthrough

```bash
# Any uv command works via molt uv
molt uv pip list
molt uv pip show requests
molt uv python list
molt uv cache clean
```

---

## Dependency Analysis

The `deps` commands analyse your full dependency surface across six layers: your source files, Python packages, package RECORD files, native extensions (`.so`), system libraries, and the Python interpreter itself. Everything is hashed.

### Full tree

```bash
molt deps tree
```

```
myapp 1.0.0
Platform: linux/amd64  glibc: 2.35

Python 3.12.1 (standalone)
  sha256: abc123def456...
  openssl: OpenSSL 3.0.11

Packages:
  ├─ cryptography 41.0.0 (native) [direct]
  │   sha256: def456...
  │   license: Apache-2.0
  ├─ cffi 1.16.0 (native)
  │   sha256: ghi789...
  ├─ requests 2.31.0 (pure) [direct]
  │   sha256: jkl012...
  └─ urllib3 2.0.7 (pure)
      sha256: mno345...

Native Extensions:
  ├─ cryptography/hazmat/_rust.abi3.so
  │   sha256: pqr678...
  │   links: libssl.so.3, libcrypto.so.3, libc.so.6
  │   hardening: canary:✓  relro:full  nx:✓  pie:✓  fortify:✓
  └─ cffi/_cffi_backend.cpython-312.so
      sha256: stu901...
      links: libffi.so.8, libc.so.6
      hardening: canary:✓  relro:partial  nx:✓  pie:✓  fortify:✗

System Libraries:
  ├─ libssl.so.3  ⚠ NON-STANDARD
  │   path: /lib/x86_64-linux-gnu/libssl.so.3
  │   sha256: vwx234...
  │   os-pkg: libssl3 3.0.11-1ubuntu2
  │   min-required: 3.0.0
  ├─ libffi.so.8  ⚠ NON-STANDARD
  │   path: /lib/x86_64-linux-gnu/libffi.so.8
  │   sha256: yza567...
  └─ libc.so.6  (standard)
      sha256: bcd890...

⚠ Security warnings: 1
  - cffi/_cffi_backend.so: FORTIFY_SOURCE not enabled
```

```bash
# Machine-readable JSON (pipe to jq, store as CI artifact)
molt deps tree --json | jq '.system_libs[] | select(.standard == false)'
molt deps tree --json > deps-$(date +%Y%m%d).json
```

### Flat list

```bash
# Greppable flat output
molt deps flat

molt deps flat | grep NON-STANDARD
molt deps flat | grep "sha256:" | wc -l    # count hashed files
```

### Why is this in my project?

```bash
# Trace the full chain for any dependency at any layer
molt deps why cryptography
molt deps why libssl.so.3
molt deps why urllib3
```

```
cryptography 41.0.0 is in the graph because:
  → it is a direct dependency in pyproject.toml

libssl.so.3 is a system library required by:
  → cryptography/hazmat/_rust.abi3.so
```

### Who is constraining this version?

```bash
# Understand why a transitive package is at a specific version
molt deps pinned-by urllib3
molt deps pinned-by certifi
molt deps pinned-by charset-normalizer
```

```
Version constraints for urllib3:

  requests    requires urllib3>=1.21.1
  httpx       requires urllib3<3,>=1.21.1
  resolved:   urllib3 2.0.7
```

### Detect version conflicts

```bash
molt deps conflicts
```

```
⚠ pydantic has potentially conflicting constraints:
   fastapi   requires pydantic>=1.6.4,!=1.7,!=1.7.1,!=1.7.2,!=1.7.3,!=1.8,!=1.8.1
   langchain requires pydantic<2.0
   resolved: pydantic 1.10.13

⚠ httpx has potentially conflicting constraints:
   ...
```

### Find unnecessary direct dependencies

```bash
molt deps minimal
```

```
Analysing which direct dependencies are imported by your source code...

  ✓ fastapi              imported directly by source
  ✓ uvicorn              imported directly by source
  ? boto3                NOT directly imported — may be transitive or unused
  ? six                  NOT directly imported — may be transitive or unused

2 dependencies may be removable. Verify before removing.
Use 'molt deps why <package>' to investigate each one.
```

### Find packages installed but never imported

```bash
molt deps unused
```

---

## Import Analysis

Static analysis of your Python source without executing it.

### Full import graph

```bash
# Text output
molt imports graph

# JSON (for tooling or visualization)
molt imports graph --json | jq '.edges | length'
```

```
Import Graph
============

Source modules (4):
  myapp.main (myapp/main.py)
  myapp.config (myapp/config.py)
  myapp.payments.core (myapp/payments/core.py)
  myapp.payments.models (myapp/payments/models.py)

Third-party imports (3):
  fastapi
  pydantic
  httpx

Stdlib imports (6):
  os, sys, json, pathlib, datetime, typing

Internal import relationships:
  myapp.main → myapp.config
  myapp.main → myapp.payments.core
  myapp.payments.core → myapp.payments.models
```

### Find unused packages

```bash
# Packages in pyproject.toml that are never actually imported
molt imports unused
```

```
Packages in pyproject.toml that are NOT imported by source:

  boto3                          2.31.0
  six                            1.16.0

These may be:
  - Transitive dependencies that became direct (safe to remove)
  - Runtime deps not visible to static analysis (plugins, etc.)
  - Genuinely unused (can be removed)
```

### Find missing declarations

```bash
# Packages imported in source but missing from pyproject.toml
# (works today because they're pulled in transitively — will break when that changes)
molt imports missing
```

```
Packages imported but NOT in pyproject.toml (only available as transitive deps):

  starlette              used in: myapp/routers/health.py, myapp/middleware.py
  anyio                  used in: myapp/workers/async_task.py

⚠ These will break if the package that pulls them in changes.
  Add them explicitly: molt add starlette anyio
```

### Detect shadowing

```bash
# Your files that shadow stdlib or installed package names
molt imports shadow
```

```
  ⚠ myapp/utils/json.py shadows stdlib module 'json'
  ⚠ tests/email.py shadows stdlib module 'email'
```

### Detect circular imports

```bash
molt imports cycles
```

```
⚠ Circular imports detected:
  myapp.payments.core → myapp.orders.core → myapp.payments.models → myapp.payments.core
```

### Trace exactly which file gets imported

```bash
# Which file wins when you do `import cryptography`?
molt imports trace cryptography
molt imports trace myapp.config
molt imports trace json    # stdlib or shadowed?
```

### List all external imports

```bash
molt imports external
```

```
External (third-party) imports used by your source:

  fastapi              0.104.1
  pydantic             2.5.0
  httpx                0.25.1
  sqlalchemy           2.0.23
```

---

## Environment Management

### Validate and diff

```bash
# Full consistency check: Python version, packages, PYTHONPATH, venv health
molt env validate
```

```
Validating environment...
  ✓ Venv exists
  ✓ Python version matches: 3.12.1
  ✓ All 47 packages match lockfile
  ✓ PYTHONPATH not set

✓ Environment is valid
```

```bash
# Compare installed packages against lockfile (file-level diff)
molt env diff
```

```
Comparing venv to lockfile...
  ≠ urllib3     lock:2.0.7        installed:2.1.0
  - boto3       in lock but NOT installed

2 differences found. Run 'molt env reset' to fix.
```

### Reset (nuke and recreate)

```bash
# Delete .venv and recreate it from uv.lock cleanly
molt env reset
```

Equivalent to `rm -rf .venv && uv sync --frozen` but in one command.

### Snapshots

Save and restore the exact state of your environment including all file hashes.

```bash
# Save current state
molt env snapshot
molt env snapshot before-upgrade          # named snapshot
molt env snapshot pre-release-v1.2.0

# List snapshots
molt env snapshots
```

```
Snapshots in .molt/snapshots/:

  before-upgrade              Python 3.12.1     47 files   2026-04-14 09:23:11
  pre-release-v1.2.0          Python 3.12.1     52 files   2026-04-15 14:05:33
  20260415-140533             Python 3.12.1     52 files   2026-04-15 14:05:33
```

```bash
# Restore to a snapshot (recreates venv with exact same packages)
molt env restore before-upgrade
molt env restore pre-release-v1.2.0
```

### Environment variables

```bash
# Print all env vars the project uses (reads .env.example + live env)
molt env vars
```

```
Environment variables (from .env.example):

  ENV                            = development (default)
  DEBUG                          = false (default)
  LOG_LEVEL                      = INFO (from env)
  DATABASE_URL                   = postgres://... (from env)
  REDIS_URL                      = redis://localhost:6379 (default)
```

```bash
# Verify .env has all keys from .env.example
molt env vars-check
```

```
  ✗ Missing: STRIPE_SECRET_KEY (in .env.example but not in .env)
  ✗ Missing: SENDGRID_API_KEY (in .env.example but not in .env)
```

---

## Hash & Reproducibility

The `.molt-deps.lock` file is a cryptographic manifest of everything that influences your project's runtime behaviour: source files, Python binary, package WHEEL files, native `.so` extensions, and system libraries. If two machines produce the same lock file, their builds are genuinely equivalent.

### Create the hash manifest

```bash
molt hash lock
```

Creates `.molt-deps.lock`:
```
# molt-deps.lock
# generated: 2026-04-15T10:23:00Z
# platform:  linux/amd64
# glibc:     2.35

[python]
/home/user/.molt/python/3.12.1/bin/python3  sha256:abc123def456...

[source]
myapp/__init__.py                              sha256:111aaa...
myapp/main.py                                  sha256:222bbb...
myapp/config.py                                sha256:333ccc...
pyproject.toml                                 sha256:444ddd...
uv.lock                                        sha256:555eee...

[packages]
cryptography-41.0.0.dist-info/WHEEL           sha256:666fff...
cryptography-41.0.0.dist-info/RECORD          sha256:777aaa...
requests-2.31.0.dist-info/WHEEL               sha256:888bbb...

[native-extensions]
cryptography/hazmat/_rust.abi3.so             sha256:999ccc...
cffi/_cffi_backend.cpython-312.so             sha256:000ddd...

[system-libs]
/lib/x86_64-linux-gnu/libssl.so.3             sha256:aaabbb...
/lib/x86_64-linux-gnu/libffi.so.8             sha256:cccfff...
/lib/x86_64-linux-gnu/libc.so.6               sha256:dddeee...
```

### Verify nothing has changed

```bash
molt hash verify
```

```
Verifying hash manifest...
  ✓ Python binary unchanged
  ✓ Source files unchanged (12 files)
  ✓ Packages unchanged (47 dist-info files)
  ✓ Native extensions unchanged (3 files)
  ✓ System libraries unchanged (8 files)

✓ All hashes match — environment is verified
```

When something changes:
```bash
molt hash verify
```
```
  ✗ Modified: myapp/main.py
  ✗ System library changed: /lib/x86_64-linux-gnu/libssl.so.3 (OS update?)
  + New package: pydantic-2.5.0.dist-info/WHEEL

✗ 3 differences found
```

### Show what changed since last lock

```bash
molt hash diff
```

```
Changes since 2026-04-14T09:23:00Z:

  M myapp/payments/core.py
  M uv.lock
  A myapp/payments/webhook.py
  A [pkg] pydantic-2.5.0.dist-info/WHEEL
  D [pkg] pydantic-1.10.13.dist-info/WHEEL

5 changes
```

### Hash a specific file

```bash
molt hash file myapp/payments/core.py
```

```
File:   myapp/payments/core.py
Size:   2847 bytes
SHA256: a3f9b2c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1
```

---

## Building & Distribution

molt produces self-contained binaries that embed your source code. The binary itself is the installer.

### Build for the current platform

```bash
molt build
```

```
Building myapp v1.0.0 (linux/amd64)...
  ✓ uv.lock up to date
  Compiled launcher (linux/amd64)
  Created: myapp-v1.0.0 (2.3MB)
  Install: molt_INSTALL_BASE=/opt ./myapp-v1.0.0 install
```

### Build profiles

| Profile | What's embedded | Size |
|---------|-----------------|------|
| `minimal` | Source code only — fetches Python + packages at install | Smallest (~2MB) |
| `standard` | Source + Python binary embedded | Medium (~25MB) |
| `extended` | Source + Python + common system libs (libssl, libffi, etc.) | Large (~35MB) |
| `full` | Everything embedded including all packages | Largest (~80MB+) |

```bash
molt build --profile minimal     # fast install, requires internet
molt build --profile standard    # Python embedded, packages fetched
molt build --profile extended    # most common system libs embedded
molt build --profile full        # completely self-contained, offline capable
```

### Build options

```bash
# Specify name, version, output
molt build --name billing --version 1.2.3 --output dist/billing-v1.2.3

# Target a specific project directory
molt build ./path/to/project

# Cross-build for testing only (system dep hashes will be inaccurate)
molt build --os linux --arch amd64 --best-effort
molt build --os darwin --arch arm64 --best-effort
molt build --os windows --arch amd64 --best-effort
```

### Production cross-builds (two-step)

For accurate system library hashes in cross-platform builds:

**Step 1:** On the target machine (e.g., your Linux prod server):
```bash
molt capture --output linux-amd64.manifest.json .
# Creates linux-amd64.manifest.json + linux-amd64.manifest.json.snap
```

**Step 2:** On your build machine (e.g., your Mac):
```bash
molt assemble \
  --manifest linux-amd64.manifest.json \
  --name billing \
  --version 1.2.3 \
  --profile standard \
  .
```

---

## Installation

The built binary is both the application and the installer.

### Default installation

```bash
./myapp-v1.0.0 install
# Installs to:
#   Linux:   ~/.local/share/myapp/1.0.0/
#   macOS:   ~/Library/Application Support/myapp/1.0.0/
#   Windows: %APPDATA%\myapp\1.0.0\
```

### Custom installation location

```bash
# Override the base directory (app/version suffix is appended automatically)
molt_INSTALL_BASE=/opt ./myapp-v1.0.0 install
# → /opt/myapp/1.0.0/

molt_INSTALL_BASE=/usr/local ./myapp-v1.0.0 install
# → /usr/local/myapp/1.0.0/

molt_INSTALL_BASE=/srv/apps ./myapp-v1.0.0 install
# → /srv/apps/myapp/1.0.0/

# Override the full path (no suffix appended)
molt_INSTALL_DIR=/opt/myapp ./myapp-v1.0.0 install
# → /opt/myapp/

# Override the cache directory (where Python + packages are downloaded)
molt_CACHE_DIR=/var/cache/molt ./myapp-v1.0.0 install

# Override via flag (highest priority)
./myapp-v1.0.0 install --prefix /custom/path
```

### Installation modes

```bash
# Minimal: smallest footprint, downloads deps at install time
./myapp-v1.0.0 install --mode minimal

# Standalone: embeds Python, downloads packages (default)
./myapp-v1.0.0 install --mode standalone

# Exact: embeds everything, verifies all hashes, writes audit log
./myapp-v1.0.0 install --mode exact
```

### Offline installation

```bash
./myapp-v1.0.0 install --offline    # uses only what's embedded in the binary
```

### Run after installation

```bash
./myapp-v1.0.0 run
./myapp-v1.0.0 run -- --verbose --config prod.yaml

# From a non-default install location
molt_INSTALL_BASE=/opt ./myapp-v1.0.0 run
```

### Other binary commands

```bash
./myapp-v1.0.0 version          # print app name and version
./myapp-v1.0.0 verify           # check installation integrity
./myapp-v1.0.0 info             # show install dir, env vars, embedded manifest
./myapp-v1.0.0 uninstall        # remove the installation directory
```

### molt install (from manifest)

```bash
# Install from a manifest.json directly
molt install .molt/manifest.json --prefix /opt/myapp/1.0.0

# With options
molt install manifest.json \
  --mode exact \
  --cache-dir /var/cache/molt \
  --audit-log /var/log/myapp-install.log \
  --verbose

# Dry run — show what would happen
molt install manifest.json --dry-run
```

---

## Verification & Health Checks

### CI gate — run everything

```bash
molt check
```

```
molt check — project health

  ✓ Python version pinned (.python-version)
  ✓ uv.lock present
  ✓ pyproject.toml present

Validating environment...
  ✓ Venv exists
  ✓ Python version matches: 3.12.1
  ✓ All 47 packages match lockfile
  ✓ PYTHONPATH not set

Import analysis:
✓ All imports are accounted for in pyproject.toml
✓ No shadowing detected

Dependency conflicts:
✓ No version conflicts detected

Hash manifest:
  ✓ Python binary unchanged
  ✓ Source files unchanged
  ✓ All packages unchanged

✓ All checks passed
```

Exit code 0 if clean, non-zero if any check fails. Put this at the top of your CI pipeline.

### Verify an installation

```bash
molt verify /opt/myapp/1.0.0
molt verify ~/.local/share/myapp/1.0.0
```

### Software Bill of Materials

```bash
# JSON SBOM of an installation
molt sbom /opt/myapp/1.0.0

# Pipe to jq
molt sbom | jq '.py_packages[] | {name, version}'
molt sbom | jq '.system_deps[] | select(.embedded == false)'

# Save for audit trail
molt sbom > sbom-$(date +%Y%m%d).json
```

### System diagnostics

```bash
molt doctor
```

```
molt 0.1.0
Platform: linux/amd64
─────────────────────────────────────
  ✓ python3             /usr/bin/python3
  ✓ uv                  found
  ✓ go                  /usr/bin/go
  ✓ git                 /usr/bin/git
  ✓ ldd                 /usr/bin/ldd
  ✓ curl                /usr/bin/curl
```

### Project info summary

```bash
molt info
```

```
myapp 1.0.0
Python:     3.12.1
Platform:   linux/amd64
Directory:  /home/user/myapp
Venv:       present
Hash lock:  present (.molt-deps.lock)
Tasks:      dev, test, lint, format, typecheck, seed
```

---

## Real-World Workflows

### Starting a new production API

```bash
# 1. Create project
molt new project billing-api --type api --python 3.12

# 2. Enter and set up
cd billing-api
molt sync

# 3. Add your deps
molt add sqlalchemy alembic redis celery

# 4. Scaffold the domain packages
molt new package invoices --with-models --with-exceptions
molt new package payments --with-models --with-cli
molt new package customers --with-models

# 5. Create templates for repeated patterns
molt create --from-template service-class myapp/invoices/service.py \
  --data '{"service_name": "invoices"}'
molt create --from-template repository myapp/invoices/repo.py \
  --data '{"entity_name": "Invoice"}'

# 6. Add tasks
molt task add db-init "alembic upgrade head"
molt task add db-reset "alembic downgrade base && alembic upgrade head"

# 7. Generate deployment files
molt new github-actions
molt new github-actions --release
molt new dockerfile

# 8. Lock the full dependency surface
molt hash lock

# 9. Verify everything is clean
molt check
```

### Debugging "works on my machine" failures

```bash
# Step 1: What does the full dep tree look like?
molt deps tree

# Step 2: Are there any non-standard system libs the target machine won't have?
molt deps flat | grep NON-STANDARD

# Step 3: What glibc version does each native extension require?
molt deps tree --json | jq '.native_exts[].imports_from'
molt deps tree --json | jq '.system_libs[] | {name, min_required}'

# Step 4: Check hardening flags (missing FORTIFY might indicate old wheels)
molt deps tree --json | jq '.native_exts[].hardening'

# Step 5: Compare two environments
molt hash lock                      # on machine A
scp .molt-deps.lock machine-b:~/
ssh machine-b "cd myapp && molt hash verify"   # on machine B
```

### Investigating a surprise dependency

```bash
# Why is this package here?
molt deps why charset-normalizer
# → requests requires it

# Who is pinning it to this version?
molt deps pinned-by charset-normalizer
# → requests requires charset-normalizer>=2.0.0
# → httpx requires charset-normalizer>=3.0.0

# What would change if I updated requests?
molt deps tree --json | jq '.packages[] | select(.name == "requests") | .requires'
```

### Pre-release checklist

```bash
# Full health check
molt check

# Update the hash manifest
molt hash lock

# Check for any transitive deps sneaking in without declaration
molt imports missing

# Check for accidentally unused deps
molt deps minimal
molt imports unused

# Verify no circular imports crept in
molt imports cycles

# Build for all platforms
molt build --profile standard
molt build --os linux --arch arm64 --best-effort
molt build --os darwin --arch arm64 --best-effort
molt build --os windows --arch amd64 --best-effort
```

### CI pipeline

```yaml
# .github/workflows/ci.yml (generated by: molt new github-actions)
name: CI
on: [push, pull_request]

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - run: pip install uv
      - run: uv sync --frozen
      - run: molt check          # ← gates everything
      - run: molt run test
      - run: molt run lint

  build:
    needs: check
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - run: molt build --profile standard
      - uses: actions/upload-artifact@v4
        with:
          name: binary-${{ matrix.os }}
          path: "*-v*"
```

### Upgrading Python version

```bash
# 1. Install the new version
molt python install 3.13.0

# 2. Snapshot current state in case you need to roll back
molt env snapshot pre-python-upgrade

# 3. Switch versions
molt python use 3.13.0

# 4. Recreate the venv
molt env reset

# 5. Run tests to verify
molt run test

# 6. If tests fail, roll back
molt python use 3.12.1
molt env restore pre-python-upgrade

# 7. If tests pass, update the hash manifest
molt hash lock
```

### Managing multiple environments

```bash
# Save snapshots for different configurations
molt env snapshot with-redis
molt env snapshot with-celery
molt env snapshot minimal-deps

# Switch between them
molt env restore with-redis
molt env restore minimal-deps

# List all snapshots
molt env snapshots
```

### Converting a legacy project

```bash
# 1. Start from existing requirements.txt
# (molt new project imports it if you have one, otherwise scaffold fresh)
molt new project myapp --type cli

# 2. Add your existing deps
molt add $(cat requirements.txt | grep -v '#' | tr '\n' ' ')

# 3. Discover hidden transitive deps you're relying on
molt imports missing
# → Add whatever it finds

# 4. Discover deps you're not actually using
molt imports unused
molt deps minimal
# → Remove whatever isn't needed

# 5. Get the full picture
molt deps tree

# 6. Lock everything
molt hash lock
```

---

## Environment Variable Reference

| Variable | Scope | Description |
|----------|-------|-------------|
| `molt_INSTALL_BASE` | Binary (`./app install`) | Base dir; app/version appended automatically |
| `molt_INSTALL_DIR` | Binary (`./app install`) | Full install path; nothing appended |
| `molt_CACHE_DIR` | Binary (`./app install`) | Download cache for Python + packages |
| `molt_DIR` | `molt uv` | Project dir for raw uv passthrough |

**Example: system-wide installation in `/opt`**

```bash
molt_INSTALL_BASE=/opt ./billing-v1.2.3 install
# Installs to: /opt/billing/1.2.3/

molt_INSTALL_BASE=/opt ./billing-v1.2.3 run
# Runs from:   /opt/billing/1.2.3/
```

**Example: Docker container**

```dockerfile
COPY billing-v1.2.3 /tmp/billing-installer
RUN molt_INSTALL_BASE=/opt molt_CACHE_DIR=/tmp/molt-cache \
    /tmp/billing-installer install --mode full --offline
RUN rm /tmp/billing-installer
```

**Example: Ansible task**

```yaml
- name: Install billing service
  command: ./billing-v1.2.3 install --mode standard
  environment:
    molt_INSTALL_BASE: /opt
    molt_CACHE_DIR: /var/cache/molt
  args:
    chdir: /tmp/releases
```
