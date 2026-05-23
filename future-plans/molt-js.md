# molt-js — molt for JavaScript

A port of molt's core ideas to the JavaScript / TypeScript ecosystem.
The central insight: **the runtime (bun, pnpm, npm, deno, yarn) is a swappable
implementation detail**, the same way `uv` is an implementation detail inside
Python molt. Users interact only with `molt` commands; which package manager
executes underneath is a one-line config choice.

---

## The problem molt-js solves

The JS ecosystem has fragmented the toolchain across many excellent but
disconnected tools:

| Need | Typical answer |
|---|---|
| Package manager | npm / pnpm / bun / yarn (pick one, locked in) |
| Node version management | Volta / fnm / nvm (separate install) |
| Task runner | package.json scripts / turbo / nx / just |
| Single-binary distribution | bun build --compile / pkg / nexe / deno compile |
| Native hot paths | wasm-pack (Rust only) / emscripten (C, verbose) |
| Cross-platform builds | CI matrix, Docker, or bun-specific flags |
| Multi-language scripts | Not supported anywhere |

Switching from bun to pnpm means rewriting CI scripts, README instructions,
team documentation, and every script reference. There is no equivalent of
Python molt's "uv is the engine; molt is the interface."

molt-js is that interface for JS.

---

## Architecture

```
molt-js/
├── main.go
└── internal/
    ├── runtime/
    │   ├── runtime.go      ← PackageManager interface
    │   ├── detect.go       ← auto-detect from lockfile
    │   ├── bun.go
    │   ├── pnpm.go
    │   ├── npm.go
    │   ├── deno.go
    │   └── yarn.go
    ├── wasm/
    │   └── build.go        ← WASM compilation + type generation
    ├── runhandler/
    │   └── runhandler.go   ← extension dispatch (same as Python molt)
    ├── binary/
    │   └── build.go        ← single-binary packaging
    └── config/
        └── config.go       ← package.json + [molt] section parsing
```

Written in Go (same as Python molt) — single static binary, no Node.js
required to run the tool itself.

---

## Config format

Stored in `package.json` under a `"molt"` key. Non-invasive — works alongside
any existing tooling. No separate config file to maintain.

```json
{
  "name": "my-api",
  "version": "1.0.0",
  "dependencies": {
    "hono": "^4.6.0",
    "zod": "^3.23.0"
  },
  "devDependencies": {
    "typescript": "^5.4.0"
  },

  "molt": {
    "runtime": "bun",

    "tasks": {
      "dev":   "src/server.ts --watch",
      "test":  "test/**/*.test.ts",
      "lint":  "eslint src/ --ext .ts",
      "bench": "bench/run.ts",
      "build": "src/index.ts"
    },

    "wasm": [
      {
        "source":  "src/native/stats.zig",
        "exports": ["sum_f64", "dot_product", "variance", "percentile"]
      },
      {
        "source":  "src/native/img.c",
        "exports": ["resize_bilinear", "to_grayscale"]
      }
    ],

    "run-handlers": {
      "rs":  "wasm-pack build --target bundler {dir} -- --features {basename}",
      "py":  "{python} {file} {args}",
      "rb":  "ruby {file} {args}",
      "sh":  "sh {file} {args}"
    },

    "node-version": "22",

    "binary": {
      "entry":  "src/index.ts",
      "output": "dist/my-api"
    }
  }
}
```

### Key fields

| Field | Description |
|---|---|
| `runtime` | `"bun"` \| `"pnpm"` \| `"npm"` \| `"deno"` \| `"yarn"`. Auto-detected from lockfile if omitted. |
| `tasks` | Named commands. Shorthand: just the args after the runtime's script runner. `"src/server.ts --watch"` becomes `bun src/server.ts --watch` or `deno run src/server.ts --watch` depending on adapter. |
| `wasm` | WASM hot paths. See [wasm-integration.md](wasm-integration.md). |
| `run-handlers` | Extension dispatch for non-JS files. Same token system as Python molt. |
| `node-version` | Pins the Node.js version for npm/pnpm/yarn adapters. Managed via Volta or fnm under the hood. |
| `binary` | Entry point and output path for `molt build`. |

---

## The PackageManager interface

All five adapters implement a single Go interface. No other code in molt-js
calls bun/pnpm/npm/deno directly.

