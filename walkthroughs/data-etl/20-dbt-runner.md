# dbt-runner — dbt CI Tool and Project Runner

`dbt-runner` is a Click-based wrapper around dbt-core that adds CI-friendly commands
for running models, testing, generating docs, and deploying compiled artifacts to S3.
It standardises how dbt is invoked across local development, CI pipelines, and scheduled
production runs. This walkthrough covers development with molt and automating dbt in a
GitHub Actions CI workflow.

---

## 1. Project Init and molt sync

```bash
$ mkdir dbt-runner && cd dbt-runner
$ molt init
✔ Created pyproject.toml
✔ Created src/dbtrunner/__init__.py
✔ Initialized uv environment

$ molt sync
✔ Resolved 124 packages
✔ Installed dbt-core==1.8.0
✔ Installed dbt-postgres==1.8.0
✔ Installed click==8.1.7
✔ Installed rich==13.7.0
✔ Installed boto3==1.34.11
✔ Installed pytest==8.1.1
Environment ready in .venv/
```

---

## 2. pyproject.toml

```toml
[project]
name = "dbt-runner"
version = "0.7.0"
description = "dbt project runner and CI tool"
requires-python = ">=3.11"
dependencies = [
    "dbt-core>=1.8.0",
    "dbt-postgres>=1.8.0",
    "click>=8.1.7",
    "rich>=13.7.0",
    "boto3>=1.34.11",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "dbt-tests-adapter>=1.8.0",
]

[project.scripts]
dbt-runner = "dbtrunner.cli:main"

[tool.molt.tasks]
run    = "python -m dbtrunner run"
test   = "python -m dbtrunner test"
docs   = "python -m dbtrunner docs"
deploy = "python -m dbtrunner deploy"
lint   = "ruff check src/ tests/ && ruff format --check src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

---

## 3. Project Structure

```
dbt-runner/
├── pyproject.toml
├── molt.yaml
├── dbt_project/
│   ├── dbt_project.yml
│   ├── profiles.yml
│   ├── models/
│   │   ├── staging/
│   │   │   ├── stg_orders.sql
│   │   │   └── stg_users.sql
│   │   └── marts/
│   │       ├── fct_orders.sql
│   │       └── dim_users.sql
│   └── tests/
│       └── assert_order_amounts_positive.sql
├── src/
│   └── dbtrunner/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── runner.py
│       └── deployer.py
└── tests/
    ├── conftest.py
    ├── test_runner.py
    └── test_deployer.py
```

### dbt_project/dbt_project.yml

```yaml
name: warehouse
version: "1.0.0"
config-version: 2

profile: warehouse

model-paths:  ["models"]
test-paths:   ["tests"]
target-path:  "target"
clean-targets: ["target", "dbt_packages"]

models:
  warehouse:
    staging:
      +materialized: view
      +schema: staging
    marts:
      +materialized: table
      +schema: marts
```

### src/dbtrunner/cli.py (excerpt)

```python
import click
from rich.console import Console
from dbtrunner.runner import DbtRunner
from dbtrunner.deployer import deploy_artifacts

console = Console()

@click.group()
@click.option("--project-dir", default="dbt_project", envvar="DBT_PROJECT_DIR")
@click.option("--profiles-dir", default="dbt_project", envvar="DBT_PROFILES_DIR")
@click.option("--target", "-t", default="dev", envvar="DBT_TARGET")
@click.pass_context
def main(ctx, project_dir, profiles_dir, target):
    """dbt-runner — run, test, document, and deploy dbt projects."""
    ctx.ensure_object(dict)
    ctx.obj["runner"] = DbtRunner(
        project_dir=project_dir,
        profiles_dir=profiles_dir,
        target=target,
    )

@main.command()
@click.option("--select", "-s", default="", help="dbt node selector")
@click.option("--full-refresh", is_flag=True)
@click.pass_context
def run(ctx, select, full_refresh):
    """Run dbt models."""
    runner: DbtRunner = ctx.obj["runner"]
    result = runner.run(select=select, full_refresh=full_refresh)
    _print_run_result(result)
    raise SystemExit(0 if result.success else 1)

