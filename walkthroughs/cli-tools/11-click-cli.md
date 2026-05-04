# tagctl — Click-Based Cloud Infrastructure Tag Manager

`tagctl` is a Click-based CLI tool for auditing, applying, and enforcing AWS resource
tags across accounts and regions. This walkthrough covers the full lifecycle: project
init, local development, task automation with molt, and shipping a self-contained binary
to a production server that has no Python installed.

---

## 1. Project Init

```bash
$ mkdir tagctl && cd tagctl
$ molt init
Initialising project "tagctl"...
Initialized project `tagctl` at `/home/user/projects/tagctl`
Resolved 1 package in 45ms
```

`molt init` runs `uv init` in the current directory, giving you a minimal working
project:

```
tagctl/
├── .python-version   # e.g. "3.11"
├── README.md
├── hello.py          # uv placeholder — delete this
├── pyproject.toml    # minimal, no dependencies yet
└── uv.lock
```

Next, set up the `src/` layout and create the application skeleton:

```bash
# Remove the uv placeholder
$ rm hello.py

# Create package directories
$ mkdir -p src/tagctl tests policies

# Create package files (populated in section 3 below)
$ touch src/tagctl/__init__.py src/tagctl/__main__.py \
        src/tagctl/cli.py src/tagctl/aws.py \
        src/tagctl/models.py src/tagctl/policy.py
$ touch tests/conftest.py tests/test_cli.py tests/test_policy.py
```

Replace the generated `pyproject.toml` with the full project config shown in
section 2, then sync:

```bash
$ molt sync
→ ↓ click 8.1.7    (~/.molt/pkg/click/8.1.7/cp311-cp311-linux_x86_64/)
→ ↓ boto3 1.34.11  (~/.molt/pkg/boto3/1.34.11/py3-none-any/)
→ ↓ rich 13.7.0    (~/.molt/pkg/rich/13.7.0/py3-none-any/)
→ ↓ pydantic 2.6.1 (~/.molt/pkg/pydantic/2.6.1/cp311-cp311-linux_x86_64/)
→ ↓ pytest 8.1.1   (~/.molt/pkg/pytest/8.1.1/py3-none-any/)
→ ↓ ruff 0.3.2     (~/.molt/pkg/ruff/0.3.2/py3-none-any/)
✔ 47 packages ready (6 downloaded, 41 cached)
```

Packages live once in `~/.molt/pkg/` and are shared across every project. A second
project that needs `click` or `rich` will see `✔ cached` and incur zero disk writes.

---

## 2. pyproject.toml

```toml
[project]
name = "tagctl"
version = "0.1.0"
description = "Cloud infrastructure tag manager"
requires-python = ">=3.11"
dependencies = [
    "click>=8.1.7",
    "boto3>=1.34.11",
    "rich>=13.7.0",
    "pydantic>=2.6.1",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "moto[ec2,s3]>=5.0.3",
]

[project.scripts]
tagctl = "tagctl.cli:main"

[tool.molt.tasks]
dev   = "python -m tagctl"
test  = "pytest tests/ -v --tb=short"
lint  = "ruff check src/ tests/ && ruff format --check src/ tests/"
fix   = "ruff check --fix src/ tests/ && ruff format src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
```

---

## 3. Final Project Structure

After adding all application files (see section 1 for the scaffolding steps):

```
tagctl/
├── .python-version
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── policies/
│   └── required-tags.yaml
├── src/
│   └── tagctl/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── aws.py
│       ├── models.py
│       └── policy.py
└── tests/
    ├── conftest.py
    ├── test_cli.py
    └── test_policy.py
```

### src/tagctl/cli.py (excerpt)

```python
import click
from rich.console import Console
from rich.table import Table
from tagctl.aws import TagScanner
from tagctl.models import TagPolicy

console = Console()

@click.group()
@click.option("--profile", envvar="AWS_PROFILE", default="default", help="AWS profile")
@click.option("--region", envvar="AWS_DEFAULT_REGION", default="us-east-1")
@click.pass_context
def main(ctx, profile, region):
    """tagctl — enforce and audit AWS resource tags."""
    ctx.ensure_object(dict)
    ctx.obj["profile"] = profile
    ctx.obj["region"] = region

@main.command()
@click.option("--policy", "policy_file", type=click.Path(exists=True), required=True)
@click.pass_context
def audit(ctx, policy_file):
    """Audit resources against a tag policy."""
    policy = TagPolicy.from_file(policy_file)
    scanner = TagScanner(profile=ctx.obj["profile"], region=ctx.obj["region"])
    violations = scanner.audit(policy)
    table = Table(title="Tag Violations", show_lines=True)
    table.add_column("Resource", style="cyan")
    table.add_column("Missing Tags", style="red")
    for v in violations:
        table.add_row(v.resource_id, ", ".join(v.missing_tags))
    console.print(table)
    raise SystemExit(1 if violations else 0)
```

