# molt global package store

molt manages Python dependencies like Go modules: packages are installed once into a global content-addressed directory (`~/.molt/pkg/`) and shared across all projects on the machine. No per-project `.venv` is created. Switching Python versions or adding a dependency that another project already uses incurs no extra disk or download cost.

---

## Concepts

### The global store

All packages live under `~/.molt/pkg/` keyed by `(name, version, py-tag, abi-tag, platform-tag)`:

```
~/.molt/
├── pkg/
│   ├── requests/
│   │   └── 2.32.3/
│   │       └── py3-none-any/
│   │           ├── requests/          ← package source, ready to import
│   │           ├── requests-2.32.3.dist-info/
│   │           ├── .ok                ← written last; presence means entry is complete
│   │           └── .meta.json         ← { entry_points, source_sha256, wheel_filename }
│   ├── black/
│   │   └── 24.4.2/
│   │       └── cp311-cp311-macosx_11_0_arm64/
│   │           ├── black/
│   │           ├── ...
│   │           ├── .ok
│   │           └── .meta.json
│   └── .tmp/                          ← staging area; cleaned after every install
├── python/                            ← standalone interpreters (unchanged)
├── uv/                                ← pinned uv binary (unchanged)
├── pkg.lock                           ← flock used during installs
└── registry.json                      ← { projectDir: lockHash } for GC
```

The path key combines the PEP 503-normalized name, version, and the three wheel compatibility tags from PEP 427. Two projects that need the same package at the same version and ABI share exactly one directory — no copies, no hardlinks.

### Per-project bookkeeping

Each project gets a `.molt/` directory (committed to source control is fine):

```
myproject/
├── pyproject.toml
├── uv.lock
└── .molt/
    ├── syspath.json        ← interpreter path + ordered store dirs + ABI tags + lock hash
    ├── sitecustomize.py    ← auto-generated; processes .pth files in each store dir
    └── bin/
        ├── black           ← console-script shim with absolute paths baked in
        ├── pytest
        └── ...
```

`syspath.json` is the single source of truth for the project environment at runtime:

```json
{
  "python": "/Users/you/.molt/python/3.12.3/bin/python3",
  "version": "3.12.3",
  "py_tag": "cp312",
  "abi_tag": "cp312",
  "platform": "macosx_14_0_arm64",
  "lock_hash": "sha256:a3f...",
  "project_dir": "/Users/you/myproject",
  "syspath": [
    "/Users/you/myproject",
    "/Users/you/.molt/pkg/requests/2.32.3/py3-none-any",
    "/Users/you/.molt/pkg/certifi/2024.2.2/py3-none-any",
    "/Users/you/.molt/pkg/black/24.4.2/cp312-cp312-macosx_11_0_arm64",
    ...
  ]
}
```

---

## How `molt sync` works

`molt sync` is the only command that writes to the global store. It does not create a `.venv`.

```
1. Ensure uv binary (download once to ~/.molt/uv/)
2. Resolve interpreter
      - reads .python-version (project) or ~/.molt/.python-version (global)
      - if the version is not installed, runs `uv python install <version>`
3. Regenerate uv.lock if pyproject.toml is newer (skipped with --frozen)
4. Parse uv.lock  →  list of (name, version, wheels[])
5. Detect interpreter ABI  →  py_tag, abi_tag, platform_tags[]
6. Acquire ~/.molt/pkg.lock  (flock; multiple concurrent syncs serialize here)
7. For each resolved package:
      a. Select the best wheel from uv.lock using ABI + platform matching
      b. Compute store key from wheel filename (PEP 427)
      c. If store entry exists (.ok present) → skip  ← cache hit
      d. Else locate wheel: check uv's wheel cache first, download if absent
      e. Unpack wheel into .tmp/{rand}/, write .meta.json + .ok, rename to final key
8. Topo-sort packages by dependency graph
9. Write .molt/syspath.json  (interpreter + ordered store dirs + lock hash)
10. Write .molt/sitecustomize.py  (calls site.addsitedir() for each store dir)
11. Regenerate .molt/bin/<script> shims for every console_scripts entry point
12. Update ~/.molt/registry.json  (projectDir → lock_hash)
```

The install step (7e) is atomic: the wheel is unpacked into a temp directory, `.ok` is written last, then the directory is renamed to its final location. If two processes race, the second sees `.ok` and exits without touching anything.

---

## Wheel selection

When a package has multiple wheels in `uv.lock`, molt picks the best one for the active interpreter using PEP 425 scoring:

| Score | Match |
|-------|-------|
| 0 | Exact: wheel's `py_tag` contains interpreter's py_tag AND `abi_tag` matches |
| 10 | abi3: wheel uses `abi3`, interpreter is CPython, and wheel's min version ≤ interpreter |
| 20 | Pure-Python: `none` abi and `py3` or matching py_tag |

Within a score, platform-specific (`macosx_14_0_arm64`) beats `any`.

Compressed tags in wheel filenames (`py2.py3-none-any`, `cp311.cp312-abi3-manylinux`) are handled correctly: each dot-separated component is tested individually.

---

## How `molt run` uses the store

`molt run <task>` (or `molt run python`, `molt run black`, etc.) never activates a venv. Instead:

1. Loads `.molt/syspath.json`
2. Builds an environment with:
   - `PYTHONPATH=<projectDir>/.molt:<store_dir_1>:<store_dir_2>:...`
   - `PATH=<projectDir>/.molt/bin:$PATH`
   - `VIRTUAL_ENV`, `PYTHONHOME` unset
3. For `molt run python`, execs the interpreter from `syspath.json` directly
4. For a named task, runs the task command under this environment
5. For anything else (e.g. `molt run black`), resolves the binary in `.molt/bin` first, then `PATH`, then `syscall.Exec`s it

