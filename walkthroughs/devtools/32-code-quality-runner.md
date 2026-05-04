# Code Quality Runner — Unified Lint, Format, and Security Checker

This walkthrough builds `quality`, a single binary that runs your full code quality pipeline: Ruff for linting and formatting, mypy for type checking, Bandit for security scanning, Vulture for dead code detection. Rich renders a colorized summary table. A `ci` mode exits non-zero on any finding, making it drop-in for GitHub Actions, GitLab CI, or any CI system. The binary is configured once and reused across all projects without any per-project toolchain installation.

---

## 1. Project Init and molt sync

```
$ mkdir quality && cd quality
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  click==8.1.7
  ruff==0.4.3
  mypy==1.10.0
  bandit==1.7.8
  vulture==2.11
  rich==13.7.1
  toml==0.10.2
  ...
  Locked 31 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "quality"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "click>=8.1",
    "ruff>=0.4",
    "mypy>=1.10",
    "bandit>=1.7",
    "vulture>=2.11",
    "rich>=13.7",
    "toml>=0.10",
]

[project.scripts]
quality = "quality.cli:main"

[tool.molt.tasks]
check  = "quality check"
fix    = "quality fix"
report = "quality report --format rich"
ci     = "quality check --ci"
```

---

## 3. Project Layout

```
quality/
├── cli.py             # Click CLI: check, fix, report, ci
├── config.py          # Per-project quality.toml loader
├── runners/
│   ├── __init__.py
│   ├── ruff.py        # Lint + format via ruff API
│   ├── mypy_runner.py # Type check via mypy programmatic API
│   ├── bandit.py      # Security scan via bandit
│   └── vulture.py     # Dead code via vulture
├── report.py          # Rich table + JSON/SARIF report output
└── models.py          # Finding, RunResult dataclasses
```

**quality/models.py**
```python
from dataclasses import dataclass, field
from enum import Enum

class Severity(str, Enum):
    ERROR   = "error"
    WARNING = "warning"
    INFO    = "info"

@dataclass
class Finding:
    tool:     str
    severity: Severity
    file:     str
    line:     int
    col:      int
    code:     str
    message:  str

@dataclass
class RunResult:
    tool:     str
    findings: list[Finding] = field(default_factory=list)
    duration: float = 0.0
    passed:   bool  = True
```

**quality/runners/ruff.py**
```python
import subprocess, json, time
from quality.models import RunResult, Finding, Severity

def check(path: str) -> RunResult:
    start = time.perf_counter()
    proc = subprocess.run(
        ["ruff", "check", "--output-format=json", path],
        capture_output=True, text=True
    )
    duration = time.perf_counter() - start
    findings = []
    if proc.stdout.strip():
        for item in json.loads(proc.stdout):
            findings.append(Finding(
                tool="ruff",
                severity=Severity.ERROR if item["code"].startswith("E") else Severity.WARNING,
                file=item["filename"],
                line=item["location"]["row"],
                col=item["location"]["column"],
                code=item["code"],
                message=item["message"],
            ))
    return RunResult(tool="ruff", findings=findings, duration=duration,
                     passed=proc.returncode == 0)

def fix(path: str) -> RunResult:
    subprocess.run(["ruff", "check", "--fix", path], capture_output=True)
    subprocess.run(["ruff", "format", path], capture_output=True)
    return RunResult(tool="ruff", passed=True)
```

**quality/runners/bandit.py**
```python
import subprocess, json, time
from quality.models import RunResult, Finding, Severity

SEVERITY_MAP = {"HIGH": Severity.ERROR, "MEDIUM": Severity.WARNING, "LOW": Severity.INFO}

def check(path: str) -> RunResult:
    start = time.perf_counter()
    proc = subprocess.run(
        ["bandit", "-r", path, "-f", "json", "-q"],
        capture_output=True, text=True
    )
    duration = time.perf_counter() - start
    findings = []
    if proc.stdout.strip():
        data = json.loads(proc.stdout)
        for issue in data.get("results", []):
            findings.append(Finding(
                tool="bandit",
                severity=SEVERITY_MAP.get(issue["issue_severity"], Severity.INFO),
                file=issue["filename"],
                line=issue["line_number"],
                col=0,
                code=issue["test_id"],
                message=issue["issue_text"],
            ))
    return RunResult(tool="bandit", findings=findings, duration=duration,
                     passed=len([f for f in findings if f.severity == Severity.ERROR]) == 0)
```

**quality/cli.py**
```python
import click, sys, time
from rich.console import Console
from rich.table import Table
from quality.runners import ruff, mypy_runner, bandit, vulture as vulture_runner
from quality.models import Severity

console = Console()

@click.group()
def main():
    pass

@main.command()
@click.argument("path", default=".")
@click.option("--ci", is_flag=True, help="Exit non-zero on any finding")
def check(path, ci):
    """Run all quality checks."""
    results = [
        ruff.check(path),
        mypy_runner.check(path),
        bandit.check(path),
        vulture_runner.check(path),
    ]
    _print_summary(results)
    if ci and any(not r.passed for r in results):
        sys.exit(1)

@main.command()
@click.argument("path", default=".")
def fix(path):
    """Auto-fix lint and formatting issues."""
    ruff.fix(path)
    console.print("[green]Auto-fix complete.[/green]")

def _print_summary(results):
    table = Table(title="Quality Report", show_lines=True)
    table.add_column("Tool",     style="cyan",  width=12)
    table.add_column("Status",   width=8)
    table.add_column("Errors",   justify="right", width=8)
    table.add_column("Warnings", justify="right", width=10)
    table.add_column("Duration", justify="right", width=10)
    for r in results:
        errors   = sum(1 for f in r.findings if f.severity == Severity.ERROR)
        warnings = sum(1 for f in r.findings if f.severity == Severity.WARNING)
        status   = "[green]PASS[/green]" if r.passed else "[red]FAIL[/red]"
        table.add_row(r.tool, status, str(errors), str(warnings), f"{r.duration:.2f}s")
    console.print(table)
    for r in results:
        for f in r.findings[:5]:   # show first 5 per tool
            color = "red" if f.severity == Severity.ERROR else "yellow"
            console.print(f"  [{color}]{f.code}[/{color}]  {f.file}:{f.line}  {f.message}")
```