```go
// internal/runtime/runtime.go

package runtime

import "os/exec"

type PackageManager interface {
    // Identity
    Name() string   // "bun" | "pnpm" | "npm" | "deno" | "yarn"

    // Dependency management
    Add(pkgs []string, dev bool) error
    Remove(pkgs []string) error
    Sync() error                        // frozen install from lockfile

    // Execution
    RunTask(task string, args []string) error
    Exec(cmd string, args []string) error
    Script(file string, args []string) (*exec.Cmd, error)

    // Distribution
    Build(entry, output string, opts BuildOpts) error

    // Environment
    GlobalStore() string       // path to the runtime's package cache
    Env() []string             // runtime-specific env vars to inject
    NodeVersion() string       // resolved Node.js version (empty for bun/deno)
}

type BuildOpts struct {
    OS            string  // "linux" | "darwin" | "windows" — empty = current
    Arch          string  // "amd64" | "arm64"              — empty = current
    BundleRuntime bool    // include JS runtime in the binary
    Minify        bool
}
```

### How each adapter implements RunTask

| Adapter | `molt run dev` becomes |
|---|---|
| bun | `bun run dev` |
| pnpm | `pnpm run dev` |
| npm | `npm run dev` |
| deno | `deno task dev` |
| yarn | `yarn run dev` |

### How each adapter implements Script

`Script` is used by `molt run src/server.ts` (file dispatch, not a named task).

| Adapter | `molt run src/server.ts` becomes |
|---|---|
| bun | `bun src/server.ts` |
| pnpm | `node src/server.ts` (pnpm resolves env; node runs the file) |
| npm | `node src/server.ts` |
| deno | `deno run --allow-all src/server.ts` |
| yarn | `node src/server.ts` |

### How each adapter implements Build

`molt build` or `molt build --os linux --arch amd64`.

| Adapter | Strategy |
|---|---|
| bun | `bun build --compile --target=bun-{os}-{arch} --outfile {output} {entry}` |
| deno | `deno compile --target={os}-{arch} --output {output} {entry}` |
| npm / pnpm / yarn | `esbuild --bundle --platform=node {entry} --outfile=dist/_bundle.js` → `pkg dist/_bundle.js --target node22-{os}-{arch} --output {output}` |

---

## Adapters — full implementations

### Bun

```go
// internal/runtime/bun.go

type Bun struct {
    bin     string
    projDir string
}

func (b *Bun) Name() string { return "bun" }

func (b *Bun) Add(pkgs []string, dev bool) error {
    args := []string{"add"}
    if dev {
        args = append(args, "-d")
    }
    return b.run(append(args, pkgs...)...)
}

func (b *Bun) Remove(pkgs []string) error {
    return b.run(append([]string{"remove"}, pkgs...)...)
}

func (b *Bun) Sync() error {
    return b.run("install", "--frozen-lockfile")
}

func (b *Bun) RunTask(task string, args []string) error {
    return b.run(append([]string{"run", task}, args...)...)
}

func (b *Bun) Exec(cmd string, args []string) error {
    return b.run(append([]string{"x", cmd}, args...)...)
}

func (b *Bun) Script(file string, args []string) (*exec.Cmd, error) {
    return b.cmd(append([]string{file}, args...)...), nil
}

func (b *Bun) Build(entry, output string, opts BuildOpts) error {
    args := []string{"build", "--compile", "--outfile", output}
    if opts.Minify {
        args = append(args, "--minify")
    }
    if opts.OS != "" || opts.Arch != "" {
        args = append(args, "--target="+bunTarget(opts))
    }
    return b.run(append(args, entry)...)
}

func (b *Bun) GlobalStore() string {
    home, _ := os.UserHomeDir()
    if d := os.Getenv("BUN_INSTALL"); d != "" {
        return filepath.Join(d, "cache")
    }
    return filepath.Join(home, ".bun", "install", "cache")
}

func (b *Bun) Env() []string {
    return []string{
        "BUN_INSTALL=" + filepath.Dir(b.GlobalStore()),
    }
}

func (b *Bun) NodeVersion() string { return "" } // bun ships its own

// bunTarget maps BuildOpts to Bun's --target format
// e.g. linux/amd64 → "bun-linux-x64"
func bunTarget(opts BuildOpts) string {
    arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[opts.Arch]
    return fmt.Sprintf("bun-%s-%s", opts.OS, arch)
}

func (b *Bun) run(args ...string) error {
    c := b.cmd(args...)
    return c.Run()
}

func (b *Bun) cmd(args ...string) *exec.Cmd {
    c := exec.Command(b.bin, args...)
    c.Dir = b.projDir
    c.Stdout = os.Stdout
    c.Stderr = os.Stderr
    c.Env = append(os.Environ(), b.Env()...)
    return c
}
```

