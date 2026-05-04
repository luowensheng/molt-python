# warehouse-dags — Airflow DAG Project

`warehouse-dags` is an Airflow project containing custom operators, hooks, and DAG
definitions for a data warehouse pipeline. It ingests data from multiple sources,
transforms with pandas, and loads to a Redshift data warehouse. This walkthrough covers
local development and testing with molt, validating DAG syntax, and deploying DAGs to
a managed Airflow environment (MWAA or a self-hosted cluster).

---

## 1. Project Init and molt sync

```bash
$ mkdir warehouse-dags && cd warehouse-dags
$ molt init
✔ Created pyproject.toml
✔ Created dags/__init__.py
✔ Initialized uv environment

$ molt sync
✔ Resolved 218 packages
✔ Installed apache-airflow==2.9.0
✔ Installed pandas==2.2.1
✔ Installed sqlalchemy==2.0.29
✔ Installed boto3==1.34.11
✔ Installed slack-sdk==3.27.1
✔ Installed pytest==8.1.1
Environment ready in .venv/
```

---

## 2. pyproject.toml

```toml
[project]
name = "warehouse-dags"
version = "2.1.0"
description = "Airflow DAGs, operators, and hooks for the data warehouse"
requires-python = ">=3.11"
dependencies = [
    "apache-airflow>=2.9.0",
    "pandas>=2.2.1",
    "sqlalchemy>=2.0.29",
    "boto3>=1.34.11",
    "slack-sdk>=3.27.1",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "apache-airflow[amazon]>=2.9.0",
]

[tool.molt.tasks]
test         = "pytest tests/ -v --tb=short"
lint         = "ruff check dags/ plugins/ tests/ && ruff format --check dags/ plugins/ tests/"
validate-dags = "python scripts/validate_dags.py"
backfill     = "airflow dags backfill"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

---

## 3. Project Structure

```
warehouse-dags/
├── pyproject.toml
├── molt.yaml
├── dags/
│   ├── orders_pipeline.py
│   ├── users_sync.py
│   └── weekly_report.py
├── plugins/
│   ├── operators/
│   │   ├── __init__.py
│   │   ├── redshift_upsert_operator.py
│   │   └── s3_parquet_operator.py
│   └── hooks/
│       ├── __init__.py
│       └── redshift_hook.py
├── scripts/
│   └── validate_dags.py
└── tests/
    ├── conftest.py
    ├── test_operators.py
    └── test_dags.py
```

### dags/orders_pipeline.py

```python
from datetime import datetime, timedelta
from airflow import DAG
from airflow.operators.python import PythonOperator
from airflow.providers.amazon.aws.hooks.s3 import S3Hook
from plugins.operators.redshift_upsert_operator import RedshiftUpsertOperator
from plugins.operators.s3_parquet_operator import S3ParquetOperator

default_args = {
    "owner":           "data-team",
    "retries":          2,
    "retry_delay":      timedelta(minutes=5),
    "email_on_failure": False,
}

with DAG(
    dag_id="orders_pipeline",
    description="Extract orders from Postgres, load to Redshift",
    schedule="0 3 * * *",
    start_date=datetime(2026, 1, 1),
    catchup=False,
    default_args=default_args,
    tags=["warehouse", "orders"],
) as dag:

    extract = PythonOperator(
        task_id="extract_orders",
        python_callable=lambda **kw: __import__(
            "pipeline.extract", fromlist=["extract_orders"]
        ).extract_orders(kw["ds"]),
    )

    upload_s3 = S3ParquetOperator(
        task_id="upload_to_s3",
        source_xcom_key="return_value",
        s3_bucket="{{ var.value.dw_s3_bucket }}",
        s3_key="orders/{{ ds }}/part-0.parquet",
    )

    load_redshift = RedshiftUpsertOperator(
        task_id="load_to_redshift",
        table="orders_staging",
        s3_key="orders/{{ ds }}/part-0.parquet",
        upsert_keys=["order_id"],
    )

    extract >> upload_s3 >> load_redshift
```

### plugins/operators/redshift_upsert_operator.py (excerpt)

```python
from airflow.models import BaseOperator
from plugins.hooks.redshift_hook import RedshiftHook

class RedshiftUpsertOperator(BaseOperator):
    template_fields = ("s3_key",)

    def __init__(self, table: str, s3_key: str, upsert_keys: list[str], **kwargs):
        super().__init__(**kwargs)
        self.table      = table
        self.s3_key     = s3_key
        self.upsert_keys = upsert_keys

    def execute(self, context):
        hook = RedshiftHook()
        hook.upsert_from_s3(
            table=self.table,
            s3_key=self.s3_key,
            upsert_keys=self.upsert_keys,
        )
        self.log.info("Upserted %s from s3://%s", self.table, self.s3_key)
