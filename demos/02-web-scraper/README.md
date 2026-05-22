# 02-web-scraper

Fetches data from a public JSON API and displays it in rich terminal tables.
Demonstrates `molt add` and how the global package store is shared across projects.

## What this demo shows

- `molt add requests rich` resolves, downloads, and caches wheels in `~/.molt/pkg/`
- A second project needing the same package hits the cache — zero re-download
- `molt run` composes `PYTHONPATH` from the global store; no venv involved
- Real package usage (HTTP client + terminal formatting) in minimal glue code

## Project layout

```
02-web-scraper/
  scraper.py         fetches and displays data from jsonplaceholder.typicode.com
  pyproject.toml     deps: requests, rich — plus task definitions
  uv.lock            pinned lockfile
```

## Running

```bash
# Show 5 most recent posts
molt run posts

# Show all users with email + city
molt run users

# Show top 10 posts
molt run top10
```

## Tasks defined

| Task | Command |
|---|---|
| `posts` | `molt python run scraper.py posts 5` |
| `users` | `molt python run scraper.py users` |
| `top10` | `molt python run scraper.py posts 10` |

## Global store sharing

When you ran `molt add requests rich`, the output showed lines like:

```
✓ cached  requests 2.33.1
✓ cached  rich 15.0.0
```

Those packages were already in `~/.molt/pkg/` from a previous project. No wheel
was downloaded; no bytes were copied. The store entry is reused directly.

To see what's currently cached:

```bash
ls ~/.molt/pkg/requests/
ls ~/.molt/pkg/rich/
```

Each entry is keyed by `(name, version, abi-platform)`. Every project that pins
the same version uses the same directory.

## How PYTHONPATH is built

`molt run` reads `.molt/syspath.json`, which looks like:

```json
{
  "python": "/path/to/cpython-3.11.14/bin/python3",
  "paths": [
    "/path/to/.molt/projects/02-web-scraper-...",
    "/Users/yourname/.molt/pkg/requests/2.33.1/py3-none-any",
    "/Users/yourname/.molt/pkg/rich/15.0.0/py3-none-any",
    "..."
  ]
}
```

No symlinks, no activation scripts — just `PYTHONPATH` set and `exec`.

## Passing extra arguments

Tasks are just commands. Pass extra args after `--`:

```bash
molt run posts -- 20       # top 20 posts (forwarded to scraper.py)
```

Or invoke the script directly under the store environment:

```bash
molt run python scraper.py users
```
