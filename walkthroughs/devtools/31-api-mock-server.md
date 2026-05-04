# API Mock Server — Local Dev and Test Proxy

This walkthrough builds `mock-server`, a developer tool that loads an OpenAPI spec, generates realistic fake responses using Faker, and serves them locally so frontend and integration tests can run without hitting real APIs. It also supports a recording mode (proxy requests to the real API and save responses) and a replay mode (serve saved recordings deterministically). A Rich-powered CLI makes it easy to inspect matched routes and toggle modes. The finished binary is small enough to commit to a repository's `tools/` directory for teammates.

---

## 1. Project Init and molt sync

```
$ mkdir mock-server && cd mock-server
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  fastapi==0.111.0
  uvicorn[standard]==0.29.0
  pyyaml==6.0.1
  faker==25.0.1
  rich==13.7.1
  click==8.1.7
  ...
  Locked 22 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "mock-server"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "fastapi>=0.111",
    "uvicorn[standard]>=0.29",
    "pyyaml>=6.0",
    "faker>=25.0",
    "rich>=13.7",
    "click>=8.1",
]

[project.scripts]
mock-server = "mock_server.cli:main"

[tool.molt.tasks]
serve    = "mock-server serve --spec openapi.yaml --port 4010"
record   = "mock-server record --spec openapi.yaml --target https://api.example.com --port 4010"
replay   = "mock-server replay --dir recordings/ --port 4010"
validate = "mock-server validate --spec openapi.yaml"
```

---

## 3. Project Layout

```
mock_server/
├── cli.py             # Click CLI: serve, record, replay, validate
├── server.py          # FastAPI app factory
├── loader.py          # OpenAPI spec parser
├── generator.py       # Faker-based response generator
├── recorder.py        # Proxy + save to disk
├── replayer.py        # Serve from recordings/
└── display.py         # Rich tables for route/log display
```

**mock_server/loader.py**
```python
import yaml
from pathlib import Path
from dataclasses import dataclass

@dataclass
class Route:
    method: str
    path: str
    status: int
    response_schema: dict

def load_spec(spec_path: str) -> list[Route]:
    spec = yaml.safe_load(Path(spec_path).read_text())
    routes = []
    for path, path_item in spec.get("paths", {}).items():
        for method, operation in path_item.items():
            if method not in {"get", "post", "put", "patch", "delete"}:
                continue
            for status_str, response in operation.get("responses", {}).items():
                try:
                    status = int(status_str)
                except ValueError:
                    continue
                schema = (
                    response.get("content", {})
                    .get("application/json", {})
                    .get("schema", {})
                )
                routes.append(Route(method=method.upper(), path=path,
                                    status=status, response_schema=schema))
    return routes
```

**mock_server/generator.py**
```python
from faker import Faker
import random, uuid

fake = Faker()

TYPE_GENERATORS = {
    "string":  lambda fmt: {
        "email":    fake.email,
        "date":     fake.date,
        "date-time": fake.iso8601,
        "uuid":     lambda: str(uuid.uuid4()),
    }.get(fmt, fake.word)(),
    "integer": lambda _: random.randint(1, 10000),
    "number":  lambda _: round(random.uniform(0, 1000), 2),
    "boolean": lambda _: random.choice([True, False]),
}

def generate(schema: dict) -> object:
    if not schema:
        return {}
    t = schema.get("type", "object")
    fmt = schema.get("format", "")
    if t == "object":
        return {k: generate(v) for k, v in schema.get("properties", {}).items()}
    if t == "array":
        items = schema.get("items", {})
        return [generate(items) for _ in range(random.randint(1, 5))]
    gen = TYPE_GENERATORS.get(t)
    return gen(fmt) if gen else None
```

**mock_server/server.py**
```python
from fastapi import FastAPI, Request, Response
import json, time
from mock_server.loader import Route
from mock_server.generator import generate
from mock_server.display import log_request

def make_app(routes: list[Route]) -> FastAPI:
    app = FastAPI(title="mock-server")

    for route in routes:
        # Capture route in closure
        def make_handler(r: Route):
            async def handler(request: Request):
                body = generate(r.response_schema)
                log_request(r.method, r.path, r.status)
                return Response(
                    content=json.dumps(body),
                    status_code=r.status,
                    media_type="application/json",
                )
            handler.__name__ = f"{r.method}_{r.path.replace('/', '_')}"
            return handler

        app.add_api_route(
            route.path,
            make_handler(route),
            methods=[route.method],
        )

    return app
```

