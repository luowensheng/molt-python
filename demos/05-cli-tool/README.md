# 05-cli-tool

A Click-based file utility CLI (`filetool`) that can be registered globally with
`molt tool install`, making it available anywhere in your shell without activating
a virtual environment.

## What this demo shows

- `src/` layout with a `[project.scripts]` entry point
- `molt tool install` writes a shim to `~/.molt/bin/filetool`
- The shim resolves the correct interpreter and `PYTHONPATH` at runtime — no venv
- CLI subcommands (`ls`, `checksum`, `stats`) built with Click + rich output

## Project layout

```
05-cli-tool/
  src/filetool/
    __init__.py      package version
    __main__.py      `python -m filetool` entry point
    cli.py           Click group with ls / checksum / stats commands
  pyproject.toml     [project.scripts] filetool = "filetool.cli:cli"
  uv.lock
```

## Running (dev mode)

```bash
# List files in the current directory
molt run ls

# Recursive file statistics
molt run stats

# Checksum a file
molt run python -m filetool checksum pyproject.toml
molt run python -m filetool checksum pyproject.toml --algo sha512

# Full help
molt run help
```

## Tasks defined

| Task | Command |
|---|---|
| `run` | `python -m filetool` (prints help) |
| `ls` | `python -m filetool ls .` |
| `stats` | `python -m filetool stats .` |
| `help` | `python -m filetool --help` |

## Installing globally

```bash
# Register filetool as a global shim in ~/.molt/bin/
molt tool install

# Now available from anywhere (add ~/.molt/bin to your PATH if not already):
filetool ls ~/Downloads
filetool stats ~/Documents --ext .pdf
filetool checksum /path/to/archive.zip --algo sha256

# See all registered tools
molt tool list

# Remove the shim
molt tool uninstall filetool
```

The shim at `~/.molt/bin/filetool` is a small script that calls back into molt to
set up the correct environment before executing the CLI. The source project does
not need to be on `PYTHONPATH` manually.

## CLI reference

### `filetool ls [DIRECTORY]`

List files with size and modification time.

```
Options:
  --ext TEXT                Filter by extension (e.g. .py)
  --sort [name|size|mtime]  Sort order (default: name)
```

### `filetool checksum FILE`

Compute a file checksum.

```
Options:
  --algo [md5|sha1|sha256|sha512]  Hash algorithm (default: sha256)
```

### `filetool stats [DIRECTORY]`

Recursive file statistics with extension breakdown.

```
Options:
  --ext TEXT   Count only files with this extension
```

## Why no venv for a global tool

Traditional approaches require either:
- `pip install --user` (pollutes the global Python)
- `pipx install` (another tool to manage)
- Manual venv + symlink dance

`molt tool install` writes a single shim. The environment is resolved at runtime
from the project's `.molt/syspath.json`. Updating the tool is `molt sync` in the
project directory — the shim stays the same.
