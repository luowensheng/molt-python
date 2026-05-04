# Flask Microservice — auth-service

This walkthrough builds a stateless JWT authentication microservice using Flask, Flask-JWT-Extended, SQLAlchemy, Alembic, and Redis for token revocation. The service issues access tokens and refresh tokens, stores user records in PostgreSQL, and maintains a Redis revocation list. You will run it locally, then ship it as a single hermetic binary deployable to any Linux server — including Docker-like single-binary container alternatives.

---

## 1. Project initialisation

```bash
molt init auth-service
cd auth-service
```

```
Initialized auth-service
  pyproject.toml
  uv.lock
  .molt/syspath.json
```

Add dependencies:

```bash
molt add flask flask-jwt-extended redis sqlalchemy alembic gunicorn
molt add --dev pytest ruff httpx
```

```
Resolving dependencies...
  + flask 3.1.0
  + flask-jwt-extended 4.7.1
  + redis 5.2.1
  + sqlalchemy 2.0.36
  + alembic 1.14.0
  + gunicorn 23.0.0
  + pytest 8.3.4 [dev]
  + ruff 0.8.2 [dev]
  + httpx 0.28.0 [dev]
Syncing global store...
  9 packages installed  →  ~/.molt/pkg/  (22.4 MB)
Wrote .molt/syspath.json
Wrote .molt/bin/flask, .molt/bin/gunicorn, .molt/bin/alembic
```

---

## 2. Project layout

```
auth-service/
├── pyproject.toml
├── uv.lock
├── alembic.ini
├── alembic/
│   ├── env.py
│   └── versions/
├── auth_service/
│   ├── __init__.py
│   ├── app.py
│   ├── config.py
│   ├── database.py
│   ├── models.py
│   └── routes/
│       ├── auth.py
│       └── health.py
└── tests/
    ├── conftest.py
    └── test_auth.py
```

---

## 3. pyproject.toml

```toml
[project]
name = "auth-service"
version = "0.4.0"
requires-python = ">=3.12"
dependencies = [
  "flask>=3.1",
  "flask-jwt-extended>=4.7",
  "redis>=5.2",
  "sqlalchemy>=2.0",
  "alembic>=1.14",
  "gunicorn>=23.0",
]

[project.optional-dependencies]
dev = [
  "pytest>=8.3",
  "ruff>=0.8",
  "httpx>=0.28",
]

[tool.molt.tasks]
dev     = "flask --app auth_service.app run --reload --port 5000"
test    = "pytest tests/ -v --tb=short"
lint    = "ruff check ."
migrate = "alembic upgrade head"

[tool.ruff.lint]
select = ["E", "F", "I", "UP"]
```

---

## 4. Application code

**`auth_service/app.py`**

```python
import os
from flask import Flask
from flask_jwt_extended import JWTManager
from auth_service.database import db
from auth_service.routes.auth import auth_bp
from auth_service.routes.health import health_bp

def create_app(config_override: dict | None = None) -> Flask:
    app = Flask(__name__)
    app.config.update(
        SQLALCHEMY_DATABASE_URI=os.environ.get(
            "DATABASE_URL", "postgresql://auth:secret@localhost:5432/auth"
        ),
        JWT_SECRET_KEY=os.environ["JWT_SECRET_KEY"],
        JWT_ACCESS_TOKEN_EXPIRES=900,   # 15 minutes
        JWT_REFRESH_TOKEN_EXPIRES=86400,  # 24 hours
    )
    if config_override:
        app.config.update(config_override)

    db.init_app(app)
    JWTManager(app)

    app.register_blueprint(auth_bp, url_prefix="/auth")
    app.register_blueprint(health_bp)
    return app

app = create_app()
```

**`auth_service/routes/auth.py`** (excerpt)

