# data-pipeline — ETL: Postgres → pandas → S3 + Redshift

`data-pipeline` is a production ETL project that extracts records from PostgreSQL,
transforms them with pandas, validates data quality with Great Expectations, and loads
the results to S3 as Parquet and to Redshift via COPY. This walkthrough covers local
development with molt, task automation, building a self-contained binary, and scheduling
it as a cron job on an EC2 instance.

---

## 1. Project Init and molt sync

```bash
$ mkdir data-pipeline && cd data-pipeline
$ molt init
✔ Created pyproject.toml
✔ Created src/pipeline/__init__.py
✔ Initialized uv environment

$ molt sync
✔ Resolved 103 packages
✔ Installed pandas==2.2.1
✔ Installed sqlalchemy==2.0.29
✔ Installed psycopg2-binary==2.9.9
✔ Installed boto3==1.34.11
✔ Installed pyarrow==15.0.1
✔ Installed great-expectations==0.18.12
✔ Installed pytest==8.1.1
Environment ready in .venv/
```

---

## 2. pyproject.toml

```toml
[project]
name = "data-pipeline"
version = "1.2.0"
description = "ETL pipeline: Postgres → pandas → S3 + Redshift"
requires-python = ">=3.11"
dependencies = [
    "pandas>=2.2.1",
    "sqlalchemy>=2.0.29",
    "psycopg2-binary>=2.9.9",
    "boto3>=1.34.11",
    "pyarrow>=15.0.1",
    "great-expectations>=0.18.12",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "moto[s3,redshift]>=5.0.3",
    "testcontainers[postgres]>=3.7.1",
]

[tool.molt.tasks]
run      = "python -m pipeline"
validate = "python -m pipeline validate"
test     = "pytest tests/ -v --tb=short"
backfill = "python -m pipeline backfill"
lint     = "ruff check src/ tests/ && ruff format --check src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

---

## 3. Project Structure

```
data-pipeline/
├── pyproject.toml
├── molt.yaml
├── config/
│   ├── pipeline.yaml
│   └── expectations/
│       └── orders_suite.json
├── src/
│   └── pipeline/
│       ├── __init__.py
│       ├── __main__.py
│       ├── extract.py
│       ├── transform.py
│       ├── validate.py
│       ├── load.py
│       └── backfill.py
└── tests/
    ├── conftest.py
    ├── test_extract.py
    ├── test_transform.py
    └── test_load.py
```

### src/pipeline/__main__.py (excerpt)

```python
import argparse
import logging
import sys
from pipeline.extract import extract_orders
from pipeline.transform import transform_orders
from pipeline.validate import validate_dataframe
from pipeline.load import load_to_s3, load_to_redshift

logging.basicConfig(level=logging.INFO,
                    format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger(__name__)

def run(date: str):
    log.info("Extracting orders for %s", date)
    df = extract_orders(date)
    log.info("Extracted %d rows", len(df))

    df = transform_orders(df)
    log.info("Transformed %d rows after filtering", len(df))

    ok, report = validate_dataframe(df, suite="orders_suite")
    if not ok:
        log.error("Validation failed:\n%s", report)
        sys.exit(1)

    s3_path = load_to_s3(df, date)
    log.info("Loaded to S3: %s", s3_path)

    load_to_redshift(s3_path, date)
    log.info("Loaded to Redshift. Pipeline complete.")

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["run", "validate", "backfill"], default="run",
                        nargs="?")
    parser.add_argument("--date", default=None)
    args = parser.parse_args()
    if args.command == "run":
        from datetime import date
        run(args.date or date.today().isoformat())
    elif args.command == "validate":
        from pipeline.validate import run_suite_report
        run_suite_report()
    elif args.command == "backfill":
        from pipeline.backfill import backfill_range
        backfill_range(args.date)
```

---

## 4. Running Tasks with molt run

```bash
# Run the pipeline for today
$ molt run run
2026-05-04 06:00:01 INFO Extracting orders for 2026-05-04
2026-05-04 06:00:03 INFO Extracted 48,291 rows
2026-05-04 06:00:04 INFO Transformed 47,814 rows after filtering
2026-05-04 06:00:05 INFO Validation passed (3/3 expectations)
2026-05-04 06:00:08 INFO Loaded to S3: s3://dw-raw/orders/2026-05-04/part-0.parquet
2026-05-04 06:00:12 INFO Loaded to Redshift. Pipeline complete.

