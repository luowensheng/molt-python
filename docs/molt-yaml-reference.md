# molt.yaml Reference

`molt.yaml` is the build manifest for **single-binary distribution** (`molt build`). It describes what Python code and commands get embedded into the self-contained binary.

> This file is only needed for `molt build`. Ordinary projects use only `pyproject.toml`.

---

## Minimal example

```yaml
version: 1

project:
  name: my-app
  version: 0.1.0

commands:
  default: run

  run:
    exec: [python, main.py]
```

```bash
molt build          # produces ./my-app
./my-app            # runs python main.py from the embedded payload
./my-app molt version   # shows embedded manifest version
```

---

## Full reference

```yaml
# Schema version — always 1.
version: 1

# ── Project metadata ──────────────────────────────────────────────────────────
project:
  name: my-app          # Binary name (required)
  version: 0.1.0        # Version string embedded in the binary (required)
  python: "3.12"        # Pin a specific Python version (optional)
                        # If omitted, uses the project's current Python.

# ── Dependency strategy ───────────────────────────────────────────────────────
deps:
  strategy: pyproject   # "pyproject" (default) — read deps from pyproject.toml
                        # "lockfile"            — embed from uv.lock exactly
                        # "none"                — no deps embedded (script-only)

# ── Files to embed ────────────────────────────────────────────────────────────
# Glob patterns relative to the project root. Matched files are included in
# the binary payload. If omitted, all non-ignored project files are embedded.
include:
  - "src/**/*.py"
  - "main.py"
  - "assets/**"

# Patterns to exclude (globs, like .gitignore). Merged with built-in defaults
# (.git, __pycache__, *.pyc, .env, *.key, uv.lock, etc.)
exclude:
  - "tests/**"
  - "docs/**"
  - "*.md"

# ── Embedded commands ─────────────────────────────────────────────────────────
# Subcommands the binary exposes. Invoked as: ./my-app <command-name> [args...]
# The special key "default" names the command run when no subcommand is given.
commands:
  default: run          # Name of the default command

  run:
    exec: [python, main.py]           # Argv passed to the embedded Python
    env:
      APP_ENV: production             # Extra env vars set for this command only

  server:
    exec: [python, -m, uvicorn, app:app, --host, 0.0.0.0]

  version:
    exec: [python, main.py, --version]

# ── Integrity ─────────────────────────────────────────────────────────────────
integrity:
  verify_on_install: true   # Run molt verify-binary after installation (default false)
                            # Fails if the payload SHA-256 doesn't match.
```

---

## `deps.strategy` values

| Strategy | Description |
|---|---|
| `pyproject` | Resolve and embed dependencies from `pyproject.toml` (default) |
| `lockfile` | Embed the exact set of packages from `uv.lock` (most reproducible) |
| `none` | No site-packages embedded; useful for scripts with no dependencies |

---

## `include` / `exclude` glob syntax

Uses [doublestar](https://github.com/bmatcuk/doublestar) glob syntax:

| Pattern | Matches |
|---|---|
| `*.py` | Python files in the root |
| `src/**/*.py` | All `.py` files under `src/` recursively |
| `assets/**` | Everything under `assets/` |
| `!tests/**` | Exclude tests (use `exclude:` key instead) |

**Always excluded by default:** `.git/`, `__pycache__/`, `*.pyc`, `.env`, `*.key`, `*.pem`, `uv.lock`, `molt.yaml`, `.claude/`.

---

## Embedded binary commands

Inside a built binary, molt exposes a hidden `molt` subcommand:

```bash
./my-app molt version   # print embedded molt.yaml version
./my-app molt inspect   # show full embedded manifest
./my-app molt verify    # verify payload integrity
```

---

## Complete example (demo-06)

```yaml
version: 1

project:
  name: sysinfo
  version: 0.1.0
  python: "3.11"

deps:
  strategy: pyproject

include:
  - "sysinfo.py"

commands:
  default: info
  info:
    exec: [python, sysinfo.py, info]
  bench:
    exec: [python, sysinfo.py, bench]
  version:
    exec: [python, sysinfo.py, version]

integrity:
  verify_on_install: true
```

See [demos/06-binary-dist](../demos/06-binary-dist/) for the full working example.
