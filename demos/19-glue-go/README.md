# Demo 19 — Transport Glue: Go functions from Python

Calls a Go statistics package from Python using molt's **transport glue**.
No `.so`, no `cgo`, no ABI headaches — the Go code runs as a lightweight
subprocess and Python calls it via JSON-RPC over a stdio pipe.

## Layout

```
19-glue-go/
  gocode/
    go.mod        ← module user/stats  (package stats, NOT package main)
    stats.go      ← Mean, Stddev, Histogram, Compress
  stats.molt.toml
  moltproject.toml
  main.py
```

> **Tip:** The Go source directory is named `gocode/` (not `stats/`) to avoid Python
> treating it as a namespace package when scanning the current directory.
> The `package stats` declaration inside `stats.go` is unaffected.

## Key config

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

## How it works

`molt sync` reads `stats.molt.toml` and:

1. **Generates** `.molt/_build_stats/go.mod` with a `replace` directive pointing at `./gocode`
2. **Generates** `.molt/_build_stats/server.go` (imports `user/stats`, JSON dispatch loop)
3. **Compiles** the server: `go build -o .molt/glue/stats_server .`
4. **Generates** `.molt/glue/stats.py` + `stats.pyi` (Python client)

After sync, `import stats` in Python starts the binary as a subprocess on
first call and routes JSON-encoded requests through it.

## Run

```sh
molt sync          # compile Go server + generate Python client
molt run demo      # python main.py
```

Expected output:
```
=== Transport Glue: Go → Python (stdio) ===

mean:   84.33
stddev:  8.52

Histogram (12 values → 5 buckets):
  bucket 1: ██ (2)
  bucket 2: ██ (2)
  bucket 3: ███ (3)
  bucket 4: ████ (4)
  bucket 5: █ (1)

Compressed 7,000 bytes → 112 bytes (1.6%)

✓ All Go functions called successfully via transport glue
```

## Useful commands

```sh
molt glue list               # show all glue modules in this project
molt glue show stats         # show config + server path
```
