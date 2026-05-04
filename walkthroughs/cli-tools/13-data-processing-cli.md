# dataflow — CSV/JSON Data Processing CLI

`dataflow` is a command-line tool for filtering, transforming, aggregating, and exporting
tabular data files. It supports CSV and JSON input, leverages pandas and polars for
high-performance operations, and exports results to CSV, JSON, Parquet, and Excel. This
walkthrough covers development with molt and shows running the finished binary on a
machine that has no pandas or Python installed.

---

## 1. Project Init and molt sync

```bash
$ mkdir dataflow && cd dataflow
$ molt init
✔ Created pyproject.toml
✔ Created src/dataflow/__init__.py
✔ Initialized uv environment

$ molt sync
✔ Resolved 89 packages
✔ Installed click==8.1.7
✔ Installed pandas==2.2.1
✔ Installed polars==0.20.15
✔ Installed rich==13.7.0
✔ Installed openpyxl==3.1.2
✔ Installed pyarrow==15.0.1
✔ Installed pytest==8.1.1
✔ Installed pytest-benchmark==4.0.0
Environment ready in .venv/
```

---

## 2. pyproject.toml

```toml
[project]
name = "dataflow"
version = "0.3.0"
description = "High-performance CLI for CSV/JSON data processing"
requires-python = ">=3.11"
dependencies = [
    "click>=8.1.7",
    "pandas>=2.2.1",
    "polars>=0.20.15",
    "rich>=13.7.0",
    "openpyxl>=3.1.2",
    "pyarrow>=15.0.1",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.1.1",
    "pytest-benchmark>=4.0.0",
    "ruff>=0.3.2",
    "hypothesis>=6.99.5",
]

[project.scripts]
dataflow = "dataflow.cli:main"

[tool.molt.tasks]
dev   = "python -m dataflow"
test  = "pytest tests/ -v --tb=short"
bench = "pytest tests/bench/ --benchmark-only --benchmark-sort=mean"
lint  = "ruff check src/ tests/ && ruff format --check src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

---

## 3. Project Structure

```
dataflow/
├── pyproject.toml
├── molt.yaml
├── src/
│   └── dataflow/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── reader.py
│       ├── transforms.py
│       ├── aggregations.py
│       └── writers.py
└── tests/
    ├── fixtures/
    │   ├── sales.csv
    │   └── events.json
    ├── test_transforms.py
    ├── test_aggregations.py
    └── bench/
        └── test_bench_transforms.py
```

### src/dataflow/cli.py (excerpt)

```python
import click
from rich.console import Console
from dataflow.reader import read_file
from dataflow.transforms import apply_filter, apply_transform
from dataflow.writers import write_output

console = Console()

@click.group()
@click.option("--engine", type=click.Choice(["pandas", "polars"]), default="polars",
              help="Processing engine")
@click.pass_context
def main(ctx, engine):
    """dataflow — filter, transform, aggregate, and export data files."""
    ctx.ensure_object(dict)
    ctx.obj["engine"] = engine

@main.command()
@click.argument("input_file", type=click.Path(exists=True))
@click.option("--where",  "-w", multiple=True, help="Filter expression: col op value")
@click.option("--select", "-s", multiple=True, help="Columns to keep")
@click.option("--output", "-o", type=click.Path(),  default="-")
@click.option("--format", "-f", "fmt",
              type=click.Choice(["csv", "json", "parquet", "excel"]), default="csv")
@click.pass_context
def filter(ctx, input_file, where, select, output, fmt):
    """Filter rows and select columns from a data file."""
    df = read_file(input_file, engine=ctx.obj["engine"])
    for expr in where:
        df = apply_filter(df, expr)
    if select:
        df = df[list(select)]
    write_output(df, output, fmt)
    console.print(f"[green]✔[/] Wrote {len(df):,} rows to {output or 'stdout'}")
```

---

## 4. Running Tasks with molt run

```bash
# Filter a CSV file
$ molt run dev -- filter sales.csv \
    --where "region = EMEA" \
    --where "amount > 1000" \
    --select date,region,amount,rep \
    --output emea-large.csv

