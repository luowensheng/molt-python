# molt

**Ship Python projects as single, verifiable, hermetic binaries.**

molt packages a Python project — source, dependencies, assets, Python interpreter itself — into one executable that installs itself on the target machine, sets up a hermetic environment, and runs. No `pip install` on the target. No "works on my machine." No guessing which `requirements.txt` you meant.

```bash
$ molt build
  ✓ Created: myapp-v2.3.1 (42.1 MB)
  root_hash: 3f8a2c1b…d21c

$ scp myapp-v2.3.1 prod:/usr/local/bin/
$ ssh prod './myapp-v2.3.1 install && ./myapp-v2.3.1 run'
```

That's it. The binary carries everything it needs.

---

## What's in the binary

```
┌─────────────────────────────┐
│  launcher (Go)              │  install / run / verify / uninstall
├─────────────────────────────┤
│  payload (tar.gz)           │  your code, molt.yaml, deps manifest,
│    src/ ...                 │  integrity record, optional assets
│    .molt/manifest.json      │
│    .molt/integrity.json     │
├─────────────────────────────┤
│  trailer                    │  [payload offset][root_hash][magic]
└─────────────────────────────┘
```

At install time the launcher extracts the payload, creates a venv, installs dependencies per the declared strategy (`requirements` / `pyproject` / `poetry` / `pipenv` / `none`), and verifies the SHA-256 root hash matches what's baked into the binary's trailer. Any tampering with the payload fails the check before a single line of app code runs.

## Who this is for

- **Shipping Python to servers you don't control** — customer boxes, isolated networks, air-gapped environments
- **Reproducible deploys** — a hashed artifact anyone can audit file-by-file
- **Adopting existing projects** — Django apps, Flask APIs, Celery workers, data pipelines. No restructuring required.
- **Not a replacement for PyPI/wheels** — if you publish a library, keep using those

## Install

```bash
# macOS / Linux
curl -sSf https://molt.dev/install.sh | sh

# From source
go install molt@latest
```

Requires uv (auto-installed on first use) and a working Go toolchain when cross-compiling.

## 30-second tour

```bash
# New project (greenfield)
molt new project myapp --type api
cd myapp
molt build                         # → myapp-v0.1.0

# Existing project (adopt)
cd my-existing-django-app
molt adopt                         # interactive — generates molt.yaml
molt build                         # → my-existing-django-app-v1.0.0

# Inspect any molt binary
molt inspect ./myapp-v0.1.0        # summary
molt inspect ./myapp-v0.1.0 --files  # full packaged-file table
molt verify-binary ./myapp-v0.1.0 --deep  # re-hash every file

# Compare two builds
molt diff ./myapp-v1.0.0 ./myapp-v1.1.0
```

## molt.yaml at a glance

One file describes everything molt needs to package and run your app:

```yaml
version: 1

project:
  name: myapp
  version: 2.3.1
  python: "3.11"

deps:
  strategy: requirements
  files: [requirements.txt]

include:
  - "src/**/*.py"
  - "templates/"
  - "static/"

assets:
  files:
    - path: models/weights.bin
      required: true

commands:
  default: web
  web:
    exec: [gunicorn, "myapp:app", "--bind", "0.0.0.0:8000"]
  worker:
    exec: [celery, "-A", "myapp", "worker"]
  migrate:
    exec: [python, "manage.py", "migrate"]

hooks:
  post_install:
    - "python manage.py collectstatic --noinput"
    - "python manage.py migrate --noinput"

integrity:
  verify_on_install: true   # default
  verify_on_launch: false   # opt-in (startup cost scales with payload size)
```

`molt.yaml` is orthogonal to `pyproject.toml`. pyproject continues describing the Python *package* (name, deps, build system). molt.yaml describes the deployment *artifact* (what ships, how to run it, integrity policy). Neither duplicates the other.

## Integrity, by default

Every molt binary carries a **root hash** — a single SHA-256 computed from the path and contents of every packaged file, then embedded in the trailer. The launcher refuses to run a binary whose extracted payload doesn't match.

You also get an audit manifest, `myapp-v2.3.1.manifest.json`, written alongside the binary — human-readable, diff-friendly, SBOM-friendly:

```json
{
  "app": {"name": "myapp", "version": "2.3.1"},
  "root_hash": "3f8a2c1bd21c…",
  "payload": {
    "total_files": 1247,
    "total_bytes": 42317819,
    "files": [
      {"path": "src/myapp/__init__.py", "size": 142, "sha256": "a1b2…", "source": "include"},
      {"path": "src/models/weights.bin", "size": 41943040, "sha256": "9f3e…", "source": "asset"}
    ]
  }
}
```

Two separate goals, same data:
- **Authentication** — trailer root_hash == what the launcher recomputes
- **Transparency** — the manifest tells anyone what shipped

> Note: integrity catches *tampering*, not *impersonation*. A determined attacker who rebuilds the binary produces a perfectly valid trailer. If you need cryptographic authenticity (signature verification against a trusted key), that's a separate feature — planned, not shipped.

## Commands

| Command | What it does |
|---|---|
| `molt new` | Scaffold a greenfield project |
| `molt adopt` | Generate a `molt.yaml` for an existing project |
| `molt build` | Produce the binary + integrity manifest |
| `molt run <cmd>` | Run a named command from molt.yaml (wrapping the built binary's semantics for local dev) |
| `molt inspect <bin>` | Print a binary's embedded manifest |
| `molt verify-binary <bin>` | Check trailer ↔ manifest consistency; `--deep` re-hashes the payload |
| `molt diff <a> <b>` | Compare two binaries' manifests |
| `molt deps`, `molt env`, `molt hash`, `molt python` | Diagnostic tooling |

See [USAGE.md](./USAGE.md) for complete examples of every command and every `molt.yaml` field.

## How this compares

| | molt | PyInstaller / Nuitka | Docker | wheel + pip |
|---|---|---|---|---|
| Single binary | ✓ | ✓ | ✗ | ✗ |
| No runtime on target | ✓ | ✓ | ✗ (needs Docker) | ✗ (needs Python + pip) |
| Integrity-verified | ✓ | ✗ | ✓ (image digest) | ✓ (wheel hashes) |
| Hermetic (own Python) | ✓ | partial | ✓ | ✗ |
| Works on existing projects without restructuring | ✓ | ✗ (needs entrypoint tuning) | ✓ | N/A |
| Multi-command (web + worker + migrate from one artifact) | ✓ | ✗ | ✓ (multi-ENTRYPOINT) | ✗ |

## Status

Actively developed. The `molt.yaml` schema is `version: 1` and will stay compatible within major releases. Binary trailer format is versioned (`MOLT0001`); old binaries keep working.

## License

Apache-2.0.

## Further reading

- [USAGE.md](./USAGE.md) — every command, every field, real `molt.yaml` files for Django/FastAPI/CLI/worker projects
- `molt <cmd> --help` — built-in help for each subcommand
