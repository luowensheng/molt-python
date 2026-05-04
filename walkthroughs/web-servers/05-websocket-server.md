# Real-Time WebSocket Server — live-dashboard

This walkthrough builds a real-time live dashboard backend using FastAPI WebSockets, Redis Pub/Sub for fan-out broadcasting, and orjson for fast serialization. Clients connect over WebSocket and receive live metric updates — CPU, memory, request rates — streamed from a Redis channel. You will run it locally, load-test it, and ship it as a self-contained binary.

---

## 1. Project initialisation

```bash
molt init live-dashboard
cd live-dashboard
```

Add dependencies:

```bash
molt add fastapi "uvicorn[standard]" websockets redis orjson
molt add --dev pytest pytest-asyncio ruff
```

```
Resolving dependencies...
  + fastapi 0.115.5
  + uvicorn 0.32.1 [standard]
  + websockets 14.1
  + redis 5.2.1
  + orjson 3.10.12
  + pytest 8.3.4 [dev]
  + pytest-asyncio 0.24.0 [dev]
  + ruff 0.8.2 [dev]
Syncing global store...
  8 packages installed  →  ~/.molt/pkg/  (24.1 MB)
Wrote .molt/syspath.json
Wrote .molt/bin/uvicorn, .molt/bin/ruff, .molt/bin/pytest
```

---

## 2. Project layout

```
live-dashboard/
├── pyproject.toml
├── uv.lock
├── live_dashboard/
│   ├── __init__.py
│   ├── main.py
│   ├── config.py
│   ├── broadcaster.py
│   ├── metrics.py
│   └── routers/
│       ├── ws.py
│       └── health.py
├── static/
│   └── dashboard.html
└── tests/
    ├── conftest.py
    └── test_ws.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "live-dashboard"
version = "0.3.0"
requires-python = ">=3.12"
dependencies = [
  "fastapi>=0.115",
  "uvicorn[standard]>=0.32",
  "websockets>=14.1",
  "redis>=5.2",
  "orjson>=3.10",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "pytest-asyncio>=0.24",
  "ruff>=0.8",
]

[tool.molt.tasks]
dev       = "uvicorn live_dashboard.main:app --reload --port 8765"
test      = "pytest tests/ -v --tb=short -x"
lint      = "ruff check ."
load-test = "python scripts/load_test.py --connections 200 --duration 30"

[tool.pytest.ini_options]
asyncio_mode = "auto"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Application code

**`live_dashboard/broadcaster.py`**

```python
import asyncio
import redis.asyncio as aioredis
from typing import AsyncGenerator

class RedisBroadcaster:
    """Fan-out metric updates from Redis Pub/Sub to all connected WebSocket clients."""

    def __init__(self, redis_url: str) -> None:
        self._redis_url = redis_url
        self._client: aioredis.Redis | None = None
        self._pubsub: aioredis.client.PubSub | None = None

    async def connect(self) -> None:
        self._client = aioredis.from_url(self._redis_url)
        self._pubsub = self._client.pubsub()
        await self._pubsub.subscribe("metrics")

    async def disconnect(self) -> None:
        if self._pubsub:
            await self._pubsub.unsubscribe("metrics")
        if self._client:
            await self._client.aclose()

    async def listen(self) -> AsyncGenerator[bytes, None]:
        async for message in self._pubsub.listen():
            if message["type"] == "message":
                yield message["data"]
```

**`live_dashboard/routers/ws.py`**

```python
import asyncio
import orjson
from fastapi import APIRouter, WebSocket, WebSocketDisconnect
from live_dashboard.broadcaster import RedisBroadcaster
from live_dashboard.config import settings

router = APIRouter()
_clients: set[WebSocket] = set()

@router.websocket("/ws/metrics")
async def metrics_ws(websocket: WebSocket) -> None:
    await websocket.accept()
    _clients.add(websocket)
    broadcaster = RedisBroadcaster(settings.redis_url)
    await broadcaster.connect()
    try:
        async for raw in broadcaster.listen():
            data = orjson.loads(raw)
            await websocket.send_bytes(orjson.dumps(data))
    except WebSocketDisconnect:
        pass
    finally:
        _clients.discard(websocket)
        await broadcaster.disconnect()
```

**`live_dashboard/main.py`**

```python
from contextlib import asynccontextmanager
from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from live_dashboard.routers import ws, health

@asynccontextmanager
async def lifespan(app: FastAPI):
    yield   # startup / shutdown hooks here

app = FastAPI(title="Live Dashboard", version="0.3.0", lifespan=lifespan)
app.mount("/static", StaticFiles(directory="static"), name="static")
app.include_router(health.router)
app.include_router(ws.router)
```

**`live_dashboard/metrics.py`** — example publisher

```python
"""Run this as a background job to push metrics into Redis."""
import asyncio, time, psutil, orjson
import redis.asyncio as aioredis

async def publish_loop(redis_url: str, interval: float = 1.0) -> None:
    client = aioredis.from_url(redis_url)
    while True:
        payload = orjson.dumps({
            "ts": time.time(),
            "cpu": psutil.cpu_percent(),
            "mem": psutil.virtual_memory().percent,
        })
        await client.publish("metrics", payload)
        await asyncio.sleep(interval)
