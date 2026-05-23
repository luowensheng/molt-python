# Polyglot Package Management

**Status:** Proposal — not yet implemented.

Extends `moltproject.toml` with first-class dependency management for C, C++,
Zig, Rust, and Go projects. The goal is `molt add`, `molt sync`, and
`molt remove` that work identically regardless of which language a project
uses — the same muscle memory, the same global content-addressed store, the
same lock file discipline.

---

## The problem

`moltproject.toml` already gives non-Python projects a consistent task runner.
What it lacks is *dependency management*. Today, a C project that needs `zlib`
has to:

- Install it via Homebrew / apt / vcpkg / Conan — different command per OS
- Hard-code `-lz` in the build task
- Hope the header path is discoverable by the compiler
- Repeat this for every collaborator and every CI machine

A Zig project is better off (Zig has a built-in package manager via
`build.zig.zon`), but the CLI, lock file location, and store layout are
entirely separate from what molt knows about.

---

## Design principles

1. **`molt add libcurl` works the same as `molt add requests`.** Language is
   an implementation detail. The CLI surface does not change.

2. **Global content-addressed store.** Packages are downloaded and built once
   into `~/.molt/pkg-native/{name}/{version}/{platform-triple}/` and symlinked
   into each project's build environment. Two C projects that both need `zlib`
   share the same compiled artifacts.

3. **Lock files are committed.** Every `molt add` / `molt remove` produces a
   deterministic `molt.lock` (TOML, human-readable). `molt sync --frozen` fails
   if the lock file is stale — safe for CI.

4. **No build system required.** The compiler flags needed to use a dependency
   (`-I`, `-L`, `-l`, `pkg-config` output) are injected automatically into
   every `{…}` token expansion at task run time. Writing a build task in
   `moltproject.toml` does not require knowing where headers live.

5. **Delegate to native tooling when it already exists.** Zig has its own
   package manager; Cargo and Go modules are industry standards. molt wraps
   them — it does not replace them. The value is a single unified CLI and
   cross-language dependency graph, not reinventing package resolution.

---

## `moltproject.toml` additions

```toml
[project]
name    = "image-encoder"
version = "0.2.0"
lang    = "c"                  # primary language: c | cpp | zig | rust | go

[dependencies]
# Canonical name → version constraint (semver-style)
libpng  = "^1.6"
zlib    = "^1.3"
libjpeg = "^9e"

[build-dependencies]
# Only needed during compilation, not at runtime
cmake   = "^3.28"              # resolved to PATH, not compiled

[dev-dependencies]
cmocka  = "^1.1"               # test framework, excluded from release builds

[tool.molt.tasks]
build = "{cc} -O2 {cflags} -o bin/encoder src/encoder.c {ldflags}"
test  = "{cc} -O2 {cflags} -o bin/test_encoder test/test_encoder.c {ldflags} && bin/test_encoder"
```

### New token expansions (injected by `molt run`)

| Token | Expands to |
|---|---|
| `{cc}` | C compiler (zig cc, clang, gcc — user-configurable) |
| `{cxx}` | C++ compiler (zig c++, clang++, g++) |
| `{cflags}` | `-I/path/to/include` flags for all resolved dependencies |
| `{ldflags}` | `-L/path -l<lib>` flags for all resolved dependencies |
| `{pkgconfig}` | Raw `pkg-config --cflags --libs <pkg>` output, merged |
| `{zig}` | Auto-installed Zig binary (existing) |
| `{zigflags}` | `--pkg-begin name path --pkg-end` flags for Zig packages |

---

## Per-language resolution strategy

### C and C++ — `lang = "c"` / `lang = "cpp"`

**Primary resolver: Zig package manager as a C library fetcher.**

Zig can download, build, and expose C libraries as Zig packages — which are
also usable from C/C++ as plain compiled archives. molt uses this as its
universal C/C++ package resolver:

```
molt add libpng@1.6.43
```

1. molt looks up `libpng` in the molt package registry
   (`~/.molt/pkg-native/registry.json`, fetched from a CDN).
2. The registry entry points to a `build.zig` + source archive for that
   version.
3. molt runs `zig build` in `~/.molt/pkg-native/libpng/1.6.43/<triple>/` to
   produce `libpng.a` and the public headers.
4. The resulting `-I` / `-L` / `-l` flags are recorded in `molt.lock`.
5. On subsequent `molt sync`, the store entry is reused — no re-download.

**Fallback: system pkg-config.**

If a package cannot be found in the molt registry, molt falls back to
`pkg-config --exists <name>`. This means system-installed libraries (OpenGL,
X11, CoreFoundation) work without any registry entry. The lock file records
the resolved pkg-config output and its version, so a `--frozen` sync fails
if the system library changes.

**Override: explicit source.**

Power users can point at an arbitrary archive or git ref:

