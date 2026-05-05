# molt — Usage Guide

A practical handbook. Every section leads with a working example; reference material follows.

**Table of contents:**

**Development workflow (global package store)**
1. [Quick start — new project](#1-quick-start--new-project)
2. [Syncing an existing project](#2-syncing-an-existing-project)
3. [Adding and removing dependencies](#3-adding-and-removing-dependencies)
4. [Running your project](#4-running-your-project)
5. [Task runner](#5-task-runner)
6. [Python version management](#6-python-version-management)
7. [The global package store](#7-the-global-package-store)
8. [Garbage collection](#8-garbage-collection)

**Distribution workflow (hermetic binaries)**
9. [Adopting an existing project](#9-adopting-an-existing-project)
10. [molt.yaml reference](#10-moltyaml-reference)
11. [Building](#11-building)
12. [Cross-compiling](#12-cross-compiling)
13. [Advanced builds: capture + assemble](#13-advanced-builds-capture--assemble)
14. [Assets: shipping non-code files](#14-assets-shipping-non-code-files)
15. [Commands: multiple entrypoints from one binary](#15-commands-multiple-entrypoints-from-one-binary)
16. [Hooks: pre/post install](#16-hooks-prepost-install)
17. [Integrity: verify, inspect, diff](#17-integrity-verify-inspect-diff)

**Reference**
18. [uv integration](#18-uv-integration)
19. [Environment variables](#19-environment-variables)
20. [Complete molt.yaml examples](#20-complete-moltyaml-examples)
21. [Troubleshooting](#21-troubleshooting)

---

## 1. Quick start — new project

```bash
# Bootstrap
molt init myapp
cd myapp

# Add dependencies
molt add fastapi uvicorn httpx
molt add --dev pytest ruff mypy

# Run
molt run dev                     # task defined in pyproject.toml
molt run python                  # your project's pinned Python
molt run pytest tests/ -v        # any binary, under store environment
```

Under the hood, `molt init` calls `uv init`, then `molt add` calls `uv add`, re-locks, and syncs the global store. No `.venv` is created.

---

## 2. Syncing an existing project

Any project with a `pyproject.toml` and `uv.lock` can be synced:

```bash
cd /path/to/existing-project
molt sync
```

**What `molt sync` does:**

```
1. Ensure uv binary (download once to ~/.molt/uv/ if not found)
2. Resolve interpreter from .python-version or ~/.molt/.python-version
   └─ auto-install if version set but not installed
3. Regenerate uv.lock if pyproject.toml is newer (skipped with --frozen)
4. Parse uv.lock → list of (name, version, wheels[])
5. Detect interpreter ABI → py_tag, abi_tag, platform_tags[]
6. Acquire ~/.molt/pkg.lock (flock — concurrent syncs serialize here)
7. For each package:
   a. Select best wheel per PEP 425 ABI matching
   b. Compute store key from wheel filename (PEP 427)
   c. If store entry has .ok → skip (cache hit, sub-millisecond)
   d. Else: find wheel in uv's cache or download it
   e. Unpack into .tmp/{rand}/, write .ok, rename atomically to final key
8. Topo-sort packages by dependency graph
9. Write .molt/syspath.json  (interpreter path + ordered store dirs)
10. Write .molt/sitecustomize.py  (calls site.addsitedir() for .pth support)
11. Regenerate .molt/bin/<script> shims for every console_scripts entry point
12. Update ~/.molt/registry.json  (used for GC)
```

**Flags:**

| Flag | Effect |
|---|---|
| `molt sync` | Re-lock if stale, then install |
| `molt sync --frozen` | Skip `uv lock`; fail if uv.lock is missing |
| `molt sync --refresh` | Force-reinstall all packages even if cached |

**Second sync is instant:** all store entries have `.ok`; `molt sync` just re-writes `syspath.json` and shims.

---

## 3. Adding and removing dependencies

```bash
molt add requests         # adds to pyproject.toml, re-locks, re-syncs
molt add --dev pytest     # dev dependency
molt remove requests      # removes, re-locks, re-syncs
```

These are thin wrappers around `uv add`/`uv remove` followed by `molt sync`. The store is updated but orphaned packages are not immediately deleted — run `molt gc` to clean them up.

---

## 4. Running your project

`molt run` never activates a venv. It builds the environment from `.molt/syspath.json` and execs:

```bash
molt run <task-name>          # task from [tool.molt.tasks] in pyproject.toml
molt run python               # project's pinned interpreter
molt run python script.py     # script under store PYTHONPATH
molt run pytest tests/ -v     # binary from .molt/bin/ or PATH
molt run black --check .       # console-script shim in .molt/bin/
```

**Environment built by `molt run`:**

| Variable | Value |
|---|---|
| `PYTHONPATH` | `<project>/.molt:<store_dir_1>:<store_dir_2>:…` |
| `PATH` | `<project>/.molt/bin:$PATH` |
| `VIRTUAL_ENV` | unset |
| `PYTHONHOME` | unset |

The `.molt/` directory is first on `PYTHONPATH` so `sitecustomize.py` is imported before any user code. `sitecustomize.py` calls `site.addsitedir(d)` for each store dir, which processes `.pth` files — this is how `setuptools`, `pkg_resources`, and namespace packages work.

**Special cases:**
- `molt run python` / `molt run python3` → resolves to `spec.Python` from `syspath.json`
- `molt run <name>` → checks `.molt/bin/` first, then `PATH`, then errors

**Require a sync first:** if `.molt/syspath.json` doesn't exist, `molt run` prints `run 'molt sync' first` and exits 1.

---

## 5. Task runner

Define tasks in `pyproject.toml`:

```toml
[tool.molt.tasks]
dev      = "uvicorn myapp.main:app --reload"
test     = "pytest tests/ -v --tb=short --cov"
lint     = "ruff check ."
format   = "ruff format ."
typecheck = "mypy myapp/"
seed     = "python scripts/seed.py"
migrate  = "alembic upgrade head"
```

Or inline table form with description and working directory:

```toml
[tool.molt.tasks]
docker-build = { command = "docker build -t myapp .", description = "Build Docker image" }
```

**Using tasks:**

```bash
molt run dev                       # run by name
molt run test                      # tasks run under .molt/syspath.json environment
molt run test -k test_auth         # extra args forwarded directly
molt run test tests/unit -v        # any positional/flag args forwarded
molt run test --watch              # molt watches files and re-runs on change

molt task list                     # show all tasks + commands
molt task add bench "python -m cProfile -o prof.out scripts/bench.py"
molt task remove bench
```

**Fallthrough:** if the name is not a known task, `molt run` treats it as a binary to exec. So `molt run black --check .` works even without a `black` task entry.

---

## 6. Python version management

```bash
molt python list                   # show all installed + system Pythons
molt python install 3.13           # download via uv python install
molt python use 3.13               # pin .python-version; re-sync required
molt python use 3.13 --global      # set ~/.molt/.python-version
molt python which                  # show active Python path
molt python remove 3.11            # uninstall a standalone Python
molt python audit                  # find every Python on the machine
molt python conflicts              # check for PYTHONPATH/PYTHONHOME pollution
molt python isolation-check        # verify .molt/syspath.json is clean
```

**Switching Python versions:**

```bash
molt python install 3.13
molt python use 3.13               # writes .python-version
molt sync                          # picks new interpreter, populates ABI-specific store entries
                                   # e.g. ~/.molt/pkg/black/24.4.2/cp313-cp313-macosx_11_0_arm64/
```

Console-script shims in `.molt/bin/` are regenerated on every sync with the new interpreter path baked in. Old ABI-specific entries (e.g. the `cp312` wheels) remain in the store until `molt gc`.

**`molt python list` output:**

```
  3.13.2       standalone   /Users/you/.molt/python/3.13.2/bin/python3  ← active
  3.12.3       standalone   /Users/you/.molt/python/3.12.3/bin/python3
  3.11.9       system       /usr/bin/python3.11
```

---

## 7. The global package store

### Layout

```
~/.molt/
├── pkg/
│   ├── requests/
│   │   └── 2.32.3/
│   │       └── py3-none-any/
│   │           ├── requests/              ← importable directly
│   │           ├── requests-2.32.3.dist-info/
│   │           ├── .ok                    ← sentinel; presence = install is complete
│   │           └── .meta.json             ← {wheel_filename, source_sha256, entry_points}
│   ├── black/
│   │   └── 24.4.2/
│   │       └── cp312-cp312-macosx_11_0_arm64/
│   │           ├── black/
│   │           ├── ...
│   │           ├── .ok
│   │           └── .meta.json
│   └── .tmp/                              ← staging; cleaned after each install
├── python/                                ← standalone interpreters
├── pkg.lock                               ← flock file; prevents concurrent installs
└── registry.json                          ← {projectDir: lockHash}; used by gc
```

### Store key

The directory path is `~/.molt/pkg/{name}/{version}/{py_tag}-{abi_tag}-{platform_tag}/`.

- Name is PEP 503-normalized (lowercase; runs of `_`, `.`, `-` collapsed to `-`)
- Tags come from the wheel filename per PEP 427

Two projects that need `requests==2.32.3` share exactly one directory. Two projects that need `black==24.4.2` but use different Python versions get separate ABI-keyed directories.

### Wheel selection

When a package has multiple wheels in `uv.lock`, molt picks the best one for the active interpreter:

| Score | Condition |
|---|---|
| 0 (best) | Exact match: `py_tag` and `abi_tag` both match interpreter |
| 10 | abi3 wheel: `abi3` tag, CPython, wheel's minimum version ≤ interpreter |
| 20 | Pure-Python: `none` abi + `py3` or matching py_tag |

Within a score, platform-specific (`macosx_14_0_arm64`) beats `any`.

### Atomic install

1. Unpack wheel into `~/.molt/pkg/.tmp/{rand}/`
2. Write `.meta.json`
3. Write `.ok` (last)
4. `os.Rename` to final path

If two processes race, the second sees `.ok` already present and discards its temp dir. No partial installs are ever visible.

### Console-script shims

`molt sync` generates `.molt/bin/<name>` for every `console_scripts` entry point in any installed package. Example for `black`:

```sh
#!/bin/sh
export PYTHONPATH='/home/you/myproject/.molt:/home/you/.molt/pkg/black/24.4.2/cp312-cp312-linux_x86_64:...'
unset VIRTUAL_ENV PYTHONHOME
exec '/home/you/.molt/python/3.12.3/bin/python3' \
  -c 'import sys; from black import patched_main as _m; sys.exit(_m())' "$@"
```

Paths are absolute. Shims are regenerated on every `molt sync`.

---

## 8. Garbage collection

Packages accumulate when you switch Python versions, remove dependencies, or delete projects. `molt gc` cleans up:

```bash
molt gc --dry-run          # show what would be removed
molt gc                    # remove it
```

**How GC works:**
1. Reads `~/.molt/registry.json` to find all registered project dirs
2. For each live project, parses its `uv.lock` to collect referenced `(name, version, abi)` keys
3. Walks `~/.molt/pkg/`, removes entries not in the referenced set
4. Purges registry entries whose project directory no longer exists

GC is safe to run while other projects are in use — it only removes entries that no current project needs. A subsequent `molt sync` in any affected project would re-download the removed entries.

**Typical GC scenarios:**

```bash
# After switching from Python 3.11 to 3.12 across all projects
molt gc                    # removes cp311-abi entries, keeps cp312

# After deleting a project
rm -rf ~/old-project
molt gc                    # removes entries unique to that project

# After removing a heavy dependency
molt remove torch
molt gc                    # reclaims the torch store entry (can be gigabytes)
```

---

## 9. Adopting an existing project

`molt adopt` detects your project's structure and writes a `molt.yaml` that you can review and edit before building.

```bash
$ cd /path/to/existing-project
$ molt adopt
```

**What it detects:**

| Signal | What it does |
|---|---|
| `pyproject.toml` | reads name, version, optional Python version |
| `.python-version` | sets `project.python` |
| `requirements.txt` (any) | suggests `deps.strategy: requirements` |
| `pyproject.toml` + `uv.lock` | suggests `deps.strategy: pyproject` |
| `Pipfile` | suggests `deps.strategy: pipenv` |
| `poetry.lock` | suggests `deps.strategy: poetry` |
| `manage.py` | suggests Django commands (migrate, shell, collectstatic) |
| `src/` layout | adjusts include glob to `src/**/*.py` |
| Top-level packages | generates per-package include globs |
| Entry hints (`console_scripts`, `__main__.py`) | suggests `commands:` exec entries |

**Flags:**

| Flag | Effect |
|---|---|
| `molt adopt` | current directory, fully interactive |
| `molt adopt ./myapp` | specific directory |
| `molt adopt --non-interactive` | take all detected defaults; suitable for CI |
| `molt adopt --force` | overwrite an existing `molt.yaml` |

After `molt adopt` runs, **review the generated file**. Particularly check:
- `include:` globs — do they match your actual source layout?
- `deps.strategy` and `deps.files`
- `commands:` exec args

---

## 10. molt.yaml reference

`molt.yaml` describes the deployment artifact (what ships, how to run it, integrity policy). It is intentionally separate from `pyproject.toml`, which continues to describe the Python package.

```yaml
# ── Required ──────────────────────────────────────────────────────────────────
version: 1                          # schema version, currently always 1

project:
  name: myapp                       # output binary name; install directory name
  version: 2.3.1                    # semver; used in binary name and manifest
  python: "3.12"                    # optional; falls back to .python-version
  description: "My application"    # shown in 'molt inspect' output

# ── Dependencies ──────────────────────────────────────────────────────────────
deps:
  strategy: requirements            # requirements | pyproject | poetry | pipenv | none
  files:
    - requirements.txt              # required when strategy=requirements
    - requirements-extras.txt       # multiple files are installed in order
  extra_args: ["--no-cache"]        # passed through to 'uv pip install'

# ── File inclusion ─────────────────────────────────────────────────────────────
include:                            # glob allowlist. When absent, legacy denylist runs.
  - "src/**/*.py"                   # doublestar (**) is supported
  - "myapp/templates/**/*"
  - "locale/**/*.po"
  - "config.yaml"                   # exact relative path

exclude:                            # always applied AFTER include
  - "tests/"
  - "**/__pycache__/"
  - "**/*.pyc"
  - ".env"                          # also blocked by the sensitive-file check

# ── Assets ────────────────────────────────────────────────────────────────────
assets:
  files:
    - path: models/weights.bin      # relative path or glob
      required: true                # build FAILS if file is missing
      description: "ML weights"     # shown in 'molt inspect --files'
    - path: data/seed/*.csv
      required: false               # missing is silently skipped
  max_file_size_mb: 100             # warn (not fail) when any single file exceeds this
  max_total_size_mb: 500            # FAIL build if combined payload exceeds this

# ── Commands ──────────────────────────────────────────────────────────────────
commands:
  default: web                      # which command './myapp run' without args picks

  web:
    exec:                           # argv-style; no shell, no injection
      - gunicorn
      - "myapp.wsgi:application"
      - "--bind=0.0.0.0:8000"
    description: "Django WSGI server"
    env:
      DJANGO_SETTINGS_MODULE: "myapp.settings.prod"
    dir: "src"                      # cwd relative to install dir

  tunnel:
    script: "ssh -L 5432:db:5432 bastion"  # shell one-liner; for pipes/redirects

  migrate:
    exec: [python, manage.py, migrate, --noinput]

# ── Environment ───────────────────────────────────────────────────────────────
env:                                # applied to every command
  PYTHONUNBUFFERED: "1"
  DJANGO_SETTINGS_MODULE: "myapp.settings.prod"

# ── Hooks ─────────────────────────────────────────────────────────────────────
hooks:
  pre_install:                      # runs on the BUILD host at build time
    - "pytest tests/smoke -q"
  post_install:                     # runs on the TARGET machine after extraction + venv
    - "python manage.py migrate --noinput"
    - "python manage.py collectstatic --noinput"

# ── Integrity ─────────────────────────────────────────────────────────────────
integrity:
  enabled: true                     # default true; set false to skip manifest write
  algorithm: sha256                 # sha256 (default) | sha512
  verify_on_install: true           # launcher re-hashes on install (default true)
  verify_on_launch: false           # launcher re-hashes on every run (opt-in)
  output: "{name}-v{version}.manifest.json"  # sidecar manifest filename template
```

### Key rules

**`include` vs `exclude`**: `include` is a positive allow-list — only matching files ship. `exclude` subtracts. If `include` is absent entirely, the legacy denylist scan runs (skips `.git/`, `__pycache__/`, `.venv/`, `*.pyc`, common editor backup files, etc.).

**Sensitive file check**: files matching patterns like `*.pem`, `*.key`, `*password*`, `id_rsa`, `.env.local` always fail the build regardless of `include`. There is no flag to disable this.

**`commands.default` resolution** (when `default:` is not set): a command named `run` → `start` → the single command if exactly one exists → `python -m <pkg>.main` as last resort.

**`exec` vs `script`**: mutually exclusive per command. `exec` is argv-style (preferred — no shell). `script` is a shell string.

---

## 11. Building

### Basic build

```bash
molt build
```

Reads `molt.yaml` (or falls back to `pyproject.toml`). Builds for the current platform. Outputs `<name>-v<version>` in the current directory.

```bash
molt build ./myapp              # build from a different project dir
molt build --name foo           # override app name
molt build --version 1.2.3      # override version
molt build --output dist/myapp  # override output path
```

### Build flags

| Flag | Default | Description |
|---|---|---|
| `--name` | directory name | application name |
| `--version` | `0.1.0` | version string |
| `--output` | `<name>-v<version>` | output binary path |
| `--os` | current OS | target OS (`linux`, `darwin`, `windows`) |
| `--arch` | current arch | target arch (`amd64`, `arm64`) |
| `--profile` | `standard` | `minimal` \| `standard` \| `extended` \| `full` |
| `--embed-strict` | `true` | fail on sensitive file matches |
| `--embed-ignore` | `.moltignore` | path to additional ignore file |
| `--best-effort` | `false` | allow cross-platform builds |

### Build output

```
Building myapp v1.0.0 (linux/amd64)...
  Config:   ./molt.yaml
  ✓ Payload: 42.1 MB (1247 files)
  ✓ Integrity manifest: myapp-v1.0.0.manifest.json
  ✓ Created: myapp-v1.0.0 (63.4 MB)
    root_hash: 3f8a2c1bd21c5a0b…
    files:     1247   total: 42.1 MB
```

Two files are produced:
- `myapp-v1.0.0` — the self-installing binary
- `myapp-v1.0.0.manifest.json` — human-readable integrity manifest

**Ship both** to end users. The sidecar manifest lets them run `molt inspect` and `molt diff` without extracting anything from the binary.

### Build profiles

| Profile | Effect |
|---|---|
| `minimal` | Absolute minimum — only direct deps, no extras |
| `standard` | Direct deps + production extras (default) |
| `extended` | Standard + optional performance/monitoring deps |
| `full` | Everything including dev/test tooling (rarely appropriate for prod) |

### .moltignore

Additional file-level ignores written in gitignore syntax:

```gitignore
# .moltignore
*.log
*.sqlite3
testdata/
notebooks/
.DS_Store
```

---

## 12. Cross-compiling

```bash
molt build --os linux --arch amd64 --best-effort
```

**Why `--best-effort` is required**: cross-builds can't be hermetic for native extensions. Python C-extensions must match the target's glibc version, CPU architecture, and OS. The right approach is to build on the target platform.

**CI matrix (recommended):**

```yaml
# .github/workflows/release.yml
jobs:
  build:
    strategy:
      matrix:
        include:
          - os: ubuntu-24.04
            target_os: linux
            target_arch: amd64
          - os: ubuntu-24.04-arm64
            target_os: linux
            target_arch: arm64
          - os: macos-14
            target_os: darwin
            target_arch: arm64
          - os: windows-2022
            target_os: windows
            target_arch: amd64
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - name: Install molt
        run: curl -sSf https://molt.dev/install.sh | sh
      - name: Build
        run: molt build --version ${{ github.ref_name }}
      - uses: actions/upload-artifact@v4
        with:
          name: binary-${{ matrix.os }}
          path: |
            *-v*
            *.manifest.json
```

---

## 13. Advanced builds: capture + assemble

For two-phase builds: capture the environment manifest on the TARGET machine (or in a matching container), then assemble the binary on your build machine. Useful when the target's glibc or Python distribution isn't available on your CI agents.

### Phase 1: capture (on the target machine)

```bash
molt capture
# → linux-amd64.manifest.json
molt capture --output target-env.json
```

### Phase 2: assemble (on the build machine)

```bash
molt assemble \
    --manifest target-env.json \
    --name myapp \
    --version 1.2.3 \
    --output ./dist/myapp-linux-amd64
```

| Flag | Required | Description |
|---|---|---|
| `--manifest` | yes | Path to the captured manifest |
| `--name` | yes | Application name |
| `--version` | no | Default `0.1.0` |
| `--output` | no | Default `<name>-v<version>` |
| `--profile` | no | Build profile |
| `--os`, `--arch` | no | Inferred from manifest if absent |

---

## 14. Assets: shipping non-code files

### `include:` globs — permissive, bulk

```yaml
include:
  - "myapp/templates/**/*.html"
  - "static/**/*"
  - "locale/**/*.po"
```

### `assets:` — explicit, validated

```yaml
assets:
  files:
    - path: models/weights.bin
      required: true           # BUILD FAILS if this file is absent
      description: "Sentence transformer weights"
    - path: "data/seed/*.csv"
      required: false
  max_file_size_mb: 200
  max_total_size_mb: 500
```

### Reading assets in your application

```python
import os
ROOT = os.environ.get("MOLT_ROOT", os.path.dirname(__file__))
weights_path = os.path.join(ROOT, "models", "weights.bin")
```

---

## 15. Commands: multiple entrypoints from one binary

```yaml
commands:
  default: web
  web:
    exec: [gunicorn, "myapp.wsgi:application", "--bind=0.0.0.0:8000"]
  worker:
    exec: [celery, "-A", "myapp", "worker", "-l", "info"]
  migrate:
    exec: [python, manage.py, migrate, --noinput]
  backup:
    script: "pg_dump $DB_URL | gzip > /backups/$(date +%Y%m%d).sql.gz"
```

```bash
./myapp-v1.0.0 run              # runs 'web'
./myapp-v1.0.0 run worker       # runs the worker
./myapp-v1.0.0 run migrate      # one-off migration
./myapp-v1.0.0 run web --workers=8   # extra args pass through
```

**Env resolution order (later wins):**
1. Host environment (minus `PYTHONHOME` / `PYTHONPATH` / `VIRTUAL_ENV`)
2. molt-injected: `PATH` (venv `bin/` first), `VIRTUAL_ENV`, `PYTHONNOUSERSITE=1`
3. Top-level `env:` in `molt.yaml`
4. Per-command `env:`

---

## 16. Hooks: pre/post install

```yaml
hooks:
  pre_install:
    - "pytest tests/smoke -q"         # runs on BUILD HOST at build time
  post_install:
    - "python manage.py migrate --noinput"
    - "python manage.py collectstatic --noinput"
```

`post_install` hooks run on the **target machine**, after payload extraction and venv setup. If any hook exits non-zero, the install aborts cleanly.

Non-interactive hooks only — use `--noinput` flags on all management commands.

---

## 17. Integrity: verify, inspect, diff

Every `molt build` outputs two files:

| File | Purpose |
|---|---|
| `myapp-v1.0.0` | Self-installing binary; root hash embedded in trailer |
| `myapp-v1.0.0.manifest.json` | Human-readable sidecar; full file list with sizes + hashes |

### Inspect

```bash
molt inspect ./myapp-v1.0.0               # summary
molt inspect ./myapp-v1.0.0 --files       # full file table
molt inspect ./myapp-v1.0.0 --json        # raw JSON
molt inspect ./myapp-v1.0.0 --json | jq '.payload.files[] | select(.size > 1048576)'
```

Accepts either the binary or the `.manifest.json` sidecar.

### Verify

```bash
# Fast: trailer ↔ manifest consistency check
molt verify-binary ./myapp-v1.0.0
# ✓ Binary integrity verified.

# Deep: re-streams every file, recomputes root hash from scratch
molt verify-binary ./myapp-v1.0.0 --deep
# ✓ Deep verification passed — every file in the payload was re-hashed.
```

The launcher runs the fast check automatically on `install`. Launch-time re-verification is opt-in:

```yaml
integrity:
  verify_on_launch: true    # appropriate for small CLIs and security-sensitive services
```

### Diff

```bash
molt diff ./myapp-v1.0.0 ./myapp-v1.1.0
```

```
Comparing:
  a: myapp v1.0.0  (root 3f8a2c…)
  b: myapp v1.1.0  (root 8b2d4f…)

Added:   2 file(s)
  + src/myapp/feature_flags.py (4.1 KB)
  + models/v2-weights.bin (180.0 MB)

Removed: 1 file(s)
  - models/v1-weights.bin (50.0 MB)

Changed: 6 file(s)
  ~ src/myapp/__init__.py (+142 bytes)
  ~ requirements.txt (+28 bytes)
  ~ src/myapp/config.py (-56 bytes)
  (3 more)

Total size change: +130014819 bytes (+124.0 MB)
```

### Root hash algorithm

1. Sort all packaged files by path (byte-wise)
2. For each file: `inner = sha256(path || 0x00 || file_sha256_bytes)`
3. Feed each 32-byte `inner` into a running outer SHA-256
4. `root_hash = hex(outer.Sum())`

The trailer is the last 48 bytes of the binary:

```
[payload_offset:  8 bytes, little-endian int64]
[root_hash:      32 bytes, raw SHA-256]
[magic:           8 bytes, "MOLT0001"]
```

---

## 18. uv integration

molt uses [uv](https://github.com/astral-sh/uv) for both dependency resolution (`uv lock`) and Python version management (`uv python`). uv is auto-downloaded to `~/.molt/uv/` on first use.

```bash
molt uv path                      # show where molt resolved uv from
molt uv version                   # show uv version
molt uv <any-uv-args>             # raw passthrough: molt uv pip list, molt uv lock, etc.
```

### uv resolution order

1. `$MOLT_UV` — absolute path override
2. `~/.molt/uv/bin/uv` — molt-managed location
3. `uv` on `$PATH` — system fallback

### Using a specific uv in CI

```yaml
env:
  MOLT_UV: /home/runner/.local/bin/uv
```

---

## 19. Environment variables

**Read by the molt CLI:**

| Variable | Effect |
|---|---|
| `MOLT_UV` | Absolute path to a uv binary. Overrides resolution order entirely. |

**Read by the installed launcher at install/run time (on the target machine):**

| Variable | Effect |
|---|---|
| `MOLT_INSTALL_BASE` | Base directory for installs. App installed at `$BASE/<name>/<version>/`. |
| `MOLT_INSTALL_DIR` | Full install directory. Overrides `MOLT_INSTALL_BASE`. |
| `MOLT_CACHE_DIR` | Cache for downloaded Python interpreters and other artifacts. |
| `MOLT_SKIP_VERIFY` | Set to `1` to skip install-time integrity check. Don't use in production. |
| `MOLT_ROOT` | Set by the launcher at run time. Absolute path to the install dir. |

```bash
# Install to a non-default location
MOLT_INSTALL_BASE=/opt ./myapp-v1.0.0 install
Installing myapp v1.0.0 → /opt/myapp/1.0.0
```

---

## 20. Complete molt.yaml examples

### Django web app

```yaml
version: 1

project:
  name: blogplatform
  version: 4.2.0
  python: "3.12"

deps:
  strategy: requirements
  files: [requirements.txt]

include:
  - "blogplatform/**/*.py"
  - "blogplatform/**/*.html"
  - "templates/**/*"
  - "static/**/*"
  - "locale/**/*"
  - "manage.py"

exclude:
  - "blogplatform/tests/"
  - "**/__pycache__/"

commands:
  default: web
  web:
    exec: [gunicorn, "blogplatform.wsgi:application", "--bind=0.0.0.0:8000", "--workers=4"]
  worker:
    exec: [celery, "-A", "blogplatform", "worker", "-l", "info"]
  beat:
    exec: [celery, "-A", "blogplatform", "beat", "-l", "info"]
  migrate:
    exec: [python, manage.py, migrate, --noinput]
  shell:
    exec: [python, manage.py, shell]

env:
  DJANGO_SETTINGS_MODULE: "blogplatform.settings.production"
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "python manage.py collectstatic --noinput --clear"
    - "python manage.py migrate --noinput"

integrity:
  verify_on_install: true
```

### FastAPI microservice

```yaml
version: 1

project:
  name: orders-api
  version: 1.5.0
  python: "3.12"

deps:
  strategy: pyproject

include:
  - "orders_api/**/*.py"
  - "orders_api/openapi/*.yaml"
  - "alembic/**/*.py"
  - "alembic.ini"

commands:
  default: serve
  serve:
    exec: [uvicorn, "orders_api.main:app", "--host=0.0.0.0", "--port=8080", "--workers=2"]
  migrate:
    exec: [alembic, upgrade, head]

env:
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "alembic upgrade head"

integrity:
  verify_on_install: true
  verify_on_launch: true
```

### Celery worker

```yaml
version: 1

project:
  name: indexer-worker
  version: 0.9.2
  python: "3.12"

deps:
  strategy: requirements
  files: [requirements.txt]

include:
  - "indexer/**/*.py"
  - "indexer/sql/*.sql"

commands:
  default: worker
  worker:
    exec: [celery, "--app=indexer.tasks", "worker", "--loglevel=info", "--concurrency=8"]
  flower:
    exec: [celery, "--app=indexer.tasks", "flower", "--port=5555"]
  purge:
    script: "celery -A indexer.tasks purge -f"

env:
  CELERY_BROKER_URL: "redis://redis:6379/0"
  PYTHONUNBUFFERED: "1"
```

### Data-science CLI with model weights

```yaml
version: 1

project:
  name: classify
  version: 3.0.0
  python: "3.12"

deps:
  strategy: requirements
  files: [requirements.txt]

include:
  - "classify/**/*.py"
  - "classify/configs/*.yaml"

assets:
  files:
    - path: models/sentence-transformers-v2.bin
      required: true
      description: "384-dim sentence embeddings"
    - path: models/tokenizer.json
      required: true
    - path: data/stopwords-en.txt
      required: false
  max_total_size_mb: 400

commands:
  default: classify
  classify:
    exec: [python, "-m", "classify"]
  reindex:
    exec: [python, "-m", "classify.reindex", "--full"]
  benchmark:
    exec: [python, "-m", "classify.bench"]

integrity:
  verify_on_install: true
  verify_on_launch: true
```

---

## 21. Troubleshooting

### `molt sync` — "no python interpreter resolved"

```
error: no python interpreter resolved
```

No `.python-version` file and no global default set. Fix:

```bash
molt python install 3.12
molt python use 3.12
molt sync
```

### `molt run` — "no .molt/syspath.json — run 'molt sync' first"

The project hasn't been synced yet. Run `molt sync`.

### `molt run` — import works in shell but not in molt run

A package was installed outside molt (e.g. `pip install` into the system Python). `molt run` builds `PYTHONPATH` exclusively from `.molt/syspath.json`. Run `molt sync` to make it official or add the package via `molt add`.

### `molt sync` — "no wheel matches cp312/cp312 on [macosx_14_0_arm64]"

The lock file contains no wheel for your current Python/platform. Try:

```bash
molt sync --refresh                  # forces uv to re-resolve
```

If you're on a platform with no pre-built wheel, the package may require compilation from source (not currently supported by the store installer).

### Console script runs the wrong version

Shims in `.molt/bin/` have the interpreter path baked in at sync time. Run `molt sync` after `molt python use <version>`.

### Two projects have conflicting versions of the same package

Each project has its own `syspath.json` pointing to different store paths. The store holds both versions side-by-side under their respective ABI keys — there is no conflict.

### Build fails: "cannot find uv"

```
error: uv not found — install from https://github.com/astral-sh/uv or set MOLT_UV
```

```bash
curl -LsSf https://astral.sh/uv/install.sh | sh
# or
MOLT_UV=/path/to/uv molt build
```

Run `molt doctor` to see the full tool resolution table.

### Build fails: "no files matched inclusion criteria"

Your `include:` patterns didn't match anything. Check whether your project is flat-layout or src-layout:

```yaml
# flat layout
include:
  - "myapp/**/*.py"
  - "*.py"

# src layout
include:
  - "src/**/*.py"
```

### Build fails: "SECURITY BLOCK: sensitive file matched"

A file matched a built-in sensitive pattern. There is no flag to disable this. Add the file to `exclude:` or move it outside the project tree.

### `molt verify-binary` fails with "root_hash mismatch"

The binary was modified after build. Check checksums:

```bash
sha256sum myapp-v1.0.0        # must match on both source and target
```

### `molt inspect` says "binary has no integrity trailer (legacy build)"

Built with an older version of molt. Rebuild, or use the sidecar manifest directly:

```bash
molt inspect ./myapp-v1.0.0.manifest.json
```

### Post-install hook hangs

The hook is waiting for stdin. Use non-interactive flags:

```yaml
hooks:
  post_install:
    - "python manage.py migrate --noinput"
    - "python manage.py collectstatic --noinput --clear"
```

### Binary is unexpectedly large

```bash
# 20 largest packaged files
molt inspect ./myapp-v1.0.0 --files | awk 'NR>1{print $2, $1}' | sort -rh | head -20

# Compare to previous release
molt diff ./myapp-v0.9.0 ./myapp-v1.0.0
```

---

## Getting help

- `molt doctor` — system diagnostics, uv resolution, tool locations
- `molt info` — project + store environment summary
- `molt inspect <binary>` — what's inside a built binary
- `molt diff <a> <b>` — what changed between two builds
- `molt python isolation-check` — verify the project environment is clean
- [docs/global-store.md](./docs/global-store.md) — deep dive into the store architecture