```python
from flask import Blueprint, request, jsonify
from flask_jwt_extended import (
    create_access_token, create_refresh_token,
    jwt_required, get_jwt_identity, get_jwt,
)
from auth_service.models import User
from auth_service.database import db
import redis, os

auth_bp = Blueprint("auth", __name__)
_redis = redis.from_url(os.environ.get("REDIS_URL", "redis://localhost:6379/0"))

@auth_bp.post("/login")
def login():
    data = request.get_json()
    user = User.query.filter_by(email=data["email"]).first()
    if not user or not user.check_password(data["password"]):
        return jsonify(msg="Bad credentials"), 401
    return jsonify(
        access_token=create_access_token(identity=user.id),
        refresh_token=create_refresh_token(identity=user.id),
    )

@auth_bp.post("/logout")
@jwt_required()
def logout():
    jti = get_jwt()["jti"]
    _redis.setex(f"revoked:{jti}", 900, "1")
    return jsonify(msg="token revoked")

@auth_bp.get("/me")
@jwt_required()
def me():
    user = User.query.get(get_jwt_identity())
    return jsonify(id=user.id, email=user.email)
```

---

## 5. Running in development

```bash
JWT_SECRET_KEY=dev-secret molt run dev
```

```
 * Serving Flask app 'auth_service.app'
 * Debug mode: off
 * Running on http://127.0.0.1:5000
Press CTRL+C to quit
 * Restarting with stat
 * Debugger is active!
```

Run tests:

```bash
molt run test
```

```
========================= test session starts ==========================
platform linux -- Python 3.12.8
collected 12 items

tests/test_auth.py::test_login_success PASSED
tests/test_auth.py::test_login_bad_password PASSED
tests/test_auth.py::test_me_requires_auth PASSED
tests/test_auth.py::test_refresh_token PASSED
tests/test_auth.py::test_logout_revokes_token PASSED
...
============================== 12 passed in 0.94s ==============================
```

Apply migrations:

```bash
DATABASE_URL=postgresql://auth:secret@localhost/auth molt run migrate
```

```
INFO  [alembic.runtime.migration] Running upgrade  -> 1a2b3c4d5e6f, create users table
INFO  [alembic.runtime.migration] Running upgrade 1a2b3c4d5e6f -> 2b3c4d5e6f7a, add revoked_tokens
```

---

## 6. molt.yaml

```yaml
version: 1

project:
  name: auth-service
  version: 0.4.0
  python: "3.12"
  description: "Stateless JWT authentication microservice"

deps:
  strategy: pyproject

include:
  - "auth_service/**/*.py"
  - "alembic/**/*.py"
  - "alembic.ini"

exclude:
  - "**/__pycache__/"
  - "**/*.pyc"
  - "tests/"

commands:
  default: serve

  serve:
    exec:
      - gunicorn
      - "auth_service.app:app"
      - "--bind=0.0.0.0:5000"
      - "--workers=2"
      - "--timeout=30"
    description: "Gunicorn WSGI server"

  migrate:
    exec: [alembic, upgrade, head]
    description: "Run Alembic database migrations"

  check:
    script: "curl -sf http://localhost:5000/health"
    description: "Health check against a running instance"

env:
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "alembic upgrade head"

integrity:
  verify_on_install: true
  verify_on_launch: true
```

---

## 7. Building

```bash
molt build
```

```
Building auth-service v0.4.0 (linux/amd64)...
  Config:    ./molt.yaml
  ✓ Payload: 18.7 MB (521 files)
  ✓ Integrity manifest: auth-service-v0.4.0.manifest.json
  ✓ Created: auth-service-v0.4.0 (34.2 MB)
    root_hash: 4d8f2a1c9e7b3d05f82c1a4b6e9d3f7a2c5b8e1d4f7a0b3c6e9d2f5a8b1c4e7
    files:     521    total: 18.7 MB
    build time: 2.9s
```

Verify the binary before shipping:

```bash
molt verify-binary ./auth-service-v0.4.0
```