### Deno

```go
// internal/runtime/deno.go

type Deno struct {
    bin     string
    projDir string
}

func (d *Deno) Name() string { return "deno" }

func (d *Deno) Add(pkgs []string, dev bool) error {
    // Deno 2.x: deno add npm:express jsr:@std/path
    // Prefixes packages with "npm:" if not already prefixed
    prefixed := make([]string, len(pkgs))
    for i, p := range pkgs {
        if !strings.HasPrefix(p, "npm:") && !strings.HasPrefix(p, "jsr:") {
            p = "npm:" + p
        }
        prefixed[i] = p
    }
    return d.run(append([]string{"add"}, prefixed...)...)
}

func (d *Deno) Remove(pkgs []string) error {
    return d.run(append([]string{"remove"}, pkgs...)...)
}

func (d *Deno) Sync() error {
    // deno caches on first import; pre-warm with deno cache
    return d.run("install")
}

func (d *Deno) RunTask(task string, args []string) error {
    return d.run(append([]string{"task", task}, args...)...)
}

func (d *Deno) Exec(cmd string, args []string) error {
    return d.run(append([]string{"run", "--allow-all", cmd}, args...)...)
}

func (d *Deno) Script(file string, args []string) (*exec.Cmd, error) {
    argv := append([]string{"run", "--allow-all", file}, args...)
    c := exec.Command(d.bin, argv...)
    c.Env = append(os.Environ(), d.Env()...)
    return c, nil
}

func (d *Deno) Build(entry, output string, opts BuildOpts) error {
    args := []string{"compile", "--allow-all", "--output", output}
    if opts.OS != "" || opts.Arch != "" {
        args = append(args, "--target="+denoTarget(opts))
    }
    return d.run(append(args, entry)...)
}

func (d *Deno) GlobalStore() string {
    if dir := os.Getenv("DENO_DIR"); dir != "" {
        return dir
    }
    home, _ := os.UserHomeDir()
    return filepath.Join(home, ".cache", "deno")
}

func (d *Deno) Env() []string {
    return []string{
        "DENO_DIR=" + d.GlobalStore(),
        "DENO_NO_UPDATE_CHECK=1",
    }
}

func (d *Deno) NodeVersion() string { return "" } // deno ships its own

// denoTarget maps BuildOpts to Deno's --target format
// e.g. linux/amd64 → "x86_64-unknown-linux-gnu"
func denoTarget(opts BuildOpts) string {
    arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[opts.Arch]
    os := map[string]string{
        "linux":   "unknown-linux-gnu",
        "darwin":  "apple-darwin",
        "windows": "pc-windows-msvc",
    }[opts.OS]
    return arch + "-" + os
}

func (d *Deno) run(args ...string) error {
    c := exec.Command(d.bin, args...)
    c.Dir = d.projDir
    c.Stdout = os.Stdout
    c.Stderr = os.Stderr
    c.Env = append(os.Environ(), d.Env()...)
    return c.Run()
}
```

### pnpm (representative of npm / yarn too)

