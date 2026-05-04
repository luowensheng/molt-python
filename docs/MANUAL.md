# molt — Reference Manual

The authoritative reference for what molt actually does, how each command
behaves, and what the system looks like from the inside. All command outputs
shown here are copy-pasted from real runs — nothing fictional.

If you want a quick start, read `README.md`. If you want the architecture
deep-dive on the package store specifically, read `docs/global-store.md`.

---

## Table of contents

1. [What molt is](#1-what-molt-is)
2. [Architecture](#2-architecture)
3. [Filesystem layout](#3-filesystem-layout)
4. [Sync — the heart of molt](#4-sync--the-heart-of-molt)
5. [Runtime — how `molt run` works](#5-runtime--how-molt-run-works)
6. [Build pipeline](#6-build-pipeline)
7. [Every command](#7-every-command)
   - 7.1 [Project lifecycle](#71-project-lifecycle)
   - 7.2 [Python versions](#72-python-versions)
   - 7.3 [Run & tasks](#73-run--tasks)
   - 7.4 [Build & deploy](#74-build--deploy)
   - 7.5 [uv passthrough](#75-uv-passthrough)
   - 7.6 [Diagnostics & meta](#76-diagnostics--meta)
8. [End-to-end workflows](#8-end-to-end-workflows)
9. [Concurrency and integrity](#9-concurrency-and-integrity)
10. [Troubleshooting](#10-troubleshooting)
11. [Glossary](#11-glossary)

---

## 1. What molt is

molt is a single-binary Python toolchain. It manages dependencies, runs your
code, and ships your project as a self-contained executable that needs no
Python or pip on the target machine.

It does this by combining three ideas:

1. **A global content-addressed package store** at `~/.molt/pkg/`. Packages
   live there once, keyed by `(name, version, py-abi-platform)`, and are
   shared across every project. There is no per-project `.venv/`. Two
   projects that both depend on `requests==2.32.3` literally point at the
   same directory.

2. **A `PYTHONPATH`-driven runtime**. Each project has a `.molt/syspath.json`
   listing the store directories that make up its environment. `molt run`
   reads it, sets `PYTHONPATH`, and execs the chosen interpreter directly.
   No activation scripts, no shell prompts, no env mutation.

3. **A binary build format** that bundles a tiny Go launcher + a tar.gz of
   your project + an integrity trailer into a single executable. Run it on
   another machine with no Python: it unpacks itself the first time, sets up
   a hermetic env, then runs.

Beneath the surface molt delegates dependency resolution and lock generation
to [uv](https://github.com/astral-sh/uv) (vendored, not the system copy), so
resolution semantics match the wider Python ecosystem.

---

## 2. Architecture

### High-level flow

```
              ┌────────────────────────────────────────────────────┐
              │                  molt CLI (Go)                     │
              └──────────┬─────────────────┬─────────────┬─────────┘
                         │                 │             │
              ┌──────────▼──────────┐ ┌────▼──────┐ ┌───▼────────┐
              │   syncplan          │ │  tasks    │ │  builder   │
              │ (resolve+install)   │ │ (run sh)  │ │ (bundle)   │
              └──────┬───────┬──────┘ └────┬──────┘ └────┬───────┘
                     │       │             │             │
              ┌──────▼───┐ ┌─▼──────┐  ┌───▼────┐  ┌─────▼──────┐
              │ uv (lock)│ │ store  │  │ syspath│  │  launcher  │
              │ vendored │ │ ~/.molt│  │.molt/  │  │  embedded  │
              └──────────┘ │  /pkg  │  │syspath │  │  go source │
                           └────────┘  │ .json  │  └────────────┘
                                       └────────┘
```

### Key components

| Component | Path | Responsibility |
|---|---|---|
| `internal/uvbin` | — | Locate / download the pinned `uv` binary into `~/.molt/uv/` |
| `internal/uv` | — | Thin wrapper around uv subcommands (init, add, remove, lock, tree) with stdout filtering |
| `internal/syncplan` | — | Orchestrator: lock → parse → ABI detect → install → write syspath + shims |
| `internal/store` | `~/.molt/pkg/` | Content-addressed wheel store; atomic install + flock |
| `internal/lockparse` | — | Parse `uv.lock` and pick the right wheel for the active interpreter ABI |
| `internal/pyabi` | — | Run a small Python script in the target interpreter to discover `(py_tag, abi_tag, platform_tag)` |
| `internal/wheelsrc` | — | Find a wheel: check uv's own cache first, download to `~/.molt/pkg/.dl/` if absent |
| `internal/syspath` | `<proj>/.molt/syspath.json` | Per-project env spec; `BuildEnv` produces `PYTHONPATH` + clean PATH |
| `internal/python` | — | Python version management via uv (list, install, use, which, audit) |
| `internal/tasks` | — | Run named commands from `[tool.molt.tasks]` |
| `internal/builder` | — | Compile launcher, build payload, assemble final binary, sign trailer |
| `internal/launcher` | embedded into every built binary | At-target-machine bootstrap: extract, verify, exec |
| `internal/integrity` | — | Manifest + root-hash format for built binaries |
| `internal/adopt` | — | Generate `molt.yaml` for an existing project |

### What stays uv, what is molt

| Operation | Owned by |
|---|---|
| Dependency resolution / `uv.lock` generation | uv |
| Wheel download (uses uv's cache when warm) | uv cache → molt fallback |
| Wheel unpacking + storage | molt (`store.Install`) |
| Per-project environment | molt (`syspath` + shims), **not** uv venvs |
| Python interpreter download/install | uv (via `molt python install`) |
| `molt build` / runtime launcher | molt |

uv `add` / `remove` is still invoked behind `molt add` / `molt remove`, but
`--no-sync` is passed and `UV_PROJECT_ENVIRONMENT` is redirected into
`<proj>/.molt/uv-env/`, so uv's own venv shell is hidden away from the
project tree (see §10 troubleshooting).

---

## 3. Filesystem layout

### Global — `~/.molt/`

```
~/.molt/
├── uv/
│   └── bin/uv                  # the pinned uv binary molt uses
├── python/                     # molt-installed standalone Pythons (rare; usually under ~/.local/share/uv/python/)
│   └── 3.12.3/bin/python3
├── pkg/                        # the package store
│   ├── click/
│   │   └── 8.1.7/
│   │       ├── py3-none-any/
│   │       │   ├── click/__init__.py
│   │       │   ├── click/...
│   │       │   ├── .ok                   ← sentinel: install completed atomically
│   │       │   └── meta.json             ← {entry_points, source_sha256, ...}
│   │       └── ...
│   ├── pydantic-core/
│   │   └── 2.16.2/
│   │       ├── cp311-cp311-macosx_11_0_arm64/   ← ABI-specific (cp311 wheel)
│   │       └── cp312-cp312-macosx_11_0_arm64/   ← also cp312
│   ├── .dl/                    # downloaded wheels keyed by sha256
│   │   └── 9a3f....whl
│   └── ...
├── pkg.lock                    # flock guard for concurrent installs
└── registry.json               # {projectDir: lockHash} for GC
```

The store path encoding is:

```
~/.molt/pkg/{name}/{version}/{py_tag}-{abi_tag}-{platform_tag}/
```

Pure-Python wheels collapse to `py3-none-any/` and are shared by every
interpreter ABI. Native-wheel packages get separate directories per ABI.

### Per-project — `<project>/`

```
<project>/
├── .python-version             # e.g. "3.12"
├── pyproject.toml              # source of truth for deps (uv-style + [tool.molt.tasks])
├── uv.lock                     # generated by uv lock
├── README.md
├── src/
│   └── <package>/...           # your code (created by you, not by molt init)
├── tests/...
└── .molt/                      # generated by molt sync, gitignore this
    ├── syspath.json            # {python, py_tag, abi_tag, platform, syspath:[...]}
    ├── sitecustomize.py        # site.addsitedir for each store path (handles .pth)
    ├── bin/                    # console-script + python shims
    │   ├── python              # exec project's interpreter w/ PYTHONPATH set
    │   ├── python3
    │   ├── pytest              # console_scripts entry from pytest's RECORD
    │   ├── ruff
    │   └── ...
    └── uv-env/                 # uv's mostly-empty project env (hidden impl detail)
```

Add `.molt/` to `.gitignore`. It is fully derivable from `pyproject.toml` +
`uv.lock` by running `molt sync`.

---

## 4. Sync — the heart of molt

`molt sync`, `molt add`, and `molt remove` all funnel through one pipeline
in `internal/syncplan/syncplan.go`. The 10 steps:

1. **Ensure uv** — download to `~/.molt/uv/bin/uv` if missing.
2. **Resolve interpreter** — `uv python find` (with cwd set to a *neutral*
   directory, never the project, to avoid uv auto-creating `.venv/`). If
   `.python-version` names a version that isn't installed, run
   `uv python install`.
3. **Maybe regenerate `uv.lock`** — only if `pyproject.toml` is newer
   than `uv.lock` (or `--frozen` is not passed).
4. **Parse the lock** — extract `[(name, version, [wheels...])]`.
5. **Detect interpreter ABI** — run a small Python script via the resolved
   interpreter, get `py_tag`, `abi_tag`, ordered `platform_tags`. Used to
   pick the right wheel from each lock entry.
6. **Acquire global flock** at `~/.molt/pkg.lock`.
7. **Install missing wheels** — for each package:
   - Pick the best-matching wheel for this interpreter ABI.
   - Cache key = `(name, version, py-abi-plat)`.
   - If `~/.molt/pkg/.../.ok` exists, skip (`✓ cached`).
   - Else locate the wheel: check uv's wheel cache → download to
     `~/.molt/pkg/.dl/{sha256}.whl` → unpack atomically into
     `~/.molt/pkg/.tmp/{rand}/` → `.ok` last → `os.Rename` to final.
8. **Topo-sort dependencies** — left-to-right on PYTHONPATH so
   dependents see their dependencies.
9. **Write `<proj>/.molt/syspath.json`** — interpreter path + ordered
   list of store dirs + ABI tags + lock hash + project source dirs (`src/`
   if present).
10. **Write shims** — `python`, `python3`, plus one for every
    `console_scripts` entry point declared by every installed package. All
    shims have absolute paths baked in and are regenerated on every sync.

The flock is held only for step 7. `Has()` is lock-free thanks to the `.ok`
sentinel + atomic rename — concurrent syncs across different projects with
overlapping deps both compute the same key; the second sees `.ok` and
short-circuits.

---

## 5. Runtime — how `molt run` works

`molt run <task>` and `molt run <binary> [args...]` both:

1. Read `<project>/.molt/syspath.json`.
2. Build the command env via `syspath.Spec.BuildEnv`:
   - **`PYTHONPATH`** = `<projectDir>/.molt/` (so `sitecustomize.py` is
     loaded first) + every store dir in topo order + project source dirs.
   - **`PATH`** = `<projectDir>/.molt/bin/` prepended (so generated shims
     win over system equivalents).
   - **Stripped:** `VIRTUAL_ENV`, `PYTHONHOME`, any inherited `PYTHONPATH`.
3. For a task: `/bin/sh -c <task.command>` with the env above. Tasks calling
   `python` find the shim at `.molt/bin/python` which execs the project's
   resolved interpreter.
4. For a binary: look up `<binary>` in `.molt/bin/` first, else `PATH`,
   then `syscall.Exec` directly — the new process literally replaces molt
   in memory. No subshell.

The `sitecustomize.py` written at sync time calls `site.addsitedir(d)` for
each entry in `syspath`. That's the standard library API that processes
`.pth` files, so namespace packages, `setuptools` plugins, and pkg_resources
metadata all behave correctly.

---

## 6. Build pipeline

`molt build` produces a single self-contained executable. The format:

```
┌─────────────────┐
│ launcher (Go)   │   stdlib-only main package, ~3 MB
├─────────────────┤
│ payload         │   tar.gz of project source + selected dep tree
│  ├─ pyproject   │   + integrity manifest at .molt/manifest.json
│  ├─ src/        │
│  ├─ deps/       │
│  └─ .molt/...   │
├─────────────────┤
│ extended trailer│   archive_offset (8B) + root_hash (32B) + version (1B) +
│                 │   trailer_size (4B) + magic (8B)
└─────────────────┘
```

When the user runs the binary:

1. Launcher mmaps itself, reads the trailer's magic + offset.
2. Verifies the trailing root hash by hashing the embedded manifest.
3. If first run: extracts the payload to `${molt_INSTALL_BASE:-/opt}/<app>/`,
   runs any `post_install` hooks declared in `molt.yaml`.
4. Subsequent runs: skips extraction, jumps straight to `run` step.
5. `run` execs the chosen interpreter (`python -m <app>.main` by default,
   or a named command from `molt.yaml`'s `commands:` block).

The launcher is **embedded into the molt binary itself** via `//go:embed
launcher_src/*.go.tpl`. At build time molt extracts those files to a temp
dir + a minimal `go.mod`, then runs `go build` there. This means `molt
build` works on any machine with Go available, not just the molt source
repo.

---

## 7. Every command

Output below is verbatim from a real run. Any time you see `<...>` it's a
placeholder, not literal output.

### 7.1 Project lifecycle

#### `molt init [name]`

Scaffolds a new project. With no name, initialises the **current directory**
in place (no nested subdir). With a name, creates `<name>/` and inits there.

```
$ mkdir tagctl && cd tagctl
$ molt init
Initialising project "tagctl"...
Initialized project `tagctl`
Using CPython 3.11.14
Resolved 1 package in 11ms
✓ Done.

$ ls
.gitignore  .python-version  README.md  hello.py  pyproject.toml  uv.lock
```

Flags:
- `--python <ver>` — pin a specific Python (writes `.python-version`).
- `--lib` — library layout (`src/<name>/__init__.py` instead of `hello.py`).
- `--no-lock` — skip the initial `uv lock`.

What `init` creates is the **flat uv default**: `.python-version`, `README.md`,
`hello.py`, `pyproject.toml`, `uv.lock`. To get the conventional `src/`
layout, do it yourself:

```sh
$ rm hello.py
$ mkdir -p src/tagctl tests
$ touch src/tagctl/__init__.py src/tagctl/__main__.py tests/conftest.py
```

#### `molt add [--dev] <pkg...>`

Adds a dependency, regenerates `uv.lock`, and populates the global store.

```
$ molt add fastapi sqlalchemy
Using CPython 3.11.14
Resolved 12 packages in 209ms
  ↓ install fastapi 0.115.0 (fastapi-0.115.0-py3-none-any.whl)
  ↓ install starlette 0.39.2 (starlette-0.39.2-py3-none-any.whl)
  ↓ install pydantic 2.9.2 (pydantic-2.9.2-py3-none-any.whl)
  ✓ cached  typing-extensions 4.12.2
  ✓ cached  anyio 4.6.0
  ...
✓ 12 package(s); store=/Users/you/.molt/pkg
```

`--dev` puts the dep in `[tool.uv] dev-dependencies` (uv's preferred
location), not `[project] dependencies`. Both are picked up by `sync`.

After running, `<project>/.molt/syspath.json` is updated and shims in
`.molt/bin/` reflect the new console-scripts.

#### `molt remove [--dev] <pkg...>`

Mirror of `add`. Updates `pyproject.toml` + `uv.lock`, then re-runs sync.
Removed packages stay in `~/.molt/pkg/` until `molt gc`.

```
$ molt remove sqlalchemy
Resolved 5 packages in 2ms
  ✓ cached  fastapi 0.115.0
  ✓ cached  starlette 0.39.2
  ...
✓ 5 package(s); store=/Users/you/.molt/pkg
```

#### `molt sync [--frozen] [--refresh]`

Idempotent: ensures the on-disk environment matches `uv.lock`. First-time
runs install missing wheels into the store; re-runs hit the cache.

```
$ molt sync
  ✓ cached  fastapi 0.115.0
  ✓ cached  starlette 0.39.2
  ...
✓ 12 package(s); store=/Users/you/.molt/pkg
```

- `--frozen` — fail if `pyproject.toml` is newer than `uv.lock`. Use in CI.
- `--refresh` — force-reinstall every package (skip the `.ok` cache check).

#### `molt lock`

Regenerate `uv.lock` from `pyproject.toml` without touching the store.

```
$ molt lock
Resolved 12 packages in 0.81ms
```

#### `molt tree`

Print the dependency tree (uv pass-through).

```
$ molt tree
Resolved 12 packages in 0.97ms
tagctl v0.1.0
├── fastapi v0.115.0
│   └── starlette v0.39.2
└── sqlalchemy v2.0.35
```

#### `molt gc [--dry-run]`

Sweeps the global store. For each project listed in
`~/.molt/registry.json`, parses its `uv.lock` to compute the set of
referenced `(name, version, abi)` keys. Anything in `~/.molt/pkg/` not
referenced anywhere is deleted. Project entries whose directory has been
removed from disk are dropped from the registry.

```
$ molt gc --dry-run
would remove 6 store entries; would drop 2 stale project(s):
  - /Users/you/.molt/pkg/certifi/2026.4.22/py3-none-any
  - /Users/you/.molt/pkg/cffi/2.0.0/cp311-cp311-macosx_11_0_arm64
  - /Users/you/.molt/pkg/idna/3.13/py3-none-any
  - /Users/you/.molt/pkg/requests/2.33.1/py3-none-any
  - registry: /tmp/old-project-i-deleted
```

Without `--dry-run`, actually removes those entries.

#### `molt info`

Project summary.

```
$ molt info

tagctl 0.1.0
Python:     3.11
Platform:   darwin/arm64
Directory:  /Users/you/projects/tagctl
Env:        7 store path(s); store=~/.molt/pkg
Python bin: /Users/you/.local/share/uv/python/cpython-3.11.14-macos-aarch64-none/bin/python3
Tasks:      dev, test, lint
```

Outside a project:

```
$ cd /tmp && molt info

No molt project here (/tmp)
Run 'molt init' to scaffold one.
```

After `molt init` but before any deps:

```
Env:        no dependencies declared yet
```

### 7.2 Python versions

Backed by uv's standalone Python distributions.

#### `molt python list`

Every Python molt can see, with provenance.

```
$ molt python list
  3.13.2       homebrew     /opt/homebrew/opt/python@3.13/bin/python3.13
  3.12.11      homebrew     /opt/homebrew/opt/python@3.12/bin/python3.12
  3.11.14      uv-managed   /Users/you/.local/share/uv/python/cpython-3.11.14-macos-aarch64-none/bin/python3
  3.11.11      homebrew     /opt/homebrew/opt/python@3.11/bin/python3.11
  3.9.6        xcode        /Applications/Xcode.app/Contents/Developer/usr/bin/python3
  3.9.25       uv-managed   /Users/you/.local/share/uv/python/cpython-3.9.25-macos-aarch64-none/bin/python3
```

Source labels:
- `molt-managed` — installed via `molt python install` into `~/.molt/python/`
- `uv-managed` — uv standalone build under `~/.local/share/uv/python/`
- `homebrew` — under `/opt/homebrew/` or `/usr/local/Cellar/`
- `xcode` — Apple's bundled Python
- `system` — under `/usr/`
- `other` — anything else

`<download available>` rows are skipped — see `molt python audit` for those.

#### `molt python install <ver>`

Downloads a uv standalone build for the given version (e.g. `3.12`,
`3.12.3`).

```
$ molt python install 3.12
✓ Python 3.12 installed
```

#### `molt python use <ver> [--global]`

Pin a Python for the project (writes `.python-version`) or globally
(writes `~/.molt/.python-version`).

```
$ molt python use 3.12
Project Python set to 3.12
```

#### `molt python remove <ver>`

Uninstalls a managed Python.

#### `molt python which`

Print the path to the active interpreter for the current project. Runs `uv
python find` from a neutral cwd to avoid creating an unwanted venv.

```
$ molt python which
/Users/you/.local/share/uv/python/cpython-3.11.14-macos-aarch64-none/bin/python3
```

#### `molt python audit`

Detailed inventory: every Python on this machine, with version, source,
and path.

#### `molt python conflicts`

Spot environment-pollution risks: shadowed Pythons on PATH, conflicting
`PYTHONPATH` entries, etc.

```
$ molt python conflicts
Checking for Python environment conflicts...
  ✓ No conflicts detected
```

#### `molt python isolation-check`

Verify that, when run via molt, `sys.path` only contains store dirs +
project source. Useful to confirm a sync is healthy.

```
$ molt python isolation-check
Checking environment isolation...
  ✓ sys.path is clean — only store dirs + project src
```

### 7.3 Run & tasks

Tasks live in `pyproject.toml` under `[tool.molt.tasks]`:

```toml
[tool.molt.tasks]
dev   = "uvicorn app.main:app --reload"
test  = "pytest tests/ -v"
lint  = "ruff check src/ tests/"
hello = 'python -c "print(123)"'
```

Note: when a command contains double-quotes, molt writes a TOML literal
string (`'…'`) so the quotes don't need escaping.

#### `molt run <task> [-- extra-args]`

Dispatch order:
1. If `<task>` is a name in `[tool.molt.tasks]`, run it.
2. Else look up `<task>` in `<project>/.molt/bin/`, then `PATH`, and
   `syscall.Exec` it under the project env.

```
$ molt run hello
$ python -c "print(123)"
123

$ molt run pytest -- -k smoke
============================= test session starts ==============================
...

$ molt run python -c "import fastapi; print(fastapi.__file__)"
/Users/you/.molt/pkg/fastapi/0.115.0/py3-none-any/fastapi/__init__.py
```

That last line is the proof: `import fastapi` resolves directly out of the
global store with zero project venv involved.

#### `molt task list`

```
$ molt task list
Available tasks:

  dev                  uvicorn app.main:app --reload
  test                 pytest tests/ -v
  lint                 ruff check src/ tests/
  hello                python -c "print(123)"

Run with: molt run <task>
```

When no tasks defined:

```
No tasks defined. Add tasks to [tool.molt.tasks] in pyproject.toml.
```

#### `molt task add <name> <command>`

```
$ molt task add hello 'python -c "print(123)"'
✓ Task 'hello' added

$ molt task add hello something
error: task "hello" already exists; remove it first or pick a different name
```

Duplicates error. Quotes are preserved (single-quoted TOML literal string).

#### `molt task remove <name>`

```
$ molt task remove hello
✓ Task 'hello' removed

$ molt task remove hello
error: task "hello" not found
```

Removing a non-existent task is a hard error, never a silent success.

### 7.4 Build & deploy

#### `molt build [flags] [project-path]`

Produce a self-contained binary. Reads `molt.yaml` for app metadata and any
custom commands; falls back to inferring from `pyproject.toml`.

```
$ molt build --output ./dist/tagctl
Building tagctl v0.1.0 (darwin/arm64)...
  Compiled launcher (darwin/arm64)
  → Embedding files (strict=true, legacy denylist)...
  ✓ Payload: 9.3 KB (23 files)
  ✓ Integrity manifest: dist/tagctl-v0.1.0.manifest.json
  ✓ Created: ./dist/tagctl (3.7MB)
    root_hash: 3a2c0881e6ee4dc85672ae82a06c74f7451741d61301d960a523eed155d8fe2a
    files:     23   total: 0.0MB
    install:   molt_INSTALL_BASE=/opt ./tagctl install
```

Common flags:
- `--output <path>` — output path. Default: `dist/<name>`.
- `--os <darwin|linux|windows>` and `--arch <amd64|arm64>` — cross-compile.
- `--version <ver>` — embed a version string (default reads `pyproject.toml`).

The output binary supports its own subcommands:

```
$ ./dist/tagctl install [--prefix DIR] [--offline] [--verbose]
$ ./dist/tagctl run [<command>] [args...]
$ ./dist/tagctl verify
$ ./dist/tagctl uninstall
$ ./dist/tagctl info
$ ./dist/tagctl version
```

Default install prefix is `/opt` (override with `molt_INSTALL_BASE` env var).

#### `molt capture [flags]`

Capture the current environment into a manifest file, without building a
binary. Useful for deterministic assembly later or for CI artifact diffing.

```
$ molt capture --output /tmp/manifest.json
Capturing environment (darwin/arm64)...
  Manifest: /tmp/manifest.json
```

#### `molt assemble [flags]`

Assemble a binary from a previously captured manifest. Pairs with `capture`
to enable a "capture once, build many" CI flow.

#### `molt adopt [dir] [--non-interactive] [--force]`

Generate a `molt.yaml` for an existing project. Detects layout heuristics:
src vs flat, framework hints, etc.

```
$ cd existing-project
$ molt adopt --non-interactive
molt adopt — detected existing project layout

  Project directory name : legacy
  ✓ pyproject.toml

✓ Wrote /Users/you/projects/legacy/molt.yaml
  Review the file, refine as needed, then run: molt build
```

#### `molt verify-binary <binary> [--deep]`

Recompute the trailing root hash of a built binary; compare to the embedded
trailer.

```
$ molt verify-binary ./dist/tagctl
✓ Binary integrity verified.
```

`--deep` also extracts and re-hashes every file.

#### `molt inspect <binary> [--files] [--json]`

Show the embedded manifest.

```
$ molt inspect ./dist/tagctl
App:         tagctl v0.1.0
Built:       2026-05-04T06:11:24Z (darwin/arm64)
Algorithm:   sha256
Root hash:   3a2c0881e6ee4dc85672ae82a06c74f7451741d61301d960a523eed155d8fe2a
Files:       23
Total size:  35.9 KB (36737 bytes)
```

`--files` lists every embedded file with its hash.
`--json` emits machine-readable JSON.

#### `molt diff <a> <b>`

Compare two builds (or manifests).

```
$ molt diff ./dist/tagctl-v0.1.0 ./dist/tagctl-v0.2.0
Comparing:
  a: tagctl v0.1.0  (root 3a2c0881e6ee…)
  b: tagctl v0.2.0  (root 9ce0fd23a1aa…)

Added:   2 file(s)
Removed: 0 file(s)
Changed: 4 file(s)

Total size change: +12 bytes
```

### 7.5 uv passthrough

#### `molt uv path`

Path to molt's pinned uv binary.

```
$ molt uv path
/Users/you/.molt/uv/bin/uv
```

#### `molt uv version`

```
$ molt uv version
uv 0.4.18 (7b55e9790 2024-10-01)
```

#### `molt uv <args...>`

Arbitrary passthrough — useful when you need a uv feature molt hasn't
wrapped.

```
$ molt uv pip compile requirements.in
$ molt uv cache clean
```

### 7.6 Diagnostics & meta

#### `molt doctor`

Health check for the toolchain.

```
$ molt doctor
molt dev
Platform: darwin/arm64
─────────────────────────────────────
  ✓ uv                   /Users/you/.molt/uv/bin/uv  [uv 0.4.18 (...) — molt-managed]
  ✓ python3              /opt/homebrew/bin/python3
  ✓ go                   /opt/homebrew/bin/go
  ✓ git                  /opt/homebrew/bin/git
  ✗ ldd                  not found
  ✓ curl                 /opt/homebrew/opt/curl/bin/curl
```

`ldd` missing is expected on macOS; not a real problem.

#### `molt version` / `molt --version`

```
$ molt version
molt dev (commit edc24dc01234, built unknown)
```

In a release build with proper ldflags, "dev" / "unknown" become real
version + date.

#### `molt help` / `molt --help` / `molt -h`

Full command reference (see `internal/usage.go`).

---

## 8. End-to-end workflows

### Scenario A — new project, dev locally, ship binary

```sh
$ mkdir orders-api && cd orders-api
$ molt init
$ molt python use 3.12

# Set up the src layout
$ rm hello.py
$ mkdir -p src/orders_api tests
$ touch src/orders_api/__init__.py src/orders_api/main.py tests/__init__.py

# Edit pyproject.toml to add a [tool.molt.tasks] block:
#   [tool.molt.tasks]
#   dev  = "uvicorn orders_api.main:app --reload"
#   test = "pytest tests/ -v"

$ molt add fastapi "uvicorn[standard]" sqlalchemy pydantic-settings
$ molt add --dev pytest pytest-asyncio ruff

$ molt run dev                    # local dev server
$ molt run test                   # run tests
$ molt build --output dist/orders-api
$ scp dist/orders-api server:/usr/local/bin/
$ ssh server '/usr/local/bin/orders-api install'
$ ssh server '/usr/local/bin/orders-api run'
```

### Scenario B — CI with frozen lockfile

```sh
# In CI:
$ molt sync --frozen           # fail if uv.lock is stale
$ molt run lint
$ molt run test
$ molt build --os linux --arch amd64 --output dist/orders-api
```

### Scenario C — adopting an existing project

```sh
$ cd existing-django-app
$ ls
manage.py  myapp/  pyproject.toml  requirements.txt

$ molt adopt --non-interactive
✓ Wrote molt.yaml — review, then run: molt build

$ molt sync                    # populates ~/.molt/pkg/
$ molt run python manage.py runserver
$ molt build
```

### Scenario D — switch Python versions

```sh
$ molt python install 3.13
$ molt python use 3.13
$ molt sync                    # repopulates ABI-specific entries
$ molt run pytest              # now under 3.13
```

The previous 3.11 store entries are not deleted — they stay in
`~/.molt/pkg/{name}/{ver}/cp311-...` so other projects still using 3.11
keep working. `molt gc` removes them once nothing references them.

---

## 9. Concurrency and integrity

### Locks

| Lock | Path | Scope |
|---|---|---|
| Global install lock | `~/.molt/pkg.lock` (flock) | Held only while `store.Install` runs. Two parallel syncs on different projects with overlapping deps both compute the same key; the loser sees `.ok` and skips. |
| Per-project sync lock | `<project>/.molt/sync.lock` (flock) | Serialises concurrent `molt sync` runs in the same project. |

### Atomic install

```
~/.molt/pkg/.tmp/{rand}/             # unpack here
~/.molt/pkg/{name}/{ver}/{abi}/.ok   # sentinel written last
```

Sequence:
1. Mkdir `.tmp/{rand}/`.
2. Unzip wheel into it (rejecting any zip-slip / `..` paths).
3. Apply RECORD permissions (preserve `+x` bits).
4. Write `.ok` containing the wheel's sha256.
5. `os.Rename` `.tmp/{rand}/` → `~/.molt/pkg/{name}/{ver}/{abi}/`.
6. If the destination already exists post-rename (race), discard the temp.

`store.Has(key)` checks `dir exists AND .ok present`, lock-free. Crashes
mid-install leave an orphan `.tmp/{rand}/` with no `.ok` — never a
half-installed visible directory.

### Built-binary integrity

Every `molt build` writes:
- A sidecar `dist/<app>-v<ver>.manifest.json` with per-file sha256s and a
  root hash.
- The same root hash embedded in the binary's trailing 53 bytes.

`molt verify-binary` recomputes both and compares. The launcher does the
same check on first install before extracting.

---

## 10. Troubleshooting

### "I see a `.molt/uv-env/` directory — what is it?"

Implementation detail. Newer uv versions always materialise a project env
on `uv add`/`uv remove`, even with `--no-sync`. molt redirects
`UV_PROJECT_ENVIRONMENT` to `<project>/.molt/uv-env/` so it doesn't clutter
the project root with a `.venv/`. The directory is mostly empty — packages
live in the global store, not in this env. Safe to ignore.

### "I see a `.venv/` in my project root after `molt add`."

You're running an old molt binary (pre-`fe350d5`). Rebuild:

```sh
go build -o ~/Documents/bin/molt . && bman add ~/Documents/bin/molt
```

Then `rm -rf .venv` from the affected projects.

### "`molt run python` says `command not found`"

Either:
- `molt sync` hasn't been run since the python-shim feature was added; run
  `molt sync`.
- The shim exists but `.molt/bin/` isn't on PATH. Check
  `cat .molt/syspath.json | jq .`. If it doesn't list the bin dir,
  re-sync.

### "Tests pass under `molt run pytest` but fail under bare `pytest`"

Bare `pytest` runs without molt's `PYTHONPATH`. Use `molt run pytest`,
period. If you must run a binary directly, use the full path
`<project>/.molt/bin/pytest` — it has the env baked in.

### "I deleted a project. Its packages are still in `~/.molt/pkg/`."

Run `molt gc`. Project entries with vanished directories are dropped from
the registry; their unique-to-them packages are removed.

### "I want a stricter sync — fail if anything would change."

`molt sync --frozen`. CI-friendly.

### "How do I share build artifacts across CI machines?"

`molt capture --output build-manifest.json` on machine A, transfer the
manifest, then `molt assemble --manifest build-manifest.json --output
dist/app` on machine B. Same root hash both sides.

---

## 11. Glossary

| Term | Meaning |
|---|---|
| **Global store** | `~/.molt/pkg/` — content-addressed package directory, one entry per `(name, version, py-abi-platform)`. |
| **ABI tag** | `cpNNN`, `abi3`, `none` — Python C-API compatibility tag. Part of the store key for native wheels. |
| **Platform tag** | `manylinux_2_28_x86_64`, `macosx_11_0_arm64`, `any`, etc. PEP 425 tag. |
| **Wheel cache key** | `(name, version, py_tag, abi_tag, platform_tag)`. |
| **`.ok` sentinel** | Empty file inside a store entry that signals "install completed atomically". |
| **`syspath.json`** | Per-project file listing the ordered store directories that make up the env. |
| **Console-script shim** | Generated POSIX/cmd script under `.molt/bin/` that execs the project interpreter with PYTHONPATH set, then calls a specific entry-point function. |
| **Python shim** | Generated `.molt/bin/python` and `.molt/bin/python3` that exec the project's interpreter with PYTHONPATH set, forwarding all args. |
| **Launcher** | Tiny Go binary embedded into every `molt build` artifact; handles install/run/verify on the target machine. |
| **Trailer** | Fixed 53-byte structure at the tail of every built binary: archive offset + root hash + version + size + magic. |
| **Root hash** | Merkle-style hash over every file's hash in the embedded manifest. Tamper-evident. |
| **Registry** | `~/.molt/registry.json` — `{projectDir: lockHash}` map used by `molt gc` to decide what's still referenced. |

---

*This manual reflects the codebase as of commit `edc24dc`. Output snippets
are real. If something here disagrees with what your `molt` binary does,
either the binary is stale (rebuild) or the manual is — open an issue.*
