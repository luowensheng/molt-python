# molt

**The hermetic Python toolchain.** Manage Python versions and dependencies with a shared global package store — then ship as a single verifiable binary.

```bash
# Development: packages live once in ~/.molt/pkg/, shared across all projects
molt init myapp && cd myapp
molt add fastapi uvicorn
molt run dev                        # exec under store PYTHONPATH — no venv activation

# Distribution: package as a self-contained binary
molt build                          # → myapp-v0.1.0 (42 MB, integrity-verified)
scp myapp-v0.1.0 prod:/usr/local/bin/
ssh prod './myapp-v0.1.0 install && ./myapp-v0.1.0 run'
```

---

## Why molt

Python packaging is fragmented. You need `pyenv` for Python versions, `uv`/`pip` for packages, each project gets its own `.venv` with duplicate copies of every dependency, and shipping to a server still requires Python and pip on the target machine.

molt unifies the development and distribution sides under one tool:

- **Global package store** — wheels are unpacked once into `~/.molt/pkg/{name}/{version}/{abi}/` and shared across every project. `molt sync` populates it; a second project with the same dep hits the cache with zero download or disk cost.
- **No per-project `.venv`** — `molt run` builds `PYTHONPATH` directly from the global store. Switch Python versions with `molt python use 3.13` and re-sync; no venv rebuild needed.
- **Hermetic binaries** — `molt build` packages your source, dependencies, and optionally the Python interpreter into one self-installing executable with a SHA-256 integrity trailer. Ship it anywhere, no Python or pip required on the target.

---

## Install

```bash
# macOS / Linux
curl -sSf https://molt.dev/install.sh | sh

# From source (requires Go 1.22+)
git clone https://github.com/yourorg/molt && cd molt
go build -o molt . && mv molt /usr/local/bin/
```

