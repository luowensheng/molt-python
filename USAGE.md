# molt — Usage Guide

A practical handbook. Every section leads with a working example; reference material follows.

**Table of contents:**

1. [What molt does](#1-what-molt-does)
2. [Quick start — your first binary](#2-quick-start--your-first-binary)
3. [Adopting an existing project](#3-adopting-an-existing-project)
4. [molt.yaml reference](#4-moltyaml-reference)
5. [Building](#5-building)
6. [Cross-compiling](#6-cross-compiling)
7. [Advanced builds: capture + assemble](#7-advanced-builds-capture--assemble)
8. [Assets: shipping non-code files](#8-assets-shipping-non-code-files)
9. [Commands: multiple entrypoints from one binary](#9-commands-multiple-entrypoints-from-one-binary)
10. [Hooks: pre/post install](#10-hooks-prepost-install)
11. [Integrity: verify, inspect, diff](#11-integrity-verify-inspect-diff)
12. [uv integration](#12-uv-integration)
13. [Environment variables](#13-environment-variables)
14. [Complete molt.yaml examples](#14-complete-moltyaml-examples)
15. [Troubleshooting](#15-troubleshooting)

---

## 1. What molt does

molt packages a Python project into a single self-contained binary. The binary ships everything needed to install and run the application — Python interpreter (or a reference to one), dependencies, source code, static assets. On the target machine the user runs the binary directly; it installs itself and then runs.

**The build pipeline:**

```
your Python project
       │
       ▼
  molt build
       │
       ├─ 1. compile launcher (tiny Go binary)
       ├─ 2. embed payload: source + assets → deterministic tar.gz
       ├─ 3. compute integrity root hash (SHA-256 over all packaged files)
       ├─ 4. write external .manifest.json sidecar
       └─ 5. assemble: launcher ‖ payload ‖ trailer (payload offset + root hash + magic)
       │
       ▼
 myapp-v1.0.0   ← single executable, ships to users
```

On the target machine:

```
./myapp-v1.0.0 install   ← extracts payload, verifies integrity, runs post_install hooks
./myapp-v1.0.0 run       ← runs default command in the hermetic venv
./myapp-v1.0.0 run web   ← runs named command
```

---

## 2. Quick start — your first binary

Assuming you have an existing Python project with a `pyproject.toml`:

```bash
# Step 1: scaffold molt.yaml interactively
$ molt adopt
molt adopt — detected existing project layout

  pyproject.toml: yes (myapp 1.0.0)
  Python:         3.12 (.python-version)
  strategy:       pyproject

Project name [myapp]:
Version [1.0.0]:
Python version [3.12]:

✓ Wrote molt.yaml

# Step 2: build the binary
$ molt build
Building myapp v1.0.0 (linux/amd64)...
  Config:   ./molt.yaml
  ✓ Payload: 4.2 MB (97 files)
  ✓ Integrity manifest: myapp-v1.0.0.manifest.json
  ✓ Created: myapp-v1.0.0 (12.1 MB)
    root_hash: 3f8a2c1bd21c…

# Step 3: ship the binary to a target machine, install, run
$ scp myapp-v1.0.0 myapp-v1.0.0.manifest.json user@target:~/
$ ssh user@target
$ ./myapp-v1.0.0 install
$ ./myapp-v1.0.0 run
```

If you prefer not to use a `molt.yaml` at all, `molt build` still works — it reads `name` and `version` from `pyproject.toml` and uses a legacy denylist to decide what ships. This is the backward-compatible mode; a `molt.yaml` gives you explicit control.

---

## 3. Adopting an existing project

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
| Entry hints (console_scripts, `__main__.py`) | suggests `commands:` exec entries |

**Flags:**

| Flag | Effect |
|---|---|
| `molt adopt` | current directory, fully interactive |
| `molt adopt ./myapp` | specific directory |
| `molt adopt --non-interactive` | take all detected defaults; suitable for CI |
| `molt adopt --force` | overwrite an existing `molt.yaml` |

After `molt adopt` runs, **review the generated file**. The tool's job is a good starting point, not a final answer. Particularly check:

- `include:` globs — does the pattern match your actual source layout?
- `deps.strategy` and `deps.files` — is this right for how you pin deps?
- `commands:` — are the exec args correct for your WSGI/ASGI setup?

---

## 4. molt.yaml reference

`molt.yaml` describes the deployment artefact. It is intentionally separate from `pyproject.toml`, which continues to describe the Python package itself.

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

**`deps.strategy` values**:

| Strategy | Requirements |
|---|---|
| `requirements` | `files:` list is required. uv installs from those files. |
| `pyproject` | `pyproject.toml` must exist. uv syncs from `uv.lock`. |
| `poetry` | `poetry export` must run at build time (see Troubleshooting). |
| `pipenv` | Same note as poetry. |
| `none` | No deps installed. Useful for pure-stdlib tools. |

---

## 5. Building

### Basic build

```bash
$ molt build
```

Reads `molt.yaml` (or falls back to `pyproject.toml`). Builds for the current platform. Outputs `<name>-v<version>` in the current directory.

```bash
$ molt build ./myapp              # build from a different project dir
$ molt build --name foo           # override app name
$ molt build --version 1.2.3      # override version
$ molt build --output dist/myapp  # override output path
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

Profiles control what dependencies are installed at build time (they don't affect what Python source ships):

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

Useful for files that your `exclude:` patterns haven't caught, or for keeping the ignore config out of `molt.yaml`.

---

## 6. Cross-compiling

```bash
$ molt build --os linux --arch amd64 --best-effort
```

**Why `--best-effort` is required**: cross-builds can't be hermetic for native extensions. Python C-extensions (numpy, grpcio, psycopg2) must match the target's glibc version, CPU architecture, and OS. molt has no safe way to determine which wheel variants to pull on your behalf.

**The right approach**: build on the target platform. CI matrix:

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
        run: |
          curl -L https://github.com/.../molt/releases/latest/download/molt-linux-amd64 -o molt
          chmod +x molt && sudo mv molt /usr/local/bin/molt
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

## 7. Advanced builds: capture + assemble

For two-phase builds: capture the environment manifest on the TARGET machine (or in a matching container), then assemble the binary on your build machine. Useful when the target's glibc or Python distribution isn't available on your CI agents.

### Phase 1: capture (on the target machine)

```bash
$ molt capture
Capturing environment (linux/amd64)...
✓ Wrote linux-amd64.manifest.json
```

This writes a manifest of the target's Python, platform info, and installed packages. Ship this file to your build machine.

```bash
$ molt capture --output target-env.json
$ molt capture --os linux --arch arm64   # for a container/VM
```

### Phase 2: assemble (on the build machine)

```bash
$ molt assemble \
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

## 8. Assets: shipping non-code files

Assets are non-Python files that the application needs at runtime. Two mechanisms exist and are complementary.

### `include:` globs — permissive, bulk

```yaml
include:
  - "myapp/templates/**/*.html"    # all templates
  - "static/**/*"                  # entire static tree
  - "locale/**/*.po"               # translation files
```

Any file matching at least one pattern ships. Missing files are silently skipped. Good for directories you want to embed wholesale.

### `assets:` — explicit, validated

```yaml
assets:
  files:
    - path: models/weights.bin
      required: true           # BUILD FAILS if this file is absent
      description: "Sentence transformer weights (512-dim)"
    - path: "config/defaults.yaml"
      required: true
    - path: "data/seed/*.csv"  # globs work here too
      required: false
  max_file_size_mb: 200        # warn if any single file exceeds this
  max_total_size_mb: 500       # fail if total payload exceeds this
```

Use `assets:` for files where you'd rather fail the build than ship a broken binary.

### Reading assets in your application

Assets land in the payload at the same relative path as they exist in your project. The launcher sets a `MOLT_ROOT` (or equivalent) environment variable so you can locate them:

```python
import os
ROOT = os.environ.get("MOLT_ROOT", os.path.dirname(__file__))
weights_path = os.path.join(ROOT, "models", "weights.bin")
```

### Size tracking

```bash
$ molt inspect ./classify-v3.0.0 --files | sort -k2 -rh | head -10
path                                     size       source   sha256
models/weights.bin                       180.0 MB   asset    9f3e2a...
models/tokenizer.json                    8.2 MB     asset    4a1b3c...
classify/data/synonyms.json              1.4 MB     include  02ef71...
```

The `source` column tells you why each file is in the payload:
- `include` — matched an `include:` glob
- `asset` — declared in `assets:`
- `default` — legacy denylist scan (no `include:` was set)
- `auto` — molt-generated metadata (the integrity manifest itself)

---

## 9. Commands: multiple entrypoints from one binary

One binary, multiple personas.

```yaml
commands:
  default: web
  web:
    exec: [gunicorn, "myapp.wsgi:application", "--bind=0.0.0.0:8000"]
    description: "WSGI server"
  worker:
    exec: [celery, "-A", "myapp", "worker", "-l", "info"]
  beat:
    exec: [celery, "-A", "myapp", "beat", "-l", "info"]
  migrate:
    exec: [python, manage.py, migrate, --noinput]
  shell:
    exec: [python, manage.py, shell]
  backup:
    script: "pg_dump $DB_URL | gzip > /backups/$(date +%Y%m%d).sql.gz"
```

At install time:

```bash
$ ./myapp-v1.0.0 install
[molt] Installing myapp v1.0.0 → /home/you/.local/share/myapp/1.0.0
[molt] Extracting payload...
[molt] Verifying integrity...
[molt] Creating virtual environment...
[molt] Running post_install hooks...
✓ Installed
```

At run time:

```bash
$ ./myapp-v1.0.0 run              # runs 'web' (the default)
$ ./myapp-v1.0.0 run worker       # runs the worker
$ ./myapp-v1.0.0 run migrate      # one-off migration
$ ./myapp-v1.0.0 run web --workers=8   # extra args pass through
```

Or after install:

```bash
$ /home/you/.local/share/myapp/1.0.0/run
```

### `exec` vs `script`

`exec:` is argv-style. Go's `os/exec` handles it directly — no shell, no glob expansion, no injection surface. Strongly preferred.

`script:` is a POSIX shell one-liner for when you genuinely need pipes, redirects, or shell builtins.

```yaml
  rotate-logs:
    script: "find /var/log/myapp -name '*.log' -mtime +7 | xargs gzip"
```

They are mutually exclusive. A command with both (or neither) fails validation at `molt build`.

### Env resolution order (later wins)

1. Host environment (inherited, minus `PYTHONHOME` / `PYTHONPATH` / `VIRTUAL_ENV`)
2. molt-injected: `PATH` (venv `bin/` first), `VIRTUAL_ENV`, `PYTHONNOUSERSITE=1`
3. Top-level `env:` in `molt.yaml`
4. Per-command `env:`

### Default command resolution

When no `default:` is set, molt picks:

1. A command named `run`
2. A command named `start`
3. The only command (if exactly one exists)
4. `python -m <appname>.main` as last resort

---

## 10. Hooks: pre/post install

```yaml
hooks:
  pre_install:
    - "pytest tests/smoke -q"          # runs on BUILD HOST at build time
  post_install:
    - "python manage.py migrate --noinput"
    - "python manage.py collectstatic --noinput"
```

`post_install` hooks run **on the target machine**, after payload extraction and venv setup, inside the hermetic venv. This is where you put operations that must happen once before the app first serves requests.

**Ordering guarantee**: integrity check → venv setup → `post_install` hooks → app is ready. A tampered payload is rejected before hooks ever run.

**Failure behaviour**: if any hook exits non-zero, the install aborts and the partial install dir is cleaned up. No half-installed state lingers.

**Hooks are shell strings** (not `exec`-style). If you need no-shell semantics, put logic in a Python script:

```yaml
hooks:
  post_install:
    - "python scripts/post_install.py"
```

**Non-interactive hooks**: hooks that prompt interactively will hang. Use non-interactive flags:

```yaml
hooks:
  post_install:
    - "DJANGO_SUPERUSER_PASSWORD=$ADMIN_PW python manage.py createsuperuser --noinput --username admin --email admin@example.com"
    - "python manage.py migrate --noinput"     # note: --noinput
```

---

## 11. Integrity: verify, inspect, diff

Every `molt build` outputs two files:

| File | Purpose |
|---|---|
| `myapp-v1.0.0` | Self-installing binary; root hash embedded in trailer |
| `myapp-v1.0.0.manifest.json` | Human-readable sidecar; full file list with sizes + hashes |

### Inspect

```bash
# Summary
$ molt inspect ./myapp-v1.0.0
App:         myapp v2.3.1
Built:       2026-04-18T10:30:00Z (linux/amd64, glibc 2.35)
Algorithm:   sha256
Root hash:   3f8a2c1bd21c5a0b3e94f1c8a2d7e6b9f4...
Files:       1247
Total size:  42.1 MB (44144876 bytes)
Python:      3.12.2

Assets:
  ✓ models/weights.bin [required]  — Sentence transformer weights
  ✓ models/tokenizer.json [required]

# Full file table
$ molt inspect ./myapp-v1.0.0 --files

# Raw JSON (pipe into jq)
$ molt inspect ./myapp-v1.0.0 --json
$ molt inspect ./myapp-v1.0.0 --json | jq '.payload.files[] | select(.size > 1048576)'
```

Accepts either the binary or the `.manifest.json` sidecar.

### Verify

```bash
# Fast: trailer root_hash matches embedded manifest, manifest is internally consistent.
# Catches manually-edited manifests.
$ molt verify-binary ./myapp-v1.0.0
✓ Binary integrity verified.

# Deep: re-streams every file in the payload, recomputes root hash from scratch.
# Catches sophisticated tampering where the attacker updated trailer + manifest
# consistently but can't match the real payload contents.
$ molt verify-binary ./myapp-v1.0.0 --deep
✓ Deep verification passed — every file in the payload was re-hashed.
```

The launcher runs the fast check automatically on `install` (`verify_on_install: true` by default). Launch-time re-verification is opt-in:

```yaml
integrity:
  verify_on_launch: true    # re-hash on every './myapp run' invocation
```

Appropriate for: small CLIs, security-sensitive services, environments where tampered-at-rest binaries are a concern.
Not appropriate for: large binaries started frequently (adds startup latency proportional to binary size).

### Diff

```bash
$ molt diff ./myapp-v1.0.0 ./myapp-v1.1.0
Comparing:
  a: myapp v1.0.0  (root 3f8a2c1bd21c…)
  b: myapp v1.1.0  (root 8b2d4f1a7c9e…)

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

**Common use cases**:
- "Why did our binary double in size between releases?" → diff shows the new giant file
- "Did the CI build actually pick up my latest code?" → diff shows which files changed
- "Did a dependency update silently change a lot of files?" → diff shows the footprint

Accepts both binary paths and sidecar `.manifest.json` paths:

```bash
$ molt diff ./builds/v1.0.0/myapp.manifest.json ./builds/v1.1.0/myapp.manifest.json
```

### Root hash algorithm

The root hash is deterministic and reimplementable in any language:

1. Sort all `PackagedFile` records by `Path` (byte-wise)
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

## 12. uv integration

molt uses [uv](https://github.com/astral-sh/uv) for Python dependency installation. uv must be available on the machine running `molt build`.

```bash
# Show where molt resolves uv from
$ molt uv path
/home/you/.local/bin/uv

# Show uv version
$ molt uv version
uv 0.4.18

# Diagnostics: shows all tool locations + uv source
$ molt doctor
molt
Platform: linux/amd64
─────────────────────────────────────
  ✓ uv                   /home/you/.local/bin/uv  [uv 0.4.18 — system PATH]
  ✓ python3              /usr/bin/python3
  ✓ go                   /usr/local/go/bin/go
  ✓ git                  /usr/bin/git
  ✗ ldd                  not found
  ✓ curl                 /usr/bin/curl
```

### uv resolution order

1. `$MOLT_UV` — absolute path override. Useful in CI where you already have a pinned uv.
2. `~/.molt/uv/bin/uv` — the managed location (where a full molt installation would put it).
3. `uv` on `$PATH` — system fallback.

If uv isn't found anywhere, `molt build` will error before touching your project.

### Using a specific uv in CI

```yaml
# .github/workflows/build.yml
env:
  MOLT_UV: /home/runner/.cargo/bin/uv

steps:
  - name: Install uv
    run: curl -LsSf https://astral.sh/uv/install.sh | sh
  - name: Build
    run: molt build
```

---

## 13. Environment variables

These are read by the molt CLI itself (build-time and inspection commands):

| Variable | Effect |
|---|---|
| `MOLT_UV` | Absolute path to a uv binary. Overrides resolution order entirely. |

These are read by the installed launcher at install/run time (on the target machine):

| Variable | Effect |
|---|---|
| `MOLT_INSTALL_BASE` | Base directory for installs. App installed at `$BASE/<name>/<version>/`. |
| `MOLT_INSTALL_DIR` | Full install directory. Overrides `MOLT_INSTALL_BASE`. |
| `MOLT_CACHE_DIR` | Cache for downloaded Python interpreters and other artifacts. |
| `MOLT_SKIP_VERIFY` | Set to `1` to skip install-time integrity check. Don't use in production. |
| `MOLT_ROOT` | Set by the launcher at run time. Absolute path to the install dir. |

```bash
# Install to a non-default location
$ MOLT_INSTALL_BASE=/opt ./myapp-v1.0.0 install
Installing myapp v1.0.0 → /opt/myapp/1.0.0

# System-wide install
$ sudo MOLT_INSTALL_BASE=/usr/local/lib ./myapp-v1.0.0 install
```

---

## 14. Complete molt.yaml examples

### Django web app

```yaml
version: 1

project:
  name: blogplatform
  version: 4.2.0
  python: "3.11"
  description: "Content management platform"

deps:
  strategy: requirements
  files:
    - requirements.txt

include:
  - "blogplatform/**/*.py"
  - "blogplatform/**/*.html"
  - "blogplatform/**/*.txt"
  - "templates/**/*"
  - "static/**/*"
  - "locale/**/*"
  - "manage.py"

exclude:
  - "blogplatform/tests/"
  - "**/__pycache__/"
  - "**/*.pyc"

commands:
  default: web

  web:
    exec:
      - gunicorn
      - "blogplatform.wsgi:application"
      - "--bind=0.0.0.0:8000"
      - "--workers=4"
      - "--access-logfile=-"
    description: "Django WSGI via gunicorn"

  worker:
    exec: [celery, "-A", "blogplatform", "worker", "-l", "info"]
    description: "Celery async worker"

  beat:
    exec: [celery, "-A", "blogplatform", "beat", "-l", "info"]
    description: "Celery periodic scheduler"

  migrate:
    exec: [python, manage.py, migrate, --noinput]
    description: "Run database migrations"

  shell:
    exec: [python, manage.py, shell]
    description: "Django shell"

  check:
    exec: [python, manage.py, check, --deploy]
    description: "Deployment checks"

env:
  DJANGO_SETTINGS_MODULE: "blogplatform.settings.production"
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "python manage.py collectstatic --noinput --clear"
    - "python manage.py migrate --noinput"

integrity:
  verify_on_install: true
  verify_on_launch: false
```

### FastAPI microservice

```yaml
version: 1

project:
  name: orders-api
  version: 1.5.0
  python: "3.12"

deps:
  strategy: pyproject     # pyproject.toml + uv.lock

include:
  - "orders_api/**/*.py"
  - "orders_api/openapi/*.yaml"
  - "alembic/**/*.py"
  - "alembic.ini"

commands:
  default: serve

  serve:
    exec:
      - uvicorn
      - "orders_api.main:app"
      - "--host=0.0.0.0"
      - "--port=8080"
      - "--workers=2"
    description: "FastAPI ASGI server"

  migrate:
    exec: [alembic, upgrade, head]
    env:
      ALEMBIC_CONFIG: "alembic.ini"

env:
  UVICORN_LOG_LEVEL: "info"
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "alembic upgrade head"

integrity:
  verify_on_install: true
  verify_on_launch: true   # small service, startup cost acceptable
```

### Celery worker only

```yaml
version: 1

project:
  name: indexer-worker
  version: 0.9.2
  python: "3.11"

deps:
  strategy: requirements
  files: [requirements.txt]

include:
  - "indexer/**/*.py"
  - "indexer/sql/*.sql"

commands:
  default: worker

  worker:
    exec:
      - celery
      - "--app=indexer.tasks"
      - "worker"
      - "--loglevel=info"
      - "--concurrency=8"
      - "--max-tasks-per-child=500"

  flower:
    exec: [celery, "--app=indexer.tasks", "flower", "--port=5555"]
    description: "Celery monitoring dashboard"

  purge:
    script: "celery -A indexer.tasks purge -f"
    description: "Clear all queued tasks"

env:
  CELERY_BROKER_URL: "redis://redis:6379/0"
  CELERY_RESULT_BACKEND: "redis://redis:6379/1"
  PYTHONUNBUFFERED: "1"

integrity:
  verify_on_install: true
```

### Data-science CLI with model weights

```yaml
version: 1

project:
  name: classify
  version: 3.0.0
  python: "3.11"
  description: "Document classification CLI"

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
      description: "384-dim sentence embeddings, domain fine-tuned"
    - path: models/tokenizer.json
      required: true
      description: "Tokenizer for sentence-transformers-v2"
    - path: data/stopwords-en.txt
      required: false    # app has a built-in fallback if missing
  max_file_size_mb: 200
  max_total_size_mb: 400

commands:
  default: classify
  classify:
    exec: [python, "-m", "classify"]
    description: "Classify documents from stdin"
  reindex:
    exec: [python, "-m", "classify.reindex", "--full"]
    description: "Rebuild search index"
  benchmark:
    exec: [python, "-m", "classify.bench"]
    description: "Throughput benchmark"

integrity:
  verify_on_install: true
  verify_on_launch: true   # catches model bit-rot on long-lived deployments
```

---

## 15. Troubleshooting

### Build fails: "cannot find uv"

```
error: uv not found — install from https://github.com/astral-sh/uv or set MOLT_UV
```

Install uv, then retry:

```bash
$ curl -LsSf https://astral.sh/uv/install.sh | sh
$ molt build
```

Or point molt at your existing uv:

```bash
$ MOLT_UV=/path/to/uv molt build
```

Run `molt doctor` to see the full tool resolution table.

### Build fails: "no files matched inclusion criteria"

Your `include:` patterns didn't match anything in the project directory.

Common causes:

```yaml
include:
  - "src/**/*.py"    # ← wrong if your project is FLAT, not src-layout
```

Flat layout fix:

```yaml
include:
  - "myapp/**/*.py"  # actual package directory name
  - "*.py"           # top-level scripts
```

Check what directories exist:

```bash
$ ls -la
$ find . -name "*.py" | head -20
```

Run `molt adopt` again to regenerate detection-based suggestions.

### Build fails: "SECURITY BLOCK: sensitive file matched"

The file matched a built-in sensitive pattern (`*.pem`, `*.key`, `*password*`, `id_rsa`, `.env.local`, etc.).

There is **no flag to disable this check**. Options:

1. Add the file to `exclude:`:
   ```yaml
   exclude:
     - "config/dev-cert.pem"
   ```
2. Move the file outside the project tree (the real fix for private keys)
3. Rename it if the pattern matched incorrectly (e.g. a file named `password_reset_template.html` — rename to `reset_email.html`)

### Build fails: "assets.required file not found"

```
error: required asset not found: models/weights.bin
```

The file declared as `required: true` in `assets:` doesn't exist at build time.

Either create the file or change it to `required: false`.

### `molt verify-binary` fails

```
error: verify: root_hash mismatch
```

The binary was modified after build. Possible causes:

- **Corrupted transfer** — check checksums:
  ```bash
  $ sha256sum myapp-v1.0.0        # on source
  $ sha256sum myapp-v1.0.0        # on target — must match
  ```
- **Binary was patched** — re-download from the trusted source
- **Disk corruption** — retry on a fresh disk
- **molt bug** — file an issue with the binary + its `.manifest.json`

### `molt inspect` says "binary has no integrity trailer (legacy build)"

The binary was built with an older version of molt that didn't yet write trailers. Either rebuild, or use the sidecar `.manifest.json` directly (if it was produced alongside the binary):

```bash
$ molt inspect ./myapp-v1.0.0.manifest.json
```

### `deps.strategy: poetry` fails at build

Poetry and Pipenv strategies require their tools on the build host and can't directly drive `uv pip install`. Workaround:

```bash
$ poetry export -f requirements.txt --without-hashes -o requirements.txt
```

Then in `molt.yaml`:

```yaml
deps:
  strategy: requirements
  files: [requirements.txt]
```

### `molt assemble` fails: "manifest has no Python spec"

The `capture` manifest doesn't contain enough information. Re-run capture on a machine with Python installed and accessible:

```bash
$ which python3 && python3 --version
$ molt capture
```

### Post-install hook hangs forever

The hook is waiting for stdin. Use non-interactive flags on every management command:

```yaml
hooks:
  post_install:
    - "python manage.py migrate --noinput"
    - "python manage.py collectstatic --noinput --clear"
```

### Binary is unexpectedly large

```bash
# See the 20 largest packaged files
$ molt inspect ./myapp-v1.0.0 --files | awk 'NR>1{print $2, $1}' | sort -rh | head -20

# Compare to the previous release
$ molt diff ./myapp-v0.9.0 ./myapp-v1.0.0
```

Common culprits: accidentally included `__pycache__/` directories, committed datasets or model files, test fixtures, `.git/` directory (report a bug if this happens — it should always be excluded).

### I want to see the full manifest as JSON

```bash
$ molt inspect ./myapp-v1.0.0 --json | python3 -m json.tool | less

# Or use jq
$ molt inspect ./myapp-v1.0.0 --json | jq '.payload.files[] | {path, size, source}' | head -40

# Largest files by source category
$ molt inspect ./myapp-v1.0.0 --json | jq '[.payload.files[] | select(.source == "asset")] | sort_by(-.size)'
```

---

## Getting help

- `molt doctor` — system diagnostics, uv resolution, tool locations
- `molt inspect <binary>` — what's inside a binary
- `molt diff <a> <b>` — what changed between two builds
- `molt adopt --non-interactive` — regenerate `molt.yaml` from project structure