The `.molt/` directory is prepended to `PYTHONPATH` so `sitecustomize.py` is imported before any user code. `sitecustomize.py` calls `site.addsitedir(d)` for each store dir, which processes `.pth` files — this is how `setuptools`, `pkg_resources`, and namespace packages work correctly without a venv.

---

## Console-script shims

After sync, `.molt/bin/` contains a self-contained script for every `console_scripts` entry point declared in any installed package. Example for `black`:

```sh
#!/bin/sh
export PYTHONPATH='/Users/you/myproject/.molt:/Users/you/.molt/pkg/black/24.4.2/cp312-cp312-macosx_11_0_arm64:/Users/you/.molt/pkg/click/8.1.7/py3-none-any:...'
unset VIRTUAL_ENV PYTHONHOME
exec '/Users/you/.molt/python/3.12.3/bin/python3' -c 'import sys; from black import patched_main as _m; sys.exit(_m())' "$@"
```

Paths are absolute and baked in at sync time. Shims are regenerated on every `molt sync`; running `molt sync` is required after:
- switching Python versions (`molt python use 3.13`)
- adding or removing a package with console scripts
- moving the project directory

---

## Garbage collection

Packages accumulate in `~/.molt/pkg/` over time. `molt gc` removes store entries that no registered project references:

```bash
molt gc --dry-run   # show what would be removed
molt gc             # remove unreferenced entries
```

GC reads `~/.molt/registry.json`, re-parses each project's `uv.lock` to collect the set of referenced `(name, version, tag)` keys, walks `~/.molt/pkg/`, and removes entries not in the referenced set. Registry entries whose project directory no longer exists are also purged.

GC is safe to run while other projects are in use: it only removes entries that no registered project currently needs. A `molt sync` after GC would re-download and reinstall any removed entry.

---

## Concurrency

- **Global install lock** (`~/.molt/pkg.lock`): `flock(LOCK_EX)` is acquired around the install phase of `molt sync`. Multiple concurrent syncs across different projects serialize here only while installing; once a package has `.ok`, any number of processes can read it lock-free.

- **Atomic install**: temp dir → write `.ok` → `os.Rename`. If two processes install the same package simultaneously, one rename wins; the other sees `.ok` in the destination and discards its temp dir. No partial installs are visible.

- **Lock-free reads**: `store.Has()` and `store.Meta()` just stat `.ok` and read `.meta.json`. They need no lock because the rename is atomic and `.ok` is the sentinel.

---

## Editable installs and path dependencies

Packages with `source = {editable = "..."}` or `source = {path = "..."}` in `uv.lock` are not unpacked into the store. Instead their source directory is added directly to `PYTHONPATH`. This means changes to those packages are visible immediately without re-syncing.

---

## Command reference

| Command | What it does |
|---------|-------------|
| `molt sync` | Resolve + install deps into global store; write `.molt/syspath.json` and shims |
| `molt sync --frozen` | Skip `uv lock`; use existing `uv.lock` as-is |
| `molt sync --refresh` | Force-reinstall every package even if already in the store |
| `molt add <pkg>` | Add dependency to `pyproject.toml`, re-lock, re-sync |
| `molt remove <pkg>` | Remove dependency, re-lock, re-sync (orphaned store entries wait for `gc`) |
| `molt run <task>` | Run a named task from `[tool.molt.tasks]` under the store environment |
| `molt run python` | Launch the project's interpreter with store `PYTHONPATH` |
| `molt run <binary>` | Exec a binary from `.molt/bin/` or `PATH` under the store environment |
| `molt gc` | Remove store entries no registered project needs |
| `molt gc --dry-run` | Show what `molt gc` would remove without deleting anything |
| `molt python list` | Show installed and system Pythons |
| `molt python install 3.13` | Install a Python version via uv |
| `molt python use 3.13` | Pin project (or global) Python version; re-sync required |
| `molt info` | Show project environment summary including store linkage count |
| `molt doctor` | Diagnose environment issues |

---

## Migrating a project from `.venv`

Existing projects that have a `.venv/` keep working. The old code path is still present as a fallback in `molt run`. To migrate:

```bash
cd myproject
molt sync        # populates ~/.molt/pkg and writes .molt/
```

After a successful sync, `.molt/syspath.json` exists and `molt run` uses it in preference to any `.venv`. The `.venv/` itself is not deleted — you can remove it manually once you've verified things work.

```bash
# Verify isolation
molt python isolation-check    # reports any unexpected sys.path entries

# Clean up if desired
rm -rf .venv
```

---

## Troubleshooting

**"no wheel matches cp312/cp312 on [macosx_14_0_arm64]"**  
The lock file contains no wheel for your current Python/platform. Run `molt sync --refresh` after verifying `uv.lock` was generated for this platform. If you're on an architecture uv doesn't have a pre-built wheel for, you may need an sdist-based install (not yet supported).

**Import works in shell but not in molt run**  
A package was installed outside molt (e.g. `pip install` into the system Python). `molt run` builds PYTHONPATH exclusively from `.molt/syspath.json`. Run `molt sync` to make it official.

**Console script runs the wrong version**  
Shims in `.molt/bin/` have the interpreter path baked in at sync time. Run `molt sync` after `molt python use <version>`.

**Two projects have conflicting versions of the same package**  
Each project has its own `syspath.json` pointing to different store paths. The store holds both versions side-by-side under their respective keys — there is no conflict.

**Store is large**  
Run `molt gc` to remove entries no registered project references. Old Python-version-specific wheels (e.g. `cp311` after upgrading to `cp312`) accumulate and are removed by GC.