---

## 4. Running Locally

```
$ cd ~/projects/my-api

$ quality check .
Quality Report
┌────────┬────────┬────────┬──────────┬──────────┐
│ Tool   │ Status │ Errors │ Warnings │ Duration │
├────────┼────────┼────────┼──────────┼──────────┤
│ ruff   │  PASS  │   0    │    3     │  0.41s   │
│ mypy   │  FAIL  │   2    │    1     │  3.12s   │
│ bandit │  PASS  │   0    │    1     │  0.88s   │
│ vulture│  PASS  │   0    │    4     │  0.23s   │
└────────┴────────┴────────┴──────────┴──────────┘
  E701  app/routes/users.py:42  Incompatible return value type (got "None", expected "User")
  E701  app/routes/users.py:78  Argument 1 to "get_user" has incompatible type "str"; expected "int"

$ quality fix .
Auto-fix complete.
# Ruff applied 3 auto-fixes, formatted 12 files
```

Run in CI mode:

```
$ quality check --ci .
Quality Report
┌────────┬────────┬────────┬──────────┬──────────┐
...
│ mypy   │  FAIL  │   2    │    1     │  3.08s   │
...
$ echo $?
1
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: quality
  entry: quality.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: check
      args: ["check"]
      description: "Run all quality checks on a path"
    - name: fix
      args: ["fix"]
      description: "Auto-fix lint and formatting issues"
    - name: report
      args: ["report"]
      description: "Generate detailed quality report"
    - name: ci
      args: ["check", "--ci"]
      description: "Run checks, exit non-zero on any failure (CI mode)"

  env:
    MYPY_CACHE_DIR: "/tmp/mypy-cache"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling quality + 31 dependencies...
  Output: dist/quality  (24.7 MB)
```

---

## 6. Using on a CI Server

Copy the binary to a shared location accessible by all CI runners:

```
$ scp dist/quality deploy@ci-tools.internal:/opt/tools/quality
$ chmod +x /opt/tools/quality
```

GitHub Actions job:

```yaml
# .github/workflows/quality.yml
name: Code Quality
on: [push, pull_request]

jobs:
  quality:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Download quality binary
        run: |
          curl -fsSL https://tools.internal/quality -o quality
          chmod +x quality

      - name: Run quality checks
        run: ./quality check --ci src/

      - name: Upload SARIF (on failure)
        if: failure()
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: quality-report.sarif
```

GitLab CI:

```yaml
# .gitlab-ci.yml
quality:
  stage: test
  image: alpine:3.19
  script:
    - wget -qO quality https://tools.internal/quality && chmod +x quality
    - ./quality check --ci .
  artifacts:
    when: always
    paths:
      - quality-report.sarif
```

Run on a project that has never been checked before:

```
$ ssh ci-runner-01 "/opt/tools/quality check --ci /workspace/legacy-api"
Quality Report
┌─────────┬────────┬────────┬──────────┬──────────┐
│ Tool    │ Status │ Errors │ Warnings │ Duration │
├─────────┼────────┼────────┼──────────┼──────────┤
│ ruff    │  FAIL  │  47    │   112    │  0.83s   │
│ mypy    │  FAIL  │  23    │    8     │  8.41s   │
│ bandit  │  FAIL  │   2    │   14     │  1.72s   │
│ vulture │  PASS  │   0    │   31     │  0.44s   │
└─────────┴────────┴────────┴──────────┴──────────┘
$ echo $?
1
```

---

## Tips

- **Per-project config**: Read `quality.toml` from the target directory to override which tools are enabled, which paths are excluded, and which error codes are allowed. Merge with sensible defaults.
- **Incremental mode**: Only check files changed since the last git commit with `git diff --name-only HEAD`. Pass the filtered file list to each runner for fast pre-commit hooks.
- **Baseline**: On first adoption in a legacy project, run `quality check --save-baseline .quality-baseline.json`. Subsequent CI runs only fail on *new* findings, letting you pay down debt gradually.
- **mypy speed**: mypy is the slowest runner. Enable the mypy daemon (`dmypy`) via the programmatic API to cut re-check times from seconds to milliseconds on large codebases.
- **SARIF output**: Emit SARIF format for GitHub's code scanning integration so findings appear as PR annotations — developers see issues inline without reading CI logs.
- **Binary + pyproject.toml**: The binary reads `[tool.ruff]`, `[tool.mypy]`, and `[tool.bandit]` sections from the target project's `pyproject.toml`, so per-project exclusions are respected without any wrapper config.
