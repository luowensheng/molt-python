# RQ Worker — Background Job System

This walkthrough builds `job-runner`, a background job processor built on RQ (Redis Queue). RQ is a lightweight alternative to Celery — jobs are plain Python functions, queues are named Redis lists, and the worker is a single long-running process. This project includes a scheduler (for delayed and recurring jobs), the RQ Dashboard for real-time visibility, and uses Rich for colorful local log output and Structlog for structured JSON logs in production.

---

## 1. Project Init and molt sync

```
$ mkdir job-runner && cd job-runner
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  rq==1.16.2
  redis==5.0.3
  rich==13.7.1
  structlog==24.1.0
  rq-scheduler==0.13.1
  rq-dashboard==0.6.1
  ...
  Locked 21 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "job-runner"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "rq>=1.16",
    "redis>=5.0",
    "rich>=13.7",
    "structlog>=24.1",
    "rq-scheduler>=0.13",
    "rq-dashboard>=0.6",
]

[project.scripts]
job-runner = "job_runner.cli:main"

[tool.molt.tasks]
worker    = "rq worker default high low --with-scheduler --url redis://localhost:6379"
scheduler = "rqscheduler --host localhost --port 6379 --db 0 --verbose"
dashboard = "rq-dashboard --redis-url redis://localhost:6379 --port 9181"
```

---

## 3. Project Layout

```
job_runner/
├── cli.py             # Click CLI wrapping rq worker / dashboard / scheduler
├── jobs/
│   ├── __init__.py
│   ├── emails.py      # send_email, send_digest
│   ├── exports.py     # export_csv, export_pdf
│   └── cleanup.py     # purge_old_sessions, vacuum_logs
├── queues.py          # Queue definitions + enqueue helpers
└── logging.py         # Structlog / Rich configuration
```

**job_runner/queues.py**
```python
from redis import Redis
from rq import Queue

redis_conn = Redis.from_url("redis://localhost:6379")

high   = Queue("high",    connection=redis_conn)
default = Queue("default", connection=redis_conn)
low    = Queue("low",     connection=redis_conn, default_timeout=600)


def enqueue_email(to: str, template: str, context: dict):
    return default.enqueue(
        "job_runner.jobs.emails.send_email",
        to, template, context,
        retry=Retry(max=3, interval=[10, 30, 60]),
    )
```

**job_runner/jobs/emails.py**
```python
import structlog
from job_runner.logging import get_logger

log = get_logger(__name__)

def send_email(to: str, template: str, context: dict) -> dict:
    log.info("sending_email", to=to, template=template)
    # ... render template, call SMTP/SES
    log.info("email_sent", to=to, message_id="msg-abc123")
    return {"status": "sent", "to": to}
```

**job_runner/logging.py**
```python
import structlog, sys
from rich.logging import RichHandler

def configure_logging(json: bool = False):
    if json:
        structlog.configure(
            processors=[
                structlog.processors.TimeStamper(fmt="iso"),
                structlog.processors.JSONRenderer(),
            ]
        )
    else:
        import logging
        logging.basicConfig(handlers=[RichHandler(rich_tracebacks=True)], level="INFO")
        structlog.configure(processors=[structlog.dev.ConsoleRenderer()])

def get_logger(name: str):
    return structlog.get_logger(name)
```

---

## 4. Running Locally

```
$ molt run worker
09:00:01 INFO     Worker rq:worker:dev-mac.1234 started
09:00:01 INFO     Listening on queues: default, high, low
09:00:01 INFO     With scheduler: True

# In a second terminal, enqueue a test job
$ python -c "
from job_runner.queues import enqueue_email
job = enqueue_email('alice@example.com', 'welcome', {'name': 'Alice'})
print('Enqueued:', job.id)
"
Enqueued: 3f7a9c12-...

# Back in the worker terminal
09:00:14 INFO     job_runner.jobs.emails  sending_email  to=alice@example.com template=welcome
09:00:14 INFO     job_runner.jobs.emails  email_sent     to=alice@example.com message_id=msg-abc123
09:00:14 INFO     Job OK (0.042s)
```

Open the dashboard:

```
$ molt run dashboard
 * Running on http://0.0.0.0:9181/ (Press CTRL+C to quit)
```

