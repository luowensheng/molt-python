# Slack Bot — Ops Incident & Deployment Bot

This walkthrough builds `ops-bot`, a Slack bot using the Bolt for Python framework. It serves three operational use cases: incident response workflows (declare, update, resolve), deployment notifications from CI/CD pipelines, and on-call rotation management. Slack Bolt handles socket mode for local development and HTTP mode for production. SQLAlchemy stores incident history and on-call schedules. HTTPX makes outbound calls to PagerDuty and internal APIs.

---

## 1. Project Init and molt sync

```
$ mkdir ops-bot && cd ops-bot
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  slack-bolt==1.18.1
  slack-sdk==3.27.1
  httpx==0.27.0
  pydantic==2.7.1
  sqlalchemy==2.0.29
  ...
  Locked 26 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "ops-bot"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "slack-bolt>=1.18",
    "slack-sdk>=3.27",
    "httpx>=0.27",
    "pydantic>=2.7",
    "sqlalchemy>=2.0",
]

[project.scripts]
ops-bot = "ops_bot.cli:main"

[tool.molt.tasks]
dev    = "python -m ops_bot.main --socket-mode"
test   = "python -m pytest tests/ -v"
deploy = "python -m ops_bot.cli deploy"
```

---

## 3. Project Layout

```
ops_bot/
├── main.py            # Bolt app setup + event registration
├── cli.py             # Click CLI: start (HTTP), dev (socket mode)
├── config.py          # Pydantic settings
├── db.py              # SQLAlchemy engine
├── models.py          # Incident, Deployment, OnCallRotation
└── listeners/
    ├── __init__.py
    ├── incidents.py   # /incident command + modal
    ├── deployments.py # deployment-notify slash command + incoming webhook
    └── oncall.py      # /oncall rotation query
```

**ops_bot/listeners/incidents.py**
```python
from slack_bolt import App
from slack_sdk.models.blocks import SectionBlock, ActionsBlock, ButtonElement
from ops_bot.db import get_session
from ops_bot.models import Incident
from datetime import datetime

def register(app: App):
    @app.command("/incident")
    def declare_incident(ack, body, client, logger):
        ack()
        client.views_open(
            trigger_id=body["trigger_id"],
            view={
                "type": "modal",
                "callback_id": "incident_declare",
                "title": {"type": "plain_text", "text": "Declare Incident"},
                "submit": {"type": "plain_text", "text": "Declare"},
                "blocks": [
                    {
                        "type": "input",
                        "block_id": "severity",
                        "label": {"type": "plain_text", "text": "Severity"},
                        "element": {
                            "type": "static_select",
                            "action_id": "value",
                            "options": [
                                {"text": {"type": "plain_text", "text": "SEV1 — Critical"}, "value": "sev1"},
                                {"text": {"type": "plain_text", "text": "SEV2 — Major"},    "value": "sev2"},
                                {"text": {"type": "plain_text", "text": "SEV3 — Minor"},    "value": "sev3"},
                            ],
                        },
                    },
                    {
                        "type": "input",
                        "block_id": "description",
                        "label": {"type": "plain_text", "text": "Description"},
                        "element": {"type": "plain_text_input", "action_id": "value", "multiline": True},
                    },
                ],
            },
        )

    @app.view("incident_declare")
    def handle_declare(ack, body, view, client, logger):
        ack()
        vals   = view["state"]["values"]
        sev    = vals["severity"]["value"]["selected_option"]["value"]
        desc   = vals["description"]["value"]["value"]
        user   = body["user"]["id"]

        with get_session() as db:
            inc = Incident(severity=sev, description=desc, declared_by=user,
                           status="open", declared_at=datetime.utcnow())
            db.add(inc)
            db.commit()
            db.refresh(inc)

        client.chat_postMessage(
            channel="#incidents",
            text=f"*INC-{inc.id} declared* ({sev.upper()}) by <@{user}>: {desc}\n"
                 f"Responders: <!here>",
        )
        logger.info(f"Incident INC-{inc.id} declared by {user}")
```

**ops_bot/listeners/deployments.py**
```python
from slack_bolt import App
import httpx

def register(app: App):
    @app.command("/deploy-notify")
    def deploy_notify(ack, body, client):
        ack()
        text = body.get("text", "")
        # Expected: "<service> <version> <environment>"
        parts = text.strip().split()
        if len(parts) != 3:
            client.chat_postMessage(
                channel=body["channel_id"],
                text="Usage: /deploy-notify <service> <version> <env>"
            )
            return
        service, version, env = parts
        client.chat_postMessage(
            channel="#deployments",
            blocks=[
                {"type": "section", "text": {"type": "mrkdwn",
                    "text": f":rocket: *{service}* `{version}` deployed to *{env}*"}},
                {"type": "context", "elements": [
                    {"type": "mrkdwn", "text": f"by <@{body['user_id']}> at <!date^{int(__import__('time').time())}^{{time}}|now>"}
                ]},
            ],
        )
```

