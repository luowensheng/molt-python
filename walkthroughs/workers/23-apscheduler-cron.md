# APScheduler — Periodic Task Runner

This walkthrough builds `scheduler`, a standalone process that runs recurring maintenance jobs: nightly database cleanup, weekly report emails via SendGrid, and hourly cache warming for a Redis-backed API. APScheduler stores job state in a SQLAlchemy-backed job store so schedules survive restarts. HTTPX makes outbound HTTP calls for cache priming. Jinja2 renders HTML email templates before they are delivered via SendGrid.

---

## 1. Project Init and molt sync

```
$ mkdir scheduler && cd scheduler
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  apscheduler==3.10.4
  sqlalchemy==2.0.29
  redis==5.0.3
  httpx==0.27.0
  jinja2==3.1.3
  sendgrid==6.11.0
  ...
  Locked 28 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "scheduler"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "apscheduler>=3.10",
    "sqlalchemy>=2.0",
    "redis>=5.0",
    "httpx>=0.27",
    "jinja2>=3.1",
    "sendgrid>=6.11",
]

[project.scripts]
scheduler = "scheduler.cli:main"

[tool.molt.tasks]
start     = "python -m scheduler.main"
test      = "python -m pytest tests/ -v"
list-jobs = "python -m scheduler.cli list-jobs"
```

---

## 3. Project Layout

```
scheduler/
├── main.py            # Scheduler setup + blocking start
├── cli.py             # Click CLI: start, list-jobs, trigger
├── jobs/
│   ├── __init__.py
│   ├── cleanup.py     # purge_old_sessions, vacuum_audit_log
│   ├── reports.py     # send_weekly_digest, send_monthly_summary
│   └── cache.py       # warm_product_cache, warm_search_index
├── config.py          # Pydantic settings
└── templates/
    ├── weekly_digest.html
    └── monthly_summary.html
```

**scheduler/main.py**
```python
import logging
from apscheduler.schedulers.blocking import BlockingScheduler
from apscheduler.jobstores.sqlalchemy import SQLAlchemyJobStore
from apscheduler.executors.pool import ThreadPoolExecutor
from scheduler.config import settings
from scheduler.jobs import cleanup, reports, cache

logging.basicConfig(level=logging.INFO)

jobstores = {
    "default": SQLAlchemyJobStore(url=settings.database_url)
}
executors = {
    "default": ThreadPoolExecutor(max_workers=4),
}

scheduler = BlockingScheduler(jobstores=jobstores, executors=executors)

# Hourly cache warming
scheduler.add_job(cache.warm_product_cache, "interval", hours=1,
                  id="warm_product_cache", replace_existing=True)
scheduler.add_job(cache.warm_search_index, "cron", minute="*/30",
                  id="warm_search_index", replace_existing=True)

# Nightly cleanup at 02:00 UTC
scheduler.add_job(cleanup.purge_old_sessions, "cron", hour=2, minute=0,
                  id="purge_old_sessions", replace_existing=True)
scheduler.add_job(cleanup.vacuum_audit_log, "cron", hour=2, minute=15,
                  id="vacuum_audit_log", replace_existing=True)

# Weekly report every Monday at 08:00 UTC
scheduler.add_job(reports.send_weekly_digest, "cron",
                  day_of_week="mon", hour=8, minute=0,
                  id="weekly_digest", replace_existing=True)

if __name__ == "__main__":
    print("Starting scheduler...")
    scheduler.start()
```

**scheduler/jobs/cache.py**
```python
import httpx
import redis as redislib
from scheduler.config import settings

def warm_product_cache():
    r = redislib.from_url(settings.redis_url)
    with httpx.Client(base_url=settings.api_base_url, timeout=30) as client:
        pages = client.get("/products?page_size=200").json()
        r.setex("products:warm", 7200, str(pages))
    print(f"[cache] warmed {len(pages)} products")

def warm_search_index():
    with httpx.Client(base_url=settings.api_base_url, timeout=30) as client:
        client.post("/search/rebuild-index")
    print("[cache] search index rebuild triggered")
```

**scheduler/config.py**
```python
from pydantic_settings import BaseSettings

class Settings(BaseSettings):
    database_url: str = "sqlite:///scheduler.db"
    redis_url: str = "redis://localhost:6379/0"
    api_base_url: str = "http://localhost:8000"
    sendgrid_api_key: str = ""
    report_recipient: str = "team@example.com"

    class Config:
        env_file = ".env"

settings = Settings()
```

---

## 4. Running Locally

