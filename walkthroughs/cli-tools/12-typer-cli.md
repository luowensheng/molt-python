# devkit — Typer CLI for Developer Productivity

`devkit` is a Typer-based CLI that accelerates day-to-day developer work: scaffold new
projects from templates, manage reusable code snippets, and initialize repos with
opinionated defaults. This walkthrough shows the full development cycle with molt and
how to distribute a zero-dependency binary to teammates who may not have Python installed.

---

## 1. Project Init and molt sync

```bash
$ mkdir devkit && cd devkit
$ molt init
Initialising project "devkit"...
Initialized project `devkit`
Using CPython 3.11.14
Resolved 1 package in 11ms
✓ Done.
```

`molt init` runs `uv init` in the current directory, giving you a minimal working
project:

```
devkit/
├── .python-version
├── README.md
├── hello.py          # uv placeholder — delete this
├── pyproject.toml
└── uv.lock
```

Set up the `src/` layout and create the application skeleton:

```bash
$ rm hello.py
$ mkdir -p src/devkit tests
$ touch src/devkit/__init__.py src/devkit/__main__.py src/devkit/cli.py \
        src/devkit/scaffold.py src/devkit/snippets.py
$ touch tests/conftest.py tests/test_scaffold.py tests/test_snippets.py
```

Replace the generated `pyproject.toml` with the config in section 2, then add deps:

```bash
$ molt add "typer[all]" rich jinja2 gitpython
$ molt add --dev pytest pytest-mock ruff
Using CPython 3.11.14
Resolved 62 packages in 209ms
  ↓ install typer 0.12.3 (typer-0.12.3-py3-none-any.whl)
  ↓ install rich 13.7.0 (rich-13.7.0-py3-none-any.whl)
  ↓ install jinja2 3.1.3 (jinja2-3.1.3-py3-none-any.whl)
  ↓ install gitpython 3.1.43 (gitpython-3.1.43-py3-none-any.whl)
  ↓ install pytest 8.1.1 (pytest-8.1.1-py3-none-any.whl)
  ↓ install ruff 0.3.2 (ruff-0.3.2-py3-none-any.whl)
  ✓ cached  pytest-mock 3.12.0
  ...
✓ 62 package(s); store=/Users/you/.molt/pkg
```

A re-run hits the cache:

```bash
$ molt sync
  ✓ cached  typer 0.12.3
  ✓ cached  rich 13.7.0
  ✓ cached  jinja2 3.1.3
  ...
✓ 62 package(s); store=/Users/you/.molt/pkg
```

---

## 2. pyproject.toml

```toml
[project]
name = "devkit"
version = "0.2.0"
description = "Developer productivity CLI — scaffolding and snippet management"
requires-python = ">=3.11"
dependencies = [
    "typer[all]>=0.12.3",
    "rich>=13.7.0",
    "jinja2>=3.1.3",
    "gitpython>=3.1.43",
]

[tool.uv]
dev-dependencies = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "typer[dev]>=0.12.3",
]

[project.scripts]
devkit = "devkit.cli:app"

[tool.molt.tasks]
dev  = { module = "devkit" }
test = "pytest tests/ -v --tb=short"
lint = "ruff check src/ tests/ && ruff format --check src/ tests/"
fix  = "ruff check --fix src/ tests/ && ruff format src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
addopts   = "-q"
```

---

## 3. Final Project Structure

After scaffolding (see section 1) and adding all application files:

```
devkit/
├── .python-version
├── uv.lock
├── pyproject.toml
├── molt.yaml
├── src/
│   └── devkit/
│       ├── __init__.py
│       ├── __main__.py
│       ├── cli.py
│       ├── scaffold.py
│       ├── snippets.py
│       └── templates/
│           ├── python-lib/
│           │   ├── pyproject.toml.j2
│           │   └── src/{{name}}/__init__.py.j2
│           └── fastapi-service/
│               ├── pyproject.toml.j2
│               └── src/{{name}}/main.py.j2
└── tests/
    ├── conftest.py
    ├── test_scaffold.py
    └── test_snippets.py
```

### src/devkit/cli.py (excerpt)

```python
import typer
from rich.console import Console
from rich.table import Table
from devkit.scaffold import Scaffolder
from devkit.snippets import SnippetStore

app = typer.Typer(help="devkit — developer productivity toolkit", no_args_is_help=True)
scaffold_app = typer.Typer(help="Project scaffolding commands")
snippet_app  = typer.Typer(help="Snippet management commands")
app.add_typer(scaffold_app, name="scaffold")
app.add_typer(snippet_app,  name="snippet")

console = Console()

@scaffold_app.command("new")
def scaffold_new(
    template: str = typer.Argument(..., help="Template name (e.g. python-lib)"),
    name:     str = typer.Argument(..., help="Project name"),
    output:   str = typer.Option(".", "--output", "-o", help="Output directory"),
):
    """Scaffold a new project from a template."""
    s = Scaffolder()
    path = s.create(template, name, output)
    console.print(f"[green]✔[/] Created [bold]{name}[/] at {path}")

@snippet_app.command("add")
def snippet_add(
    name:    str = typer.Argument(...),
    file:    str = typer.Option(..., "--file", "-f", help="Source file"),
    tags:    str = typer.Option("", "--tags", "-t", help="Comma-separated tags"),
):
    """Save a file as a named snippet."""
    store = SnippetStore()
    store.add(name, file, tags.split(",") if tags else [])
    console.print(f"[green]✔[/] Snippet [bold]{name}[/] saved")
```

