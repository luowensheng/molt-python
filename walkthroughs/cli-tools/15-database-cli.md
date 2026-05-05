# dbctl — Database Operations CLI

`dbctl` is a CLI for database introspection, ad-hoc query execution, data export, and
schema diffing across PostgreSQL and MySQL databases. It connects to remote databases,
renders results in rich terminal tables, and exports to CSV, JSON, or Parquet. This
walkthrough shows development with molt and deploying a standalone binary to a remote
DB server.

---

## 1. Project Init and molt sync

```bash
$ mkdir dbctl && cd dbctl
$ molt init
Initialising project "dbctl"...
Initialized project `dbctl`
Using CPython 3.11.14
Resolved 1 package in 11ms
✓ Done.
```

`molt init` creates a flat scaffold (`.python-version`, `README.md`, `hello.py`,
`pyproject.toml`, `uv.lock`). Set up the `src/` layout:

```bash
$ rm hello.py
$ mkdir -p src/dbctl tests
$ touch src/dbctl/__init__.py src/dbctl/__main__.py src/dbctl/cli.py \
        src/dbctl/connection.py src/dbctl/inspect.py src/dbctl/query.py \
        src/dbctl/export.py src/dbctl/diff.py
$ touch tests/conftest.py tests/test_inspect.py tests/test_query.py tests/test_diff.py
```

Replace `pyproject.toml` with the config in section 2, then add deps:

```bash
$ molt add click sqlalchemy psycopg2-binary pymysql rich tabulate
$ molt add --dev pytest pytest-mock ruff "testcontainers[postgres,mysql]"
Using CPython 3.11.14
Resolved 71 packages in 244ms
  ↓ install click 8.1.7 (click-8.1.7-py3-none-any.whl)
  ↓ install sqlalchemy 2.0.29 (sqlalchemy-2.0.29-cp311-cp311-...whl)
  ↓ install psycopg2-binary 2.9.9 (psycopg2-binary-2.9.9-cp311-cp311-...whl)
  ↓ install pymysql 1.1.0 (PyMySQL-1.1.0-py3-none-any.whl)
  ↓ install rich 13.7.0 (rich-13.7.0-py3-none-any.whl)
  ↓ install tabulate 0.9.0 (tabulate-0.9.0-py3-none-any.whl)
  ...
✓ 71 package(s); store=/Users/you/.molt/pkg
```

A re-run hits the cache:

```bash
$ molt sync
  ✓ cached  click 8.1.7
  ✓ cached  sqlalchemy 2.0.29
  ...
✓ 71 package(s); store=/Users/you/.molt/pkg
```

---

## 2. pyproject.toml

```toml
[project]
name = "dbctl"
version = "0.5.0"
description = "Database inspection, query, export, and diff CLI"
requires-python = ">=3.11"
dependencies = [
    "click>=8.1.7",
    "sqlalchemy>=2.0.29",
    "psycopg2-binary>=2.9.9",
    "pymysql>=1.1.0",
    "rich>=13.7.0",
    "tabulate>=0.9.0",
]

[tool.uv]
dev-dependencies = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "testcontainers[postgres,mysql]>=3.7.1",
]

[project.scripts]
dbctl = "dbctl.cli:main"

[tool.molt.tasks]
dev  = { module = "dbctl" }
test = "pytest tests/ -v --tb=short"
lint = "ruff check src/ tests/ && ruff format --check src/ tests/"

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
dbctl/
├── .python-version
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── src/
│   └── dbctl/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── connection.py
│       ├── inspect.py
│       ├── query.py
│       ├── export.py
│       └── diff.py
└── tests/
    ├── conftest.py
    ├── test_inspect.py
    ├── test_query.py
    └── test_diff.py
```

### src/dbctl/cli.py (excerpt)

```python
import click
from rich.console import Console
from dbctl.connection import get_engine
from dbctl.inspect import inspect_schema
from dbctl.query import run_query
from dbctl.export import export_results
from dbctl.diff import diff_schemas

console = Console()

@click.group()
@click.option("--url", envvar="DATABASE_URL", required=True,
              help="SQLAlchemy database URL")
@click.pass_context
def main(ctx, url):
    """dbctl — inspect, query, export, and diff databases."""
    ctx.ensure_object(dict)
    ctx.obj["engine"] = get_engine(url)

@main.command()
@click.option("--table", "-t", default=None, help="Show details for a specific table")
@click.pass_context
def inspect(ctx, table):
    """Inspect database schema: list tables, columns, and indexes."""
    results = inspect_schema(ctx.obj["engine"], table=table)
    console.print(results)

@main.command()
@click.argument("sql")
@click.option("--output", "-o", type=click.Path(), default=None)
@click.option("--format", "-f", "fmt",
              type=click.Choice(["table", "csv", "json", "parquet"]), default="table")
@click.pass_context
def query(ctx, sql, output, fmt):
    """Run a SQL query and display or export results."""
    rows, cols = run_query(ctx.obj["engine"], sql)
    export_results(rows, cols, output, fmt)

@main.command()
@click.option("--target", envvar="TARGET_DATABASE_URL", required=True)
@click.pass_context
def diff(ctx, target):
    """Diff schemas between two databases."""
    target_engine = get_engine(target)
    changes = diff_schemas(ctx.obj["engine"], target_engine)
    for change in changes:
        color = "green" if change.type == "added" else "red" if change.type == "removed" else "yellow"
        console.print(f"[{color}]{change.type.upper():8}[/] {change.object}")
```