```go
// internal/runtime/pnpm.go

type Pnpm struct {
    bin     string
    node    string // resolved node binary for Script()
    projDir string
}

func (p *Pnpm) Name() string { return "pnpm" }

func (p *Pnpm) Add(pkgs []string, dev bool) error {
    args := []string{"add"}
    if dev {
        args = append(args, "-D")
    }
    return p.run(append(args, pkgs...)...)
}

func (p *Pnpm) Remove(pkgs []string) error {
    return p.run(append([]string{"remove"}, pkgs...)...)
}

func (p *Pnpm) Sync() error {
    return p.run("install", "--frozen-lockfile")
}

func (p *Pnpm) RunTask(task string, args []string) error {
    return p.run(append([]string{"run", task}, args...)...)
}

func (p *Pnpm) Exec(cmd string, args []string) error {
    return p.run(append([]string{"exec", cmd}, args...)...)
}

// pnpm doesn't run files directly — delegate to the project's node binary
func (p *Pnpm) Script(file string, args []string) (*exec.Cmd, error) {
    argv := append([]string{file}, args...)
    c := exec.Command(p.node, argv...)
    c.Env = append(os.Environ(), p.Env()...)
    return c, nil
}

func (p *Pnpm) Build(entry, output string, opts BuildOpts) error {
    // Step 1: bundle with esbuild (universally available via pnpm exec)
    bundle := filepath.Join(p.projDir, "dist", "_bundle.cjs")
    if err := p.run("exec", "esbuild", entry,
        "--bundle", "--platform=node", "--format=cjs",
        "--outfile="+bundle); err != nil {
        return fmt.Errorf("esbuild: %w", err)
    }
    // Step 2: package with @vercel/pkg
    target := pkgTarget(opts)
    return p.run("exec", "pkg", bundle,
        "--output", output,
        "--target", target,
        "--no-bytecode",          // reproducible output
        "--public-packages", "*") // allow all packages
}

func (p *Pnpm) GlobalStore() string {
    out, err := exec.Command(p.bin, "store", "path").Output()
    if err == nil {
        return strings.TrimSpace(string(out))
    }
    home, _ := os.UserHomeDir()
    return filepath.Join(home, ".local", "share", "pnpm", "store", "v3")
}

func (p *Pnpm) Env() []string {
    return []string{"PNPM_HOME=" + filepath.Dir(p.GlobalStore())}
}

func (p *Pnpm) NodeVersion() string {
    out, _ := exec.Command(p.node, "--version").Output()
    return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}

// pkgTarget maps BuildOpts to @vercel/pkg --target format
// e.g. linux/amd64 → "node22-linux-x64"
func pkgTarget(opts BuildOpts) string {
    arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[opts.Arch]
    if arch == "" {
        arch = "x64"
    }
    os := opts.OS
    if os == "" {
        os = runtime.GOOS
    }
    return fmt.Sprintf("node22-%s-%s", os, arch)
}

func (p *Pnpm) run(args ...string) error {
    c := exec.Command(p.bin, args...)
    c.Dir = p.projDir
    c.Stdout = os.Stdout
    c.Stderr = os.Stderr
    c.Env = append(os.Environ(), p.Env()...)
    return c.Run()
}
```

---

## Auto-detection

```go
// internal/runtime/detect.go

// Detect returns the PackageManager for projDir.
// Priority: explicit config > lockfile heuristic > npm default.
func Detect(projDir, explicit string) (PackageManager, error) {
    name := explicit
    if name == "" {
        name = detectFromLockfile(projDir)
    }
    return newAdapter(name, projDir)
}

// detectFromLockfile walks up from projDir looking for a lockfile.
// Returns the runtime name whose lockfile was found first.
func detectFromLockfile(projDir string) string {
    // ordered: most-specific first
    candidates := []struct{ file, runtime string }{
        {"bun.lockb",         "bun"},
        {"bun.lock",          "bun"},   // bun 1.2+ text format
        {"pnpm-lock.yaml",    "pnpm"},
        {"yarn.lock",         "yarn"},
        {"deno.json",         "deno"},
        {"deno.jsonc",        "deno"},
        {"package-lock.json", "npm"},
    }
    dir := projDir
    for {
        for _, c := range candidates {
            if fileExists(filepath.Join(dir, c.file)) {
                return c.runtime
            }
        }
        parent := filepath.Dir(dir)
        if parent == dir {
            break // reached fs root
        }
        dir = parent
    }
    return "npm" // safe default
}

func newAdapter(name, projDir string) (PackageManager, error) {
    bin, err := findBin(name)
    if err != nil {
        return nil, fmt.Errorf(
            "runtime %q not found in PATH\n"+
                "Install it, or set molt.runtime in package.json to switch runtimes.\n"+
                "Error: %w", name, err)
    }
    switch name {
    case "bun":  return &Bun{bin: bin, projDir: projDir}, nil
    case "pnpm": return &Pnpm{bin: bin, node: nodeBin(), projDir: projDir}, nil
    case "npm":  return &Npm{bin: bin, node: nodeBin(), projDir: projDir}, nil
    case "deno": return &Deno{bin: bin, projDir: projDir}, nil
    case "yarn": return &Yarn{bin: bin, node: nodeBin(), projDir: projDir}, nil
    }
    return nil, fmt.Errorf("unknown runtime %q — valid: bun pnpm npm deno yarn", name)
}
```

---

## CLI surface

Every command is identical regardless of the runtime underneath.