---

## 4. Running Tasks with molt run

```bash
# Start the CLI in development mode
$ molt run dev -- audit --policy policies/required-tags.yaml

  Tag Violations
┌──────────────────────┬───────────────────────────────┐
│ Resource             │ Missing Tags                  │
├──────────────────────┼───────────────────────────────┤
│ i-0abc123def456789a  │ Environment, CostCenter       │
│ i-0def456abc123789b  │ Owner                         │
│ vol-0123456789abcdef │ Environment, Owner, CostCenter│
└──────────────────────┴───────────────────────────────┘
Exit code: 1

# Run the test suite
$ molt run test
========================= test session starts ==========================
platform linux -- Python 3.11.8, pytest-8.1.1
collected 24 items

tests/test_cli.py::test_audit_no_violations PASSED               [  4%]
tests/test_cli.py::test_audit_with_violations PASSED             [  8%]
tests/test_cli.py::test_apply_dry_run PASSED                     [ 12%]
tests/test_policy.py::test_policy_load PASSED                    [ 16%]
tests/test_policy.py::test_policy_required_tags PASSED           [ 20%]
...
========================= 24 passed in 1.83s ===========================

# Lint
$ molt run lint
All checks passed.
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: tagctl
  entry: tagctl.cli:main
  output: dist/tagctl

  include:
    - src/tagctl/
    - policies/

  python_version: "3.11"

  compress: true
  strip_debug: true

commands:
  audit:
    description: "Audit AWS resources against a tag policy"
    run: tagctl audit

  apply:
    description: "Apply tags to non-compliant resources"
    run: tagctl apply

  export:
    description: "Export tag report to CSV"
    run: tagctl export
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling tagctl and 47 packages
✔ Compiling bootstrap
✔ Writing dist/tagctl (18.4 MB)

$ ls -lh dist/tagctl
-rwxr-xr-x 1 user user 18M May  4 09:12 dist/tagctl

$ ./dist/tagctl --help
Usage: tagctl [OPTIONS] COMMAND [ARGS]...

  tagctl — enforce and audit AWS resource tags.

Options:
  --profile TEXT  AWS profile  [default: default]
  --region TEXT
  --help          Show this message and exit.

Commands:
  apply   Apply tags to non-compliant resources
  audit   Audit resources against a tag policy
  export  Export tag report to CSV
```

---

## 6. Deploy to a Server with No Python

Copy the binary to a production server (e.g., a bastion host or CI runner):

```bash
# From your dev machine
$ scp dist/tagctl ec2-user@10.0.1.42:/usr/local/bin/tagctl
tagctl                                  100%   18MB  45.2MB/s   00:00

# SSH onto the server
$ ssh ec2-user@10.0.1.42

# No Python required — the binary is self-contained
$ python3 --version
bash: python3: command not found

$ tagctl --version
tagctl 0.1.0

$ tagctl audit --policy /etc/tagctl/required-tags.yaml
  Tag Violations
┌─────────────────────────┬─────────────────────┐
│ Resource                │ Missing Tags        │
├─────────────────────────┼─────────────────────┤
│ i-0aaabbbccc111222333   │ CostCenter          │
└─────────────────────────┴─────────────────────┘
```

### Scheduling with cron (no virtualenv needed)

```cron
# /etc/cron.d/tagctl
0 6 * * * ec2-user AWS_PROFILE=prod /usr/local/bin/tagctl audit \
  --policy /etc/tagctl/required-tags.yaml >> /var/log/tagctl.log 2>&1
```

---

## Tips

- The binary includes all dependencies — no `pip install`, no virtualenv, no Python runtime needed on the target host.
- Use `molt build --target linux/amd64` on macOS to cross-compile for EC2 instances.
- Pin `boto3` and `moto` to the same minor version to avoid mock compatibility issues in tests.
- Store tag policies in a separate `policies/` directory and version-control them alongside the code.
- Pass `--profile` via `AWS_PROFILE` in CI so the binary works identically in dev and prod.