**ops_bot/main.py**
```python
import os
from slack_bolt import App
from slack_bolt.adapter.socket_mode import SocketModeHandler
from ops_bot.config import settings
from ops_bot.listeners import incidents, deployments, oncall

app = App(token=settings.slack_bot_token, signing_secret=settings.slack_signing_secret)

incidents.register(app)
deployments.register(app)
oncall.register(app)

if __name__ == "__main__":
    import sys
    if "--socket-mode" in sys.argv:
        handler = SocketModeHandler(app, settings.slack_app_token)
        handler.start()
    else:
        app.start(port=int(settings.port))
```

---

## 4. Running in Development

```
$ cp .env.example .env
# Edit .env:
# SLACK_BOT_TOKEN=xoxb-...
# SLACK_SIGNING_SECRET=abc123...
# SLACK_APP_TOKEN=xapp-...    (for socket mode)

$ molt run dev
⚡️ Bolt app is running in Socket Mode! (development)
INFO Connected to Slack

# In Slack, type /incident
# → Modal appears with severity + description fields
# → On submit, bot posts to #incidents:
#   *INC-1 declared* (SEV2) by @alice: API latency spike on checkout service
#   Responders: @here

# Type /deploy-notify api v1.4.2 production
# → Bot posts to #deployments:
#   🚀 *api* `v1.4.2` deployed to *production*
```

Run tests:

```
$ molt run test
========================= test session starts ==========================
collected 12 items

tests/test_incidents.py::test_declare_modal_opens     PASSED
tests/test_incidents.py::test_incident_created_in_db  PASSED
tests/test_incidents.py::test_incident_message_posted PASSED
tests/test_deployments.py::test_deploy_notify         PASSED
tests/test_deployments.py::test_deploy_notify_bad_args PASSED
tests/test_oncall.py::test_who_is_oncall              PASSED
...

========================= 12 passed in 2.61s ==========================
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: ops-bot
  entry: ops_bot.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: start
      args: ["start"]
      description: "Start in HTTP mode (production)"
    - name: migrate
      args: ["migrate"]
      description: "Run database migrations"

  env:
    PORT: "3000"
    LOG_LEVEL: "INFO"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling ops_bot + 26 dependencies...
  Output: dist/ops-bot  (19.3 MB)
```

---

## 6. Deploy

```
$ scp dist/ops-bot deploy@slackbot-01.prod:/opt/ops-bot/
ops-bot                               100%   19MB  28.1MB/s   00:00

deploy@slackbot-01$ cat /etc/ops-bot/env
SLACK_BOT_TOKEN=xoxb-...
SLACK_SIGNING_SECRET=abc123...
DATABASE_URL=postgresql+psycopg2://opsbot:secret@db.internal/opsbot
PORT=3000

deploy@slackbot-01$ /opt/ops-bot/ops-bot migrate
Applying migration 001_create_incidents ... OK
Applying migration 002_create_deployments ... OK
Applying migration 003_create_oncall ... OK

deploy@slackbot-01$ /opt/ops-bot/ops-bot start
INFO  Starting Bolt in HTTP mode on port 3000
```

Expose the bot via nginx (Slack requires HTTPS):

```nginx
location /slack/events {
    proxy_pass http://127.0.0.1:3000/slack/events;
}
```

Set your app's Request URL in the Slack app settings to `https://ops.example.com/slack/events`.

### systemd unit

```ini
[Unit]
Description=ops-bot Slack Bot
After=network.target

[Service]
User=deploy
EnvironmentFile=/etc/ops-bot/env
ExecStart=/opt/ops-bot/ops-bot start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

---

## Tips

- **Socket Mode vs HTTP Mode**: Socket Mode requires no public URL (great for dev and internal tools behind a firewall). HTTP Mode requires a public HTTPS endpoint and is preferred for production.
- **Signing secret verification**: Bolt verifies the `X-Slack-Signature` header automatically. Never disable this in production.
- **Ack within 3 seconds**: Slack requires `ack()` within 3 s. For slow operations (DB writes, API calls), ack immediately then do the work in a background thread or `asyncio.create_task`.
- **Block Kit Builder**: Use Slack's [Block Kit Builder](https://app.slack.com/block-kit-builder) to design message layouts before coding them.
- **Modal state management**: Store intermediate state in `context.user_data` (in-memory per process) or in Redis for multi-step workflows spanning multiple interactions.
- **Metrics**: Instrument each command handler with a counter. The binary ships with Prometheus client — expose `/metrics` for Grafana.
