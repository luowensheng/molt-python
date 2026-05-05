# gitflow — Git Workflow Helper CLI

`gitflow` is a CLI that streamlines team Git workflows: enforce conventional commit
messages, auto-generate changelogs from commit history, and draft pull request
descriptions using OpenAI. This walkthrough covers development with molt, task
automation, building a self-contained binary, and distributing it to the whole team
so everyone follows the same workflow regardless of their local Python setup.

---

## 1. Project Init and molt sync

```bash
$ mkdir gitflow && cd gitflow
$ molt init
Initialising project "gitflow"...
Initialized project `gitflow`
Using CPython 3.11.14
Resolved 1 package in 11ms
✓ Done.
```

`molt init` creates a flat scaffold (`.python-version`, `README.md`, `hello.py`,
`pyproject.toml`, `uv.lock`). Set up the `src/` layout:

```bash
$ rm hello.py
$ mkdir -p src/gitflow/templates tests
$ touch src/gitflow/__init__.py src/gitflow/__main__.py src/gitflow/cli.py \
        src/gitflow/commits.py src/gitflow/changelog.py src/gitflow/pr_description.py
$ touch src/gitflow/templates/changelog.md.j2 src/gitflow/templates/pr_description.md.j2
$ touch tests/conftest.py tests/test_commits.py tests/test_changelog.py tests/test_pr_description.py
```

Replace `pyproject.toml` with the config in section 2, then add deps:

```bash
$ molt add click gitpython openai rich jinja2
$ molt add --dev pytest pytest-mock ruff responses
Using CPython 3.11.14
Resolved 58 packages in 187ms
  ↓ install click 8.1.7 (click-8.1.7-py3-none-any.whl)
  ↓ install gitpython 3.1.43 (gitpython-3.1.43-py3-none-any.whl)
  ↓ install openai 1.23.2 (openai-1.23.2-py3-none-any.whl)
  ↓ install rich 13.7.0 (rich-13.7.0-py3-none-any.whl)
  ↓ install jinja2 3.1.3 (jinja2-3.1.3-py3-none-any.whl)
  ...
✓ 58 package(s); store=/Users/you/.molt/pkg
```

A re-run hits the cache:

```bash
$ molt sync
  ✓ cached  click 8.1.7
  ✓ cached  gitpython 3.1.43
  ...
✓ 58 package(s); store=/Users/you/.molt/pkg
```

---

## 2. pyproject.toml

```toml
[project]
name = "gitflow"
version = "0.4.0"
description = "Git workflow helper — conventional commits, changelogs, PR descriptions"
requires-python = ">=3.11"
dependencies = [
    "click>=8.1.7",
    "gitpython>=3.1.43",
    "openai>=1.23.2",
    "rich>=13.7.0",
    "jinja2>=3.1.3",
]

[tool.uv]
dev-dependencies = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "responses>=0.25.0",
]

[project.scripts]
gitflow = "gitflow.cli:main"

[tool.molt.tasks]
dev  = { module = "gitflow" }
test = "pytest tests/ -v --tb=short"
lint = "ruff check src/ tests/ && ruff format --check src/ tests/"
fix  = "ruff check --fix src/ tests/ && ruff format src/ tests/"

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
gitflow/
├── .python-version
├── pyproject.toml
├── uv.lock
├── molt.yaml
├── src/
│   └── gitflow/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── commits.py
│       ├── changelog.py
│       ├── pr_description.py
│       └── templates/
│           ├── changelog.md.j2
│           └── pr_description.md.j2
└── tests/
    ├── conftest.py
    ├── test_commits.py
    ├── test_changelog.py
    └── test_pr_description.py
```

### src/gitflow/cli.py (excerpt)

```python
import click
from rich.console import Console
from gitflow.commits import validate_commit_msg, CommitType
from gitflow.changelog import generate_changelog
from gitflow.pr_description import draft_pr_description

console = Console()

@click.group()
def main():
    """gitflow — conventional commits, changelogs, and AI-powered PR descriptions."""

@main.command()
@click.argument("message")
def commit(message):
    """Validate and format a conventional commit message."""
    result = validate_commit_msg(message)
    if result.valid:
        console.print(f"[green]✔[/] {result.formatted}")
    else:
        console.print(f"[red]✘[/] {result.error}")
        raise SystemExit(1)

@main.command()
@click.option("--from-tag", "-f", required=True, help="Start tag or commit")
@click.option("--to",       "-t", default="HEAD")
@click.option("--output",   "-o", type=click.Path(), default="CHANGELOG.md")
def changelog(from_tag, to, output):
    """Generate a changelog between two refs."""
    content = generate_changelog(from_tag, to)
    with open(output, "w") as fh:
        fh.write(content)
    console.print(f"[green]✔[/] Changelog written to {output}")

@main.command("pr-desc")
@click.option("--base",    "-b", default="main")
@click.option("--head",    "-H", default="HEAD")
@click.option("--model",   "-m", default="gpt-4o-mini")
def pr_desc(base, head, model):
    """Draft a pull request description using OpenAI."""
    description = draft_pr_description(base, head, model=model)
    console.print(description)
```

### src/gitflow/commits.py (excerpt)