```toml
[dependencies]
mylib = { git = "https://github.com/org/mylib", rev = "abc1234", build = "zig" }
mylib = { url = "https://example.com/mylib-1.0.tar.gz", hash = "sha256:abcdef…" }
```

---

### Zig — `lang = "zig"`

**Primary resolver: native Zig package manager (`build.zig.zon`).**

molt generates and manages `build.zig.zon` from `moltproject.toml`. Running
`molt add <pkg>` fetches the package URL and hash via `zig fetch`, writes the
entry into `moltproject.toml`, and regenerates `build.zig.zon`.

```toml
[dependencies]
zig-clap  = "^0.9"
known-folders = "^0.6"
```

Generated `build.zig.zon` (managed by molt, do not edit manually):

```zig
.{
    .name = "image-encoder",
    .version = "0.2.0",
    .dependencies = .{
        .@"zig-clap" = .{
            .url  = "https://github.com/Hejsil/zig-clap/archive/0.9.1.tar.gz",
            .hash = "12207e42c4efb41e6e4b6edfe76c99697de62b9de48b19ef00f7e9de7af9caee",
        },
    },
}
```

The `build.zig` is also auto-generated by molt from the task graph unless the
user opts out:

```toml
[tool.molt.zig]
managed_build = true   # default: molt writes build.zig; false: user owns it
```

**Store location:** `~/.molt/pkg-native/zig/<name>/<version>/<host-triple>/`

---

### Rust — `lang = "rust"`

**Delegate entirely to Cargo.** molt does not replace Cargo — it wraps it.

```toml
[dependencies]
serde      = { version = "^1", features = ["derive"] }
tokio      = { version = "^1", features = ["full"] }
reqwest    = "^0.12"
```

`molt sync` runs `cargo fetch` (offline-safe after first run). `molt add
serde` runs `cargo add serde` and reflects the change back to
`moltproject.toml`. The lock file is `Cargo.lock` — molt commits it
alongside `molt.lock` (which records only the resolved tool versions and
registry snapshot timestamp).

**Why the wrapper?** Consistency in CI (`molt sync --frozen`) and a unified
`molt run build` / `molt run test` that works across all project types,
without needing to remember `cargo build` vs `go build` vs `zig build`.

---

### Go — `lang = "go"`

**Delegate to Go modules.** `go.mod` is the source of truth; molt generates
and wraps it:

```toml
[dependencies]
"github.com/spf13/cobra"  = "v1.8.1"
"golang.org/x/net"        = "v0.26.0"
```

`molt add github.com/spf13/cobra@v1.8.1` runs `go get
github.com/spf13/cobra@v1.8.1` and records the canonical import path in
`moltproject.toml`. `molt sync` runs `go mod download`. The lock file is
`go.sum` — molt ensures it is committed.

---

## Global native package store

```
~/.molt/pkg-native/
├── registry.json                  # CDN-fetched index: name → {versions, resolver}
├── registry.json.sig              # Ed25519 signature (future)
├── c/
│   ├── libpng/
│   │   └── 1.6.43/
│   │       ├── aarch64-macos/
│   │       │   ├── lib/libpng.a
│   │       │   └── include/png.h
│   │       └── x86_64-linux/
│   │           ├── lib/libpng.a
│   │           └── include/png.h
│   └── zlib/…
└── zig/
    ├── zig-clap/
    │   └── 0.9.1/<hash>/          # keyed by content hash, not platform
    └── …
```

**Key properties:**

- **Content-addressed for Zig packages** (pure-zig, no native compilation): keyed by hash only — the same package works on any platform.
- **Platform-addressed for compiled C/C++ packages**: keyed by target triple (`aarch64-macos`, `x86_64-linux-musl`, etc.) — a cross-compile of `libpng` for Linux from macOS is stored alongside the native macOS build.
- **Garbage collected** by `molt gc --native` — same ref-counting as the Python store.
- **Shared across all projects** — `molt add libpng` in project A costs nothing in project B.

---

## Lock file format — `molt.lock`