---

## 4. Running Tasks with molt run

```bash
# Inspect a Postgres database
$ export DATABASE_URL="postgresql://admin:secret@db.prod.internal:5432/appdb"
$ molt run dev -- inspect

 Database: appdb (PostgreSQL 15.4)
┌────────────────────┬─────────┬──────────┬─────────────┐
│ Table              │ Rows    │ Columns  │ Indexes     │
├────────────────────┼─────────┼──────────┼─────────────┤
│ users              │ 142,841 │ 18       │ 4           │
│ orders             │ 891,204 │ 24       │ 6           │
│ products           │   8,542 │ 15       │ 3           │
│ order_items        │ 2,341,005│ 9       │ 5           │
└────────────────────┴─────────┴──────────┴─────────────┘

# Inspect a specific table
$ molt run dev -- inspect --table orders
Table: orders
┌──────────────────┬─────────────────┬──────────┬─────────────────────┐
│ Column           │ Type            │ Nullable │ Default             │
├──────────────────┼─────────────────┼──────────┼─────────────────────┤
│ id               │ BIGINT          │ NO       │ nextval('orders_id') │
│ user_id          │ BIGINT          │ NO       │                     │
│ status           │ VARCHAR(32)     │ NO       │ 'pending'           │
│ total_amount     │ NUMERIC(12,2)   │ NO       │                     │
│ created_at       │ TIMESTAMPTZ     │ NO       │ now()               │
└──────────────────┴─────────────────┴──────────┴─────────────────────┘

# Run a query and export to CSV
$ molt run dev -- query "SELECT status, COUNT(*) n, SUM(total_amount) revenue FROM orders GROUP BY status" \
    --output report.csv --format csv

✔ Wrote 5 rows to report.csv

$ cat report.csv
status,n,revenue
completed,721394,8921341.50
pending,124501,1542891.00
cancelled,45309,492384.75
...

# Run tests
$ molt run test
========================= test session starts ==========================
collected 28 items
tests/test_inspect.py::test_list_tables PASSED
tests/test_inspect.py::test_inspect_table PASSED
tests/test_query.py::test_simple_select PASSED
tests/test_query.py::test_export_csv PASSED
tests/test_diff.py::test_added_table PASSED
tests/test_diff.py::test_removed_column PASSED
...
========================= 28 passed in 4.17s ===========================
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: dbctl
  entry: dbctl.cli:main
  output: dist/dbctl

  include:
    - src/dbctl/

  # psycopg2-binary includes native .so files; molt bundles them automatically
  python_version: "3.11"
  compress: true
  strip_debug: true

commands:
  inspect:
    description: "Inspect database schema"
    run: dbctl inspect

  query:
    description: "Run a SQL query"
    run: dbctl query

  export:
    description: "Export table data"
    run: dbctl export

  diff:
    description: "Diff two database schemas"
    run: dbctl diff
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling dbctl and 71 packages
✔ Bundling native extensions (psycopg2, pymysql)
✔ Writing dist/dbctl (29.8 MB)

$ ls -lh dist/dbctl
-rwxr-xr-x 1 user user 30M May  4 11:22 dist/dbctl
```

---

## 6. Deploy and Use on a Remote DB Server

Copy the binary to a bastion or application server and run queries directly:

```bash
# Upload binary
$ scp dist/dbctl ops@bastion.internal:/usr/local/bin/dbctl

# SSH and run — no Python, no pip, no drivers to install
$ ssh ops@bastion.internal

ops@bastion:~$ dbctl --version
dbctl 0.5.0

# Inspect prod DB
ops@bastion:~$ DATABASE_URL="postgresql://readonly:s3cr3t@db-primary.internal/appdb" \
    dbctl inspect

# Schema diff between staging and prod
ops@bastion:~$ DATABASE_URL="postgresql://readonly:s3cr3t@db-primary.internal/appdb" \
    dbctl diff --target "postgresql://readonly:s3cr3t@db-staging.internal/appdb"

ADDED    public.feature_flags
REMOVED  public.legacy_sessions
CHANGED  public.users.column: avatar_url (NULL -> NOT NULL)

# Export large table to parquet for offline analysis
ops@bastion:~$ DATABASE_URL="$PROD_URL" \
    dbctl query "SELECT * FROM order_items WHERE created_at > '2026-01-01'" \
    --output /tmp/order_items_2026.parquet --format parquet

✔ Wrote 1,284,441 rows to /tmp/order_items_2026.parquet
```

### Read-only alias for ops team

```bash
# /etc/profile.d/dbctl.sh
export DATABASE_URL="postgresql://readonly:${READONLY_PASS}@db-primary.internal/appdb"
alias dbq='dbctl query'
alias dbi='dbctl inspect'
```

---

## Tips

- Use `psycopg2-binary` rather than `psycopg2` so the C extension is pre-compiled and
  molt can bundle it without a build toolchain on the target machine.
- Pass `DATABASE_URL` via environment variable, not as a CLI flag, to avoid credentials
  appearing in shell history or process listings.
- For destructive operations (DDL, deletes), add a `--dry-run` flag that prints the SQL
  without executing it — the `diff` command is read-only by design.
- Use `testcontainers` in the test suite to spin up real Postgres and MySQL containers;
  this ensures your SQLAlchemy dialect handling is correct for both engines.
- The `diff` command is particularly useful in CI: run it against a staging DB after
  migrations to confirm the schema matches expectations before promoting to production.
