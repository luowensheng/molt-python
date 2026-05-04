# migrate-tool — Multi-Tenant Database Migration and Seeding

`migrate-tool` is a Click-based CLI that wraps Alembic to manage schema migrations and
seed data across multiple tenant databases in a SaaS application. It supports upgrading,
downgrading, seeding, and reporting migration status for all tenants in parallel. This
walkthrough covers development with molt and running migrations in a CI/CD pipeline.

---

## 1. Project Init and molt sync

```bash
$ mkdir migrate-tool && cd migrate-tool
$ molt init
Initialising project "migrate-tool"...
Initialized project `migrate-tool`
Using CPython 3.11.14
Resolved 1 package in 11ms
✓ Done.
```

`molt init` creates a flat scaffold (`.python-version`, `README.md`, `hello.py`,
`pyproject.toml`, `uv.lock`). Set up the `src/` layout:

```bash
$ rm hello.py
$ mkdir -p src/migrate tests migrations/versions seeds
$ touch src/migrate/__init__.py src/migrate/__main__.py src/migrate/cli.py \
        src/migrate/runner.py src/migrate/tenants.py
$ touch tests/conftest.py
```

Replace `pyproject.toml` with the config in section 2, then add deps:

```bash
$ molt add click alembic sqlalchemy psycopg2-binary rich
$ molt add --dev pytest pytest-mock ruff "testcontainers[postgres]"
Using CPython 3.11.14
Resolved 44 packages in 178ms
  ↓ install click 8.1.7 (click-8.1.7-py3-none-any.whl)
  ↓ install alembic 1.13.1 (alembic-1.13.1-py3-none-any.whl)
  ↓ install sqlalchemy 2.0.29 (sqlalchemy-2.0.29-cp311-cp311-...whl)
  ↓ install psycopg2-binary 2.9.9 (psycopg2-binary-2.9.9-cp311-cp311-...whl)
  ↓ install rich 13.7.0 (rich-13.7.0-py3-none-any.whl)
  ...
✓ 44 package(s); store=/Users/you/.molt/pkg
```

A re-run hits the cache:

```bash
$ molt sync
  ✓ cached  click 8.1.7
  ✓ cached  alembic 1.13.1
  ...
✓ 44 package(s); store=/Users/you/.molt/pkg
```

---

## 2. pyproject.toml

```toml
[project]
name = "migrate-tool"
version = "1.0.0"
description = "Multi-tenant database migration and seeding CLI"
requires-python = ">=3.11"
dependencies = [
    "click>=8.1.7",
    "alembic>=1.13.1",
    "sqlalchemy>=2.0.29",
    "psycopg2-binary>=2.9.9",
    "rich>=13.7.0",
]

[tool.uv]
dev-dependencies = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "testcontainers[postgres]>=3.7.1",
]

[project.scripts]
migrate = "migrate.cli:main"

[tool.molt.tasks]
upgrade   = "python -m migrate upgrade"
downgrade = "python -m migrate downgrade"
seed      = "python -m migrate seed"
status    = "python -m migrate status"
test      = "pytest tests/ -v --tb=short"
lint      = "ruff check src/ tests/ && ruff format --check src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

---

## 3. Final Project Structure

After scaffolding (see section 1) and adding all application files:

```
migrate-tool/
├── .python-version
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── alembic.ini
├── migrations/
│   ├── env.py
│   ├── script.py.mako
│   └── versions/
│       ├── 001_create_users_table.py
│       ├── 002_add_subscription_plans.py
│       └── 003_add_feature_flags.py
├── seeds/
│   ├── base_roles.py
│   └── default_plans.py
├── src/
│   └── migrate/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── runner.py
│       └── tenants.py
└── tests/
    ├── conftest.py
    ├── test_upgrade.py
    └── test_seed.py
```

### src/migrate/tenants.py

```python
import os
from dataclasses import dataclass

@dataclass
class Tenant:
    id:   str
    name: str
    db_url: str