```toml
# Auto-generated by molt. Do not edit manually. Commit this file.
# molt version: 0.3.0
# generated:    2026-05-23T17:00:00Z

[[package]]
name     = "libpng"
version  = "1.6.43"
resolver = "zig-build"
url      = "https://sourceforge.net/projects/libpng/files/libpng16/1.6.43/libpng-1.6.43.tar.gz"
hash     = "sha256:fecc95b46cf05e8e3fc8a414750e0ba5aad00d89e9fdf175e94ff041caf1a03a"
triples  = ["aarch64-macos", "x86_64-linux"]

  [package.flags.aarch64-macos]
  cflags  = "-I/Users/you/.molt/pkg-native/c/libpng/1.6.43/aarch64-macos/include"
  ldflags = "-L/Users/you/.molt/pkg-native/c/libpng/1.6.43/aarch64-macos/lib -lpng"

  [package.flags.x86_64-linux]
  cflags  = "-I/home/you/.molt/pkg-native/c/libpng/1.6.43/x86_64-linux/include"
  ldflags = "-L/home/you/.molt/pkg-native/c/libpng/1.6.43/x86_64-linux/lib -lpng"

[[package]]
name     = "zlib"
version  = "1.3.1"
resolver = "pkgconfig"
pkgname  = "zlib"
pc_ver   = "1.3.1"
# flags resolved at sync time from pkg-config on each machine

[[package]]
name     = "zig-clap"
version  = "0.9.1"
resolver = "zig-fetch"
url      = "https://github.com/Hejsil/zig-clap/archive/0.9.1.tar.gz"
hash     = "12207e42c4efb41e6e4b6edfe76c99697de62b9de48b19ef00f7e9de7af9caee"
```

The lock file is **platform-inclusive**: it records flags for every target
triple that has been synced. CI can cross-compile for Linux from macOS with
`molt sync --target x86_64-linux` without changing the lock file format —
just adding a new `[package.flags.x86_64-linux]` block.

---

## CLI surface

All existing commands gain language awareness — no new commands for the common
path:

```sh
# Adding dependencies
molt add libpng              # C/C++: resolves via registry; zig: zig fetch
molt add libpng@1.6.43      # pin exact version
molt add serde --features derive  # Rust: passes --features to cargo add
molt add github.com/spf13/cobra@v1.8.1  # Go: full module path

# Removing
molt remove libpng

# Install / materialise
molt sync                   # install all deps, write flags to build env
molt sync --frozen          # fail if lock file is stale (CI)
molt sync --target x86_64-linux  # cross-compile deps for target triple

# Inspect
molt info                   # shows [dependencies] section + resolved flags
molt list                   # tabular view: name, version, resolver, triple

# Upgrade
molt upgrade libpng         # bump to newest compatible version
molt upgrade                # upgrade all (respects version constraints)
```

---

## Token injection at task run time

When `molt run build` is invoked, molt resolves `{cflags}` and `{ldflags}`
from the lock file for the current platform and substitutes them into the
task command before exec:

```toml
[tool.molt.tasks]
build = "{cc} -O2 {cflags} -o bin/encoder src/encoder.c {ldflags}"
```

Becomes (on macOS arm64, after syncing `libpng` and `zlib`):

```sh
zig cc -O2 \
  -I/Users/you/.molt/pkg-native/c/libpng/1.6.43/aarch64-macos/include \
  -I/Users/you/.molt/pkg-native/c/zlib/1.3.1/aarch64-macos/include \
  -o bin/encoder src/encoder.c \
  -L/Users/you/.molt/pkg-native/c/libpng/1.6.43/aarch64-macos/lib -lpng \
  -L/Users/you/.molt/pkg-native/c/zlib/1.3.1/aarch64-macos/lib -lz
```

The user never writes a path. The task stays readable and platform-agnostic.

---

## Cross-language projects

A common pattern: Python project with a C library dependency (not a Python
extension — the raw C binary, used via `subprocess` or `ctypes`).

```toml
[project]
name = "ml-pipeline"
lang = "mixed"

[python.dependencies]
numpy  = "^2.0"
scipy  = "^1.13"

[c.dependencies]
hdf5   = "^1.14"
fftw3  = "^3.3"

[tool.molt.tasks]
build-native = "{cc} -O2 {cflags} -shared -o lib/libfft_fast.so src/fft_fast.c {ldflags}"
test         = "pytest tests/"
```

`molt sync` resolves Python deps (via uv) and C deps (via zig-build /
pkg-config) in a single pass. `{cflags}` / `{ldflags}` are injected only
into tasks that use them; Python tasks are unaffected.

This mirrors how `molt sync` today handles both Python packages and C
extension compilation in one step — the polyglot case just extends that
to non-extension C libraries.

---

## Package registry

The molt package registry is a JSON index hosted on a CDN, versioned, and
signed (future):

```json
{
  "version": 3,
  "packages": {
    "libpng": {
      "description": "PNG image library",
      "homepage":    "http://www.libpng.org/",
      "license":     "libpng",
      "versions": {
        "1.6.43": {
          "resolver": "zig-build",
          "url":  "https://…/libpng-1.6.43.tar.gz",
          "hash": "sha256:fecc95…",
          "build_zig_url":  "https://github.com/nektro/zig-build-libpng/…",
          "build_zig_hash": "sha256:…"
        }
      },
      "pkgconfig_name": "libpng16"   // fallback if zig-build fails
    }
  }
}
```

