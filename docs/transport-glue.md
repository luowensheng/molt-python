# Transport Glue

Call Go or Rust functions from Python over a zero-configuration IPC channel.
No `.so`, no ABI constraints, no cgo — functions are exposed as a JSON-RPC
server that molt generates, compiles, and wires into your project automatically.

---

## Why Transport Glue?

Python `.so` extensions require matching ABIs, complex build setups (cgo,
Cython, cffi), and break across Python versions. Transport Glue side-steps all
of that: the Go or Rust code runs as a subprocess or daemon, and Python talks
to it over a local IPC pipe. Any type that survives a JSON round-trip works.
The generated `.py` + `.pyi` client looks like a plain Python module to your
code and IDE.

---

## Manifest format: `<name>.molt.toml`

Each glue module lives in its own manifest file alongside `moltproject.toml`.
The filename prefix becomes the Python import name:

```
stats.molt.toml   →   import stats
```

molt distinguishes glue manifests from kernel manifests by the presence of the
`lang` key (glue manifests always have it; kernel manifests never do).

### Top-level keys

| Key | Required | Description |
|---|---|---|
| `lang` | yes | Language driver: `"go"` or `"rust"` |
| `src` | yes | Go: local package dir (`"./gocode"`), stdlib import path (`"crypto/sha256"`), or third-party path (`"golang.org/x/crypto/sha3"`). Rust: adapter source file (`"compress_glue.rs"`). |
| `transport` | no | `"stdio"` (default), `"unix_socket"`, or `"tcp"` |
| `crates` | no | Rust only. Extra crates added to the generated `Cargo.toml`, e.g. `["flate2 = '1.0'"]` |
| `pkg_module` | no | Go only. Override the generated Go module name (default: `molt-glue-<name>`) |

### `[[fn]]` section

Each `[[fn]]` block declares one callable function.

| Key | Required | Description |
|---|---|---|
| `name` | yes | Python-side function name (e.g. `"mean"`) |
| `call` | no | Go/Rust symbol to call if different from `name` (e.g. `"sha256.Sum256"`) |
| `args` | yes | Array of `{ name = "…", type = "…" }` tables |
| `returns` | yes | Return type string (see type table below) |

---

## Type system

| molt type | Go type | Rust type | Python type | Wire (JSON) |
|---|---|---|---|---|
| `i32` | `int32` | `i32` | `int` | number |
| `i64` | `int64` | `i64` | `int` | number |
| `f32` | `float32` | `f32` | `float` | number |
| `f64` | `float64` | `f64` | `float` | number |
| `bool` | `bool` | `bool` | `bool` | boolean |
| `string` | `string` | `String` | `str` | string |
| `bytes` | `[]byte` | `Vec<u8>` | `bytes` | base64 string |
| `[]i32` | `[]int32` | `Vec<i32>` | `list[int]` | array |
| `[]f64` | `[]float64` | `Vec<f64>` | `list[float]` | array |
| `[]string` | `[]string` | `Vec<String>` | `list[str]` | array |
| `json` | `interface{}` | `serde_json::Value` | `Any` | any JSON |

`bytes` arguments and return values are base64-encoded on the wire; the Python
client encodes/decodes transparently so callers always pass and receive `bytes`.

---

## Transport semantics

### `stdio` (default)

The server binary is launched as a child process when the Python client module
is first imported. Requests and responses are newline-delimited JSON on
stdin/stdout. One process per Python process; no sockets, no daemon management.

### `unix_socket`

The server runs as a persistent daemon. On first call the Python client checks
for a socket file at `MOLT_GLUE_SOCK` (defaulting to
`/tmp/molt-glue-<name>-<pid>.sock`); if absent it starts the daemon. All
Python threads in the same process share the connection — safe for concurrent
use.

### `tcp`

Like `unix_socket` but listens on a TCP port. Useful when Go/Rust and Python
run in different containers or machines.

---

## Examples

