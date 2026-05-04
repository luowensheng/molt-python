# Django 5 Web App — cms

This walkthrough builds a Django 5 CMS with Celery background workers, S3-backed media storage via django-storages, WhiteNoise for static files, and Redis for the cache and broker. You will run it locally with molt, then ship it to a server as a single binary that automatically migrates the database and collects static files on install.

---

## 1. Project initialisation

```bash
molt init cms
cd cms
```

Add all dependencies:

```bash
molt add django gunicorn psycopg2-binary django-storages boto3 whitenoise redis celery
molt add --dev pytest-django factory-boy ruff
```

```
Resolving dependencies...
  + django 5.1.4
  + gunicorn 23.0.0
  + psycopg2-binary 2.9.10
  + django-storages 1.14.4
  + boto3 1.35.68
  + whitenoise 6.8.2
  + redis 5.2.1
  + celery 5.4.0
  + pytest-django 4.9.0 [dev]
  + factory-boy 3.3.1 [dev]
  + ruff 0.8.2 [dev]
Syncing global store...
  14 packages installed  →  ~/.molt/pkg/  (48.6 MB new)
  3 packages cached
Wrote .molt/syspath.json
Wrote .molt/bin/django-admin, .molt/bin/gunicorn, .molt/bin/celery, .molt/bin/ruff, .molt/bin/pytest
```

Scaffold the Django project:

```bash
molt run django-admin startproject cms .
molt run python manage.py startapp articles
molt run python manage.py startapp pages
```

---

## 2. Project layout

```
cms/
├── pyproject.toml
├── uv.lock
├── manage.py
├── cms/
│   ├── __init__.py
│   ├── settings/
│   │   ├── base.py
│   │   ├── local.py
│   │   └── production.py
│   ├── urls.py
│   ├── wsgi.py
│   ├── asgi.py
│   └── celery.py
├── articles/
│   ├── models.py
│   ├── views.py
│   ├── admin.py
│   └── tests/
├── pages/
│   ├── models.py
│   └── views.py
├── templates/
└── static/
```

---

## 3. pyproject.toml

```toml
[project]
name = "cms"
version = "2.0.0"
requires-python = ">=3.12"
dependencies = [
  "django>=5.1",
  "gunicorn>=23.0",
  "psycopg2-binary>=2.9",
  "django-storages[s3]>=1.14",
  "boto3>=1.35",
  "whitenoise[brotli]>=6.8",
  "redis>=5.2",
  "celery[redis]>=5.4",
]

[project.optional-dependencies]
dev = [
  "pytest-django>=4.9",
  "factory-boy>=3.3",
  "ruff>=0.8",
]

[tool.molt.tasks]
dev           = "python manage.py runserver 0.0.0.0:8000"
test          = "pytest --tb=short -x"
lint          = "ruff check ."
format        = "ruff format ."
shell         = "python manage.py shell"
migrate       = "python manage.py migrate --noinput"
collectstatic = "python manage.py collectstatic --noinput --clear"
worker        = "celery -A cms worker -l info --concurrency=4"
beat          = "celery -A cms beat -l info"

[tool.pytest.ini_options]
DJANGO_SETTINGS_MODULE = "cms.settings.local"
python_files = ["tests/*.py", "*/tests/*.py"]

[tool.ruff.lint]
select = ["E", "F", "I", "DJ"]
```

---

## 4. Key settings snippets

**`cms/settings/base.py`** (excerpt)

