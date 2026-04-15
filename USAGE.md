# PyExec: From Zero to Production

A complete walkthrough — from a fresh Python project to a running production deployment on Linux, macOS, or Windows.

---

## Prerequisites

**Build machine** (any OS):

```bash
# Go 1.22+
# https://go.dev/dl/

# uv — Python package manager
curl -LsSf https://astral.sh/uv/install.sh | sh       # Linux/macOS
# Windows: powershell -c "irm https://astral.sh/uv/install.ps1 | iex"

# PyExec
git clone https://pyexec
cd pyexec
go build -o pyexec ./cmd/pyexec          # Linux/macOS
go build -o pyexec.exe ./cmd/pyexec      # Windows

pyexec version   # → pyexec dev (linux/amd64)
```

**Target/production machine**: nothing required. Not even Python.

---

## Step 1 — Create a New Project

```bash
pyexec init myapp
cd myapp
```

This calls `uv init` to scaffold the project, then automatically runs `uv lock` to generate the initial lockfile. The project is immediately buildable.

```bash
pyexec init myapp --python 3.12    # pin Python version
pyexec init myapp --lib            # library layout (src/)
pyexec init myapp --no-lock        # skip initial lock
```

Project layout:
```
myapp/
├── pyproject.toml    ← project metadata and dependencies
├── uv.lock           ← exact lockfile (auto-generated, commit to git)
└── myapp/
    ├── __init__.py
    └── main.py
```

---

## Step 2 — Add Dependencies

```bash
pyexec add fastapi uvicorn requests
pyexec add --dev pytest ruff
```

This calls `uv add` — updates `pyproject.toml`, resolves the graph, and regenerates `uv.lock` in one step. No separate `pip install` or lock command needed.

```bash
pyexec remove requests          # remove a dependency
pyexec tree                     # show dependency tree
pyexec uv pip show fastapi      # inspect a package (raw uv passthrough)
```

---

## Step 3 — Write Your Application

**`myapp/main.py`**:
```python
import sys
from fastapi import FastAPI
import uvicorn

app = FastAPI()

@app.get("/health")
def health():
    return {"status": "ok", "python": sys.version}

def main():
    uvicorn.run(app, host="0.0.0.0", port=8000)
```

**`pyproject.toml`**:
```toml
[project.scripts]
myapp = "myapp.main:main"
```

---

## Step 4 — Develop Locally

```bash
pyexec sync                    # sync local venv from lockfile
pyexec uv run python -m myapp.main   # run without activating venv
```

---

## Step 5 — Build the Binary

```bash
# Build for the current machine (recommended starting point)
pyexec build --profile standard --version 1.0.0 .

# Build for a different platform (cross-compile)
pyexec build --profile standard --version 1.0.0 --os linux  --arch amd64  .
pyexec build --profile standard --version 1.0.0 --os darwin --arch arm64  .
pyexec build --profile standard --version 1.0.0 --os windows --arch amd64 .
```

PyExec automatically checks `uv.lock` before building and re-locks if it is stale. The output is a single executable file.

Expected output:
```
Building myapp v1.0.0 (linux/amd64)...
  ✓ uv.lock up to date
  ✓ Captured Python 3.12.1
  ✓ Captured 14 system dependencies
  ✓ Captured 6 Python packages
Created: myapp-v1.0.0 (24MB)
```

**Choose your profile:**

| Profile | Binary | Downloads on install | Use when |
|---|---|---|---|
| `minimal` | ~5 MB | ~115 MB | Bandwidth unlimited, fast deploys |
| `standard` | ~25 MB | ~70 MB | General use (recommended) |
| `extended` | ~50 MB | ~10 MB | Edge / metered links |
| `full` | ~130 MB | 0 MB | Airgapped / offline |

---

## Step 6 — Smoke Test the Binary

```bash
./myapp-v1.0.0 install --verbose
./myapp-v1.0.0 run
./myapp-v1.0.0 verify
```