**mock_server/display.py**
```python
from rich.console import Console
from rich.table import Table
from rich.live import Live

console = Console()

def print_routes(routes):
    table = Table(title="Loaded Routes", show_lines=True)
    table.add_column("Method", style="cyan", width=8)
    table.add_column("Path", style="white")
    table.add_column("Status", style="green", width=8)
    for r in routes:
        table.add_row(r.method, r.path, str(r.status))
    console.print(table)

def log_request(method: str, path: str, status: int):
    color = "green" if status < 400 else "red"
    console.print(f"  [{color}]{status}[/{color}]  {method:6} {path}")
```

---

## 4. Running the Server

```
$ molt run serve
Loading spec: openapi.yaml
Loaded Routes
┌────────┬──────────────────────────────────┬────────┐
│ Method │ Path                             │ Status │
├────────┼──────────────────────────────────┼────────┤
│ GET    │ /users                           │ 200    │
│ POST   │ /users                           │ 201    │
│ GET    │ /users/{id}                      │ 200    │
│ PUT    │ /users/{id}                      │ 200    │
│ DELETE │ /users/{id}                      │ 204    │
│ GET    │ /products                        │ 200    │
│ POST   │ /orders                          │ 201    │
└────────┴──────────────────────────────────┴────────┘
INFO:     Uvicorn running on http://0.0.0.0:4010

$ curl -s http://localhost:4010/users | jq '.[0]'
{
  "id": 7823,
  "email": "alice.smith@example.com",
  "name": "Alice Smith",
  "created_at": "2023-11-15T08:32:11",
  "active": true
}
  200    GET    /users
```

Record real API traffic:

```
$ molt run record
Proxying to: https://api.example.com
Recording responses to: recordings/
INFO:     Uvicorn running on http://0.0.0.0:4010

$ curl http://localhost:4010/products
  200    GET    /products → recordings/GET_products_200.json  (saved)
```

Replay saved recordings:

```
$ molt run replay
Loading recordings from: recordings/
Replaying 14 saved responses
INFO:     Uvicorn running on http://0.0.0.0:4010

$ curl http://localhost:4010/products
  200    GET    /products  (from recording)
```

Validate spec for common issues:

```
$ molt run validate
Validating: openapi.yaml
  OK  openapi version: 3.1.0
  OK  14 paths defined
  OK  all $ref targets resolve
  WARN  GET /users/{id}: no 404 response defined
  WARN  POST /orders: request body schema missing
Validation complete: 2 warnings, 0 errors
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: mock-server
  entry: mock_server.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: serve
      args: ["serve"]
      description: "Serve fake responses from an OpenAPI spec"
    - name: record
      args: ["record"]
      description: "Proxy and record real API traffic"
    - name: replay
      args: ["replay"]
      description: "Replay saved recordings"
    - name: validate
      args: ["validate"]
      description: "Validate an OpenAPI spec file"

  env:
    LOG_LEVEL: "INFO"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling mock_server + 22 dependencies...
  Output: dist/mock-server  (15.3 MB)
```

---

## 6. Distributing to Teammates

Commit the binary to the repository's `tools/` directory:

```
$ cp dist/mock-server tools/mock-server-linux-amd64
$ git add tools/mock-server-linux-amd64
$ git commit -m "chore: update mock-server binary to v0.1.0"
```

On macOS developers' machines, build the macOS binary:

```
$ molt build --target macos/arm64
  Output: dist/mock-server-macos-arm64  (14.8 MB)
```

Add a helper script so teammates don't need to know which binary to use:

```bash
#!/usr/bin/env bash
# tools/mock
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
[[ "$ARCH" == "x86_64" ]] && ARCH="amd64"
exec "$(dirname "$0")/mock-server-${OS}-${ARCH}" "$@"
```

```
$ chmod +x tools/mock
$ tools/mock serve --spec openapi.yaml
Loading spec: openapi.yaml
...
```

---

## Tips

- **Path parameter matching**: OpenAPI uses `{id}` syntax; FastAPI uses `{id}` too, so they map directly. Starlette's path matching handles optional trailing slashes.
- **Example values**: If the OpenAPI spec includes `example` or `x-faker` extension fields, prefer those over randomly generated data for deterministic tests.
- **Seeded Faker**: Pass `--seed 42` to seed Faker so the same request always returns the same fake data across test runs.
- **CORS**: Add `fastapi.middleware.cors.CORSMiddleware` with `allow_origins=["*"]` so browser-based frontends can hit the mock without proxy configuration.
- **Auth bypass**: Add a middleware that accepts any `Authorization: Bearer ...` header and injects a fake user identity, so tests don't need real JWT tokens.
- **Binary in CI**: Install the binary in CI by downloading it from S3 or GitHub Releases — faster than `pip install` for a large spec with many deps.
