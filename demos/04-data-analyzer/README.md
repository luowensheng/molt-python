# 04-data-analyzer

Generates synthetic sales data and analyzes it with pandas, displayed as rich
terminal tables with an ASCII bar chart. Shows that heavy scientific packages
(pandas, numpy) are installed once into the global store and shared across every
project that needs them.

## What this demo shows

- `molt add pandas rich` caches wheels in `~/.molt/pkg/` — not in a per-project venv
- numpy (pandas' dependency) is also cached; re-used by any other project that needs it
- Multiple analysis commands exposed as simple molt tasks
- No environment setup needed before running — `molt run generate` just works

## Project layout

```
04-data-analyzer/
  generate_data.py   generates data/sales.csv (240 rows, reproducible seed)
  analyze.py         four analysis views: summary, top, region, trend
  data/
    sales.csv        generated output (gitignored)
  pyproject.toml     deps: pandas, rich — plus task definitions
  uv.lock
```

## Running

```bash
# Step 1: generate the sample dataset
molt run generate
# → data/sales.csv: 240 rows (12 months × 4 regions × 5 products)

# Step 2: run analysis
molt run summary    # totals panel
molt run top        # top 5 products by revenue
molt run region     # revenue split by region with percentages
molt run trend      # monthly trend with ASCII bar chart
```

## Tasks defined

| Task | Command |
|---|---|
| `generate` | `python generate_data.py` |
| `summary` | `python analyze.py summary` |
| `top` | `python analyze.py top` |
| `region` | `python analyze.py region` |
| `trend` | `python analyze.py trend` |
| `all` | runs all four views in sequence |

## Store footprint

After `molt add pandas rich`, the global store gains entries like:

```
~/.molt/pkg/
  pandas/3.0.2/cp311-cp311-macosx_11_0_arm64/
  numpy/2.4.4/cp311-cp311-macosx_11_0_arm64/
  python-dateutil/2.9.0.post0/py3-none-any/
  ...
```

A second project that lists `pandas` in its dependencies and pins the same version
will show `✓ cached pandas 3.0.2` — the wheel is not re-downloaded, not re-unpacked.

Verify this by running `molt add pandas` in any other demo project — you will see the
cached hit immediately.

## Dataset schema

`data/sales.csv` has five columns:

| Column | Description |
|---|---|
| `month` | `YYYY-MM` (2024-01 through 2024-12) |
| `region` | North, South, East, West |
| `product` | Widget A, Widget B, Gadget X, Gadget Y, Tool Z |
| `units` | random integer 10–500 |
| `unit_price` | random float $5–$150 |

`analyze.py` derives `revenue = units × unit_price` at load time.