```python
import os

INSTALLED_APPS = [
    "django.contrib.admin",
    "django.contrib.auth",
    "django.contrib.contenttypes",
    "django.contrib.sessions",
    "django.contrib.messages",
    "whitenoise.runserver_nostatic",
    "django.contrib.staticfiles",
    "storages",
    "articles",
    "pages",
]

MIDDLEWARE = [
    "django.middleware.security.SecurityMiddleware",
    "whitenoise.middleware.WhiteNoiseMiddleware",
    # ...
]

DATABASES = {
    "default": {
        "ENGINE": "django.db.backends.postgresql",
        "NAME": os.environ["DB_NAME"],
        "USER": os.environ["DB_USER"],
        "PASSWORD": os.environ["DB_PASSWORD"],
        "HOST": os.environ.get("DB_HOST", "localhost"),
        "PORT": os.environ.get("DB_PORT", "5432"),
    }
}

CACHES = {
    "default": {
        "BACKEND": "django.core.cache.backends.redis.RedisCache",
        "LOCATION": os.environ.get("REDIS_URL", "redis://localhost:6379/1"),
    }
}

CELERY_BROKER_URL = os.environ.get("CELERY_BROKER_URL", "redis://localhost:6379/0")
CELERY_RESULT_BACKEND = CELERY_BROKER_URL

# S3 media storage in production
DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"
STATIC_ROOT = BASE_DIR / "staticfiles"
STATICFILES_STORAGE = "whitenoise.storage.CompressedManifestStaticFilesStorage"
```

---

## 5. Running in development

```bash
DJANGO_SETTINGS_MODULE=cms.settings.local molt run dev
```

```
Watching for file changes with StatReloader
Performing system checks...

System check identified no issues (0 silenced).
December 04, 2025 - 09:14:22
Django version 5.1.4, using settings 'cms.settings.local'
Starting development server at http://0.0.0.0:8000/
Quit the server with CONTROL-C.
```

Run the test suite:

```bash
molt run test
```

```
========================== test session starts ==========================
platform linux -- Python 3.12.8, pytest-8.3.4, pluggy-1.5.0
django: version: 5.1.4, settings: cms.settings.local (from ini)
collected 31 items

articles/tests/test_models.py::test_article_create PASSED
articles/tests/test_views.py::test_article_list PASSED
articles/tests/test_views.py::test_article_detail PASSED
pages/tests/test_models.py::test_page_slug_unique PASSED
...
============================== 31 passed in 3.14s ==============================
```

Apply migrations locally:

```bash
molt run migrate
```

```
Operations to perform:
  Apply all migrations: admin, articles, auth, contenttypes, pages, sessions
Running migrations:
  Applying contenttypes.0001_initial... OK
  Applying auth.0001_initial... OK
  Applying articles.0001_initial... OK
  Applying pages.0001_initial... OK
```

Start a Celery worker in a second terminal:

```bash
DJANGO_SETTINGS_MODULE=cms.settings.local molt run worker
```

```
[2025-12-04 09:20:11,824: INFO/MainProcess] Connected to redis://localhost:6379/0
[2025-12-04 09:20:11,831: INFO/MainProcess] mingle: searching for neighbors
[2025-12-04 09:20:12,849: INFO/MainProcess] celery@dev-machine ready.
```

---

## 6. molt.yaml

```yaml
version: 1

project:
  name: cms
  version: 2.0.0
  python: "3.12"
  description: "Django 5 CMS with Celery workers and S3 storage"

deps:
  strategy: pyproject

include:
  - "cms/**/*.py"
  - "articles/**/*.py"
  - "pages/**/*.py"
  - "templates/**/*"
  - "static/**/*"
  - "manage.py"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "staticfiles/"
  - "*/tests/"
  - ".env"

commands:
  default: web

  web:
    exec:
      - gunicorn
      - "cms.wsgi:application"
      - "--bind=0.0.0.0:8000"
      - "--workers=4"
      - "--timeout=120"
      - "--access-logfile=-"
    description: "Gunicorn WSGI server"
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

  worker:
    exec: [celery, "-A", "cms", "worker", "-l", "info", "--concurrency=4"]
    description: "Celery task worker"
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

  beat:
    exec: [celery, "-A", "cms", "beat", "-l", "info", "--scheduler=django_celery_beat.schedulers:DatabaseScheduler"]
    description: "Celery periodic task scheduler"
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

  migrate:
    exec: [python, manage.py, migrate, --noinput]
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

  collectstatic:
    exec: [python, manage.py, collectstatic, --noinput, --clear]
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

  shell:
    exec: [python, manage.py, shell]
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

  createsuperuser:
    exec: [python, manage.py, createsuperuser]
    env:
      DJANGO_SETTINGS_MODULE: "cms.settings.production"

env:
  PYTHONUNBUFFERED: "1"
  DJANGO_SETTINGS_MODULE: "cms.settings.production"

hooks:
  post_install:
    - "python manage.py migrate --noinput"
    - "python manage.py collectstatic --noinput --clear"

integrity:
  verify_on_install: true
```