Requires [uv](https://github.com/astral-sh/uv) (auto-downloaded on first use).

---

## Quick start

### New project

```bash
molt init myapp
cd myapp
molt add requests black pytest      # → installs into ~/.molt/pkg/, writes .molt/syspath.json
molt run python -c 'import requests; print(requests.__version__)'
molt run pytest
```

### Existing project

```bash
cd my-existing-project              # must have pyproject.toml + uv.lock
molt sync                           # populate ~/.molt/pkg/ from uv.lock
molt run python                     # your project's Python, store PYTHONPATH applied
```

### Ship as a binary

```bash
molt adopt                          # generate molt.yaml from your project layout
molt build                          # → myapp-v1.0.0 (self-installing binary)
```

---

## The global package store

Packages live in `~/.molt/pkg/` keyed by `(name, version, py-abi-platform)`:

```
~/.molt/
├── pkg/
│   ├── requests/2.32.3/py3-none-any/       ← shared across every project
│   ├── black/24.4.2/cp312-cp312-macosx_11_0_arm64/
│   └── ...
├── python/3.12.3/                           ← standalone interpreters
└── registry.json                            ← GC manifest
```

Per project:

```
myproject/
├── pyproject.toml
├── uv.lock
└── .molt/
    ├── syspath.json          ← interpreter path + ordered store dirs
    ├── sitecustomize.py      ← processes .pth files in each store dir
    └── bin/
        ├── black             ← console-script shim (absolute paths baked in)
        └── pytest
```

**How `molt run` works** — no venv activation, no `source .venv/bin/activate`:

1. Loads `.molt/syspath.json`
2. Sets `PYTHONPATH=<project>/.molt:<store_dir_1>:<store_dir_2>:…`
3. Prepends `.molt/bin/` to `PATH`
4. Strips `VIRTUAL_ENV` and `PYTHONHOME`
5. `syscall.Exec`s the pinned interpreter or the requested binary

---

## What's in the binary

```
┌────────────────────────┐
│  launcher (Go)         │  install / run / verify / uninstall
├────────────────────────┤
│  payload (tar.gz)      │  your code + deps + assets + integrity manifest
├────────────────────────┤
│  trailer               │  [payload offset][root_hash][MOLT0001]
└────────────────────────┘
```

On the target machine — no Python, no pip:

```bash
./myapp-v1.0.0 install       # extracts, verifies root hash, runs post_install hooks
./myapp-v1.0.0 run           # runs default command in hermetic environment
./myapp-v1.0.0 run migrate   # named command from molt.yaml
```

---

## Commands

### Development

| Command | What it does |
|---|---|
| `molt init [name]` | Scaffold a new project (via uv init) |
| `molt add <pkg...>` | Add dependency, re-lock, re-sync store |
| `molt remove <pkg...>` | Remove dependency, re-lock, re-sync store |
| `molt sync` | Install uv.lock into `~/.molt/pkg/`, write `.molt/syspath.json` |
| `molt sync --frozen` | Use existing uv.lock as-is |
| `molt sync --refresh` | Force-reinstall all packages |
| `molt lock` | Regenerate `uv.lock` only |
| `molt run <task>` | Run a named task from `[tool.molt.tasks]` |
| `molt run <binary>` | Exec a binary under the project store environment |
| `molt run python` | Launch the project's pinned interpreter |
| `molt task list/add/remove` | Manage tasks in `pyproject.toml` |
| `molt gc` | Remove `~/.molt/pkg` entries no project references |
| `molt gc --dry-run` | Preview what GC would remove |
| `molt info` | Project + environment summary |

### Python version management

| Command | What it does |
|---|---|
| `molt python list` | List installed and system Pythons |
| `molt python install 3.13` | Download and install a Python version via uv |
| `molt python use 3.13` | Pin project Python; re-sync to pick up new ABI |
| `molt python use 3.13 --global` | Set global default Python |
| `molt python which` | Show the active Python path |
| `molt python audit` | Find every Python on the machine |
| `molt python isolation-check` | Verify `.molt/syspath.json` is clean |

### Build and distribution

| Command | What it does |
|---|---|
| `molt adopt` | Generate `molt.yaml` for an existing project |
| `molt build` | Produce a self-installing binary + integrity manifest |
| `molt capture` | Capture a target machine's environment manifest |
| `molt assemble` | Assemble binary from a captured manifest |
| `molt inspect <bin>` | Print a binary's embedded manifest |
| `molt verify-binary <bin>` | Check trailer ↔ manifest consistency |
| `molt diff <a> <b>` | Compare two binaries' manifests |

### Diagnostics

| Command | What it does |
|---|---|
| `molt doctor` | System diagnostics: uv, Python, Go, git |
| `molt uv path` | Print the resolved uv binary path |
| `molt uv version` | Print the uv version |

---

## Task runner

Define tasks in `pyproject.toml`:

```toml
[tool.molt.tasks]
dev     = "uvicorn myapp.main:app --reload"
test    = "pytest tests/ -v --tb=short"
lint    = "ruff check ."
format  = "ruff format ."
```

Run them:

```bash
molt run dev          # executes under store PYTHONPATH — black, pytest, etc. in .molt/bin/
molt run test
molt run test -k test_auth      # extra args forwarded to the task
```

If the name isn't a task, `molt run` treats it as a binary exec:

```bash
molt run black --check .
molt run python -c 'import sys; print(sys.path)'
```

---

## molt.yaml — for distribution

`molt.yaml` describes the deployment artifact. It is separate from `pyproject.toml`, which continues to describe the Python package.

```yaml
version: 1

project:
  name: myapp
  version: 2.3.1
  python: "3.12"

deps:
  strategy: pyproject          # reads pyproject.toml + uv.lock

include:
  - "src/**/*.py"
  - "templates/"
  - "static/"

commands:
  default: web
  web:
    exec: [gunicorn, "myapp:app", "--bind", "0.0.0.0:8000"]
  worker:
    exec: [celery, "-A", "myapp", "worker"]
  migrate:
    exec: [python, manage.py, migrate]

hooks:
  post_install:
    - "python manage.py migrate --noinput"

integrity:
  verify_on_install: true
```

---

## How this compares

| | molt | pip + venv | PyInstaller | Docker |
|---|---|---|---|---|
| Shared package cache across projects | ✓ | ✗ (per-venv copies) | ✗ | ✗ |
| No `.venv` per project | ✓ | ✗ | N/A | N/A |
| Single binary for distribution | ✓ | ✗ | ✓ | ✗ |
| No Python required on target | ✓ | ✗ | ✓ | ✗ (needs Docker) |
| Integrity-verified artifact | ✓ | ✗ | ✗ | ✓ (image digest) |
| `.pth` / namespace packages | ✓ | ✓ | partial | ✓ |
| Multiple entry points in one binary | ✓ | ✗ | ✗ | ✓ |

---

## Status

Actively developed. The global store layout is stable. The `molt.yaml` schema is `version: 1` and stays compatible within major releases. Binary trailer format is versioned (`MOLT0001`).

## License

Apache-2.0.

## Further reading

- [USAGE.md](./USAGE.md) — full reference: every command, the store internals, molt.yaml fields, complete examples
- [docs/global-store.md](./docs/global-store.md) — deep dive into the global package store architecture
- `molt <cmd> --help` — built-in help for each subcommand