```
✓ Binary integrity verified.
  root_hash embedded in trailer matches manifest.
```

---

## 8. Deploy to a Linux server (Docker-like single-binary approach)

This binary is self-contained — no Docker daemon, no runtime install. Copy and run:

```bash
# From your local machine
scp auth-service-v0.4.0 auth-service-v0.4.0.manifest.json app@auth-server-01:/opt/services/

ssh app@auth-server-01
```

```bash
# Install once per version — verifies integrity, runs migrations
DATABASE_URL="postgresql://auth:$(cat /run/secrets/db_pass)@db.internal:5432/auth" \
JWT_SECRET_KEY="$(cat /run/secrets/jwt_secret)" \
REDIS_URL="redis://redis.internal:6379/0" \
  /opt/services/auth-service-v0.4.0 install
```

```
Installing auth-service v0.4.0...
  Verifying integrity...  ✓ root_hash match
  Extracting payload (521 files)...
  Setting up Python environment...
  Running post_install hooks:
    [1/1] alembic upgrade head
          INFO  Running upgrade  -> 1a2b3c4d5e6f, create users table
  ✓ Installed to /home/app/.molt/apps/auth-service/0.4.0/
```

```bash
# Start the service
DATABASE_URL="postgresql://auth:secret@db.internal:5432/auth" \
JWT_SECRET_KEY="$(cat /run/secrets/jwt_secret)" \
REDIS_URL="redis://redis.internal:6379/0" \
  /opt/services/auth-service-v0.4.0 run
```

```
[2025-12-04 10:02:17 +0000] [8831] [INFO] Starting gunicorn 23.0.0
[2025-12-04 10:02:17 +0000] [8831] [INFO] Listening at: http://0.0.0.0:5000
[2025-12-04 10:02:17 +0000] [8834] [INFO] Booting worker with pid: 8834
[2025-12-04 10:02:17 +0000] [8835] [INFO] Booting worker with pid: 8835
```

Quick smoke test from another host:

```bash
curl -sf http://auth-server-01:5000/health
# {"status":"ok","version":"0.4.0"}

curl -sf -X POST http://auth-server-01:5000/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"test"}' \
  | python -m json.tool
# {
#   "access_token": "eyJhbGciOiJIUzI1NiIs...",
#   "refresh_token": "eyJhbGciOiJIUzI1NiIs..."
# }
```

---

## 9. Deploying to multiple replicas

The binary is the same artifact on every host. Deploy to N hosts in parallel:

```bash
for host in auth-01 auth-02 auth-03; do
  scp auth-service-v0.4.0 app@${host}:/opt/services/ &
done
wait

for host in auth-01 auth-02 auth-03; do
  ssh app@${host} "
    DATABASE_URL=$DATABASE_URL \
    JWT_SECRET_KEY=$JWT_SECRET_KEY \
    REDIS_URL=$REDIS_URL \
    /opt/services/auth-service-v0.4.0 install && \
    systemctl restart auth-service
  " &
done
wait
echo "Deployed to all replicas."
```

---

## 10. Tips

- **Token revocation via Redis**: the service checks `revoked:{jti}` on every protected request. The binary ships with the Redis client built in — no separate dependency install on the target.
- **`verify_on_launch: true`** is set in `molt.yaml` because this service is security-sensitive. Every process start re-verifies the payload hash before serving requests.
- **Minimal binary size**: at 34 MB, the binary fits comfortably in a 1-minute deploy pipeline. Use `molt build --profile minimal` to shave additional size if you have no optional extras.
- **Secrets never in the binary**: the sensitive-file check blocks `.env`, `*.key`, and `*secret*` from accidentally shipping. Pass `JWT_SECRET_KEY` and `DATABASE_URL` through your secrets manager at runtime.
- **Rolling back**: keep the previous version's binary in `/opt/services/`. Restart the service pointing at the old binary — no reinstall needed if it was previously installed.