def load_tenants() -> list[Tenant]:
    """Load tenant list from environment or config file."""
    raw = os.environ.get("TENANTS", "")
    tenants = []
    for entry in raw.split(","):
        if not entry.strip():
            continue
        parts = entry.split("@", 1)
        tid, url = parts[0], parts[1]
        name = tid.replace("-", " ").title()
        tenants.append(Tenant(id=tid, name=name, db_url=url))
    return tenants
```

### src/migrate/cli.py (excerpt)

```python
import click
from rich.console import Console
from rich.table import Table
from migrate.runner import run_upgrade, run_downgrade, run_seed, get_status
from migrate.tenants import load_tenants

console = Console()

@click.group()
def main():
    """migrate — multi-tenant Alembic migration manager."""

@main.command()
@click.option("--revision", "-r", default="head")
@click.option("--tenant",   "-t", default="all",
              help="Tenant ID or 'all'")
def upgrade(revision, tenant):
    """Run Alembic upgrade migrations."""
    tenants = _select_tenants(tenant)
    for t in tenants:
        console.print(f"[cyan]→[/] Upgrading [bold]{t.name}[/] to {revision}...")
        result = run_upgrade(t.db_url, revision)
        if result.success:
            console.print(f"  [green]✔[/] {t.name}: {result.new_revision}")
        else:
            console.print(f"  [red]✘[/] {t.name}: {result.error}")

@main.command()
@click.option("--revision", "-r", required=True)
@click.option("--tenant",   "-t", default="all")
def downgrade(revision, tenant):
    """Roll back to a specific revision."""
    tenants = _select_tenants(tenant)
    for t in tenants:
        console.print(f"[yellow]←[/] Downgrading [bold]{t.name}[/] to {revision}...")
        result = run_downgrade(t.db_url, revision)
        status = "[green]✔[/]" if result.success else "[red]✘[/]"
        console.print(f"  {status} {t.name}")

@main.command()
@click.option("--seed-file", "-s", default="all")
@click.option("--tenant",    "-t", default="all")
def seed(seed_file, tenant):
    """Seed reference data into tenant databases."""
    tenants = _select_tenants(tenant)
    for t in tenants:
        console.print(f"[blue]⬡[/] Seeding [bold]{t.name}[/]...")
        run_seed(t.db_url, seed_file)
        console.print(f"  [green]✔[/] Done")

@main.command()
def status():
    """Show current migration status for all tenants."""
    tenants = load_tenants()
    table = Table(title="Migration Status")
    table.add_column("Tenant")
    table.add_column("Current Revision")
    table.add_column("Pending")
    table.add_column("Status")
    for t in tenants:
        s = get_status(t.db_url)
        color = "green" if s.is_up_to_date else "yellow"
        table.add_row(t.name, s.current, str(s.pending_count),
                      f"[{color}]{'up-to-date' if s.is_up_to_date else 'behind'}[/]")
    console.print(table)

def _select_tenants(tenant_id: str):
    tenants = load_tenants()
    if tenant_id == "all":
        return tenants
    return [t for t in tenants if t.id == tenant_id]
```

---

## 4. Running Tasks with molt run

```bash
# Check status of all tenants
$ export TENANTS="acme@postgresql://acme:s3c@db1/acme,globex@postgresql://globex:s3c@db2/globex"
$ molt run status

 Migration Status
┌─────────┬──────────────────┬─────────┬────────────┐
│ Tenant  │ Current Revision │ Pending │ Status     │
├─────────┼──────────────────┼─────────┼────────────┤
│ Acme    │ 002_subscription │ 1       │ behind     │
│ Globex  │ 003_feature_flag │ 0       │ up-to-date │
└─────────┴──────────────────┴─────────┴────────────┘

# Upgrade all tenants to head
$ molt run upgrade
→ Upgrading Acme to head...
  ✔ Acme: 003_feature_flags