The dashboard at [http://localhost:9181](http://localhost:9181) shows queue depths, worker status, job history, and failed job details with full tracebacks.

Start the scheduler for cron-style jobs:

```
$ molt run scheduler
2024-05-04 09:01:00,000 - rqscheduler - INFO - Checking for scheduled jobs...
2024-05-04 09:01:00,002 - rqscheduler - INFO - Enqueueing job_runner.jobs.cleanup.purge_old_sessions
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: job-runner
  entry: job_runner.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: worker
      args: ["worker"]
      description: "Start RQ worker (queues: default, high, low)"
    - name: scheduler
      args: ["scheduler"]
      description: "Start the RQ Scheduler for periodic jobs"
    - name: dashboard
      args: ["dashboard"]
      description: "Start the RQ Dashboard on port 9181"

  env:
    REDIS_URL: "redis://redis:6379/0"
    LOG_FORMAT: "json"
    WORKER_CONCURRENCY: "4"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling job_runner + 21 dependencies...
  Output: dist/job-runner  (14.1 MB)

$ ls -lh dist/
-rwxr-xr-x  1 user  staff  14M May  4 09:15 job-runner
```

---

## 6. Deploy to Another Machine

```
$ scp dist/job-runner deploy@jobs-01.prod:/opt/job-runner/
job-runner                            100%   14MB  38.7MB/s   00:00
```

```
deploy@jobs-01$ export REDIS_URL=redis://redis.internal:6379/0
deploy@jobs-01$ export LOG_FORMAT=json

# Start three worker processes for throughput
deploy@jobs-01$ /opt/job-runner/job-runner worker &
deploy@jobs-01$ /opt/job-runner/job-runner worker &
deploy@jobs-01$ /opt/job-runner/job-runner worker &

[1] INFO {"event": "worker_started", "queues": ["default", "high", "low"]}
[2] INFO {"event": "worker_started", "queues": ["default", "high", "low"]}
[3] INFO {"event": "worker_started", "queues": ["default", "high", "low"]}

# One scheduler instance per cluster
deploy@jobs-02$ /opt/job-runner/job-runner scheduler

# Dashboard behind nginx
deploy@jobs-01$ /opt/job-runner/job-runner dashboard -- --port=9181
```

### supervisord config

```ini
[program:job-runner-worker]
command=/opt/job-runner/job-runner worker
numprocs=4
process_name=%(program_name)s_%(process_num)02d
autostart=true
autorestart=true
environment=REDIS_URL="redis://redis.internal:6379/0",LOG_FORMAT="json"
stdout_logfile=/var/log/job-runner/worker.log
stderr_logfile=/var/log/job-runner/worker-err.log

[program:job-runner-scheduler]
command=/opt/job-runner/job-runner scheduler
autostart=true
autorestart=true
environment=REDIS_URL="redis://redis.internal:6379/0",LOG_FORMAT="json"
stdout_logfile=/var/log/job-runner/scheduler.log
```

```
$ supervisorctl reread && supervisorctl update
job-runner-worker: added process group
job-runner-scheduler: added process group
$ supervisorctl start all
job-runner-worker_00: started
job-runner-worker_01: started
job-runner-worker_02: started
job-runner-worker_03: started
job-runner-scheduler: started
```

---

## Tips

- **Queue priority**: Enqueue urgent jobs to the `high` queue, expensive exports to `low`. The worker drains `high` before touching `low`.
- **Job timeouts**: Set `job_timeout` per job or per queue. The default is 180 s — exports can take longer, so configure `low` with `default_timeout=600`.
- **Failed job retention**: By default failed jobs stay in Redis forever. Set `failure_ttl` on the queue or call `failed_job_registry.requeue(job_id)` after fixing a bug.
- **Burst mode**: For CI or lambda-style invocations, pass `--burst` to the worker; it exits once the queue is empty.
- **Structured logs in prod**: Set `LOG_FORMAT=json` and pipe to your log aggregator. Structlog adds `job_id`, `queue`, and `duration_ms` fields automatically.
- **Binary size**: The dashboard (Flask + assets) adds ~4 MB. Omit `rq-dashboard` from deps and remove the `dashboard` task if you don't need it.
