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
   - 7.5 [Multi-project ops](#75-multi-project-ops)
   - 7.6 [Global tools](#76-global-tools)
   - 7.7 [Cython without ceremony](#77-cython-without-ceremony)
   - 7.8 [Eliminating sys.path.insert boilerplate](#78-eliminating-syspathinsert-boilerplate)
   - 7.9 [uv passthrough](#79-uv-passthrough)
   - 7.10 [Diagnostics & meta](#710-diagnostics--meta)
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

The project tree itself stays clean. molt-managed state lives outside the
project root, under `~/.molt/projects/<basename>-<hash16>/`. The hash is a
deterministic sha256 of the absolute project path, so the same checkout
always maps to the same state dir.

```
<project>/
├── .python-version             # e.g. "3.12"
├── pyproject.toml              # source of truth for deps (uv-style + [tool.molt.tasks])
├── uv.lock                     # generated by uv lock
├── README.md
├── src/                        # if you scaffolded with --template src
│   └── <package>/...           # your code
└── tests/...                   # ditto
```

No `.molt/` in your tree, no `.gitignore` entry needed.

### Per-project state — `~/.molt/projects/<basename>-<hash>/`

```
~/.molt/projects/myapp-1a2b3c4d5e6f7g8h/
├── syspath.json            # {python, py_tag, abi_tag, platform, syspath:[...]}
├── sitecustomize.py        # site.addsitedir for each store path (handles .pth)
├── meta.json               # back-pointer: {project_dir, created, last_sync, molt_version}
├── bin/                    # console-script + python shims
│   ├── python              # exec project's interpreter w/ PYTHONPATH set
│   ├── python3
│   ├── pytest              # console_scripts entry from pytest's RECORD
│   ├── ruff
│   └── ...
└── uv-env/                 # uv's mostly-empty project env (hidden impl detail)
```

Run `molt where` from inside the project to print these paths, or
`molt where <key>` for a single one.

> **Note for `molt build`.** The build path momentarily creates a transient
> `<project>/.molt/` directory containing the integrity manifest, embeds it
> into the produced binary at `src/.molt/manifest.json`, then deletes the
> directory on exit. The artifact tar layout is unchanged from earlier
> molt versions.

> **Migrating from older molt.** If a stale `<project>/.molt/` is already
> on disk, `molt sync` prints a one-time notice — it's safe to
> `rm -rf <project>/.molt`. The new state in `~/.molt/projects/` is
> populated automatically.

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

`molt init` is **minimal by default** — `pyproject.toml` + `.python-version`
+ `.gitignore` + `uv.lock` and nothing else. Opt in to scaffolding via
`--template`:

```
$ mkdir tagctl && cd tagctl
$ molt init
Initialising project "tagctl" (template: bare)...
Initialized project `tagctl`
Using CPython 3.11.14
Resolved 1 package in 11ms
✓ Done.

$ ls
.gitignore  .python-version  README.md  pyproject.toml  uv.lock
```

`.gitignore` is created (or amended) so `.molt/` is ignored — `uv init` writes
`.venv` here, which molt doesn't use, so the entry is rewritten.

Project names with dashes/dots are normalised for the package directory in
templates that need it: `molt init tag-control --template src` produces
`src/tag_control/`.

##### Built-in templates

| Template | Layout |
|---|---|
| `bare` (default) | minimal — pyproject.toml + python-version + uv.lock |
| `flat` | uv default — keeps `hello.py` |
| `app` | single-file: `main.py` at root + a `run` task |
| `src` | conventional `src/<pkg>/__init__.py` + `__main__.py` + `tests/` |
| `lib` | library — `uv init --lib` (hatchling) + `tests/` |

```
$ molt init my-app --template app
$ ls my-app
.gitignore  .python-version  README.md  main.py  pyproject.toml  uv.lock

$ molt init my-cli --template src
$ ls my-cli/src/my_cli
__init__.py  __main__.py
```

User templates can be registered too — see `molt template`.

Flags:
- `--template <name>` — apply a template (default `bare`). Lists with `molt template list`.
- `--python <ver>` — pin a specific Python (writes `.python-version`).
- `--no-lock` — skip the initial `uv lock`.

Existing files are never overwritten — re-running `molt init` on a populated
directory is safe.

#### `molt template`

Manage project templates.

```
$ molt template list
Available templates:

  app          [builtin] Single-file application: main.py at the project root.
  bare         [builtin] Minimal — just pyproject.toml + .python-version + uv.lock.
  flat         [builtin] uv default: keeps hello.py at the project root.
  lib          [builtin] Library: uv --lib layout + tests/.
  src          [builtin] Conventional src/ layout: src/<pkg>/__init__.py + __main__.py + tests/.
  my-cli       [user]    My team's standard CLI scaffold

User templates dir: /Users/you/.molt/templates
Apply with:        molt init --template <name>

$ molt template show src
Template: src [builtin]
  Conventional src/ layout: src/<pkg>/__init__.py + __main__.py + tests/conftest.py.

Files:
  src/
  src/__pkg__/
  src/__pkg__/__init__.py
  src/__pkg__/__main__.py
  template.toml
  tests/
  tests/conftest.py

$ molt template add my-cli ./my-cli-template
✓ added user template "my-cli" from ./my-cli-template
  → /Users/you/.molt/templates/my-cli

$ molt template remove my-cli
✓ removed user template "my-cli"
```

##### Authoring a user template

A template is a directory tree. Two substitution rules:

- A path component equal to `__pkg__` is renamed to the project's
  normalised package name (e.g. `tag-control` → `tag_control`).
- File contents are passed through string substitution: `{{name}}` becomes
  the project name, `{{pkg}}` becomes the package name.

Optional `template.toml` at the template root provides metadata:

```toml
description = "FastAPI service with uvicorn + pytest"

# Toggle uv init flags
use_uv_lib = false              # pass --lib to uv init
keep_hello = false              # don't delete uv's hello.py placeholder

# Packages to install post-init (calls `molt add` / `molt add --dev`)
add     = ["fastapi", "uvicorn[standard]"]
add_dev = ["pytest", "httpx"]

# Tasks injected under [tool.molt.tasks] in the new project's pyproject.toml.
# {{name}} and {{pkg}} are substituted.
tasks = """
dev  = { module = "{{pkg}}", args = ["--reload"] }
test = "pytest tests/ -v"
"""
```

User templates take precedence over built-ins of the same name, so you can
override `src` with your own opinionated version if you want.

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

##### `-r requirements.txt` — bulk add from a requirements file

```
$ molt add -r requirements.txt
$ molt add -r requirements.txt -r requirements-extra.txt --dev
```

Repeatable; mixes freely with positional packages. uv reads each file line
by line and adds every entry to `pyproject.toml`. For an automated
"requirements.txt as the source of truth" workflow, see [`[tool.molt]
requirements`](#requirements-files-as-source-of-truth) below.

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

<a id="requirements-files-as-source-of-truth"></a>
##### Requirements files as the source of truth

If you'd rather edit a `requirements.txt` than `pyproject.toml`, declare it:

```toml
# pyproject.toml
[tool.molt]
requirements = ["requirements.txt"]
# (or multiple: requirements = ["requirements.txt", "requirements-extra.txt"])
```

On every `molt sync`, molt checks each listed file's mtime against
`uv.lock`. If the requirements file is newer (i.e. you edited it), molt
runs `uv add -r <file>` to merge its lines into `pyproject.toml`'s
`[project] dependencies`, then locks. Net effect: edit
`requirements.txt`, run `molt sync`, done.

Skipped under `--frozen`, so CI never mutates deps.

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

#### `molt python run <args...>`

Invoke the project's Python interpreter directly with the given arguments,
under the project environment (`PYTHONPATH` set to the global store, venv
variables stripped). molt never relies on a `python` command being on
PATH — it execs the uv-managed interpreter resolved at sync time.

If the project hasn't been synced yet, `molt python run` syncs first.

```
$ molt python run -c 'import sys; print(sys.executable)'
/Users/you/.local/share/uv/python/cpython-3.11.14-macos-aarch64-none/bin/python3

$ molt python run -m tagctl --help
Usage: tagctl [OPTIONS] COMMAND [ARGS]...
```

##### Version override: `-v <version>` / `--python <version>`

Run an arbitrary Python version, not the project's pinned one. Useful for
quick stdlib checks across versions, scratch scripts, or smoke-testing a
new Python before pinning it. Works in either position:

```
$ molt python -v 3.12 run -c 'import sys; print(sys.version)'
3.12.11 (main, ...)
$ molt python run -v 3.12 -c 'import sys; print(sys.version)'
3.12.11 (main, ...)
```

If the requested version isn't installed, molt auto-installs it via uv —
no external Python required.

This mode runs Python with a **clean env** — `PYTHONPATH`, `VIRTUAL_ENV`,
and `PYTHONHOME` are stripped, and the project's store directories are
**not** injected (they're ABI-specific to the project's pinned Python and
generally won't be compatible with a different version). Use without
`-v` for "run my project's Python with all its deps".

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

Tasks live in `pyproject.toml` under `[tool.molt.tasks]`. Three forms are
supported:

```toml
[tool.molt.tasks]
# 1. Module form — runs the project's Python with `-m <module>`. molt
#    execs spec.Python directly, so you never type "python" yourself.
dev   = { module = "tagctl", args = ["--reload"] }

# 2. Script form — runs a Python file. Path is relative to the project
#    root (or the task's `dir =` if set).
seed  = { script = "scripts/seed_db.py", args = ["--env", "dev"] }

# 3. Shell form — arbitrary shell command. Console-script shims under
#    .molt/bin/ (pytest, ruff, etc.) are on PATH automatically.
test  = "pytest tests/ -v"
lint  = "ruff check src/ tests/"
```

`module` and `script` bypass `/bin/sh` entirely and exec the uv-managed
interpreter directly — molt has zero dependence on a system `python` being
on PATH. Prefer them for "run my Python code"; reserve the shell form for
shell-y tasks (pipes, multi-step commands, console-scripts).

Exactly one of `command` / `module` / `script` must be set per task.

Note: when a shell command contains double-quotes, molt writes a TOML
literal string (`'…'`) so the quotes don't need escaping.

**Auto-sync on first run.** If `.molt/syspath.json` doesn't exist when you
run `molt run <task>`, molt syncs first (one-time, prints `→ first run,
syncing project…`). After that the env is materialised and subsequent
runs go straight to dispatch.

#### `molt run <task> [args...]`

Dispatch order:
1. If `<task>` ends in `.py` and the file exists, exec the project's
   Python interpreter on it (single-file mode — see below).
2. If `<task>` is a name in `[tool.molt.tasks]`, run it.
3. Else look up `<task>` in `<project>/.molt/bin/`, then `PATH`, and
   `syscall.Exec` it under the project env.

##### Single-file: `molt run <script>.py`

For projects that are just `pyproject.toml` + a script, no task
definition is required:

```
$ molt init my-app --template app
$ cd my-app
$ molt run main.py
→ first run, syncing project…
Hello from my-app!

$ molt run scripts/seed.py --env dev
```

molt resolves the path, execs the project's Python with PYTHONPATH set,
and forwards remaining args verbatim.

```
$ molt run hello
$ python -c "print(123)"
123

$ molt run pytest -k smoke
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
custom commands; falls back to inferring from `pyproject.toml`. Embeds
`[tool.molt.tasks]` so the binary can run user-defined entry points
(`./<bin> <task>`) — see [§7.4 Built-binary commands](#built-binary-commands)
below.

```
$ molt build
Building tagctl v0.1.0 (darwin/arm64)...
  Compiled launcher (darwin/arm64)
  → Embedding files (strict=true, legacy denylist)...
  ✓ Payload: 9.3 KB (23 files)
  ✓ Integrity manifest: tagctl-v0.1.0.manifest.json
  ✓ Created: ./tagctl (3.7MB)
    root_hash: 3a2c0881e6ee4dc85672ae82a06c74f7451741d61301d960a523eed155d8fe2a
    files:     23   total: 0.0MB
    install:   molt_INSTALL_BASE=/opt ./tagctl install
```

Common flags:
- `--output <path>` — output path. Default: just `<name>` in cwd (no
  version stamp, no `dist/` prefix). The version is in the embedded
  manifest and exposed via `<bin> molt version`.
- `--os <darwin|linux|windows>` and `--arch <amd64|arm64>` — cross-compile.
- `--version <ver>` — embed a version string (default reads `pyproject.toml`).

##### Excluding files from the binary

The embedder honours `.moltignore` (gitignore-style globs) at the project
root. Use it to skip large or sensitive files that don't need to ship:

```
# .moltignore
.git/
__pycache__/
*.pyc
docs/
notebooks/
*.parquet
.env
```

`molt build --embed-strict` (the default) refuses to build if files matching
known sensitive patterns (`.env`, `*.key`, `*.pem`, etc.) are not excluded.
Pass `--embed-strict=false` to override (not recommended).

#### `molt package [flags]`

Build Python distribution artefacts (wheel + sdist) suitable for upload to
PyPI. Thin wrapper over `uv build`. Output goes to `dist/` by default.

```
$ molt package
Successfully built dist/mylib-0.1.0.tar.gz and dist/mylib-0.1.0-py3-none-any.whl
✓ Packaged to dist/ — upload with `uv publish` or `twine upload dist/*`

$ ls dist/
mylib-0.1.0-py3-none-any.whl
mylib-0.1.0.tar.gz
```

Flags:
- `--output <dir>` — output directory (default `dist`).
- `--sdist` — produce only the source tarball.
- `--wheel` — produce only the wheel.

`molt build` and `molt package` solve different problems: `build` produces
**a single executable** that runs on machines without Python; `package`
produces **importable Python distribution artefacts** for `pip install` and
PyPI.

#### Built-binary commands

The binary produced by `molt build` separates user tasks from molt's
meta operations cleanly:

```
$ ./myapp                        # default entry (main.py / __main__.py)
$ ./myapp <task> [args...]       # task from [tool.molt.tasks]
$ ./myapp <flags...>             # if first arg is flag-shaped, passes through to default entry
$ ./myapp -- <args...>           # explicit escape — args go to default entry
$ ./myapp --<meta> [args...]     # molt meta command (install/info/version/...)
```

User tasks come from the project's `[tool.molt.tasks]` block — embedded at
build time. Same syntax as dev (`module` / `script` / `command`). Example:

```toml
[tool.molt.tasks]
serve   = { module = "myapp.server", args = ["--port", "8080"] }
worker  = { module = "myapp.worker" }
migrate = { script = "scripts/migrate.py" }
```

Then on the target host:

```
$ ./myapp serve
$ ./myapp worker --concurrency 4
$ ./myapp migrate --dry-run
```

Meta commands live entirely under reserved `--<flag>` form. The reserved
set is closed (8 flags) so user task names can never accidentally collide:

| Flag | Purpose |
|---|---|
| `<bin> --install [--prefix DIR] [--offline] [--verbose] [--no-verify]` | Extract payload, set up env. |
| `<bin> --uninstall` | Remove the install. |
| `<bin> --verify [--deep]` | Recompute root hash; compare to embedded. |
| `<bin> --info` | Name, version, build time, target, integrity, install state, defined tasks. |
| `<bin> --version` | `<name> <version>`. |
| `<bin> --hash` | Embedded root hash, hex (script-friendly). |
| `<bin> --commands` | List defined tasks (one per line). |
| `<bin> --help` / `-h` | Usage. |

**Conflict escape.** If your program legitimately uses one of those flags
(e.g. argparse's `--version`), prefix args with `--`:

```
$ ./myapp -- --version       # default entry receives --version
$ ./myapp --version          # molt prints the build's version
```

This is the standard Unix convention. The first `--` is the molt-flag
terminator; everything after goes to the program.

##### Environment exposed to user code

Every `<bin> <task>` and default-entry invocation runs with these env
vars set:

| Var | Value |
|---|---|
| `MOLT_APP_DIR` | Top-level install directory. |
| `MOLT_APP_SRC` | Project source root inside the install dir (`.../src/`). |
| `MOLT_APP_NAME` | App name. |
| `MOLT_APP_VERSION` | App version. |
| `MOLT_APP_BUILT` | Build timestamp (RFC3339). |
| `MOLT_APP_TARGET` | Build target (`darwin/arm64`, `linux/amd64`, …). |

Use these for locating shipped data files, logging build provenance, or
implementing self-update checks:

```python
import os
from pathlib import Path

config = Path(os.environ["MOLT_APP_DIR"]) / "config" / "default.yaml"
print(f"Running {os.environ['MOLT_APP_NAME']} v{os.environ['MOLT_APP_VERSION']}")
```

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

### 7.5 Multi-project ops

Once a project has been synced, it appears in molt's registry. The
`project` subcommand surfaces the registry; the global `--project` flag
targets a registered project from anywhere — no `cd` required.

#### `molt project list`

```
$ molt project list
NAME              HASH              LAST SYNC            STATE           PATH
myapp             1a2b3c4d5e6f7g8h  2026-05-04 14:22:01  ✓ alive         /Users/me/work/myapp
tagctl            9c1b3a2d8e4f5e7d  2026-05-03 10:15:33  ✓ alive         /Users/me/work/tagctl
old-experiment    f7e8d9c0a1b2c3d4  2026-04-12 09:00:01  ✗ source missing /Users/me/old/experiment

3 project(s); state at /Users/me/.molt/projects
```

#### `molt --project <q> <command>`

`<q>` is matched in three forms (in order):

1. Absolute path — `--project /Users/me/work/myapp`
2. Hash prefix — `--project 1a2b3c` (≥ 4 hex chars; ambiguous → error with candidates)
3. Basename — `--project myapp` (unique match wins; ambiguous → error)

Works with every command that operates on "the current project" —
`run`, `sync`, `add`, `remove`, `lock`, `tree`, `info`, `python`,
`where`, `editor`, `task`. Position-flexible: works as
`molt --project foo run dev` or `molt run --project foo dev`.

```
$ cd /tmp
$ molt --project myapp run dev          # dispatch myapp's `dev` task from /tmp
$ molt --project myapp sync              # re-sync myapp from anywhere
$ molt --project tagctl info             # show tagctl's status
```

Commands that take a project as an arg (`init`, `build`, `package`,
`adopt`, `capture`, `assemble`) ignore `--project` — those define a *new*
project at cwd or arg.

#### `molt project info <q>`, `molt project where <q> [<key>]`

Same surface as plain `molt info` and `molt where`, but for any registered
project.

#### `molt project cd <q>`

Print the project's source path. Shell idiom:

```
$ cd "$(molt project cd myapp)"
```

#### `molt project purge <q>`

Drop registry + state dir. The project's source directory is **never
touched**. Renamed from `remove` because that was ambiguous.

```
$ molt project purge old-experiment
✓ purged old-experiment
```

##### Bulk purge filters

```
$ molt project purge --older-than 30d   # by last_sync age (units: d, w, h, m, s)
$ molt project purge --unused           # source dir is gone
$ molt project purge --dry-run          # preview only
$ molt project purge --older-than 30d --yes   # skip confirmation
```

Bulk operations require interactive confirmation by default (or `--yes`
for scripted use). `--state-only` keeps the registry entry but drops the
materialised state — useful to free disk without forgetting a project.

#### `molt project reinit <path>`

Re-register a previously purged project. Takes a path because post-purge
molt no longer knows the name.

```
$ molt project purge old-experiment
$ ls /Users/me/old/experiment/pyproject.toml   # source still there
$ molt project reinit /Users/me/old/experiment
```

### 7.6 Global tools

Like `uv tool install` / `pipx`: install a project's CLI globally so
`<name>` runs from any cwd. Distinct from `molt build`:

| | `molt build` | `molt tool install` |
|---|---|---|
| Output | Self-contained binary | Tiny shell shim |
| Dep changes | Need to rebuild | Reflected immediately |
| Cython/multipy recompile | Need to rebuild | Reflected immediately |
| Target | Any machine, no Python required | Dev machine only |
| Invocation | `./myapp` | `myapp` (on PATH) |

#### `molt tool install [<path>] [--name <n>] [--task <task>]`

Registers the project at `<path>` (default cwd) as a global tool.

```
$ cd ~/work/myapp
$ molt tool install
✓ installed tool "myapp"
  shim:    /Users/me/.molt/bin/myapp
  source:  /Users/me/work/myapp

/Users/me/.molt/bin is not on your PATH. Add this to your shell profile:
  export PATH="/Users/me/.molt/bin:$PATH"

$ myapp                       # runs from anywhere
```

The shim is generated as `~/.molt/bin/<name>` (POSIX shell script;
`.cmd` on Windows). Each invocation re-resolves the project's current
syspath, so dep changes take effect with no re-install. `--name` and
`--task` override defaults; `--force` overwrites an existing tool.

#### `molt tool list`, `molt tool show <name>`, `molt tool uninstall <name>`

```
$ molt tool list
NAME                 TASK       LAST UPDATE          STATE           SOURCE
myapp                (default)  2026-05-04 14:22:01  ✓ alive         /Users/me/work/myapp
tagctl               serve      2026-05-03 10:15:33  ✓ alive         /Users/me/work/tagctl

2 tool(s); shims at /Users/me/.molt/bin

$ molt tool uninstall tagctl
✓ uninstalled tool "tagctl"
```

`molt gc` automatically prunes orphan tools (whose source dir disappeared)
alongside orphan project state.

#### `molt tool path`

Print `~/.molt/bin/` for shell PATH wiring:

```
$ molt tool path
/Users/me/.molt/bin

# .zshrc / .bashrc:
export PATH="$(molt tool path):$PATH"
```

### 7.7 Cython without ceremony

Drop a `.pyx` next to a `.py` in `src/`, run `molt sync`, and `import` works.
No `setup.py`, no `[build-system]` wiring, no manual `cythonize` call. molt
discovers `.pyx` files at sync time, compiles them with the project's
uv-managed Python + the system C compiler, caches the output by content
hash at `~/.molt/native/<hash>/`, and merges the compiled extensions back
into your package via a generated `__init__.py` shim.

```sh
$ molt init --template src fastmath
$ cd fastmath
$ cat > src/fastmath/_inner.pyx <<'EOF'
def add(int a, int b):
    return a + b
EOF
$ cat > src/fastmath/__main__.py <<'EOF'
from fastmath._inner import add
print(add(2, 3))
EOF
$ molt add Cython
$ molt run
→ cython: 1 source(s)
  ↻ cython  fastmath._inner
result: 5
```

Edit the `.pyx`, re-run, and only the changed module recompiles
(content-hash cache hit otherwise). No build script, no `[build-system]`
config, no `try / except ImportError` shadow trick.

##### How it works

1. **Discover.** Sync walks `<project>/src/` for `*.pyx`, pairs each with
   its dotted module name (`src/fastmath/_inner.pyx` → `fastmath._inner`).

2. **Cache key.** `sha256(content || abi_tag || platform)`. Same source,
   same Python ABI → cache hit. Different ABI → separate cache entry.

3. **Compile.**

   ```sh
   <spec.Python> -m cython --3str -o /tmp/<rand>/<mod>.c <src>.pyx
   <cc> -O2 -shared -fPIC -I<py-include> -o <out>/<mod>.<ext_suffix> /tmp/<rand>/<mod>.c
   ```

   - `<spec.Python>` is the uv-managed interpreter — never your system
     `python3`. `PYTHONPATH` is set so Cython resolves out of molt's
     global store.
   - `<cc>` is the first usable compiler on PATH (`cc` → `clang` → `gcc`,
     or `$CC` if set).
   - `<ext_suffix>` is `.cpython-311-darwin.so` etc., from
     `sysconfig.get_config_var('EXT_SUFFIX')`.

4. **Cache.** Output goes to `~/.molt/native/<hash>/<mod>.<ext_suffix>` plus
   a `meta.json` sidecar. Shared across every project on the machine.

5. **Project view.** `~/.molt/projects/<base>-<hash>/cython/<pkg>/<mod>.<ext_suffix>`
   is symlinked to the cache (hard copy on Windows). The cython dir is
   prepended to `Spec.Syspath` so Python finds the merged package.

6. **Path merge.** Each `cython/<pkg>/__init__.py` is generated by molt
   to extend `__path__` so the user's `src/<pkg>/` (with `.py` modules) is
   union'd with the cython dir (with `.so` modules). One importable
   package, two locations on disk, no source-tree pollution.

##### Editor support

Pyright and Pylance pick up the cython dirs through the existing
`extraPaths` plumbing — `molt editor pyright` re-runs after every sync.
Type stubs (`*.pyi`) work as expected; Cython generates accurate ones
when invoked with `--3str`.

##### Edge cases

| Case | Behaviour |
|---|---|
| No `.pyx` files | No-op; sync proceeds unchanged. |
| Cython not in deps | Warning at sync, no compilation. Existing `.so` files keep working. Add it: `molt add Cython`. |
| C compiler missing | Warning, sync skips compilation. Install `clang` or `gcc`. |
| `.pyx` syntax error | Sync fails with the full Cython error + offending source. |
| C compile error | Sync fails with the cc output. |
| Source unchanged since last build | Cache hit; no work. |
| Source content same but different ABI | New cache entry per ABI; both coexist for multi-version projects. |
| `molt project purge` | Project state removed; cache survives for future re-syncs. |
| `molt gc` | Prunes `~/.molt/native/` entries no live project's `native.json` references. |

##### `molt build` — bundling Cython into binaries

`molt build` injects compiled `.so` files into the binary's payload at
`src/<pkg>/<mod>.<ext_suffix>`, next to the source modules. Cross-build is
refused if the cython artefact's OS+arch doesn't match the build target —
build on the target host, or on a host with a matching cross-toolchain
configured (deferred to a future round).

> **Caveat — built-binary Python ABI.** `molt build`'s install lifecycle
> currently symlinks to whatever `python3` is on the target host, *not*
> the molt-managed Python from build time. If the target's `python3` has
> a different ABI than the build's (e.g. 3.14 host vs 3.11 build), the
> Cython `.so` won't load — you'll see `No module named <pkg>._<mod>`.
> This is a pre-existing limitation of `molt build` (any native wheel has
> the same issue); for now, ensure target hosts have a matching CPython
> minor version. A follow-up will bundle the uv-managed Python into the
> binary so this just works.

##### Other native paths

Cython is one of five native-module pipelines molt ships:

| Pipeline | What you write | Reference |
|---|---|---|
| Cython | `.pyx` source | this section |
| Rust + PyO3 | `.rs` source with `#[pymodule]` | [`handbook.md` → Rust + PyO3](handbook.md#rust--pyo3-modules) |
| Kernel module (any C-ABI lang) | `<name>.molt.toml` + `<name>.zig`/`.c`/`.cpp`/`.odin`/… | [`kernel-modules.md`](kernel-modules.md) |
| Pre-built `.so` binding | `<name>.molt.toml` + `<name>.so` | [`kernel-modules.md` → Pre-built `.so`](kernel-modules.md#pre-built-so-binding) |
| Multi-file external project | `[[tool.molt.native]]` recipe | [`native-modules.md`](native-modules.md) |

Per-extension build commands for kernel modules are managed via
`molt kernel-builder list / show / add / remove / edit / reset / path`.
Adding support for Odin / Nim / Fortran / etc. is a one-line YAML edit
(or `molt kernel-builder add <ext> --from-template`); see
[`kernel-modules.md` → Kernel builders](kernel-modules.md#kernel-builders).

For runtime path configuration (vendored `.so` files, helper binaries,
macOS frameworks), use the `[tool.molt.runtime]` section:

```toml
[tool.molt.runtime]
extra_paths = ["vendor/lib", "vendor/bin", "vendor/Frameworks"]
```

Each entry is prepended to `PATH`, and to the platform-appropriate
dynamic-linker / framework env vars (`LD_LIBRARY_PATH` on Linux,
`DYLD_FALLBACK_LIBRARY_PATH` and `DYLD_FALLBACK_FRAMEWORK_PATH` on
macOS). This is **distinct from `[tool.molt] extra_paths`** in the
next section, which adds *Python source* dirs to `PYTHONPATH`. The
`runtime` variant is for native binaries and shared libraries; the
top-level one is for Python code.

##### Environment variables

Two layers, both injected into every process molt spawns:

```sh
$ molt env list                               # global ~/.molt/env.yaml
$ molt env get HTTPS_PROXY
$ molt env set HTTPS_PROXY http://proxy:8080  # global
$ molt env set DATABASE_URL postgresql://localhost/dev --local
                                              # writes to project pyproject.toml
$ molt env unset HTTPS_PROXY
$ molt env unset DATABASE_URL --local
$ molt env edit                                # opens ~/.molt/env.yaml in $EDITOR
$ molt env path                                # prints global YAML path
```

Project layer in pyproject.toml:

```toml
[tool.molt.runtime.env]
DATABASE_URL     = "postgresql://localhost/dev"
LOG_LEVEL        = "DEBUG"
PYTHONUNBUFFERED = "1"
```

Resolution priority (highest first): project `[tool.molt.runtime.env]`
> parent shell env (so `export FOO=…` always wins over a global
default) > global `~/.molt/env.yaml`. Reserved names (`PYTHONPATH`,
`VIRTUAL_ENV`, `PYTHONHOME`) are molt-managed and rejected.

The global file is `0600` since it can hold credentials. Saves are
atomic. Env injection happens in `syspath.Spec.BuildEnv`, the same hook
that sets `PYTHONPATH` and `extra_paths`, so it flows through every
`molt run` / `molt task` / `molt python run` / `molt build` invocation.

For full reference and use cases see
[`handbook.md` → Environment variables](handbook.md#environment-variables).

### 7.8 Eliminating `sys.path.insert` boilerplate

Common Python pain — shared code outside the package's installed location:

```python
# top of every script
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))
```

Instead, declare those paths in `pyproject.toml`:

```toml
[tool.molt]
extra_paths = ["../shared", "vendor/lib", "scripts"]
```

molt adds them to `Spec.Syspath` at sync time, so they're on `PYTHONPATH`
for every `molt run` / `<bin> task` / structured task invocation, and
editor configs (`.vscode/settings.json`, `pyrightconfig.json`)
auto-include them via `extraPaths` — IntelliSense and goto-def work
without sys.path hacks.

```toml
# pyproject.toml
[tool.molt]
extra_paths = ["../shared"]
```

```python
# main.py — no sys.path.insert needed
import shared_helpers
print(shared_helpers.OK)
```

```
$ molt sync
$ molt run main.py
hello from shared
```

Resolution: each entry is relative to the project root (absolute paths
taken as-is). Missing paths emit a warning at sync but don't fail.
Order in `Syspath`: project source → extra_paths → global-store dirs.

### 7.9 uv passthrough

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

### 7.10 Diagnostics & meta

#### `molt where [<key>]`

Print the absolute path of any molt-managed location for the current
project. With no key, prints a labelled table; with a key, prints just the
path + newline (script-friendly).

```
$ molt where
  python         /Users/me/.local/share/uv/python/cpython-3.11.14-…/bin/python3
  state          /Users/me/.molt/projects/myapp-1a2b3c4d5e6f7g8h
  bin            /Users/me/.molt/projects/myapp-1a2b3c4d5e6f7g8h/bin
  syspath        /Users/me/.molt/projects/myapp-1a2b3c4d5e6f7g8h/syspath.json
  sitecustomize  /Users/me/.molt/projects/myapp-1a2b3c4d5e6f7g8h/sitecustomize.py
  uv-env         /Users/me/.molt/projects/myapp-1a2b3c4d5e6f7g8h/uv-env
  store          /Users/me/.molt/pkg

$ cd "$(molt where state)"            # jump into the state dir
$ ls "$(molt where bin)"              # list available shims
$ "$(molt where python)" --version    # invoke the project's interpreter
```

Keys: `state`, `python`, `bin`, `syspath`, `sitecustomize`, `uv-env`,
`store`. Path-derived keys (`state`, `bin`, `uv-env`, `store`) work even
before the first sync; `python` and `syspath` require a successful sync.

#### `molt editor [vscode|pyright]`

Write or refresh editor / language-server config so IntelliSense, goto-def,
and type-checking find the project's interpreter and dep store dirs.

| Editor | File written | Keys |
|---|---|---|
| `vscode` | `<project>/.vscode/settings.json` | `python.defaultInterpreterPath`, `python.analysis.extraPaths`, `python.terminal.activateEnvironment` |
| `pyright` | `<project>/pyrightconfig.json` | `pythonPath`, `extraPaths` (used by pyright/Pylance/basedpyright) |

```
$ molt editor vscode
✓ wrote /Users/me/myapp/.vscode/settings.json

$ molt editor pyright
✓ wrote /Users/me/myapp/pyrightconfig.json

$ molt editor                       # auto-detects from existing files
✓ refreshed vscode config
✓ refreshed pyright config
```

VS Code settings are **JSON-merged** — only the three molt-managed keys are
touched, every other key in the file is preserved. If the file uses
JSON-with-comments (jsonc), molt errors out unless you pass `--force`
(which strips comments and overwrites).

`pyrightconfig.json` is treated as **molt-managed**: every call overwrites
it wholesale. If you need a hand-tuned pyrightconfig, edit it after
`molt sync` (or simply remove it — molt only auto-refreshes when the file
already exists).

**Auto-refresh on sync.** If either config file already exists, every
successful `molt sync` rewrites the molt-managed keys silently. Create the
file once with `molt editor <name>`, then forget about it.

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
