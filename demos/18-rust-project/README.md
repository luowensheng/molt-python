# Demo 18 — Rust as a Primary Language

This demo shows **Rust as a first-class language** in a molt project.
molt's pkg-backend system routes `molt add` / `molt sync` / `molt remove`
to **cargo** automatically when `lang = "rust"` is set in `moltproject.toml`
— no manual cargo invocation required.

## What it demonstrates

- Rust managed by molt: `molt add`, `molt sync`, `molt remove` all delegate to cargo
- The pkg-backend routing system: `pkgbackend.Find("rust")` → cargo adapter
- `moltproject.toml` with `lang = "rust"` as the trigger for backend selection
- A pure-std-library statistics CLI (mean, std\_dev, min, max, histogram)
- `molt run test` wiring to `cargo test` — unit tests included in `src/main.rs`

## Run it

```bash
# Run the stats binary directly via cargo (no pre-build step needed)
molt run stats 88 92 71

# Build an optimised release binary
molt run build

# Pre-baked demo with 10 numbers
molt run demo
```

## Package management — molt delegates to cargo

When `lang = "rust"` is declared, molt's pkg-backend system forwards every
package operation to cargo:

```
molt add serde --features derive   →  cargo add serde --features derive
molt add clap --features derive    →  cargo add clap --features derive
molt remove serde                  →  cargo remove serde
molt sync                          →  cargo fetch
```

`Cargo.toml` is the source of truth for dependencies; `molt add` edits it
exactly as `cargo add` would — because it *is* `cargo add`.

To inspect or configure the backend:

```bash
molt pkg-backend show rust
```

## How pkg-backend routing works

```
molt add serde
  │
  └─► pkgbackend.Find("rust")       # looks up lang from moltproject.toml
        │
        └─► RustBackend.Add(["serde"])
              │
              └─► cargo add serde   # delegates to the real cargo
```

```
molt sync
  │
  └─► RustBackend.Sync()
        │
        └─► cargo fetch             # resolves Cargo.lock, downloads crates
```

The backend is resolved once per project from the `lang` field in
`moltproject.toml`. Python projects continue to use uv as their backend
without any configuration change.

## moltproject.toml tasks

This demo uses `moltproject.toml` — the config format for non-Python projects.
It has the same `[tool.molt.tasks]` format as `pyproject.toml` but without
`requires-python` or `dependencies`; package management is handled entirely
by the pkg-backend instead.

```toml
[project]
name = "rust-project"
version = "0.1.0"
lang = "rust"

[tool.molt.tasks]
build  = "cargo build --release"
stats  = "cargo run --release --"
test   = "cargo test"
demo   = "cargo run --release -- 88 92 71 95 84 76 89 93 67 88"
clean  = "cargo clean"
```

## Project layout

```
moltproject.toml      # lang = "rust" → activates the cargo pkg-backend
Cargo.toml            # standard Rust manifest — edited by molt add/remove
src/
  main.rs             # statistics CLI: parse args, compute stats, print histogram
                      # also contains #[cfg(test)] unit tests (molt run test)
```

`lang = "rust"` in `moltproject.toml` is the single switch that tells molt
to route all package operations to cargo and to use cargo as the build
orchestrator.

## Key code — Rust idioms

**Iterators and fold for min/max in one pass:**

```rust
let (min, max) = data.iter().fold((data[0], data[0]), |(lo, hi), &x| {
    (lo.min(x), hi.max(x))
});
```

**Two-pass variance using `map` + `sum`:**

```rust
let variance: f64 = data.iter()
    .map(|&x| { let d = x - mean; d * d })
    .sum::<f64>()
    / n as f64;
```

**Building the histogram bar with a collected iterator:**

```rust
let bar: String = (0..bar_width)
    .map(|j| if j < filled { '#' } else { ' ' })
    .collect();
```

**Graceful error handling with `unwrap_or_else`:**

```rust
let data: Vec<f64> = raw_args.iter()
    .map(|s| s.parse::<f64>().unwrap_or_else(|_| {
        eprintln!("error: not a number: {}", s);
        process::exit(1);
    }))
    .collect();
```

## Sample output

```
─────────────────────────────
  n      10
  sum      863.0000
  mean      86.3000
  std        8.0561
  min       67.0000
  max       95.0000
─────────────────────────────

  Distribution (8 buckets):
   67.00– 70.75  [####################]  1
   70.75– 74.50  [                    ]  0
   74.50– 78.25  [####################]  1
   78.25– 82.00  [                    ]  0
   82.00– 85.75  [####################]  1
   85.75– 89.50  [########################################]  2
   89.50– 93.25  [########################################]  2
   93.25– 97.00  [########################################]  2
```