```
Usage: molt <command> [args]

Dependency management:
  add <pkg...>              Add dependencies
  add -D <pkg...>           Add dev dependencies
  remove <pkg...>           Remove dependencies
  sync                      Install from lockfile (frozen, no network if cached)
  update [pkg...]           Update deps to latest matching version

Execution:
  run <task|file> [args]    Run named task or dispatch by file extension
  exec <cmd> [args]         Run a command in the project environment
  repl                      Open the runtime's REPL in project context

Native / WASM:
  wasm build                Compile WASM modules from source, generate types
  wasm list                 Show all declared WASM modules and their status
  wasm clean                Remove compiled .wasm files

Distribution:
  build [entry]             Bundle into a single self-contained binary
  build --os <os>           Cross-compile for target OS (linux|darwin|windows)
  build --arch <arch>       Cross-compile for target arch (amd64|arm64)

Project management:
  init [name]               Create a new molt-js project
  info                      Show runtime, store path, tasks, WASM modules
  switch <runtime>          Migrate to a different package manager

Toolchain:
  runtime list              Show available runtimes and their versions
  runtime install <rt>      Install a runtime via molt's toolchain manager
  node use <version>        Pin the Node.js version (for npm/pnpm/yarn)
```

---

## `molt switch` — runtime migration

```bash
$ molt switch pnpm

  Current runtime:  bun  (detected from bun.lockb)
  Target runtime:   pnpm  (1.9.2)

  Migration plan:
    1. Run pnpm import  — convert bun.lockb → pnpm-lock.yaml
    2. Delete bun.lockb
    3. Run pnpm install --frozen-lockfile  — verify lockfile is consistent
    4. Update package.json molt.runtime → "pnpm"
    5. Regenerate .molt/plugin.ts for pnpm-compatible import aliases

  All molt commands will continue to work unchanged after migration.
  Proceed? [y/N] y

  ✓ pnpm import (412 packages)
  ✓ Deleted bun.lockb
  ✓ pnpm install --frozen-lockfile
  ✓ Updated package.json
  ✓ Regenerated import aliases

  Done in 3.2s. Runtime is now pnpm.

$ molt run dev        # → pnpm run dev  (was: bun run dev)
$ molt add express    # → pnpm add express
$ molt build          # → esbuild + pkg wrap
```

The migration table covers all pairs:

| From | To | Lockfile conversion |
|---|---|---|
| bun | pnpm | `pnpm import` reads `bun.lockb` |
| bun | npm | delete `bun.lockb`, `npm install` re-resolves |
| bun | deno | delete `bun.lockb`, `deno install`, add `npm:` prefixes |
| pnpm | bun | delete `pnpm-lock.yaml`, `bun install` re-resolves |
| pnpm | npm | `npm install` re-resolves from `package.json` |
| npm | pnpm | `pnpm import` reads `package-lock.json` |
| npm | bun | delete `package-lock.json`, `bun install` re-resolves |
| deno | npm | strip `npm:` prefixes from `deno.json`, `npm install` |
| any | yarn | `yarn import` for npm/pnpm, re-resolve otherwise |

---

## Run-handler extension dispatch

Same system as Python molt. Any file with a registered extension is dispatched
to the appropriate runtime — no config, no task definition needed.

Built-in handlers:

```yaml
# ~/.molt-js/run-handlers.yaml
ts:   "{runtime} {file} {args}"        # bun/deno run it; node needs ts-node
mts:  "{runtime} {file} {args}"
js:   "{runtime} {file} {args}"
mjs:  "{runtime} {file} {args}"
cjs:  "{runtime} {file} {args}"
zig:  "molt wasm build {file} && {runtime} {dir}/{basename}.js {args}"
c:    "zig cc -O2 -target wasm32-freestanding --no-entry -o {dir}/{basename}.wasm {file} && {runtime} {dir}/{basename}.js {args}"
rs:   "wasm-pack build {dir} --target bundler && {runtime} {dir}/pkg/index.js {args}"
py:   "{python} {file} {args}"
rb:   "ruby {file} {args}"
sh:   "sh {file} {args}"
```

Token reference:

| Token | Expands to |
|---|---|
| `{runtime}` | Active runtime binary (bun, node, deno) |
| `{file}` | Absolute path to the script |
| `{dir}` | Directory containing the script |
| `{basename}` | Filename without extension |
| `{args}` | Extra arguments, space-joined |
| `{python}` | System or pinned Python interpreter |
| `{zig}` | Auto-installed zig binary |

Custom handlers via CLI (persisted to `~/.molt-js/run-handlers.yaml`):

```bash
molt run-handler add tsx  "npx ts-node {file} {args}"
molt run-handler add jl   "julia {file} {args}"
molt run-handler list
```