---

## 7. Building

```bash
molt build
```

```
Building cms v2.0.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 58.3 MB (2148 files)
  ✓ Integrity manifest: cms-v2.0.0.manifest.json
  ✓ Created: cms-v2.0.0 (84.1 MB)
    root_hash: 7a3c9e12f56d8b4a21c7e9f30d82b6a5c14e87f2d9b3a0c6e4f82d1b7a9c3e5
    files:     2148   total: 58.3 MB
    build time: 6.1s
```

---

## 8. Deploy to a Linux server

Copy to the server:

```bash
scp cms-v2.0.0 cms-v2.0.0.manifest.json deploy@prod-01:/opt/deploys/
```

On the server:

```bash
ssh deploy@prod-01

export DB_NAME=cms_prod
export DB_USER=cms
export DB_PASSWORD="$(cat /run/secrets/db_password)"
export DB_HOST=db.internal
export REDIS_URL=redis://redis.internal:6379/0
export CELERY_BROKER_URL=redis://redis.internal:6379/0
export SECRET_KEY="$(cat /run/secrets/django_secret)"
export AWS_ACCESS_KEY_ID="$(cat /run/secrets/aws_key)"
export AWS_SECRET_ACCESS_KEY="$(cat /run/secrets/aws_secret)"
export AWS_STORAGE_BUCKET_NAME=cms-media-prod

/opt/deploys/cms-v2.0.0 install
```

```
Installing cms v2.0.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (2148 files)...
  Setting up Python environment...
  Running post_install hooks:
    [1/2] python manage.py migrate --noinput
          Operations to perform: Apply all migrations
          Running migrations: 14 applied.
    [2/2] python manage.py collectstatic --noinput --clear
          131 static files copied to '/home/deploy/.molt/apps/cms/2.0.0/staticfiles'.
  ✓ Installed to /home/deploy/.molt/apps/cms/2.0.0/
```

Start all three processes (e.g. via systemd):

```bash
# web server
cms-v2.0.0 run web

# worker (separate systemd unit)
cms-v2.0.0 run worker

# beat scheduler (separate systemd unit)
cms-v2.0.0 run beat
```

---

## 9. Systemd units

**`/etc/systemd/system/cms-web.service`**

```ini
[Unit]
Description=CMS Gunicorn web server
After=network.target postgresql.service

[Service]
User=deploy
EnvironmentFile=/etc/cms/env
ExecStart=/opt/deploys/cms-v2.0.0 run web
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**`/etc/systemd/system/cms-worker.service`**

```ini
[Unit]
Description=CMS Celery worker
After=network.target redis.service

[Service]
User=deploy
EnvironmentFile=/etc/cms/env
ExecStart=/opt/deploys/cms-v2.0.0 run worker
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now cms-web cms-worker cms-beat
systemctl status cms-web
```

---

## 10. Tips

- **Django SECRET_KEY**: never embed it in the binary. Pass via environment variable. The sensitive-file check in `molt build` blocks `.env.local` and similar files from accidentally shipping.
- **Static files in production**: `collectstatic` runs automatically in the `post_install` hook. WhiteNoise serves them directly from gunicorn — no nginx needed for a simple deployment.
- **Multi-worker scaling**: deploy the same binary to multiple servers. Each calls `install` once; workers and web servers all read from the same database and Redis. The binary is the same artifact on every host — root hash guarantees it.
- **Celery beat in HA**: run exactly one `beat` process across the cluster. Use `django-celery-beat` with the database scheduler so the beat schedule survives restarts.
- **Pinning the Python version**: `molt python use 3.12 && molt sync` before building guarantees the binary ships with exactly the ABI you tested against. The `.python-version` file is read by `molt build` automatically.