@main.command()
@click.option("--select", "-s", default="")
@click.pass_context
def test(ctx, select):
    """Run dbt tests."""
    runner: DbtRunner = ctx.obj["runner"]
    result = runner.test(select=select)
    _print_test_result(result)
    raise SystemExit(0 if result.success else 1)

@main.command()
@click.option("--open", "open_browser", is_flag=True, help="Open docs in browser")
@click.pass_context
def docs(ctx, open_browser):
    """Generate dbt documentation."""
    runner: DbtRunner = ctx.obj["runner"]
    runner.docs_generate()
    if open_browser:
        runner.docs_serve()
    else:
        console.print("[green]✔[/] Docs generated at dbt_project/target/index.html")

@main.command()
@click.option("--bucket", envvar="DBT_ARTIFACTS_BUCKET", required=True)
@click.option("--prefix", default="dbt-artifacts")
@click.pass_context
def deploy(ctx, bucket, prefix):
    """Deploy compiled artifacts to S3."""
    result = deploy_artifacts(bucket=bucket, prefix=prefix)
    console.print(f"[green]✔[/] Uploaded {result.file_count} artifacts to s3://{bucket}/{prefix}/")
```

### src/dbtrunner/runner.py (excerpt)

```python
import subprocess
from dataclasses import dataclass, field

@dataclass
class RunResult:
    success: bool
    nodes_ok:   int = 0
    nodes_err:  int = 0
    nodes_skip: int = 0
    errors: list[str] = field(default_factory=list)

class DbtRunner:
    def __init__(self, project_dir: str, profiles_dir: str, target: str):
        self.base_args = [
            "dbt",
            "--project-dir", project_dir,
            "--profiles-dir", profiles_dir,
        ]
        self.target = target

    def _run_cmd(self, *args) -> subprocess.CompletedProcess:
        cmd = self.base_args + list(args) + ["--target", self.target]
        return subprocess.run(cmd, capture_output=False)

    def run(self, select: str = "", full_refresh: bool = False) -> RunResult:
        args = ["run"]
        if select:
            args += ["--select", select]
        if full_refresh:
            args.append("--full-refresh")
        proc = self._run_cmd(*args)
        return RunResult(success=proc.returncode == 0)

    def test(self, select: str = "") -> RunResult:
        args = ["test"]
        if select:
            args += ["--select", select]
        proc = self._run_cmd(*args)
        return RunResult(success=proc.returncode == 0)
```

---

## 4. Running Tasks with molt run

```bash
# Run all dbt models in dev
$ DBT_TARGET=dev molt run run
Running with dbt==1.8.0
Found 12 models, 8 tests, 0 sources

Concurrency: 4 threads (target='dev')

1 of 12 START sql view model staging.stg_orders ........................... [RUN]
1 of 12 OK created sql view model staging.stg_orders ...................... [CREATE VIEW in 0.45s]
2 of 12 START sql view model staging.stg_users ............................ [RUN]
2 of 12 OK created sql view model staging.stg_users ....................... [CREATE VIEW in 0.39s]
...
12 of 12 OK created sql table model marts.fct_orders ...................... [CREATE TABLE in 2.34s]

Finished running 12 models in 0:00:18.

Completed successfully

# Run only staging models
$ DBT_TARGET=dev molt run run -- --select staging
...
Finished running 6 models in 0:00:04. Completed successfully

# Run dbt tests
$ DBT_TARGET=dev molt run test
Running with dbt==1.8.0

1 of 8 START test not_null_stg_orders_order_id ............................ [RUN]
1 of 8 PASS  not_null_stg_orders_order_id ................................. [PASS in 0.31s]
...
8 of 8 PASS  assert_order_amounts_positive ................................ [PASS in 0.58s]

Finished running 8 tests in 0:00:05. Completed successfully

# Generate docs
$ molt run docs
✔ Docs generated at dbt_project/target/index.html

# Deploy artifacts to S3
$ DBT_ARTIFACTS_BUCKET=my-dbt-artifacts molt run deploy
✔ Uploaded 7 artifacts to s3://my-dbt-artifacts/dbt-artifacts/
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: dbt-runner
  entry: dbtrunner.cli:main
  output: dist/dbt-runner

  include:
    - src/dbtrunner/
    - dbt_project/

  python_version: "3.11"
  compress: true