---

## 4. Running Tasks with molt run

```bash
# Scaffold a new Python library project
$ molt run dev -- scaffold new python-lib myutils --output ~/projects
✔ Created myutils at /home/alice/projects/myutils

# List available templates
$ molt run dev -- scaffold list
┌──────────────────┬─────────────────────────────────────────┐
│ Template         │ Description                             │
├──────────────────┼─────────────────────────────────────────┤
│ python-lib       │ Python library with pyproject.toml      │
│ fastapi-service  │ FastAPI service with Docker + CI        │
│ cli-tool         │ Click/Typer CLI with molt               │
└──────────────────┴─────────────────────────────────────────┘

# Run the test suite
$ molt run test
========================= test session starts ==========================
platform darwin -- Python 3.11.8, pytest-8.1.1
collected 31 items

tests/test_scaffold.py::test_python_lib_template PASSED
tests/test_scaffold.py::test_fastapi_template PASSED
tests/test_scaffold.py::test_invalid_template PASSED
tests/test_snippets.py::test_add_snippet PASSED
tests/test_snippets.py::test_list_snippets PASSED
tests/test_snippets.py::test_get_snippet PASSED
...
========================= 31 passed in 2.14s ===========================

# Lint the codebase
$ molt run lint
All checks passed.
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: devkit
  entry: devkit.cli:app
  output: dist/devkit

  include:
    - src/devkit/
    - src/devkit/templates/

  python_version: "3.11"
  compress: true

commands:
  scaffold:
    description: "Scaffold a new project from a template"
    run: devkit scaffold

  snippet:
    description: "Manage code snippets"
    run: devkit snippet

  init:
    description: "Initialize a repo with devkit defaults"
    run: devkit init
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling devkit and 62 packages
✔ Embedding templates directory
✔ Writing dist/devkit (22.1 MB)

$ ./dist/devkit --help
Usage: devkit [OPTIONS] COMMAND [ARGS]...

  devkit — developer productivity toolkit

Options:
  --help  Show this message and exit.

Commands:
  scaffold  Project scaffolding commands
  snippet   Snippet management commands
  init      Initialize a repo with devkit defaults
```

---

## 6. Distribute to Teammates Without Python

Package and share via your team's internal artifact store or a shared drive:

```bash
# Upload to S3 (or any internal store)
$ aws s3 cp dist/devkit s3://company-tools/devkit/latest/devkit-linux-amd64
upload: dist/devkit to s3://company-tools/devkit/latest/devkit-linux-amd64

# Teammate downloads and installs — no Python, no pip, no virtualenv
$ curl -sSL https://tools.internal/devkit/latest/devkit-linux-amd64 -o devkit
$ chmod +x devkit && sudo mv devkit /usr/local/bin/devkit

# First use — works immediately
$ devkit --version
devkit 0.2.0

$ devkit scaffold new fastapi-service payments-service --output ~/work
✔ Created payments-service at /home/bob/work/payments-service

$ ls ~/work/payments-service
pyproject.toml  src/  Dockerfile  .github/  README.md
```

### One-liner install script for the team wiki

```bash
# install-devkit.sh
#!/usr/bin/env bash
set -euo pipefail
LATEST=$(curl -sSL https://tools.internal/devkit/latest/version.txt)
curl -sSL "https://tools.internal/devkit/${LATEST}/devkit-linux-amd64" -o /tmp/devkit
chmod +x /tmp/devkit
sudo mv /tmp/devkit /usr/local/bin/devkit
echo "devkit ${LATEST} installed to /usr/local/bin/devkit"
```

---

## Tips

- Typer's `[all]` extra includes `rich` for pretty help text and `shellingham` for shell
  completion — include these in the binary for the best teammate experience.
- Use Jinja2 `{{ name | kebab_case }}` filters in templates; register them in a custom
  `Environment` in `scaffold.py` so they resolve at bundle time.
- Store templates inside the package (`src/devkit/templates/`) and use
  `importlib.resources` to load them — this ensures they are embedded correctly in the
  binary by molt's bundler.
- Automate binary publishing in CI: on every tag push, build and upload to S3, then
  update `version.txt` so the install script always fetches the latest.
- Enable shell completion by instructing teammates to run `devkit --install-completion`
  after installation; Typer handles bash/zsh/fish automatically.
