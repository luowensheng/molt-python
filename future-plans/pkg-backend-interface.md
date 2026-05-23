# Package Backend Interface

**Status:** Proposal — not yet implemented.
**Depends on:** `polyglot-packages.md` (the "what"; this doc covers the "how")

---

## The insight

The run-handler system already solved the "one command, many runtimes" problem
for executing files. `~/.molt/run-handlers.yaml` maps a file extension to a
shell command template with `{token}` substitutions — and any user can extend
or override it in one command.

Package management has the same shape:

```
molt run hello.rb     →  look up .rb handler   →  exec "ruby {file} {args}"
molt add requests     →  look up python backend →  exec "uv add {package}"
molt add serde        →  look up rust backend   →  exec "cargo add {package}"
molt add libpng       →  look up c backend      →  exec "zig fetch …"
```

**`molt add`, `molt remove`, `molt sync`, `molt list`, and `molt upgrade` are
not Python commands. They are a language-agnostic interface. The implementation
is a set of shell command templates stored in `~/.molt/pkg-backends.yaml` —
same mechanism as run-handlers, different vocabulary.**

The Python backend (uv) is the built-in reference implementation. Every other
package ecosystem — Cargo, go mod, npm, Zig's fetcher, vcpkg, Conan,
CocoaPods, Swift Package Manager — can be described as a backend with no molt
code change, exactly like adding a new run-handler.

---

## Backend structure

Mirrors `internal/runhandler/runhandler.go` exactly:

```go
// Backend is one package-manager recipe, keyed by language name.
type Backend struct {
    Lang    string `yaml:"lang"`              // e.g. "rust", "go", "c", "node"
    Add     string `yaml:"add"`               // command to add one package
    Remove  string `yaml:"remove"`            // command to remove one package
    Sync    string `yaml:"sync"`              // command to install all deps
    List    string `yaml:"list"`              // command to list installed packages
    Upgrade string `yaml:"upgrade"`           // command to upgrade one or all packages
    // Per-OS overrides follow the same unix/windows pattern as run-handlers.
    AddWindows     string `yaml:"add_windows,omitempty"`
    SyncWindows    string `yaml:"sync_windows,omitempty"`
    RemoveWindows  string `yaml:"remove_windows,omitempty"`
}
```

Stored at `~/.molt/pkg-backends.yaml`. Global file, user-editable, seeded
with built-in defaults on first use — identical lifecycle to
`~/.molt/run-handlers.yaml`.

---

## Token vocabulary

| Token | Expands to |
|---|---|
| `{package}` | Package name as typed by the user |
| `{version}` | Raw version string (e.g. `1.6.43`, `^1.6`, `v1.8.1`) |
| `{version_flag}` | Formatted for the backend: `==1.6` (pip/uv), `@v1.8.1` (go), `@1.6` (npm), empty if unspecified |
| `{flags}` | Extra flags passed through from the CLI (`molt add serde --features derive` → `--features derive`) |
| `{manifest}` | Absolute path to the project's manifest file (`Cargo.toml`, `go.mod`, `package.json`, …) |
| `{lockfile}` | Absolute path to the project's lock file (`Cargo.lock`, `go.sum`, `package-lock.json`, …) |
| `{project}` | Absolute path to the project root |
| `{zig}` | Auto-installed Zig binary (shared with run-handler system) |

---

## Built-in backends (seeded into `~/.molt/pkg-backends.yaml`)

```yaml
# Python — uv as the resolver, same as today
- lang: python
  add:     "uv add {package}{version_flag}"
  remove:  "uv remove {package}"
  sync:    "uv sync"
  list:    "uv pip list"
  upgrade: "uv add --upgrade {package}"

# Rust — delegate entirely to Cargo
- lang: rust
  add:     "cargo add {package} {version_flag} {flags}"
  remove:  "cargo remove {package}"
  sync:    "cargo fetch"
  list:    "cargo tree --depth 1"
  upgrade: "cargo update {package}"

# Go — delegate to go modules
- lang: go
  add:     "go get {package}@{version}"
  remove:  "go mod edit -droprequire {package} && go mod tidy"
  sync:    "go mod download"
  list:    "go list -m all"
  upgrade: "go get -u {package}"

# Node.js — npm by default; swap to bun/pnpm via project override
- lang: node
  add:     "npm install {package}@{version}"
  remove:  "npm uninstall {package}"
  sync:    "npm install"
  list:    "npm list --depth 0"
  upgrade: "npm update {package}"
  add_windows:  "npm.cmd install {package}@{version}"
  sync_windows: "npm.cmd install"

# Zig — wraps zig fetch; URL-based so {package} is a URL or registry alias
- lang: zig
  add:     "{zig} fetch --save {package}"
  remove:  "molt-zig-remove {package}"
  sync:    "{zig} build --fetch"
  list:    "cat build.zig.zon"
  upgrade: "{zig} fetch --save {package}"

# C/C++ via pkg-config (system libraries)
- lang: c
  add:     "molt-pkg-resolve {package} {version}"
  remove:  "molt-pkg-remove {package}"
  sync:    "molt-pkg-sync"
  list:    "pkg-config --list-all"
  upgrade: "molt-pkg-resolve {package} latest"

# Swift Package Manager
- lang: swift
  add:     "swift package add {package} --exact {version}"
  remove:  "swift package remove {package}"
  sync:    "swift package resolve"
  list:    "swift package show-dependencies"
  upgrade: "swift package update {package}"
```