✔ Wrote 4,821 rows to emea-large.csv

# Aggregate by region and export to Excel
$ molt run dev -- aggregate sales.csv \
    --group-by region \
    --agg "amount:sum,amount:mean,rep:count" \
    --output summary.xlsx --format excel

✔ Wrote 8 rows to summary.xlsx

# Run the test suite
$ molt run test
========================= test session starts ==========================
platform linux -- Python 3.11.8
collected 44 items

tests/test_transforms.py::test_filter_eq PASSED
tests/test_transforms.py::test_filter_gt PASSED
tests/test_transforms.py::test_filter_combined PASSED
tests/test_aggregations.py::test_groupby_sum PASSED
tests/test_aggregations.py::test_groupby_mean PASSED
...
========================= 44 passed in 3.21s ===========================

# Run benchmarks
$ molt run bench
--------------------------------------------------------------- benchmark: 3 tests ---
Name                          Min       Mean     Max    Rounds
test_filter_1m_rows_polars   0.089s    0.092s   0.101s      5
test_filter_1m_rows_pandas   0.241s    0.247s   0.259s      5
test_aggregate_1m_rows       0.134s    0.138s   0.145s      5
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: dataflow
  entry: dataflow.cli:main
  output: dist/dataflow

  include:
    - src/dataflow/

  # pyarrow and polars ship native extensions; molt bundles them automatically
  python_version: "3.11"
  compress: true
  strip_debug: true

commands:
  filter:
    description: "Filter rows and select columns"
    run: dataflow filter

  aggregate:
    description: "Group by and aggregate"
    run: dataflow aggregate

  transform:
    description: "Apply column transformations"
    run: dataflow transform

  export:
    description: "Convert between file formats"
    run: dataflow export
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling dataflow and 89 packages
✔ Bundling native extensions (pyarrow, polars)
✔ Writing dist/dataflow (54.7 MB)

$ ls -lh dist/dataflow
-rwxr-xr-x 1 user user 55M May  4 10:05 dist/dataflow
```

---

## 6. Run on a Machine Without pandas Installed

```bash
# Confirm target machine has no Python or pandas
$ ssh analyst@data-server-03

analyst@data-server-03:~$ python3 --version
bash: python3: command not found

analyst@data-server-03:~$ which pandas
bash: which: pandas: command not found

# Copy the binary
$ scp dist/dataflow analyst@data-server-03:/usr/local/bin/dataflow

# Run immediately — no install step
analyst@data-server-03:~$ dataflow --version
dataflow 0.3.0

analyst@data-server-03:~$ dataflow filter /data/raw/events.json \
    --where "event_type = purchase" \
    --where "value > 50" \
    --output /data/processed/purchases.parquet \
    --format parquet

✔ Wrote 128,443 rows to /data/processed/purchases.parquet

analyst@data-server-03:~$ dataflow aggregate /data/processed/purchases.parquet \
    --group-by product_id \
    --agg "value:sum,value:mean,user_id:count" \
    --format csv

product_id,value_sum,value_mean,user_id_count
P-1001,94821.50,47.41,2000
P-1002,81234.00,40.62,2000
P-1003,73456.75,36.73,2000
...
```

---

## Tips

- Default to polars as the engine — it is significantly faster than pandas for filtering
  and aggregation on files larger than ~100 MB, and its memory usage is lower.
- Use `pyarrow` as the serialization layer for Parquet; it allows zero-copy reads between
  polars and pandas DataFrames when both engines are needed in a pipeline.
- Hypothesis-based property tests in `tests/test_transforms.py` catch edge cases
  (empty files, all-null columns, mixed-type columns) that are hard to enumerate manually.
- For very large files (>2 GB), stream with `polars.scan_csv` / `scan_parquet` and call
  `.collect()` only at the write step to avoid loading everything into memory.
- Build separate Linux and macOS binaries in CI using a matrix job; upload both to an
  internal release page so analysts on any OS can grab the right one.