```python
import re
from dataclasses import dataclass
from enum import Enum

CONVENTIONAL_RE = re.compile(
    r"^(?P<type>feat|fix|docs|style|refactor|perf|test|chore|ci|build)"
    r"(\((?P<scope>[^)]+)\))?(?P<breaking>!)?: (?P<desc>.+)$"
)

class CommitType(str, Enum):
    FEAT     = "feat"
    FIX      = "fix"
    DOCS     = "docs"
    REFACTOR = "refactor"
    PERF     = "perf"
    TEST     = "test"
    CHORE    = "chore"

@dataclass
class ValidationResult:
    valid: bool
    formatted: str = ""
    error: str = ""

def validate_commit_msg(message: str) -> ValidationResult:
    m = CONVENTIONAL_RE.match(message.strip())
    if not m:
        return ValidationResult(valid=False,
            error="Message must follow: type(scope)!: description")
    return ValidationResult(valid=True, formatted=message.strip())
```

---

## 4. Running Tasks with molt run

```bash
# Validate a commit message
$ molt run dev -- commit "feat(auth): add OAuth2 PKCE flow"
✔ feat(auth): add OAuth2 PKCE flow

$ molt run dev -- commit "added stuff"
✘ Message must follow: type(scope)!: description

# Generate a changelog
$ molt run dev -- changelog --from-tag v0.3.0 --to HEAD --output CHANGELOG.md
✔ Changelog written to CHANGELOG.md

$ head -30 CHANGELOG.md
# Changelog

## [Unreleased] — 2026-05-04

### Features
- **auth**: add OAuth2 PKCE flow (#142)
- **export**: support Parquet output format (#138)

### Bug Fixes
- **cli**: handle missing config file gracefully (#145)

### Chores
- bump gitpython to 3.1.43 (#140)

# Draft a PR description with OpenAI
$ OPENAI_API_KEY=sk-... molt run dev -- pr-desc --base main --head feature/oauth2
## Summary
Add OAuth2 PKCE flow to the authentication module.

**Changes:**
- Implement PKCE code verifier/challenge generation
- Add `/oauth/callback` endpoint with state validation
- Store tokens in encrypted local keychain
- Unit tests for all PKCE helpers (coverage: 94%)

**Testing:**
- [ ] Manual test with GitHub OAuth app
- [ ] Review token storage security

# Run the test suite
$ molt run test
========================= test session starts ==========================
collected 36 items
tests/test_commits.py::test_valid_feat PASSED
tests/test_commits.py::test_valid_fix_with_scope PASSED
tests/test_commits.py::test_breaking_change PASSED
tests/test_commits.py::test_invalid_message PASSED
tests/test_changelog.py::test_changelog_generation PASSED
tests/test_pr_description.py::test_pr_desc_mocked PASSED
...
========================= 36 passed in 2.88s ===========================
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: gitflow
  entry: gitflow.cli:main
  output: dist/gitflow

  include:
    - src/gitflow/
    - src/gitflow/templates/

  python_version: "3.11"
  compress: true

commands:
  commit:
    description: "Validate a conventional commit message"
    run: gitflow commit

  changelog:
    description: "Generate a changelog from git history"
    run: gitflow changelog

  pr-desc:
    description: "Draft a PR description using OpenAI"
    run: gitflow pr-desc
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling gitflow and 58 packages
✔ Embedding Jinja2 templates
✔ Writing dist/gitflow (21.3 MB)

$ ./dist/gitflow --version
gitflow 0.4.0
```

---

## 6. Distribute to the Whole Team

### Publish via GitHub Releases (CI)

```yaml
# .github/workflows/release.yml
on:
  push:
    tags: ["v*"]

jobs:
  build-and-release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: molt-build/setup-molt@v1
      - run: molt build
      - uses: softprops/action-gh-release@v2
        with:
          files: dist/gitflow
```

### Teammate installs in 10 seconds

```bash
# macOS / Linux — no Python needed
$ curl -sSL https://github.com/myorg/gitflow/releases/latest/download/gitflow-linux-amd64 \
    -o gitflow && chmod +x gitflow && sudo mv gitflow /usr/local/bin/

$ gitflow --version
gitflow 0.4.0

# Set up as a git commit-msg hook
$ cat .git/hooks/commit-msg
#!/usr/bin/env bash
gitflow commit "$1" || exit 1

$ chmod +x .git/hooks/commit-msg

# Now every bad commit is caught automatically
$ git commit -m "wip stuff"
✘ Message must follow: type(scope)!: description
```

---

## Tips

- Pipe `gitflow pr-desc` output directly to `gh pr create --body "$(gitflow pr-desc)"` to
  create PRs with AI-generated descriptions in a single command.
- Store the `OPENAI_API_KEY` in your team's secret manager (1Password, AWS Secrets
  Manager) and inject it via environment variable — never hardcode it.
- The `commit-msg` git hook approach means the binary enforces conventional commits for
  the whole team without requiring each developer to install anything manually.
- Use `--model gpt-4o-mini` for fast, cheap PR descriptions and `--model gpt-4o` only for
  complex, multi-file changes where a more thorough summary is worth the cost.
- Add a `gitflow lint-history` command that scans all commits since a given tag and
  reports non-conventional messages — useful as a CI check on PRs.