The `build_zig_url` points to a community-maintained repository of
`build.zig` wrappers for C libraries — similar to the Homebrew formulae
ecosystem but targeted at Zig's build system. molt uses these as the
default build recipe; users can override with `[dependencies.<name>.build]`.

**Seeding the registry.** Rather than building a registry from scratch, the
initial set of packages is bootstrapped from:

1. Libraries with existing `build.zig` wrappers (ziglings ecosystem)
2. Libraries detectable via `pkg-config` on common platforms (fallback tier)
3. User-submitted recipes via GitHub PRs to `molt-python/registry`

---

## Implementation phases

### Phase 1 — Token injection (no dependency resolution)

Enable `{cflags}` and `{ldflags}` as manual overrides in `moltproject.toml`
so users can write their own paths while the registry matures:

```toml
[build-env]
cflags  = "-I/opt/homebrew/include"
ldflags = "-L/opt/homebrew/lib -lpng -lz"
```

`{cflags}` / `{ldflags}` tokens in task commands expand to these values.
No resolution, no store — just consistent injection.

**Files:** `internal/runhandler/runhandler.go` (add tokens), `internal/tasks/tasks.go` (read `[build-env]`)

---

### Phase 2 — pkg-config resolver

`molt add libpng` checks `pkg-config --exists libpng` and, if found, records
the resolved flags in `molt.lock`:

```sh
molt add libpng
# → pkg-config --exists libpng16 ✓
# → records: cflags=$(pkg-config --cflags libpng16), ldflags=$(pkg-config --libs libpng16)
# → writes molt.lock
```

`molt sync` verifies the system library is still present and its version
matches the lock. A `--frozen` sync fails if the system library has been
upgraded.

**Files:** new `internal/pkgresolver/pkgconfig.go`, `internal/tasks/tasks.go` (token expansion from lock)

---

### Phase 3 — Zig-build C/C++ resolver

For packages not available via pkg-config, molt downloads and builds them
using Zig:

```sh
molt add libpng --resolver zig-build   # explicit; auto-detected from registry later
```

**Files:** new `internal/pkgresolver/zigbuild.go`, `internal/store-native/` (global native store), `internal/lockfile/` (molt.lock read/write)

---

### Phase 4 — Registry and auto-detection

- Ship `~/.molt/registry.json` with the initial package index
- `molt add libpng` auto-selects resolver (zig-build if in registry, pkg-config fallback)
- `molt upgrade` bumps versions
- `molt publish` for contributing packages back to the registry (future)

---

### Phase 5 — Native language delegates (Zig, Rust, Go)

- `lang = "zig"`: generate and manage `build.zig.zon`; `molt add` wraps `zig fetch`
- `lang = "rust"`: delegate `add`/`remove`/`sync` to Cargo; unified `molt run` surface
- `lang = "go"`: delegate to `go get` / `go mod download`; unified task runner

---

## Comparison to existing tools

| Tool | Scope | Global store | Cross-platform build | Lock file | Unified CLI |
|---|---|---|---|---|---|
| Conan | C/C++ | ✓ | partial | ✓ | — |
| vcpkg | C/C++ | ✓ | partial | ✓ | — |
| Zig pkg | Zig + C | ✓ (per-project) | ✓ | ✓ (hash-locked) | — |
| Cargo | Rust | ✓ | via cross | ✓ | — |
| **molt** | C/C++/Zig/Rust/Go/Python | ✓ (shared) | ✓ (zig cc) | ✓ | ✓ |

The key differentiator: **one tool, one CLI, one lock file discipline, shared
global store** — regardless of which language(s) a project uses. A team
adopting molt for Python gets C and Zig package management for free when they
add native hot paths; they do not need to learn Conan or vcpkg.

---

## Open questions

1. **Registry governance.** Who maintains the `build.zig` wrappers for popular
   C libraries? The Zig community ecosystem (Mitchell Hashimoto's work, the
   `nektro/` and `allyourcodebase/` organisations) is the most natural source.
   molt could automatically mirror their outputs.

2. **Binary caching.** Should pre-compiled C library binaries for common
   triples be published to a CDN (like Homebrew bottles) so users don't need
   to compile from source? This would dramatically speed up `molt sync` for
   popular packages.

3. **ABI stability.** C library binaries are tied to a specific compiler
   version (zig cc 0.16.0 vs 0.17.0 may produce incompatible ABIs). The store
   key should include the zig version used to compile. Alternatively, building
   with `-static` sidesteps the problem at the cost of binary size.

4. **`moltproject.toml` vs language-native manifests.** For Rust and Go,
   `Cargo.toml` and `go.mod` are the de-facto project files — most editors and
   CI systems know how to read them. Should molt keep these as the source of
   truth and treat `moltproject.toml` as an optional overlay, or generate them
   from `moltproject.toml`? The Python precedent (molt wraps `pyproject.toml`)
   suggests the former.
