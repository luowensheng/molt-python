# Celery Worker — Async Task Processing

This walkthrough builds `task-worker`, a standalone Celery worker that handles three classes of background jobs: image resizing (via Pillow), transactional emails (via Jinja2 + Premailer), and report generation that uploads results to S3 (via Boto3). Redis is used as both the broker and result backend. The worker, beat scheduler, and Flower monitoring UI are each runnable as named molt tasks, and the finished project is packaged into a self-contained binary for deployment to a Linux server.

---

## 1. Project Init and molt sync

```
$ mkdir task-worker && cd task-worker
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  celery[redis]==5.3.6
  redis==5.0.3
  pillow==10.3.0
  boto3==1.34.84
  jinja2==3.1.3
  premailer==3.10.0
  ...
  Locked 47 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "task-worker"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "celery[redis]>=5.3",
    "redis>=5.0",
    "pillow>=10.3",
    "boto3>=1.34",
    "jinja2>=3.1",
    "premailer>=3.10",
]

[project.scripts]
task-worker = "task_worker.cli:main"

[tool.molt.tasks]
worker   = "celery -A task_worker.celery_app worker --loglevel=info --concurrency=4"
beat     = "celery -A task_worker.celery_app beat --loglevel=info --scheduler django_celery_beat.schedulers:DatabaseScheduler"
flower   = "celery -A task_worker.celery_app flower --port=5555 --basic_auth=admin:secret"
purge    = "celery -A task_worker.celery_app purge -f"
```

---

## 3. Project Layout

```
task_worker/
├── celery_app.py      # Celery application factory
├── tasks/
│   ├── __init__.py
│   ├── images.py      # resize_image, generate_thumbnail
│   ├── emails.py      # send_welcome_email, send_invoice
│   └── reports.py     # generate_monthly_report, upload_to_s3
├── templates/
│   ├── welcome.html
│   └── invoice.html
└── cli.py
```

**task_worker/celery_app.py**
```python
from celery import Celery

app = Celery("task_worker")
app.config_from_object("task_worker.celeryconfig")
app.autodiscover_tasks(["task_worker.tasks"])
```

**task_worker/tasks/images.py**
```python
from celery import shared_task
from PIL import Image
import io, boto3

@shared_task(bind=True, max_retries=3)
def resize_image(self, s3_key: str, width: int, height: int) -> str:
    s3 = boto3.client("s3")
    obj = s3.get_object(Bucket="my-bucket", Key=s3_key)
    img = Image.open(io.BytesIO(obj["Body"].read()))
    img.thumbnail((width, height), Image.LANCZOS)
    buf = io.BytesIO()
    img.save(buf, format=img.format or "JPEG")
    out_key = f"resized/{width}x{height}/{s3_key}"
    s3.put_object(Bucket="my-bucket", Key=out_key, Body=buf.getvalue())
    return out_key
```

---

## 4. Running Tasks Locally

Start Redis first, then run each process in separate terminals:

```
$ molt run worker
[2024-05-04 09:00:01,234: INFO/MainProcess] Connected to redis://localhost:6379//
[2024-05-04 09:00:01,241: INFO/MainProcess] mingle: searching for neighbors
[2024-05-04 09:00:02,265: INFO/MainProcess] celery@dev-mac ready.

$ molt run beat
[2024-05-04 09:00:05,112: INFO/MainProcess] beat: Starting...
[2024-05-04 09:00:05,114: INFO/MainProcess] Scheduler: Sending due task generate-monthly-report (task_worker.tasks.reports.generate_monthly_report)

$ molt run flower
[I 240504 09:00:10 command:139] Inspecting nodes...
[I 240504 09:00:10 command:152] celery@dev-mac: OK
[I 240504 09:00:10 app:341] Broker: redis://localhost:6379//
[I 240504 09:00:10 app:342] Registered tasks:
    . task_worker.tasks.emails.send_welcome_email
    . task_worker.tasks.emails.send_invoice
    . task_worker.tasks.images.resize_image
    . task_worker.tasks.images.generate_thumbnail
    . task_worker.tasks.reports.generate_monthly_report
[I 240504 09:00:10 app:342] Listening on: http://0.0.0.0:5555/
```