---

## How the active backend is selected

A project declares its language in `moltproject.toml` (or `pyproject.toml`
for Python):

```toml
[project]
name = "my-engine"
lang = "rust"       # selects the "rust" backend
```

Resolution order (highest priority first):

1. `[tool.molt.backend]` in the project's config file — full inline override
2. `[tool.molt.backend.add]` etc. in the project's config — partial override
3. Entry matching `lang` in `~/.molt/pkg-backends.yaml` (user-modified)
4. Built-in defaults compiled into molt (same fallback as run-handlers)

For Python projects, `lang` defaults to `"python"` even when omitted (backward
compatibility — `pyproject.toml` without `lang` is always Python).

---

## Per-project backend override

Projects can partially or fully override the backend in their config file —
no global change needed:

```toml
# moltproject.toml — Node project preferring Bun over npm
[project]
lang = "node"

[tool.molt.backend]
add     = "bun add {package}@{version}"
remove  = "bun remove {package}"
sync    = "bun install"
list    = "bun pm ls"
upgrade = "bun update {package}"
```

```toml
# moltproject.toml — C project using vcpkg instead of pkg-config
[project]
lang = "c"

[tool.molt.backend]
add    = "vcpkg install {package}:{triplet}"
remove = "vcpkg remove {package}"
sync   = "vcpkg install --triplet {triplet}"
list   = "vcpkg list"
```

```toml
# moltproject.toml — exotic language (Odin, Nim, Crystal, …)
[project]
lang = "nim"

[tool.molt.backend]
add    = "nimble install {package}@{version}"
remove = "nimble uninstall {package}"
sync   = "nimble install"
list   = "nimble list --installed"
```

This is exactly how a user today adds a custom run-handler:

```sh
molt run-handler add odin "odin run {file} {args}"
```

Except for packages:

```sh
molt pkg-backend add nim \
  --add    "nimble install {package}@{version}" \
  --remove "nimble uninstall {package}" \
  --sync   "nimble install" \
  --list   "nimble list --installed"
```

---

## `molt pkg-backend` management CLI

Mirrors `molt run-handler` exactly:

```sh
# Inspect
molt pkg-backend list              # show all backends (built-in + user)
molt pkg-backend show rust         # Lang: rust  Add: cargo add {package} …
molt pkg-backend show python       # Lang: python  Add: uv add {package}{version_flag}

# Add / override globally
molt pkg-backend add node \
  --add "bun add {package}@{version}" \
  --remove "bun remove {package}" \
  --sync "bun install"

# Remove a user override (restores built-in default)
molt pkg-backend remove node

# Reset all user overrides to built-in defaults
molt pkg-backend reset
```

---

## Unified command surface

Once backends are configurable, the five package commands become genuinely
language-agnostic. The user types the same thing regardless of language:

```sh
# Python project (lang = python → uv)
molt add requests
molt add "numpy>=2.0"
molt remove requests
molt sync
molt list
molt upgrade numpy

# Rust project (lang = rust → cargo)
molt add serde --features derive
molt add tokio
molt remove serde
molt sync
molt list
molt upgrade tokio

# Go project (lang = go → go get)
molt add github.com/spf13/cobra@v1.8.1
molt remove github.com/spf13/cobra
molt sync
molt list
molt upgrade github.com/spf13/cobra

# Nim project (lang = nim → nimble, user-defined backend)
molt add httpbeast
molt sync
```

The only thing that changes is `moltproject.toml`'s `lang` field — and that
changes once per project, not per command.

---

## Mixed-language projects

When a project has both Python and a native language, the backend is selected
per-section rather than per-project:

```toml
[project]
name = "ml-pipeline"
lang = "mixed"

[python]
# Handled by the python backend (uv)
dependencies = ["numpy>=2.0", "scipy"]

[c]
# Handled by the c backend (pkg-config / zig-build)
dependencies = ["hdf5>=1.14", "fftw3>=3.3"]
```

```sh
molt add numpy                        # → uv add numpy  (python section)
molt add libhdf5 --lang c             # → pkg-resolve libhdf5  (c section)
molt sync                             # → uv sync + pkg-sync, in order
```

For the common case (Python project that happens to have one C dependency),
the `--lang` flag is the escape hatch. For projects where native is primary,
`lang = "c"` with an explicit `[python]` section for scripting deps.

---

## Implementation — what changes in Go

### New file: `internal/pkgbackend/pkgbackend.go`

Exactly mirrors `internal/runhandler/runhandler.go`:

