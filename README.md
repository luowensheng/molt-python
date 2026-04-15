# PyExec

**Hermetic Python application distribution — Docker-level reproducibility as a single binary, on Linux, macOS, and Windows.**

PyExec is a full project lifecycle tool: it manages dependencies via `uv`, locks your environment, and packages your Python application as a self-contained executable that reconstructs an isolated environment on any target machine — no Docker, no root, no "works on my machine".

```bash
# Start a new project
pyexec init myapp && cd myapp
pyexec add fastapi uvicorn

# Build for any platform
pyexec build --profile standard .
pyexec build --profile standard --os darwin --arch arm64 .   # cross-compile
pyexec build --profile standard --os windows --arch amd64 .  # cross-compile

# Ship and run anywhere — nothing pre-installed on target
./myapp-v0.1.0 install
./myapp-v0.1.0 run
```

---

## How It Works

### Bring everything, touch nothing

PyExec never relies on what is already on the user's machine beyond the OS kernel. Every component — Python, system libraries, packages — is either embedded in the binary or downloaded to a **private directory that PyExec fully controls**. Nothing is written outside that directory. No PATH changes. No admin rights needed. Deletable by removing one folder.

### Three stages

**1. Develop** — `pyexec init/add/remove/sync/lock` wrap `uv` so you never call it directly. `pyexec build` auto-locks the environment before building if `uv.lock` is missing or stale.

**2. Build** — captures Python version, system library dependencies (via `ldd` / `otool` / PE walk), and packages from `uv.lock`. Cross-compiles a Go launcher binary for the target OS/arch and concatenates it with a gzipped payload archive. The result is a single executable.