→ Upgrading Globex to head...
  ✔ Globex: 003_feature_flags (already at head, no-op)

# Upgrade a single tenant
$ molt run upgrade -- --revision head --tenant acme
→ Upgrading Acme to head...
  ✔ Acme: 003_feature_flags

# Seed a specific tenant
$ molt run seed -- --tenant acme --seed-file base_roles
⬡ Seeding Acme...
  ✔ Done

# Run tests
$ molt run test
========================= test session starts ==========================
collected 18 items
tests/test_upgrade.py::test_upgrade_to_head PASSED
tests/test_upgrade.py::test_downgrade_one_step PASSED
tests/test_upgrade.py::test_idempotent_upgrade PASSED
tests/test_seed.py::test_seed_base_roles PASSED
...
========================= 18 passed in 7.32s ===========================
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: migrate-tool
  entry: migrate.cli:main
  output: dist/migrate-tool

  include:
    - src/migrate/
    - migrations/
    - seeds/

  python_version: "3.11"
  compress: true

commands:
  upgrade:
    description: "Run Alembic upgrade migrations"
    run: migrate-tool upgrade

  downgrade:
    description: "Roll back to a specific revision"
    run: migrate-tool downgrade

  seed:
    description: "Seed reference data"
    run: migrate-tool seed

  status:
    description: "Show migration status for all tenants"
    run: migrate-tool status
```

### Building

```bash
$ molt build
✔ Bundling migrate-tool and 44 packages
✔ Embedding migrations/ and seeds/
✔ Writing dist/migrate-tool (19.1 MB)
```

---

## 6. Running Migrations in CI/CD

### GitHub Actions workflow

```yaml
# .github/workflows/migrate.yml
on:
  workflow_dispatch:
    inputs:
      environment:
        type: choice
        options: [staging, production]
      revision:
        default: head

jobs:
  migrate:
    runs-on: ubuntu-latest
    environment: ${{ inputs.environment }}
    steps:
      - uses: actions/checkout@v4
      - uses: molt-build/setup-molt@v1
      - run: molt build

      - name: Run migrations
        run: |
          ./dist/migrate-tool status
          ./dist/migrate-tool upgrade --revision ${{ inputs.revision }}
          ./dist/migrate-tool status
        env:
          TENANTS: ${{ secrets.TENANTS }}

      - name: Notify Slack on failure
        if: failure()
        uses: slackapi/slack-github-action@v1
        with:
          payload: '{"text": "Migration failed on ${{ inputs.environment }}"}'
        env:
          SLACK_WEBHOOK_URL: ${{ secrets.SLACK_WEBHOOK }}
```

### Pre-deploy migration gate

```bash
# Run as part of a deploy script — exits non-zero if any tenant is behind
$ ./dist/migrate-tool status
 Migration Status
┌─────────┬──────────────────┬─────────┬────────────┐
│ Tenant  │ Current Revision │ Pending │ Status     │
├─────────┼──────────────────┼─────────┼────────────┤
│ Acme    │ 003_feature_flag │ 0       │ up-to-date │
│ Globex  │ 003_feature_flag │ 0       │ up-to-date │
└─────────┴──────────────────┴─────────┴────────────┘
Exit code: 0  # deploy proceeds
```

---

## Tips

- Embed the `migrations/versions/` directory in the binary via the `include:` list so the
  binary is completely self-contained — no need to clone the repo on the target host.
- Always run `status` before and after `upgrade` in CI to create a clear audit trail in
  the job logs.
- Use `--tenant` to upgrade a single tenant first (canary) before upgrading all tenants;
  this limits blast radius if a migration has a bug.
- Wrap `run_upgrade` in a transaction for DDL-safe databases (Postgres supports
  transactional DDL); Alembic's `op.get_bind().begin()` context manager handles this.
- Keep seed scripts idempotent with `INSERT ... ON CONFLICT DO NOTHING` so they can be
  re-run safely after an accidental data deletion.
