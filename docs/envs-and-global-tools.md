# Named environments and global tools

molt lets you run Python scripts in a reusable environment without having a
project, and install those scripts as global CLI tools that are accessible
from any directory. This document covers both features end-to-end.

---

## Table of contents

- [The problem being solved](#the-problem-being-solved)
- [Running a script with no project](#running-a-script-with-no-project)
- [Named environments](#named-environments)
  - [Creating an environment](#creating-an-environment)
  - [Running a script inside an environment](#running-a-script-inside-an-environment)
  - [Adding and removing packages](#adding-and-removing-packages)
  - [Pinning a Python version](#pinning-a-python-version)
  - [Per-environment variables](#per-environment-variables)
  - [Inspecting an environment](#inspecting-an-environment)
  - [Manual edits and re-sync](#manual-edits-and-re-sync)
  - [Deleting an environment](#deleting-an-environment)
- [Package age safety](#package-age-safety)
  - [How it works](#how-it-works)
  - [Configuring the minimum age](#configuring-the-minimum-age)
  - [Bypassing the check](#bypassing-the-check)
- [Global tools](#global-tools)
  - [One-time PATH setup](#one-time-path-setup)
  - [Installing a script as a global tool](#installing-a-script-as-a-global-tool)
  - [Installing a project task as a global tool](#installing-a-project-task-as-a-global-tool)
  - [Sharing one environment across many tools](#sharing-one-environment-across-many-tools)
  - [Overriding the environment on a project tool](#overriding-the-environment-on-a-project-tool)
  - [Changing or removing an environment binding](#changing-or-removing-an-environment-binding)
  - [Listing installed tools](#listing-installed-tools)
  - [Uninstalling a tool](#uninstalling-a-tool)
- [Borrowing a project's environment](#borrowing-a-projects-environment)
- [Reference: env.toml fields](#reference-envtoml-fields)
- [Reference: file layout](#reference-file-layout)

---

## The problem being solved

A normal molt project ties a Python environment to a source directory via
`pyproject.toml`. That model is great for applications, but awkward for:

- **One-off scripts** — you just want to run `plot.py` with `matplotlib`
  without creating a whole project
- **Shared utilities** — a `fetch-data.py` script used by three different
  projects; you don't want to duplicate the dependency list three times
- **CLI tools** — a script you want to call from anywhere in your shell,
  like `analyze`, `build-report`, or `upload`

Named environments and global tools solve all three cases.

---

## Running a script with no project

If there is no `pyproject.toml` in the current directory, molt falls back to
system Python automatically:

```
cd /tmp
echo 'print("hello")' > hello.py
molt run hello.py
```

```
hello
```

No setup needed. molt finds Python via `~/.molt/.python-version` (if set) or
the system `python3`/`python` on `PATH`. No virtualenv is activated, no
`PYTHONPATH` is set — just a bare interpreter.

This is useful for quick one-liners and scripts that only use the standard
library. For scripts that need third-party packages, create a named
environment instead.

---

## Named environments

A named environment is a reusable, project-independent package set. It lives
at `~/.molt/envs/<name>/` and can be used from any directory.

### Creating an environment

```
molt envs create <name> [package...] [--python <version>]
```

```
molt envs create ds pandas numpy matplotlib scikit-learn
```

```
Resolved 4 packages in 230ms
Installed 4 packages in 1.8s
✓ env "ds" ready (pandas, numpy, matplotlib, scikit-learn)
  run a script in it: molt run --env ds <script.py>
```

Packages follow [PEP 508](https://peps.python.org/pep-0508/) — the same
format `pip` and `uv` accept:

```
molt envs create web "flask>=3.0" "requests==2.31.0" "pydantic~=2.5"
```

### Running a script inside an environment

```
molt run --env <name> <script.py> [args...]
```

```
cd ~/data
molt run --env ds analyze.py sales.csv --output report.pdf
```

The script runs with the environment's packages on `PYTHONPATH`. The
environment's extra env vars (if any) are layered on top of the current shell
environment. The current working directory is unchanged.

`--env` can appear anywhere in the argument list:

```
molt run analyze.py --env ds sales.csv
molt run analyze.py sales.csv --env ds
```

### Adding and removing packages

```
molt envs add <name> <package...> [--allow-fresh]
molt envs remove <name> <package...>
```

```
molt envs add ds seaborn plotly
molt envs remove ds plotly
```

`add` skips packages already present (by base name, ignoring version
specifiers). `remove` matches by base name — you do not need to repeat the
version specifier.

### Pinning a Python version

```
molt envs create myenv --python 3.11 requests httpx
```

The Python version is written to `~/.molt/envs/myenv/.python-version` and
picked up automatically by `uv` on every subsequent sync. Omit `--python`
and the environment inherits the global `~/.molt/.python-version` (or
whatever `python3` resolves to on `PATH`).

### Per-environment variables

`env.toml` supports an optional `[env]` section. Variables declared there are
layered into the process environment whenever a script runs in that env:

```toml
# ~/.molt/envs/ml/env.toml
name = "ml"
packages = ["torch", "transformers"]

[env]
HF_HUB_CACHE = "/mnt/models"
TOKENIZERS_PARALLELISM = "false"
```

Edit `env.toml` by hand, then run `molt envs sync ml` to pick up the change.

### Inspecting an environment

```
molt envs list
```

```
NAME       PYTHON   PACKAGES   SYNCED
ds         3.12     6          yes
ml         3.11     2          yes
web        3.12     3          yes
```

```
molt envs info <name>
```

```
molt envs info ds
```

```
Env:      ds
Python:   3.12.3
Packages: pandas, numpy, matplotlib, scikit-learn, seaborn (5)
Synced:   yes
Path:     /Users/alice/.molt/envs/ds
```

### Manual edits and re-sync

`env.toml` is human-editable. After changing it by hand:

```
molt envs sync <name>
```

`sync` regenerates the internal `pyproject.toml` from `env.toml` and
re-runs the package installer.

### Deleting an environment

```
molt envs delete <name>
```

Removes `~/.molt/envs/<name>/` and the associated compiled state (syspath
cache). Packages in the global store (`~/.molt/pkg/`) are kept — they may
be shared with other envs or projects.

---

## Package age safety

Typosquatting and supply-chain attacks often rely on publishing a malicious
package and hoping someone installs it within the first few days. molt guards
against this by refusing to install packages that were uploaded to PyPI
recently.

### How it works

After every `envs create`, `envs add`, or `envs sync`, molt:

1. Reads the resolved package list from the compiled `syspath.json`
2. For each package found in `~/.molt/pkg/<name>/<version>/…`, queries the
   PyPI JSON API for the upload timestamp of that exact version
3. Caches results in `~/.molt/pkg-age-cache.json` (no repeat requests for
   the same name+version)
4. Returns an error if any package was uploaded less than the minimum age ago

Packages not found on PyPI (private indexes, VCS dependencies, local wheels)
are silently skipped.

Example error:

```
package age check failed (minimum: 1w):
  requests==3.0.0  uploaded 2d ago (too fresh)
  fastapi==0.112.0 uploaded 4d ago (too fresh)
Re-run with --allow-fresh to bypass this check.
```

### Configuring the minimum age

The default is **1 week**. Override it per-environment in `env.toml`:

```toml
name = "internal"
min_package_age = "0"     # disable — internal packages won't be on PyPI anyway
packages = ["my-private-lib"]
```

```toml
name = "prod"
min_package_age = "4w"    # extra caution: require 4 weeks
packages = ["django", "celery", "redis"]
```

Supported suffixes: `h` (hours), `d` (days), `w` (weeks).

| Value  | Meaning          |
|--------|------------------|
| `"1w"` | 1 week (default) |
| `"3d"` | 3 days           |
| `"24h"` | 24 hours        |
| `"0"`  | disabled         |

### Bypassing the check

Pass `--allow-fresh` when you know the packages are safe — for example, when
pinning an exact version you've already vetted:

```
molt envs create pinned "requests==2.31.0" --allow-fresh
molt envs add ds "seaborn==0.13.2" --allow-fresh
molt envs sync myenv --allow-fresh
```

---

## Global tools

A global tool is a thin shell script in `~/.molt/bin/` that delegates to
`molt`. Once `~/.molt/bin` is on your `PATH`, you can call the tool from any
directory like any other shell command.

### One-time PATH setup

Add this to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.) and reload
it:

```sh
export PATH="$HOME/.molt/bin:$PATH"
```

You only do this once. All tools installed now and in the future will be
picked up automatically.

### Installing a script as a global tool

```
molt tool install <script.py> --env <name> --name <tool-name>
```

```
molt tool install ~/scripts/analyze.py --env ds --name analyze
```

From now on, `analyze` is available from any directory:

```
cd ~/data/q1
analyze sales.csv --output q1-report.pdf
```

What happens under the hood:

```sh
# ~/.molt/bin/analyze (generated shim)
#!/bin/sh
# Auto-generated by `molt tool install`. Edits will be overwritten.
exec molt run --env 'ds' '/Users/alice/scripts/analyze.py' "$@"
```

The shim is just a file. No daemons, no registry services, no PATH patching
per-tool — just `exec`.

`--name` is required. The name becomes both the shim filename and the tool's
display name in `molt tool list`.

If a tool with that name already exists, use `--force` to overwrite:

```
molt tool install ~/scripts/analyze.py --env ds --name analyze --force
```

### Installing a project task as a global tool

For tools that belong to a molt project (i.e. they use that project's
dependencies and source code), use the project-based install:

```
cd ~/projects/myapp
molt tool install --name myapp          # installs the default entry point
molt tool install --name myapp-migrate --task db:migrate   # a named task
```

The generated shim points back to the project directory, so any change to
`pyproject.toml` (adding packages, editing code) is reflected immediately on
the next invocation — there is no "rebuild" step.

### Sharing one environment across many tools

Install multiple scripts with the same `--env`:

```
molt tool install ~/scripts/fetch.py    --env ds --name fetch
molt tool install ~/scripts/clean.py    --env ds --name clean
molt tool install ~/scripts/visualize.py --env ds --name visualize
```

All three tools use the `ds` environment. When you add a package to `ds`:

```
molt envs add ds polars
```

All three tools pick up `polars` on their next invocation — no reinstall, no
shim regeneration, nothing to do.

### Overriding the environment on a project tool

A project-based tool normally runs in the project's own environment. You can
override this to use a named environment instead:

```
cd ~/projects/myapp
molt tool install --name myapp --env ds
```

The shim becomes:

```sh
exec molt --project '/Users/alice/projects/myapp' run --env 'ds' "$@"
```

This is useful when multiple projects share a common set of packages via a
named env, or when you want a lighter, separately managed environment.

### Changing or removing an environment binding

```
molt tool set-env <tool-name> <env-name>    # switch to a different env
molt tool unset-env <tool-name>             # revert to the project's own env
```

```
molt tool set-env analyze ml      # analyze now runs in the 'ml' env
molt tool unset-env myapp         # myapp reverts to its project env
```

The shim is regenerated in place. No restart required.

### Listing installed tools

```
molt tool list
```

```
NAME         SOURCE                                    ENV    ALIVE
analyze      /Users/alice/scripts/analyze.py           ds     yes
clean        /Users/alice/scripts/clean.py             ds     yes
myapp        /Users/alice/projects/myapp               —      yes
myapp-test   /Users/alice/projects/myapp  (test)       —      yes
visualize    /Users/alice/scripts/visualize.py         ds     yes
```

`ALIVE` is `yes` when the backing script or `pyproject.toml` is still present
on disk. A `no` here means the source moved or was deleted — the shim still
exists but won't work.

### Uninstalling a tool

```
molt tool uninstall <name>
```

Removes both the shim (`~/.molt/bin/<name>`) and the metadata record
(`~/.molt/tools/<name>.json`). Packages in the named environment are
untouched.

---

## Borrowing a project's environment

`--env` also accepts a project directory path or a registered project name.
This lets you run an ad-hoc script using the packages from an existing
project without touching that project:

```
# By path
molt run --env ~/projects/myapp helper.py

# By registered name (if the project is registered with molt)
molt run --env myapp helper.py
```

The script sees the project's packages but runs in the current directory.
The project's own source files are not on `PYTHONPATH` unless you add the
project root explicitly.

---

## Reference: env.toml fields

`~/.molt/envs/<name>/env.toml` is human-editable TOML. Run
`molt envs sync <name>` after editing it by hand.

| Field             | Type            | Default  | Description |
|-------------------|-----------------|----------|-------------|
| `name`            | string          | required | Environment name (letters, digits, `-`, `_`) |
| `python`          | string          | (global) | Python version: `"3.11"`, `"3.12"` |
| `packages`        | array of strings | `[]`    | PEP 508 dependency specifiers |
| `min_package_age` | string          | `"1w"`   | Minimum age before a PyPI package is allowed; `"0"` to disable |
| `[env]`           | table           | `{}`     | Extra environment variables set when running in this env |

### Example

```toml
name = "prod"
python = "3.12"
min_package_age = "4w"
packages = [
  "django>=5.0,<6.0",
  "celery==5.3.6",
  "redis>=5.0",
  "gunicorn~=21.2",
]

[env]
DJANGO_SETTINGS_MODULE = "mysite.settings.prod"
REDIS_URL = "redis://localhost:6379/0"
```

---

## Reference: file layout

```
~/.molt/
├── bin/
│   ├── analyze          ← shim: exec molt run --env 'ds' '/path/analyze.py' "$@"
│   ├── clean            ← shim
│   └── myapp            ← shim: exec molt --project '/path/myapp' run "$@"
│
├── envs/
│   └── ds/
│       ├── env.toml         ← user-owned; edit here
│       ├── pyproject.toml   ← auto-generated from env.toml (do not edit)
│       └── uv.lock          ← written by uv
│
├── pkg/                     ← global package store (shared across all envs + projects)
│   └── pandas/2.2.1/…
│
├── pkg-age-cache.json        ← PyPI upload-time cache (advisory; safe to delete)
│
├── projects/
│   └── ds-a3f7c9b2/          ← compiled state for the 'ds' env
│       └── syspath.json
│
└── tools/
    ├── analyze.json          ← {name, script, env, created, updated}
    ├── clean.json
    └── myapp.json            ← {name, project_dir, task, created, updated}
```

Key points:

- **`~/.molt/bin/`** — add this to your `PATH` once; contains all tool shims
- **`~/.molt/envs/<name>/env.toml`** — the only file you need to edit;
  everything else is auto-generated or managed by molt
- **`~/.molt/pkg/`** — the global store; packages are shared automatically
  across environments and projects; nothing in here should be edited by hand
- **`~/.molt/pkg-age-cache.json`** — safe to delete; it is a performance
  cache only and will be rebuilt on the next sync