```

### scripts/validate_dags.py

```python
"""Import all DAGs and report any parse errors."""
import importlib
import sys
from pathlib import Path

errors = []
for dag_file in Path("dags").glob("*.py"):
    spec = importlib.util.spec_from_file_location(dag_file.stem, dag_file)
    mod  = importlib.util.module_from_spec(spec)
    try:
        spec.loader.exec_module(mod)
        print(f"  ✔ {dag_file.name}")
    except Exception as exc:
        print(f"  ✘ {dag_file.name}: {exc}")
        errors.append(dag_file.name)

if errors:
    sys.exit(1)
```

---

## 4. Running Tasks with molt run

```bash
# Validate all DAG files parse without errors
$ molt run validate-dags
  ✔ orders_pipeline.py
  ✔ users_sync.py
  ✔ weekly_report.py
All 3 DAGs valid.

# Run the test suite
$ molt run test
========================= test session starts ==========================
collected 22 items
tests/test_operators.py::test_redshift_upsert_operator PASSED
tests/test_operators.py::test_s3_parquet_operator PASSED
tests/test_dags.py::test_orders_pipeline_structure PASSED
tests/test_dags.py::test_dag_no_import_errors PASSED
tests/test_dags.py::test_task_count PASSED
...
========================= 22 passed in 6.84s ===========================

# Lint
$ molt run lint
All checks passed.

# Backfill a DAG for a date range
$ molt run backfill -- orders_pipeline --start-date 2026-04-01 --end-date 2026-04-30
[2026-05-04 10:00:00] INFO Running backfill for orders_pipeline
[2026-05-04 10:00:02] INFO Created 30 DagRun instances
...
```

---

## 5. molt.yaml

```yaml
# molt.yaml
schema_version: "1"

# No binary build needed for Airflow — DAGs are deployed as source files.
# molt is used here purely for task orchestration and dependency management.

commands:
  validate:
    description: "Validate all DAG files"
    run: python scripts/validate_dags.py

  test:
    description: "Run the test suite"
    run: pytest tests/ -v

  deploy:
    description: "Sync DAGs to the Airflow S3 bucket"
    run: python scripts/deploy_dags.py
```

---

## 6. Deploy DAGs to an Airflow Server

### Option A — MWAA (Amazon Managed Workflows for Apache Airflow)

```bash
# scripts/deploy_dags.py
import boto3, pathlib

BUCKET = "my-mwaa-bucket"
s3 = boto3.client("s3")

for dag_file in pathlib.Path("dags").glob("*.py"):
    s3.upload_file(str(dag_file), BUCKET, f"dags/{dag_file.name}")
    print(f"Uploaded dags/{dag_file.name}")

for plugin_dir in pathlib.Path("plugins").rglob("*.py"):
    s3.upload_file(str(plugin_dir), BUCKET, f"plugins/{plugin_dir}")
    print(f"Uploaded plugins/{plugin_dir}")
```

```bash
$ python scripts/deploy_dags.py
Uploaded dags/orders_pipeline.py
Uploaded dags/users_sync.py
Uploaded dags/weekly_report.py
Uploaded plugins/operators/redshift_upsert_operator.py
...
# MWAA picks up changes within ~30 seconds
```

### Option B — Self-hosted Airflow (rsync)

```bash
$ rsync -avz --delete dags/ airflow@airflow-scheduler.internal:/opt/airflow/dags/
$ rsync -avz --delete plugins/ airflow@airflow-scheduler.internal:/opt/airflow/plugins/

# Trigger a DAG run manually
$ airflow dags trigger orders_pipeline --conf '{"date": "2026-05-04"}'
Created <DagRun orders_pipeline @ 2026-05-04T10:15:00+00:00>
```

### CI/CD Pipeline (GitHub Actions)

```yaml
# .github/workflows/deploy-dags.yml
on:
  push:
    branches: [main]
    paths: ["dags/**", "plugins/**"]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: molt-build/setup-molt@v1
      - run: molt sync
      - run: molt run validate-dags
      - run: molt run test
      - run: python scripts/deploy_dags.py
        env:
          AWS_DEFAULT_REGION: us-east-1
```

---

## Tips

- Always run `validate-dags` before deploying — a DAG parse error can break the Airflow
  scheduler for all DAGs in the folder, not just the broken one.
- Use `catchup=False` for all new DAGs unless you explicitly need historical backfills;
  accidental catchup on a high-frequency DAG can flood your workers.
- Write operator unit tests that mock the hook layer — this lets you test the full
  operator logic without a live Redshift or S3 connection in CI.
- Pin `apache-airflow` to a specific minor version (e.g., `==2.9.0`) in pyproject.toml;
  Airflow minor releases occasionally introduce breaking changes in the provider APIs.
- Use Airflow Variables and Connections for all credentials and environment-specific
  configuration — never hardcode connection strings in DAG files.
