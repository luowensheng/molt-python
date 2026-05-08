# 06-binary-dist

Packages a Click CLI (`sysinfo`) into a self-installing hermetic binary using
`molt build`. The binary embeds source, dependencies, and an SHA-256 integrity
trailer — no Python or pip required on the target machine.

## What this demo shows

- `molt.yaml` describes the deployment artifact (separate from `pyproject.toml`)
- Multiple named commands (`info`, `bench`, `version`) from one binary
- `molt build` produces a single executable with an embedded integrity manifest
- `molt inspect` and `molt verify-binary` for auditing built artifacts
- The binary installs and runs without Python on the target

## Project layout

```
06-binary-dist/
  sysinfo.py         Click CLI: info / bench / version subcommands
  molt.yaml          build config: commands, includes, integrity settings
  pyproject.toml     runtime deps: click, rich — plus dev tasks
  uv.lock
```

## Running in dev mode

```bash
# Display system info
molt run info

# Run a Python loop benchmark
molt run bench

# Print version
molt run version
```

## Building the binary

```bash
molt build
# → ./06-binary-dist  (~3.7 MB, darwin/arm64)
# → sysinfo-v0.1.0.manifest.json  (integrity manifest)
```

The build output includes:
- The compiled Go launcher
- A `tar.gz` payload containing `sysinfo.py`, wheels, and the integrity manifest
- A trailer with `[payload_offset][root_hash][MOLT0001]`

## Inspecting the binary

```bash
# Print the embedded manifest
molt inspect 06-binary-dist

# Verify the SHA-256 root hash matches the payload
molt verify-binary 06-binary-dist

# List all embedded files
molt inspect 06-binary-dist --files
```

## Installing and running on a target machine

```bash
# Install (extracts payload, verifies root hash, runs post_install hooks)
./06-binary-dist --install

# Run the default command (info)
./06-binary-dist run

# Run a named command
./06-binary-dist run bench
./06-binary-dist run version
./06-binary-dist run bench -- --n 50000000
```

The target machine needs no Python installation. The binary carries everything it
needs (or links back to a bundled interpreter when built with `--embed-python`).

## molt.yaml explained

```yaml
version: 1

project:
  name: sysinfo
  version: 0.1.0
  python: "3.11"

deps:
  strategy: pyproject      # read dependencies from pyproject.toml + uv.lock

include:
  - "sysinfo.py"           # source files to bundle

commands:
  default: info            # ./binary run → runs info
  info:
    exec: [python, sysinfo.py, info]
  bench:
    exec: [python, sysinfo.py, bench]
  version:
    exec: [python, sysinfo.py, version]

integrity:
  verify_on_install: true  # hash checked before first run
```

`molt.yaml` is deliberately separate from `pyproject.toml`. The Python packaging
metadata (for PyPI) stays in `pyproject.toml`; the deployment artifact config
lives in `molt.yaml`.

## Binary structure

```
┌────────────────────────┐
│  launcher (Go)         │  install / run / verify / uninstall subcommands
├────────────────────────┤
│  payload (tar.gz)      │  sysinfo.py + wheel dirs + integrity manifest
├────────────────────────┤
│  trailer               │  [payload_offset][root_hash][MOLT0001]
└────────────────────────┘
```

The root hash in the trailer is the Merkle root over all payload files. Any
tampering — even a single byte — fails `molt verify-binary`.

## Comparing distribution approaches

| | molt build | PyInstaller | Docker | pip + venv |
|---|---|---|---|---|
| Single file | ✓ | ✓ | ✗ (image) | ✗ |
| No Python on target | ✓ | ✓ | ✗ | ✗ |
| Integrity verified | ✓ SHA-256 | ✗ | ✓ digest | ✗ |
| Multiple entry points | ✓ | ✗ | ✓ | ✗ |
| Shared dep cache in dev | ✓ | ✗ | ✗ | ✗ |