commands:
  run:
    description: "Run dbt models"
    run: dbt-runner run

  test:
    description: "Run dbt tests"
    run: dbt-runner test

  docs:
    description: "Generate dbt documentation"
    run: dbt-runner docs
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling dbt-runner and 124 packages
✔ Embedding dbt_project/
✔ Writing dist/dbt-runner (84.3 MB)

$ ls -lh dist/dbt-runner
-rwxr-xr-x 1 user user 84M May  4 12:41 dist/dbt-runner
```

---

## 6. Automating dbt in CI

### GitHub Actions — full CI workflow

```yaml
# .github/workflows/dbt-ci.yml
on:
  pull_request:
    paths: ["dbt_project/**"]
  push:
    branches: [main]

jobs:
  dbt-ci:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:15
        env:
          POSTGRES_DB:       dw_test
          POSTGRES_USER:     dbt
          POSTGRES_PASSWORD: dbt
        ports: ["5432:5432"]

    steps:
      - uses: actions/checkout@v4
      - uses: molt-build/setup-molt@v1
      - run: molt sync

      # Run models against the ephemeral CI database
      - name: dbt run
        run: molt run run
        env:
          DBT_TARGET: ci
          DBT_DB_HOST: localhost
          DBT_DB_USER: dbt
          DBT_DB_PASS: dbt
          DBT_DB_NAME: dw_test

      # Run all tests
      - name: dbt test
        run: molt run test
        env:
          DBT_TARGET: ci

      # Generate and upload docs on main
      - name: dbt docs
        if: github.ref == 'refs/heads/main'
        run: molt run docs

      - name: Upload docs artifact
        if: github.ref == 'refs/heads/main'
        uses: actions/upload-artifact@v4
        with:
          name: dbt-docs
          path: dbt_project/target/

      # Deploy artifacts to S3 on main
      - name: Deploy artifacts
        if: github.ref == 'refs/heads/main'
        run: molt run deploy
        env:
          DBT_ARTIFACTS_BUCKET: my-dbt-artifacts
          AWS_DEFAULT_REGION:   us-east-1
```

### profiles.yml (CI target)

```yaml
warehouse:
  target: dev
  outputs:
    dev:
      type: postgres
      host:   localhost
      port:   5432
      user:   "{{ env_var('DBT_DB_USER', 'dbt') }}"
      pass:   "{{ env_var('DBT_DB_PASS', 'dbt') }}"
      dbname: "{{ env_var('DBT_DB_NAME', 'warehouse_dev') }}"
      schema: public
      threads: 4
    ci:
      type: postgres
      host:   "{{ env_var('DBT_DB_HOST') }}"
      port:   5432
      user:   "{{ env_var('DBT_DB_USER') }}"
      pass:   "{{ env_var('DBT_DB_PASS') }}"
      dbname: "{{ env_var('DBT_DB_NAME') }}"
      schema: public
      threads: 4
    prod:
      type: postgres
      host:   "{{ env_var('PROD_DB_HOST') }}"
      port:   5432
      user:   "{{ env_var('PROD_DB_USER') }}"
      pass:   "{{ env_var('PROD_DB_PASS') }}"
      dbname: warehouse
      schema: public
      threads: 8
```

---

## Tips

- Embed `profiles.yml` inside the binary and resolve credentials at runtime from
  environment variables using dbt's `env_var()` macro — never commit passwords.
- Run `dbt-runner run --select state:modified+` in CI on PRs to run only models that
  changed (and their downstream dependencies), dramatically reducing CI time.
- Upload `target/manifest.json` to S3 after each production run so the next CI job can
  diff against it with `--state s3://...` for accurate change detection.
- Separate the `docs` and `deploy` steps from `run` and `test` — docs generation is
  slow and only needed on main branch merges, not every PR.
- Use `dbt source freshness` as a pre-run gate in your scheduled CI job to fail fast if
  upstream tables haven't been refreshed, rather than running stale models silently.