Open [http://localhost:5555](http://localhost:5555) to see the Flower dashboard.

To purge all queued tasks during development:

```
$ molt run purge
WARNING: This will remove all tasks from the queue!
Are you sure you want to do this? (yes/no): yes
Purged 12 messages from 1 known task queue.
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: task-worker
  entry: task_worker.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: worker
      args: ["worker"]
      description: "Start the Celery worker process"
    - name: beat
      args: ["beat"]
      description: "Start the Celery beat scheduler"
    - name: flower
      args: ["flower"]
      description: "Start the Flower monitoring UI"
    - name: purge
      args: ["purge"]
      description: "Purge all queued tasks"

  env:
    CELERY_BROKER_URL: "redis://redis:6379/0"
    CELERY_RESULT_BACKEND: "redis://redis:6379/1"
    AWS_DEFAULT_REGION: "us-east-1"
```

Build the binary:

```
$ molt build
  Resolving platform: linux/amd64
  Bundling task_worker + 47 dependencies...
  Compiling bootstrap...
  Output: dist/task-worker  (28.4 MB)
  SHA256: a3f8c21...

$ ls -lh dist/
-rwxr-xr-x  1 user  staff  28M May  4 09:15 task-worker
```

---

## 6. Deploy to Another Machine

Copy the single binary to the production server:

```
$ scp dist/task-worker deploy@worker-01.prod:/opt/task-worker/
task-worker                           100%   28MB  42.1MB/s   00:00
```

Set environment variables and run:

```
deploy@worker-01$ export CELERY_BROKER_URL=redis://redis.internal:6379/0
deploy@worker-01$ export CELERY_RESULT_BACKEND=redis://redis.internal:6379/1
deploy@worker-01$ export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE
deploy@worker-01$ export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY

# Start worker (4 processes)
deploy@worker-01$ /opt/task-worker/task-worker worker -- --concurrency=4
[2024-05-04 11:00:01,234: INFO/MainProcess] Connected to redis://redis.internal:6379/0
[2024-05-04 11:00:01,241: INFO/MainProcess] celery@worker-01 ready.

# Start beat on a second server (only one beat per cluster)
deploy@worker-02$ /opt/task-worker/task-worker beat
[2024-05-04 11:00:03,112: INFO/MainProcess] beat: Starting...

# Start flower for monitoring
deploy@worker-03$ /opt/task-worker/task-worker flower -- --port=5555
[I 240504 11:00:05 app:342] Listening on: http://0.0.0.0:5555/
```

### systemd unit (worker-01)

```ini
[Unit]
Description=task-worker Celery Worker
After=network.target

[Service]
User=deploy
EnvironmentFile=/etc/task-worker/env
ExecStart=/opt/task-worker/task-worker worker -- --concurrency=4 --loglevel=warning
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```
$ sudo systemctl enable --now task-worker
$ sudo systemctl status task-worker
● task-worker.service - task-worker Celery Worker
     Loaded: loaded (/etc/systemd/system/task-worker.service; enabled)
     Active: active (running) since Sun 2024-05-04 11:00:10 UTC; 2min ago
```

---

## Tips

- **Concurrency**: For I/O-bound tasks (HTTP calls, S3 uploads), use `--concurrency=16 --pool=gevent`. For CPU-bound tasks (image resizing), stick with prefork and set concurrency to CPU count.
- **Retries**: Always set `max_retries` and use exponential backoff: `self.retry(exc=exc, countdown=2 ** self.request.retries)`.
- **Task routing**: Route heavy tasks to a dedicated queue so image jobs don't block email sends. Define `task_routes` in celeryconfig.py.
- **Flower auth**: The binary bakes in the `--basic_auth` flag. Rotate credentials by rebuilding with an updated `molt.yaml` env block or pass via env var override.
- **One beat per cluster**: Running multiple beat processes will cause duplicate task scheduling. Use a distributed lock (Redbeat) for HA setups.
- **Binary size**: Pillow adds ~20 MB. If you don't need all image formats, set `PILLOW_SIMD` or exclude unused codecs in molt.yaml's `exclude` list.