```

---

## 5. Running in development

```bash
REDIS_URL=redis://localhost:6379/0 molt run dev
```

```
INFO:     Will watch for changes in these directories: ['/home/dev/live-dashboard']
INFO:     Uvicorn running on http://127.0.0.1:8765 (Press CTRL+C to quit)
INFO:     Started reloader process [52341]
INFO:     Started server process [52344]
INFO:     Application startup complete.
```

Run the test suite:

```bash
molt run test
```

```
========================= test session starts ==========================
platform linux -- Python 3.12.8
collected 9 items

tests/test_ws.py::test_connect_and_receive_message PASSED
tests/test_ws.py::test_disconnect_cleanup PASSED
tests/test_ws.py::test_multiple_clients_broadcast PASSED
tests/test_ws.py::test_health_endpoint PASSED
...
============================== 9 passed in 0.68s ==============================
```

Run the load test (200 concurrent WebSocket connections for 30 seconds):

```bash
REDIS_URL=redis://localhost:6379/0 molt run load-test
```

```
Load test: 200 connections × 30s
  Publisher: 1 msg/s → Redis channel 'metrics'
  Clients:   200 WebSocket connections

  [████████████████████████████████████████] 30s

Results:
  Messages received : 5,991 (target: 6,000)
  Messages lost     : 9  (0.15%)
  Avg latency       : 2.1ms
  p99 latency       : 8.3ms
  Peak connections  : 200 / 200 stable
```

---

## 6. molt.yaml

```yaml
version: 1

project:
  name: live-dashboard
  version: 0.3.0
  python: "3.12"
  description: "Real-time WebSocket metrics dashboard"

deps:
  strategy: pyproject

include:
  - "live_dashboard/**/*.py"
  - "static/**/*"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"
  - "scripts/"

commands:
  default: serve

  serve:
    exec:
      - uvicorn
      - "live_dashboard.main:app"
      - "--host=0.0.0.0"
      - "--port=8765"
      - "--workers=1"
      - "--ws=websockets"
    description: "Start the WebSocket server"
    env:
      PYTHONUNBUFFERED: "1"

  publisher:
    exec: [python, "-m", "live_dashboard.metrics"]
    description: "Start the metrics publisher (pushes data into Redis)"

  health:
    script: "curl -sf http://localhost:8765/health"
    description: "HTTP health check"

env:
  PYTHONUNBUFFERED: "1"
  REDIS_URL: "redis://localhost:6379/0"

integrity:
  verify_on_install: true
```

---

## 7. Building

```bash
molt build
```

```
Building live-dashboard v0.3.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 26.8 MB (743 files)
  ✓ Integrity manifest: live-dashboard-v0.3.0.manifest.json
  ✓ Created: live-dashboard-v0.3.0 (46.2 MB)
    root_hash: 8e2c5f14a9d6b3e07c4f1a8d5e2b9f6c3a0d7f4b1e8c5a2d9f6b3e0c7a4d1f8
    files:     743    total: 26.8 MB
    build time: 3.5s
```

---

## 8. Deploy to a Linux server

```bash
scp live-dashboard-v0.3.0 live-dashboard-v0.3.0.manifest.json app@dash-01:/opt/services/
ssh app@dash-01
```

```bash
REDIS_URL="redis://redis.internal:6379/0" \
  /opt/services/live-dashboard-v0.3.0 install
```

```
Installing live-dashboard v0.3.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (743 files)...
  ✓ Installed to /home/app/.molt/apps/live-dashboard/0.3.0/
```

Start the server and the publisher as separate processes:

```bash
# WebSocket server
REDIS_URL="redis://redis.internal:6379/0" \
  /opt/services/live-dashboard-v0.3.0 run serve &

# Metrics publisher
REDIS_URL="redis://redis.internal:6379/0" \
  /opt/services/live-dashboard-v0.3.0 run publisher &
```

Both processes run from the same binary; the `serve` and `publisher` commands are independent entrypoints defined in `molt.yaml`.

---

## 9. Tips

- **Single-worker WebSocket server**: FastAPI's WebSocket handler is async and handles many concurrent connections in one process. Use `--workers=1` with uvicorn for WebSocket workloads — multiple workers cannot share in-process state.
- **Fan-out at scale**: the Redis Pub/Sub broadcaster decouples the publisher from the connected clients. Adding more publisher instances or more server replicas just requires pointing them at the same Redis channel.
- **orjson performance**: orjson serialization benchmarks at 3–10x faster than the stdlib `json` module for typical metric payloads. The library uses a Rust extension — the `.so` is included in the binary via the global store.
- **Static HTML client**: `static/dashboard.html` ships in the binary via the `include` glob. The FastAPI `StaticFiles` mount serves it directly — no separate web server needed.
- **Load testing in CI**: add `molt run load-test` as a step in your pipeline against a staging instance. A drop in `Messages received` or a spike in `p99 latency` flags regressions before they reach production.