# Validate data quality separately
$ molt run validate
Running Great Expectations suite: orders_suite
  ✔ expect_column_values_to_not_be_null: order_id      (48291/48291)
  ✔ expect_column_values_to_be_between: total_amount   (min=0, max=50000)
  ✔ expect_column_values_to_be_in_set:  status         (all valid)
Validation PASSED

# Backfill a date range
$ molt run backfill -- --date 2026-04-01/2026-04-30
2026-05-04 07:12:01 INFO Backfilling 2026-04-01 ... 2026-04-30 (30 days)
2026-05-04 07:12:03 INFO [2026-04-01] 41,201 rows extracted
...
2026-05-04 07:24:18 INFO Backfill complete. 30 dates processed.

# Run the test suite
$ molt run test
========================= test session starts ==========================
collected 33 items
tests/test_extract.py::test_extract_returns_dataframe PASSED
tests/test_transform.py::test_transform_drops_invalid_rows PASSED
tests/test_transform.py::test_transform_normalizes_amounts PASSED
tests/test_load.py::test_load_to_s3_mocked PASSED
...
========================= 33 passed in 5.91s ===========================
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: data-pipeline
  entry: pipeline.__main__
  output: dist/data-pipeline

  include:
    - src/pipeline/
    - config/

  python_version: "3.11"
  compress: true
  strip_debug: true

commands:
  run:
    description: "Run the ETL pipeline for a given date"
    run: data-pipeline run

  validate:
    description: "Run Great Expectations validation suite"
    run: data-pipeline validate

  backfill:
    description: "Backfill a date range"
    run: data-pipeline backfill
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling data-pipeline and 103 packages
✔ Embedding config/ and expectations/
✔ Writing dist/data-pipeline (68.2 MB)
```

---

## 6. Deploy as a Cron Job on EC2

```bash
# Upload the binary to the EC2 instance
$ scp dist/data-pipeline ec2-user@etl-worker.internal:/opt/pipeline/data-pipeline
$ ssh ec2-user@etl-worker.internal

# Verify — no Python installation required
ec2-user@etl-worker:~$ python3 --version
bash: python3: command not found

ec2-user@etl-worker:~$ /opt/pipeline/data-pipeline --help
usage: data-pipeline [-h] [--date DATE] [{run,validate,backfill}]

# Set up environment file
ec2-user@etl-worker:~$ cat /etc/pipeline/env
SOURCE_DB_URL=postgresql://etl_user:s3cr3t@db-prod.internal/appdb
TARGET_REDSHIFT_URL=redshift+psycopg2://etl:pass@cluster.abc123.us-east-1.redshift.amazonaws.com/dw
AWS_DEFAULT_REGION=us-east-1
S3_BUCKET=dw-raw

# Schedule with cron — runs daily at 06:00 UTC
ec2-user@etl-worker:~$ crontab -l
0 6 * * * . /etc/pipeline/env && /opt/pipeline/data-pipeline run >> /var/log/pipeline.log 2>&1

# Trigger a manual backfill
ec2-user@etl-worker:~$ . /etc/pipeline/env && \
    /opt/pipeline/data-pipeline backfill --date 2026-04-01/2026-04-30
```

### CloudWatch log tail

```bash
$ aws logs tail /pipeline/etl-worker --follow
2026-05-04 06:00:01 INFO Extracting orders for 2026-05-04
2026-05-04 06:00:03 INFO Extracted 48,291 rows
2026-05-04 06:00:12 INFO Pipeline complete.
```

---

## Tips

- Use `moto` to mock S3 and Redshift in tests — this keeps the test suite fast and
  avoids needing live AWS credentials in CI.
- Store the Great Expectations suite JSON in `config/expectations/` and embed it in the
  binary via the `include:` list; this makes the binary self-contained for validation.
- Separate the `validate` command from `run` so you can re-run validation on already-
  loaded data without re-running the full extract/transform/load cycle.
- Write Parquet with `pyarrow` using `snappy` compression; it achieves a good balance of
  compression ratio and read speed for Redshift COPY operations.
- Send pipeline completion and failure notifications to Slack using a simple `requests`
  call in a `try/finally` block in `__main__.py` — no additional dependency needed.