---

## Full session example

```bash
$ cd my-api
$ molt info

  Project:  my-api  v1.0.0
  Runtime:  bun  1.1.38  (from bun.lockb)
  Node:     —  (bun ships its own)
  Store:    ~/.bun/install/cache  (3.8 GB)
  Tasks:    dev  test  lint  bench  build
  WASM:     src/native/stats.zig  [sum_f64 dot_product variance]
            src/native/img.c      [resize_bilinear to_grayscale]

$ molt add hono zod
  bun add hono zod
  ✓ hono@4.6.3
  ✓ zod@3.23.8

$ molt add -D @types/bun
  bun add -d @types/bun
  ✓ @types/bun@1.1.38

$ molt sync
  bun install --frozen-lockfile
  ✓ 214 packages installed (cache hit: 214)

$ molt run dev
  bun src/server.ts --watch
  [server] http://localhost:3000

$ molt run hello.zig Alice
  → .zig handler → zig build-lib -target wasm32-freestanding
  → generate .molt/wasm/hello.wasm + hello.d.ts
  → bun .molt/wasm/hello.js Alice
  Hello from Zig! 👋 Alice

$ molt run preprocess.py data.csv
  → .py handler → python preprocess.py data.csv
  Processing data.csv...

$ molt wasm build
  Compiling src/native/stats.zig...
  ✓  .molt/wasm/stats.wasm   (18 KB)
  ✓  .molt/wasm/stats.d.ts
  ✓  .molt/wasm/stats.js

  Compiling src/native/img.c...
  ✓  .molt/wasm/img.wasm     (42 KB)
  ✓  .molt/wasm/img.d.ts
  ✓  .molt/wasm/img.js

  Import aliases written:
  ✓  package.json "imports"   →  import { load } from "#wasm/stats"
  ✓  .molt/plugin.ts          →  import stats from "wasm:stats"
  ✓  wasm.ts                  →  import { wasm } from "./wasm"

$ molt build
  bun build --compile --outfile dist/my-api src/index.ts
  ✓  dist/my-api  (51 MB, bun runtime bundled)

$ molt build --os linux --arch amd64
  bun build --compile --target=bun-linux-x64 --outfile dist/my-api-linux src/index.ts
  ✓  dist/my-api-linux

$ scp dist/my-api-linux user@prod-server:/opt/my-api
$ ssh user@prod-server /opt/my-api
  [server] http://0.0.0.0:3000
```

---

## What makes this different from just using bun

| | bun directly | molt-js + bun |
|---|---|---|
| Switch to pnpm/deno | Rewrite CI, scripts, docs | `molt switch pnpm` |
| WASM from C/Zig | Manual wasm-pack / emcc | `molt wasm build` + auto types |
| TypeScript types for WASM | Manual | Auto-generated `.d.ts` |
| Clean import path | `./.molt/wasm/stats.js` | `wasm:stats` / `#wasm/stats` |
| Cross-compile | `--target=bun-linux-x64` | `--os linux --arch amd64` (any runtime) |
| Run `.zig` / `.py` / `.rb` | Not supported | Extension dispatch |
| Migrate lockfile | Manual | `molt switch` handles conversion |
| Team onboarding | "install bun, run bun install" | "install molt, run molt sync" |

The runtime is an implementation detail. Teams that standardise on `molt`
commands can freely switch the underlying runtime as better options emerge —
without touching CI pipelines, READMEs, or developer habits.

---

## Implementation phases

### Phase 1 — Core (MVP)
- PackageManager interface + bun, pnpm, npm adapters
- Auto-detection from lockfile
- `molt add`, `molt remove`, `molt sync`, `molt run`, `molt exec`
- `molt info`, `molt switch`
- package.json config parsing

### Phase 2 — Distribution
- `molt build` with cross-compilation for all three adapters
- Deno adapter
- Yarn adapter
- `molt init` scaffolding

### Phase 3 — Native
- `molt wasm build` (Zig + C via zig cc)
- TypeScript type generation from WASM exports
- Import alias generation (#wasm, wasm: protocol, barrel)
- Bun plugin + bunfig.toml wiring
- Vite plugin generation
- Rust via wasm-pack

### Phase 4 — Advanced
- Run-handler extension dispatch
- `molt node use <version>` (Volta / fnm integration)
- Global package store (NODE_PATH injection, skip node_modules)
- `molt gc` for WASM cache
- MCP server (`molt mcp`) — same as Python molt