```go
package pkgbackend

type Backend struct {
    Lang           string `yaml:"lang"`
    Add            string `yaml:"add"`
    Remove         string `yaml:"remove"`
    Sync           string `yaml:"sync"`
    List           string `yaml:"list"`
    Upgrade        string `yaml:"upgrade"`
    AddWindows     string `yaml:"add_windows,omitempty"`
    RemoveWindows  string `yaml:"remove_windows,omitempty"`
    SyncWindows    string `yaml:"sync_windows,omitempty"`
}

// Resolve returns the platform-appropriate command for the given operation.
func (b Backend) Resolve(op, goos string) string { … }

// Expand substitutes tokens in a command string.
// tokens: {"package": "requests", "version": "2.31.0", "version_flag": "==2.31.0", …}
func Expand(command string, tokens map[string]string) string { … }

var defaultBackends = []Backend{
    {Lang: "python", Add: "uv add {package}{version_flag}", …},
    {Lang: "rust",   Add: "cargo add {package} {version_flag} {flags}", …},
    {Lang: "go",     Add: "go get {package}@{version}", …},
    {Lang: "node",   Add: "npm install {package}@{version}", …},
    {Lang: "zig",    Add: "{zig} fetch --save {package}", …},
    // …
}

func GlobalPath() (string, error) { … }  // ~/.molt/pkg-backends.yaml
func Load() ([]Backend, error) { … }
func Save(backends []Backend) error { … }
func Find(lang string) (Backend, bool, error) { … }
func Add(b Backend) error { … }
func Remove(lang string) error { … }
func Reset() error { … }
```

### `main.go` — re-route existing commands through the backend

`cmdAdd`, `cmdRemove`, `cmdSync` currently hard-code uv calls. After this
change they:

1. Read `lang` from the project config (default `"python"` for `pyproject.toml`)
2. Look up the backend via `pkgbackend.Find(lang)`
3. Expand tokens and exec the backend command

```go
func cmdAdd(args []string) error {
    lang := projectLang()          // reads moltproject.toml / pyproject.toml
    backend, ok, err := pkgbackend.Find(lang)
    if !ok {
        return fmt.Errorf("no package backend for lang %q — add one with "+
            "'molt pkg-backend add %s --add \"...\"'", lang, lang)
    }
    pkg, version := parsePackageArg(args)
    cmd := backend.Resolve("add", runtime.GOOS)
    cmd = pkgbackend.Expand(cmd, map[string]string{
        "package":      pkg,
        "version":      version,
        "version_flag": formatVersionFlag(lang, version),
        "flags":        strings.Join(extraFlags(args), " "),
        "manifest":     projectManifest(),
        "project":      projectRoot(),
        "zig":          zigBin(),
    })
    return execShell(cmd)
}
```

For Python projects with no `lang` field, `projectLang()` returns `"python"`
and the behavior is identical to today — **zero breaking change**.

### New subcommand: `molt pkg-backend`

```go
case "pkg-backend":
    err = cmdPkgBackend(os.Args[2:])
```

Implementation is a direct copy of `cmdRunHandler` with `pkgbackend.` instead
of `runhandler.`.

---

## Relationship to `polyglot-packages.md`

`polyglot-packages.md` describes *what* polyglot package management looks like
from the user's perspective (store layout, lock file format, registry,
per-language resolution strategies).

This document describes *how the CLI dispatches* to the right tool — the
configurable backend layer that sits between `molt add` and whatever package
manager actually runs.

They compose:

```
molt add libpng
    │
    ▼
pkgbackend.Find("c")
    │
    ▼
Backend.Add = "molt-pkg-resolve {package} {version}"
    │
    ▼
molt-pkg-resolve libpng ""   ← this is the C resolver from polyglot-packages.md
    │                           (zig-build, pkg-config fallback, registry lookup)
    ▼
~/.molt/pkg-native/c/libpng/1.6.43/aarch64-macos/
```

The backend interface handles dispatch. The resolver (from
`polyglot-packages.md`) handles the actual download, build, and store
management. Users who just want to wrap an existing package manager (Cargo,
npm, vcpkg) only need the backend interface — they never touch the resolver.
Users who want truly hermetic C library management need both.

---

## Design parallel: run-handlers vs pkg-backends

| Dimension | run-handler | pkg-backend |
|---|---|---|
| Keyed by | file extension (`.rb`, `.go`) | language name (`rust`, `node`) |
| Command set | one command | five commands (add/remove/sync/list/upgrade) |
| Token vocabulary | `{file}`, `{args}`, `{tmp}`, `{zig}` | `{package}`, `{version}`, `{flags}`, `{manifest}` |
| Global file | `~/.molt/run-handlers.yaml` | `~/.molt/pkg-backends.yaml` |
| Per-project override | N/A (handlers are global) | `[tool.molt.backend]` in project config |
| CLI management | `molt run-handler add/remove/list/reset` | `molt pkg-backend add/remove/list/reset` |
| Built-in count | 28 | 6 (python, rust, go, node, zig, c) |
| User extensible | yes — any language | yes — any package manager |

Both follow the same invariant: **molt ships useful defaults; users can
override or extend without touching molt source code.**
