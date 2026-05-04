# event-processor — Kafka Consumer Writing to ClickHouse

`event-processor` is a long-running Kafka consumer that deserializes Avro events,
validates them with Pydantic, and writes batches to ClickHouse. Prometheus metrics
expose consumer lag, throughput, and error rates. This walkthrough covers local
development with molt and deploying the consumer to a Kafka consumer group on a
production cluster.

---

## 1. Project Init and molt sync

```bash
$ mkdir event-processor && cd event-processor
$ molt init
✔ Created pyproject.toml
✔ Created src/processor/__init__.py
✔ Initialized uv environment

$ molt sync
✔ Resolved 52 packages
✔ Installed confluent-kafka==2.3.0
✔ Installed clickhouse-driver==0.2.7
✔ Installed pydantic==2.6.1
✔ Installed prometheus-client==0.20.0
✔ Installed pytest==8.1.1
✔ Installed pytest-asyncio==0.23.6
Environment ready in .venv/
```

---

## 2. pyproject.toml

```toml
[project]
name = "event-processor"
version = "0.6.0"
description = "Kafka consumer → ClickHouse writer with Prometheus metrics"
requires-python = ">=3.11"
dependencies = [
    "confluent-kafka==2.3.0",
    "clickhouse-driver>=0.2.7",
    "pydantic>=2.6.1",
    "prometheus-client>=0.20.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=8.1.1",
    "pytest-mock>=3.12.0",
    "ruff>=0.3.2",
    "pytest-asyncio>=0.23.6",
    "testcontainers[kafka]>=3.7.1",
]

[tool.molt.tasks]
consume = "python -m processor"
test    = "pytest tests/ -v --tb=short"
bench   = "pytest tests/bench/ --benchmark-only"
lint    = "ruff check src/ tests/ && ruff format --check src/ tests/"

[tool.ruff]
line-length = 100
target-version = "py311"

[tool.pytest.ini_options]
testpaths = ["tests"]
asyncio_mode = "auto"
```

---

## 3. Project Structure

```
event-processor/
├── pyproject.toml
├── molt.yaml
├── config/
│   └── processor.yaml
├── src/
│   └── processor/
│       ├── __init__.py
│       ├── __main__.py
│       ├── consumer.py
│       ├── models.py
│       ├── clickhouse_writer.py
│       └── metrics.py
└── tests/
    ├── conftest.py
    ├── test_consumer.py
    ├── test_models.py
    ├── test_writer.py
    └── bench/
        └── test_bench_throughput.py
```

### src/processor/models.py

```python
from pydantic import BaseModel, field_validator
from datetime import datetime
from enum import Enum

class EventType(str, Enum):
    PAGE_VIEW  = "page_view"
    CLICK      = "click"
    PURCHASE   = "purchase"
    SIGN_UP    = "sign_up"

class Event(BaseModel):
    event_id:   str
    event_type: EventType
    user_id:    str
    session_id: str
    timestamp:  datetime
    properties: dict[str, str | int | float | bool]

    @field_validator("event_id")
    @classmethod
    def event_id_not_empty(cls, v: str) -> str:
        if not v.strip():
            raise ValueError("event_id must not be empty")
        return v
```

### src/processor/consumer.py (excerpt)

```python
import logging
import signal
import json
from confluent_kafka import Consumer, KafkaError
from processor.models import Event
from processor.clickhouse_writer import ClickHouseWriter
from processor.metrics import EVENTS_CONSUMED, EVENTS_FAILED, BATCH_WRITE_DURATION

log = logging.getLogger(__name__)
BATCH_SIZE = 500

def run_consumer(config: dict):
    consumer = Consumer({
        "bootstrap.servers":  config["kafka"]["bootstrap_servers"],
        "group.id":           config["kafka"]["group_id"],
        "auto.offset.reset":  "earliest",
        "enable.auto.commit": False,
    })
    consumer.subscribe([config["kafka"]["topic"]])
    writer = ClickHouseWriter(config["clickhouse"])
    batch: list[Event] = []
    running = True

    def _shutdown(sig, frame):
        nonlocal running
        log.info("Shutting down consumer")
        running = False
    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    while running:
        msg = consumer.poll(timeout=1.0)
        if msg is None:
            continue
        if msg.error():
            if msg.error().code() != KafkaError._PARTITION_EOF:
                log.error("Kafka error: %s", msg.error())
                EVENTS_FAILED.inc()
            continue
        try:
            event = Event(**json.loads(msg.value()))
            batch.append(event)
            EVENTS_CONSUMED.inc()
        except Exception as exc:
            log.warning("Invalid event: %s", exc)
            EVENTS_FAILED.inc()
            consumer.commit(msg)
            continue

        if len(batch) >= BATCH_SIZE:
            with BATCH_WRITE_DURATION.time():
                writer.insert(batch)
            consumer.commit(asynchronous=False)
            log.info("Flushed %d events to ClickHouse", len(batch))
            batch.clear()

    if batch:
        writer.insert(batch)
        consumer.commit(asynchronous=False)
    consumer.close()
```

### src/processor/metrics.py

