# Demo 20 — Transport Glue: Go library imports

Import Go's standard library and third-party packages from Python — **no local
Go code required**. molt detects that the `src` field is a library import path
(contains `/` with no file extension) and generates the server automatically.

## Layout

```
20-glue-lib/
  gosha256.molt.toml   ← wraps crypto/sha256 (stdlib)
  sha3.molt.toml       ← wraps golang.org/x/crypto/sha3 (third-party)
  moltproject.toml
  main.py
```

## Config

Each glue module has its own manifest file alongside `moltproject.toml`.

```toml
# gosha256.molt.toml
lang = "go"
src  = "crypto/sha256"   # stdlib — no go get needed

[[fn]]
name    = "sum256"
call    = "sha256.Sum256"
args    = [{ name = "data", type = "bytes" }]
returns = "bytes"
```

```toml
# sha3.molt.toml
lang = "go"
src  = "golang.org/x/crypto/sha3"  # third-party; molt runs go get

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

## What molt does

For `crypto/sha256`:
- Creates a temp Go module, generates `server.go` with `import "crypto/sha256"`
- Builds the server binary, generates `gosha256.py`

For `golang.org/x/crypto/sha3`:
- Creates a temp Go module, runs `go get golang.org/x/crypto/sha3`
- Generates `server.go` with the import, builds, generates `sha3.py`

## Run

```sh
molt sync      # downloads golang.org/x/crypto, compiles both servers
molt run demo  # python main.py
```

Expected output:
```
=== Transport Glue: Go library imports from Python ===

SHA-256 (Go stdlib crypto/sha256): 5b9a2b69...
SHA3-256 (golang.org/x/crypto):    fa3f8ac3...
SHA3-512 (golang.org/x/crypto):    8a5e4f12...

✓ Both Go packages called from Python with zero local Go code
  SHA-256 ≠ SHA3-256: True
```