### Demo 19 — Go local package (stdio)

```toml
# stats.molt.toml
lang      = "go"
src       = "./gocode"
transport = "stdio"

[[fn]]
name    = "mean"
args    = [{ name = "data", type = "[]f64" }]
returns = "f64"

[[fn]]
name    = "stddev"
args    = [{ name = "data", type = "[]f64" }]
returns = "f64"

[[fn]]
name    = "histogram"
args    = [{ name = "data", type = "[]f64" }, { name = "buckets", type = "i32" }]
returns = "[]i32"

[[fn]]
name    = "compress"
args    = [{ name = "payload", type = "bytes" }]
returns = "bytes"
```

The Go source in `gocode/` is `package stats` (not `package main`). molt
generates a wrapper `main.rs` that imports it via a Go module `replace`
directive. See [demos/19-glue-go](../demos/19-glue-go/).

### Demo 20 — Go library imports (stdlib + third-party)

No local Go code required. Set `src` to an import path and molt generates the
server for you. Third-party paths trigger `go get` automatically.

```toml
# gosha256.molt.toml
lang = "go"
src  = "crypto/sha256"

[[fn]]
name    = "sum256"
call    = "sha256.Sum256"
args    = [{ name = "data", type = "bytes" }]
returns = "bytes"
```

```toml
# sha3.molt.toml
lang = "go"
src  = "golang.org/x/crypto/sha3"

[[fn]]
name    = "sum256"
call    = "sha3.Sum256"
args    = [{ name = "data", type = "bytes" }]
returns = "bytes"

[[fn]]
name    = "sum512"
call    = "sha3.Sum512"
args    = [{ name = "data", type = "bytes" }]
returns = "bytes"
```

See [demos/20-glue-lib](../demos/20-glue-lib/).

### Demo 21 — Rust adapter file (unix_socket)

`compress_glue.rs` is a plain file with `pub fn` declarations — not a full
Cargo crate. molt generates a `main.rs` that pulls it in with `include!` and a
`Cargo.toml` that adds the listed crates.

```toml
# compress.molt.toml
lang      = "rust"
src       = "compress_glue.rs"
crates    = ["flate2 = '1.0'"]
transport = "unix_socket"

[[fn]]
name    = "deflate"
args    = [{ name = "data", type = "bytes" }]
returns = "bytes"

[[fn]]
name    = "inflate"
args    = [{ name = "data", type = "bytes" }]
returns = "bytes"

[[fn]]
name    = "ratio"
args    = [{ name = "data", type = "bytes" }]
returns = "f64"
```

See [demos/21-glue-rust](../demos/21-glue-rust/).

---

## What molt generates

After `molt sync`, the following files appear under `.molt/glue/`:

```
.molt/glue/
  stats_server        ← compiled Go/Rust server binary
  stats.py            ← Python client module (lazy start on first call)
  stats.pyi           ← type stubs for IDE completion
```

The build intermediates live under `.molt/_build_<name>/` and are
safe to delete; `molt sync` recreates them.

The Python shims at `~/.molt/projects/<id>/bin/python` are rewritten after
each glue build so that `.molt/glue/` is included in the hardcoded
`PYTHONPATH` inside the shim.

---

## CLI reference

### Glue module commands

```sh
molt glue list              # list all glue modules in the current project
molt glue show <name>       # show manifest, server path, and transport config
molt glue regen <name>      # regenerate server + Python client without full sync
molt glue start <name>      # start the glue server daemon (unix_socket / tcp only)
molt glue stop  <name>      # stop the glue server daemon
```

### Glue driver commands

```sh
molt glue-driver list            # list built-in and user-installed drivers
molt glue-driver show <name>     # show driver details and template paths
molt glue-driver add <path>      # install a custom driver from a directory
```

Custom drivers are registered in `~/.molt/glue-drivers.yaml`. Built-in drivers
cover Go and Rust; the system is extensible to Nim, Zig, Crystal, and others.