```
$ molt run start
Starting scheduler...
INFO:apscheduler.scheduler:Scheduler started
INFO:apscheduler.executors.default:Running job "warm_product_cache" (scheduled at 2024-05-04 09:00:00)
[cache] warmed 182 products
INFO:apscheduler.executors.default:Job "warm_product_cache" executed successfully
```

List all registered jobs:

```
$ molt run list-jobs
Job ID                  Next Run                     Trigger
----------------------  ---------------------------  --------------------------
warm_product_cache      2024-05-04 10:00:00 UTC      interval[1:00:00]
warm_search_index       2024-05-04 09:30:00 UTC      cron[*/30 * * * *]
purge_old_sessions      2024-05-05 02:00:00 UTC      cron[0 2 * * *]
vacuum_audit_log        2024-05-05 02:15:00 UTC      cron[15 2 * * *]
weekly_digest           2024-05-06 08:00:00 UTC      cron[0 8 * * mon]
```

Run tests:

```
$ molt run test
========================= test session starts ==========================
platform darwin -- Python 3.12.3 -- pytest-8.1.1
collected 8 items

tests/test_cleanup.py::test_purge_old_sessions PASSED
tests/test_cleanup.py::test_vacuum_audit_log   PASSED
tests/test_cache.py::test_warm_product_cache   PASSED
tests/test_cache.py::test_warm_search_index    PASSED
tests/test_reports.py::test_weekly_digest_renders  PASSED
tests/test_reports.py::test_weekly_digest_sends    PASSED
tests/test_jobs.py::test_all_jobs_registered    PASSED
tests/test_jobs.py::test_job_schedules_correct  PASSED

========================= 8 passed in 1.34s ==========================
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: scheduler
  entry: scheduler.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: start
      args: ["start"]
      description: "Start the APScheduler process"
    - name: list-jobs
      args: ["list-jobs"]
      description: "Print all registered jobs and next run times"

  env:
    DATABASE_URL: "postgresql+psycopg2://scheduler:secret@db:5432/scheduler"
    REDIS_URL: "redis://redis:6379/0"
    API_BASE_URL: "http://api:8000"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling scheduler + 28 dependencies...
  Output: dist/scheduler  (18.9 MB)
```

---

## 6. Deploy to Another Machine

```
$ scp dist/scheduler deploy@cron-01.prod:/opt/scheduler/
scheduler                             100%   19MB  35.2MB/s   00:00
```

```
deploy@cron-01$ cat /etc/scheduler/env
DATABASE_URL=postgresql+psycopg2://scheduler:secret@db.internal:5432/scheduler
REDIS_URL=redis://redis.internal:6379/0
API_BASE_URL=http://api.internal:8000
SENDGRID_API_KEY=SG.xxxx
REPORT_RECIPIENT=team@example.com

deploy@cron-01$ /opt/scheduler/scheduler start
Starting scheduler...
INFO:apscheduler.scheduler:Scheduler started
INFO:apscheduler.executors.default:Running job "warm_product_cache" ...
```

### systemd unit

```ini
[Unit]
Description=APScheduler periodic task runner
After=network.target

[Service]
User=deploy
EnvironmentFile=/etc/scheduler/env
ExecStart=/opt/scheduler/scheduler start
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

```
$ sudo systemctl enable --now scheduler
$ sudo systemctl status scheduler
● scheduler.service - APScheduler periodic task runner
     Active: active (running) since Sun 2024-05-04 11:00:00 UTC; 5s ago
```

---

## Tips

- **SQLite vs Postgres**: SQLite is fine for single-server setups. For HA deployments (multiple replicas), use a PostgreSQL job store and a distributed lock (`SQLAlchemyJobStore` with a Postgres URL) so only one instance fires each job.
- **Missed job policy**: Set `misfire_grace_time=300` on critical jobs so a brief restart doesn't cause them to be skipped.
- **Max instances**: Set `max_instances=1` on cleanup jobs to prevent overlap if a run exceeds its interval.
- **Testing jobs in isolation**: Call job functions directly in tests — they are plain Python functions. Use `respx` to mock HTTPX calls and `fakeredis` for Redis.
- **Alerting on failures**: Wrap job functions in a try/except and call a `notify_failure(job_id, exc)` helper that posts to Slack or PagerDuty.
- **Dynamic job management**: APScheduler supports `scheduler.modify_job`, `scheduler.pause_job`, and `scheduler.remove_job` at runtime. Expose these via a small FastAPI admin endpoint for operators.