If it works here, it will work on any compatible machine.

---

## Step 7 — Ship to Production

### Linux / macOS target

```bash
scp myapp-v1.0.0 deploy@prod-server:/opt/myapp/
```

### Windows target

```powershell
Copy-Item myapp-v1.0.0.exe \\server\share\myapp\
```

### Multiple servers in parallel

```bash
for server in web-1 web-2 web-3; do
    scp myapp-v1.0.0 deploy@$server:/opt/myapp/ &
done
wait
```

### S3 / object storage

```bash
aws s3 cp myapp-v1.0.0 s3://releases/myapp/myapp-v1.0.0
```

---

## Step 8 — Install on the Target Machine

```bash
# Linux / macOS
/opt/myapp/myapp-v1.0.0 install --mode standalone --verbose

# Windows
.\myapp-v1.0.0.exe install --mode standalone --verbose
```

What happens:
1. Downloads [python-build-standalone](https://github.com/indygreg/python-build-standalone) for this OS/arch (self-contained, needs nothing from the system)
2. Extracts it to `<install-dir>/python/`
3. Creates a venv pointing at that private Python
4. Installs packages from the manifest
5. Saves the manifest for future integrity checks

Everything lands under the platform install base — nothing else is touched:
- **Linux**: `~/.local/share/myapp/1.0.0/`
- **macOS**: `~/Library/Application Support/myapp/1.0.0/`
- **Windows**: `%APPDATA%\myapp\1.0.0\`

For regulated environments:
```bash
./myapp-v1.0.0 install --mode exact --audit-log /var/log/myapp-install.log
```

---

## Step 9 — Run as a Service

### Linux — systemd

```ini
# /etc/systemd/system/myapp.service
[Unit]
Description=myapp service
After=network.target

[Service]
Type=simple
User=myapp
ExecStart=/opt/myapp/myapp-v1.0.0 run
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now myapp
journalctl -u myapp -f
```

### macOS — launchd

```xml
<!-- ~/Library/LaunchAgents/com.myapp.plist -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>       <string>com.myapp</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/myapp-v1.0.0</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key>   <true/>
  <key>KeepAlive</key>   <true/>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.myapp.plist
```

### Windows — NSSM or Task Scheduler

```powershell
# Using NSSM (Non-Sucking Service Manager)
nssm install myapp "C:\path\to\myapp-v1.0.0.exe" run
nssm start myapp
```

---

## Step 10 — Zero-Downtime Updates

```bash
# 1. Bump deps if needed
pyexec add httpx
pyexec remove requests

# 2. Build new version (uv.lock auto-updated above; build re-checks it)
pyexec build --profile standard --version 1.1.0 .

# 3. Transfer and install (non-destructive — old version stays intact)
scp myapp-v1.1.0 deploy@prod-server:/opt/myapp/
ssh deploy@prod-server "./myapp-v1.1.0 install --mode standalone"

# 4. Switch over (update service to point to new binary)
sudo sed -i 's/myapp-v1.0.0/myapp-v1.1.0/' /etc/systemd/system/myapp.service
sudo systemctl daemon-reload && sudo systemctl restart myapp
```

### Rollback — one command

```bash
sudo sed -i 's/myapp-v1.1.0/myapp-v1.0.0/' /etc/systemd/system/myapp.service
sudo systemctl daemon-reload && sudo systemctl restart myapp
```

The old environment is still fully intact. No reinstall.

---

## Step 11 — Health Checks

```bash
# Integrity check
pyexec verify ~/.local/share/myapp/1.1.0

# Full diagnostics — checks python3, uv, go, ldd/otool, namespaces
pyexec doctor

# List installed versions
pyexec versions myapp

# Generate SBOM
pyexec sbom ~/.local/share/myapp/1.1.0
```

---

## Airgapped / Offline Deployments

```bash
# Build with everything embedded
pyexec build --profile full --version 1.0.0 .
# → myapp-v1.0.0 (~130MB, completely self-contained)

# Transfer to airgapped machine via USB, internal share, etc.
cp myapp-v1.0.0 /media/usb/

# On the airgapped machine — no internet needed at any step
./myapp-v1.0.0 install --offline
./myapp-v1.0.0 run
```

---

## CI/CD Integration

### GitHub Actions

```yaml
name: Build and Release
on:
  push:
    tags: ['v*']

jobs:
  build:
    strategy:
      matrix:
        include:
          - os: ubuntu-latest,  target_os: linux,   arch: amd64
          - os: macos-latest,   target_os: darwin,  arch: arm64
          - os: windows-latest, target_os: windows, arch: amd64
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - name: Install uv
        run: curl -LsSf https://astral.sh/uv/install.sh | sh
      - name: Build PyExec
        run: go build -o pyexec ./cmd/pyexec
      - name: Build application
        run: |
          VERSION=${GITHUB_REF#refs/tags/v}
          ./pyexec build \
            --profile standard \
            --version $VERSION \
            --os ${{ matrix.target_os }} \
            --arch ${{ matrix.arch }} \
            .
      - uses: softprops/action-gh-release@v2
        with:
          files: myapp-v*
```

---

## uv Passthrough Reference

```bash
pyexec uv python list              # list available Python versions
pyexec uv python install 3.13      # install a Python version
pyexec uv python pin 3.12          # pin project to a version
pyexec uv pip show fastapi         # inspect a package
pyexec uv pip list                 # list installed packages
pyexec uv cache clean              # clear uv's download cache
pyexec uv run pytest               # run a command in the project venv
pyexec uv --help                   # full uv help
```

---

## Troubleshooting

**`uv.lock not found and uv is not installed`**
Install uv or commit a `uv.lock` to the repo.

**`python not found` during install**
Use `--mode standalone` — it downloads its own Python automatically.

**`Installation corrupted`**
```bash
rm -rf ~/.local/share/myapp/1.0.0
./myapp-v1.0.0 install --mode standalone
```

**`--isolated` has no effect (macOS/Windows)**
Namespace isolation is a Linux kernel feature. On macOS and Windows the flag is accepted but does nothing — isolation comes from the private install directory and standalone Python's self-containment.

**Download failures behind a proxy**
```bash
export HTTPS_PROXY=http://proxy.corp.com:8080
./myapp-v1.0.0 install --verbose
```

**Check everything:**
```bash
pyexec doctor
```

---

## Cross-Platform Build Guide

### The rule: capture must run on the target platform

PyExec captures the environment by actually inspecting it — running `ldd`/`otool`/PE-walk on the Python binary, resolving platform-specific wheels from the lockfile. That inspection must happen on the target OS. There is no way to accurately snapshot a Windows environment from a Linux machine.

PyExec enforces this:

```
$ pyexec build --os windows --arch amd64 .

error: cross-build detected (build: linux/amd64 → target: windows/amd64)

Cross-building requires capturing the target environment on the target machine.
...
```

### Option 1 — Two-step: capture + assemble (recommended for cross-platform)

Run `capture` on each target platform, then `assemble` from anywhere.

**On the Windows machine (or GitHub Actions `windows-latest`):**

```bash
pyexec capture \
  --os windows \
  --arch amd64 \
  --output windows-amd64.manifest.json \
  .

# Produces two files:
#   windows-amd64.manifest.json       ← environment snapshot
#   windows-amd64.manifest.json.snap  ← source file list
```

**Transfer back to your dev machine** (git commit, scp, artifact download, etc.):

```bash
# e.g. from GitHub Actions artifact or git:
# windows-amd64.manifest.json
# windows-amd64.manifest.json.snap
```

**On your Linux dev machine (or any machine with Go):**

```bash
pyexec assemble \
  --manifest windows-amd64.manifest.json \
  --name myapp \
  --version 1.0.0 \
  --profile standard \
  .

# Produces: myapp-v1.0.0.exe
```

The `assemble` step cross-compiles the Go launcher (`CGO_ENABLED=0`, no C toolchain needed) and packages everything. It can run on any platform.

### Option 2 — Native builds in CI (simplest, always correct)

Each platform builds its own binary. No capture/assemble split needed.

```yaml
# .github/workflows/release.yml
jobs:
  build:
    strategy:
      matrix:
        include:
          - runs-on: ubuntu-latest
          - runs-on: macos-latest
          - runs-on: windows-latest
    runs-on: ${{ matrix.runs-on }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - name: Install uv
        run: curl -LsSf https://astral.sh/uv/install.sh | sh
      - name: Build PyExec + app
        run: |
          go build -o pyexec ./cmd/pyexec
          VERSION=${GITHUB_REF#refs/tags/v}
          ./pyexec build --profile standard --version $VERSION .
      - uses: softprops/action-gh-release@v2
        with:
          files: myapp-v*
```

### Option 3 — Best-effort cross-build (testing only, not for production)

If you just want to test the binary format or the install/run flow on another platform without caring about accurate system dep snapshots:

```bash
pyexec build --os darwin --arch arm64 --best-effort .
```

`--best-effort` explicitly opts into the inaccuracy. PyExec prints a warning. **Do not use `--profile full` or `--profile extended` with `--best-effort`** — those profiles embed system deps that will be wrong.

### `pyexec capture` flag reference

```
pyexec capture [flags] [project-path]

Flags:
  -output  string   Output manifest path
                    (default: <os>-<arch>.manifest.json)
  -os      string   Declare target OS   (default: current OS)
  -arch    string   Declare target arch (default: current arch)
```

### `pyexec assemble` flag reference

```
pyexec assemble [flags] [project-path]

Flags:
  -manifest string   Path to manifest from `pyexec capture`  (required)
  -name     string   Application name                         (required)
  -version  string   Application version  (default: 0.1.0)
  -output   string   Output binary path
  -profile  string   minimal|standard|extended|full  (default: standard)
  -os       string   Target OS   (read from manifest if not given)
  -arch     string   Target arch (read from manifest if not given)
```

### Complete multi-platform example with capture+assemble

```yaml
# .github/workflows/release.yml
jobs:

  # Step 1: capture on each platform
  capture:
    strategy:
      matrix:
        include:
          - runs-on: ubuntu-latest,  os: linux,   arch: amd64
          - runs-on: macos-latest,   os: darwin,  arch: arm64
          - runs-on: windows-latest, os: windows, arch: amd64
    runs-on: ${{ matrix.runs-on }}
    steps:
      - uses: actions/checkout@v4
      - name: Install uv
        run: curl -LsSf https://astral.sh/uv/install.sh | sh
      - name: Build PyExec
        run: go build -o pyexec ./cmd/pyexec
      - name: Capture environment
        run: |
          ./pyexec capture \
            --os ${{ matrix.os }} \
            --arch ${{ matrix.arch }} \
            --output ${{ matrix.os }}-${{ matrix.arch }}.manifest.json \
            .
      - uses: actions/upload-artifact@v4
        with:
          name: manifest-${{ matrix.os }}-${{ matrix.arch }}
          path: |
            ${{ matrix.os }}-${{ matrix.arch }}.manifest.json
            ${{ matrix.os }}-${{ matrix.arch }}.manifest.json.snap

  # Step 2: assemble all binaries on one machine
  assemble:
    needs: capture
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - name: Build PyExec
        run: go build -o pyexec ./cmd/pyexec
      - uses: actions/download-artifact@v4
        with: { pattern: manifest-*, merge-multiple: true }
      - name: Assemble all binaries
        run: |
          VERSION=${GITHUB_REF#refs/tags/v}
          for manifest in *.manifest.json; do
            ./pyexec assemble \
              --manifest $manifest \
              --name myapp \
              --version $VERSION \
              --profile standard \
              .
          done
      - uses: softprops/action-gh-release@v2
        with:
          files: myapp-v*
```