```python
from prometheus_client import Counter, Histogram, start_http_server

EVENTS_CONSUMED = Counter("events_consumed_total", "Total events consumed from Kafka")
EVENTS_FAILED   = Counter("events_failed_total",   "Total events that failed validation")
BATCH_WRITE_DURATION = Histogram(
    "batch_write_duration_seconds", "Time to write a batch to ClickHouse",
    buckets=[0.01, 0.05, 0.1, 0.5, 1.0, 5.0],
)

def start_metrics_server(port: int = 8000):
    start_http_server(port)
```

---

## 4. Running Tasks with molt run

```bash
# Start the consumer locally (requires local Kafka + ClickHouse)
$ KAFKA_BOOTSTRAP=localhost:9092 CLICKHOUSE_HOST=localhost molt run consume
2026-05-04 09:00:01 INFO Consumer started. Group: event-processors. Topic: raw-events
2026-05-04 09:00:02 INFO Metrics server listening on :8000
2026-05-04 09:00:04 INFO Flushed 500 events to ClickHouse (0.041s)
2026-05-04 09:00:05 INFO Flushed 500 events to ClickHouse (0.038s)
^C
2026-05-04 09:00:07 INFO Shutting down consumer
2026-05-04 09:00:07 INFO Flushed 213 remaining events to ClickHouse

# Run tests
$ molt run test
========================= test session starts ==========================
collected 29 items
tests/test_models.py::test_valid_event PASSED
tests/test_models.py::test_invalid_event_type PASSED
tests/test_models.py::test_empty_event_id PASSED
tests/test_consumer.py::test_batch_flush PASSED
tests/test_writer.py::test_insert_batch PASSED
...
========================= 29 passed in 3.44s ===========================

# Throughput benchmark
$ molt run bench
test_throughput_500_events_per_batch    0.038s mean    5  rounds
test_throughput_1000_events_per_batch   0.071s mean    5  rounds
```

---

## 5. molt.yaml — Building a Distributable Binary

```yaml
# molt.yaml
schema_version: "1"

build:
  name: event-processor
  entry: processor.__main__
  output: dist/event-processor

  include:
    - src/processor/
    - config/

  python_version: "3.11"
  compress: true
  strip_debug: true

commands:
  consume:
    description: "Start the Kafka consumer"
    run: event-processor

  metrics:
    description: "Print current Prometheus metrics to stdout"
    run: event-processor metrics
```

### Building the binary

```bash
$ molt build
✔ Resolving dependencies...
✔ Bundling event-processor and 52 packages
✔ Bundling confluent-kafka native extension
✔ Writing dist/event-processor (31.6 MB)
```

---

## 6. Deploy to a Kafka Consumer Group

```bash
# Push binary to all consumer hosts
$ for host in consumer-01 consumer-02 consumer-03; do
    scp dist/event-processor ec2-user@${host}.internal:/opt/processor/event-processor
  done

# SSH to one node and verify
$ ssh ec2-user@consumer-01.internal

ec2-user@consumer-01:~$ /opt/processor/event-processor --version
event-processor 0.6.0

# Run as a systemd service (no Python install needed)
ec2-user@consumer-01:~$ cat /etc/systemd/system/event-processor.service
[Unit]
Description=Kafka Event Processor
After=network.target

[Service]
ExecStart=/opt/processor/event-processor
EnvironmentFile=/etc/processor/env
Restart=always
RestartSec=5
User=processor

[Install]
WantedBy=multi-user.target

ec2-user@consumer-01:~$ sudo systemctl enable event-processor
ec2-user@consumer-01:~$ sudo systemctl start event-processor
ec2-user@consumer-01:~$ sudo systemctl status event-processor
● event-processor.service — Kafka Event Processor
     Active: active (running) since Mon 2026-05-04 09:00:01 UTC; 2min ago
   Main PID: 4821

# Check Prometheus metrics
ec2-user@consumer-01:~$ curl -s localhost:8000/metrics | grep events_consumed
events_consumed_total 142948.0

# Kafka consumer group lag
$ kafka-consumer-groups.sh --bootstrap-server kafka.internal:9092 \
    --describe --group event-processors
GROUP             TOPIC       PARTITION  CURRENT-OFFSET  LOG-END-OFFSET  LAG
event-processors  raw-events  0          8492341         8492341         0
event-processors  raw-events  1          8391204         8391204         0
event-processors  raw-events  2          8512009         8512009         0
```

---

## Tips

- Set `enable.auto.commit: false` and commit offsets manually only after a successful
  ClickHouse insert — this prevents data loss if the process crashes mid-batch.
- Tune `BATCH_SIZE` based on your ClickHouse insert performance; 500–2000 rows per
  insert is a good starting range for most workloads.
- Use the Prometheus `BATCH_WRITE_DURATION` histogram to alert on p99 latency spikes
  which indicate ClickHouse back-pressure before consumer lag grows.
- For schema evolution, store the Avro schema in a Schema Registry and update your
  Pydantic models with `model_rebuild()` on schema change notifications.
- Run 3 consumer instances per partition to allow one node to restart without affecting
  throughput; Kafka's consumer group rebalance handles partition reassignment automatically.