**3. Run** — on the target machine, the binary downloads and installs [python-build-standalone](https://github.com/indygreg/python-build-standalone) (a self-contained Python that needs nothing from the system), creates a venv, installs packages, and runs your code in a private environment.

### Per-platform strategy

| Layer | Linux | macOS | Windows |
|---|---|---|---|
| Python runtime | python-build-standalone (glibc ≥ 2.17) | python-build-standalone (macOS 10.15+) | python-build-standalone |
| System libs | Private `lib/` + `LD_LIBRARY_PATH` | Bundled in standalone; `@rpath` patching for edge cases | Bundled in standalone; wheel-local DLLs |
| Native packages | Wheels + `LD_LIBRARY_PATH` | Wheels (self-contained) | Wheels (self-contained) |
| Process isolation | Linux namespaces (no root) | None (stable ABI makes it unnecessary) | None |
| System files touched | **zero** | **zero** | **zero** |
| Admin rights | **no** | **no** | **no** |
| Reproducibility | 95% | 90% | 85% |

---

## Project Structure

```
pyexec/
├── cmd/pyexec/          # CLI (stdlib only, no external deps)
├── internal/
│   ├── platform/        # OS-specific behaviour behind one interface
│   │   ├── platform.go          # shared: paths, URLs, env vars
│   │   ├── platform_linux.go    # ldd + CLONE_* namespaces
│   │   ├── platform_darwin.go   # otool -L, nil namespace attr
│   │   ├── platform_windows.go  # PE dep walk, nil namespace attr
│   │   └── platform_other.go    # stub for other OS
│   ├── launcher/        # target-side runner (compiled into each binary)
│   │   ├── main.go              # install/run/verify/uninstall subcommands
│   │   ├── isolation_linux.go   # CLONE_NEWNS|CLONE_NEWPID|CLONE_NEWUTS
│   │   └── isolation_other.go   # no-op on non-Linux
│   ├── uv/              # uv wrapper: Init, Add, Remove, Lock, Sync, Raw…
│   ├── builder/         # capture → manifest → cross-compile launcher → assemble
│   ├── capturer/        # Python version, platform.TraceDeps, uv.lock parse
│   ├── installer/       # standalone Python download, venv, pip, @rpath patch
│   ├── executor/        # launch Python via platform.EnvVars + NamespaceSysProcAttr
│   ├── manifest/        # JSON manifest encode/decode/filter
│   ├── downloader/      # parallel HTTP + content-addressable cache
│   ├── verifier/        # SHA-256 integrity + SBOM
│   ├── cache/           # content-addressable local cache
│   └── testutil/        # minimal stdlib-only test helpers
├── pkg/types/           # Manifest, BuildConfig, InstallConfig, …
├── testdata/
│   └── sample-project/
├── integration_test.go
└── go.mod
```

---

## CLI Reference

### Project lifecycle

| Command | What it does |
|---|---|
| `pyexec init [name]` | Scaffold project + run `uv lock` |
| `pyexec add <pkg...>` | Add dependency, update `pyproject.toml` + `uv.lock` |
| `pyexec remove <pkg...>` | Remove dependency |
| `pyexec sync` | Sync venv with current lockfile |
| `pyexec lock` | Regenerate `uv.lock` from `pyproject.toml` |
| `pyexec tree` | Show dependency tree |
| `pyexec uv <args...>` | Raw passthrough to uv |

### Distribution

| Command | What it does |
|---|---|
| `pyexec build [path]` | Build hermetic binary (auto-locks if stale) |
| `pyexec install [manifest]` | Install on target machine |
| `pyexec run` | Run the installed application |
| `pyexec versions <app>` | List installed versions |
| `pyexec verify [dir]` | Verify installation integrity |
| `pyexec doctor` | System diagnostics |
| `pyexec sbom [dir]` | Software Bill of Materials (JSON) |

---

## Build Profiles

| Profile | Binary | Download on install | Reproducibility | Best for |
|---|---|---|---|---|
| `minimal` | 5–10 MB | ~115 MB | 85% | CI, frequent deploys |
| `standard` | 20–30 MB | ~70 MB | 90% | General use (recommended) |
| `extended` | 40–60 MB | ~10 MB | 93% | Edge / metered connections |
| `full` | 100–150 MB | 0 MB | 95% | Airgapped / offline |

---

## Cross-Compilation

Build for any platform from any platform:

```bash
# From Linux, build for macOS Apple Silicon
pyexec build --os darwin --arch arm64 --profile standard .

# From macOS, build for Linux amd64
pyexec build --os linux --arch amd64 --profile standard .

# From any platform, build for Windows
pyexec build --os windows --arch amd64 --profile standard .
```

`go` must be on PATH for cross-compilation. The launcher binary is cross-compiled with `CGO_ENABLED=0` so no C toolchain is needed.

---

## Install Paths

| OS | Install base | Cache |
|---|---|---|
| Linux | `~/.local/share/<app>/<ver>` | `~/.cache/pyexec` |
| macOS | `~/Library/Application Support/<app>/<ver>` | `~/Library/Caches/pyexec` |
| Windows | `%APPDATA%\<app>\<ver>` | `%LOCALAPPDATA%\pyexec\cache` |

---

## Install Modes

| Mode | Isolation | Reproducibility | Use case |
|---|---|---|---|
| `minimal` | venv only | 85% | Dev / quick testing |
| `standalone` | Private Python + libs + namespace (Linux) | 93% | Production (recommended) |
| `exact` | Everything + checksum audit log | 95% | Regulated / critical |

---

## Detailed Flag Reference

### `pyexec build`
```
-profile  string   minimal|standard|extended|full  (default: standard)
-name     string   Application name (default: dir name)
-version  string   Application version (default: 0.1.0)
-output   string   Output binary path
-os       string   Target OS: linux|darwin|windows  (default: current)
-arch     string   Target arch: amd64|arm64         (default: current)
-offline           Build for offline deployment
-sign-key string   Path to signing private key
```

### `pyexec install`
```
-mode      string  minimal|standalone|exact  (default: standalone)
-prefix    string  Custom installation directory
-cache-dir string  Cache directory
-offline           No network downloads
-parallel  int     Parallel download workers  (default: 4)
-verbose           Verbose output
-dry-run           Show what would happen without doing it
-audit-log string  Write installation audit log to file
```

### `pyexec run`
```
-app      string  Application name
-version  string  Application version
-isolated         Linux namespace isolation (no-op on macOS/Windows)
-debug            Print exec command and environment
```

---

## Getting Started

```bash
# Install Go 1.22+ and uv, then:
git clone https://pyexec
cd pyexec
go build -o /usr/local/bin/pyexec ./cmd/pyexec

# Create and build a project
pyexec init myapp && cd myapp
pyexec add requests
pyexec build --profile standard .
./myapp-v0.1.0 install
./myapp-v0.1.0 run
```

---

## License

MIT

---

## Cross-Platform Builds

### Native build (always correct)

```bash
pyexec build --profile standard .
```

Builds for the machine you're running on. Always produces accurate system dep snapshots and wheel hashes. **Use this in CI on each platform.**

### Two-step cross-build (correct, any machine)

**Step 1 — run on the target machine** (or a CI runner of that OS):

```bash
# On the Windows machine / GitHub Actions windows-latest runner:
pyexec capture --os windows --arch amd64 --output windows-amd64.manifest.json .
# Produces: windows-amd64.manifest.json + windows-amd64.manifest.json.snap
```

**Step 2 — run on any machine** (your Linux dev box, CI, anywhere):

```bash
# Back on your Linux machine:
pyexec assemble \
  --manifest windows-amd64.manifest.json \
  --name myapp \
  --version 1.0.0 \
  --profile standard \
  .
# Produces: myapp-v1.0.0.exe
```

The `capture` step is fast (~seconds) — it just inspects the environment and writes JSON. The heavy work (cross-compiling the launcher, packaging) happens in `assemble` and can run anywhere.

### Best-effort cross-build (testing only)

```bash
pyexec build --os windows --arch amd64 --best-effort .
```

Explicitly opt-in with `--best-effort`. **Produces inaccurate system dep snapshots.** Fine for checking that the binary format works; not for production builds.

Without `--best-effort`, cross-builds error with a clear message explaining the options.

### Recommended CI pattern

```yaml
jobs:
  build:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - run: pyexec build --profile standard --version $VERSION .
      # Each platform builds its own binary natively — always correct.
```

---

## `pyexec doctor` Output

```
PyExec dev
Platform:  linux/amd64
─────────────────────────────────────
  Install base: /home/user/.local/share
  Cache dir:    /home/user/.cache/pyexec

  ✓ python3              /usr/bin/python3
  ✓ uv                   uv 0.4.0
  ✓ go                   /usr/local/go/bin/go
  ✓ ldd                  /usr/bin/ldd
  ✓ namespaces           kernel feature

Cross-build capability:
  ✓ linux/amd64          (native)
  ~ linux/arm64          (launcher only — use capture+assemble for accurate deps)
  ~ darwin/amd64         (launcher only — use capture+assemble for accurate deps)
  ~ darwin/arm64         (launcher only — use capture+assemble for accurate deps)
  ~ windows/amd64        (launcher only — use capture+assemble for accurate deps)

Core checks passed ✓

For cross-platform builds, see: pyexec capture --help
```
