# Telegram Bot — SaaS Support Bot

This walkthrough builds `support-bot`, a Telegram bot for a SaaS product. Users can check their subscription status, receive proactive usage alerts when they approach plan limits, and open support tickets that are stored in PostgreSQL. Stripe webhooks trigger subscription change messages. The bot is powered by python-telegram-bot v20 (async), SQLAlchemy for the database layer, and psycopg2-binary for the Postgres driver.

---

## 1. Project Init and molt sync

```
$ mkdir support-bot && cd support-bot
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  python-telegram-bot==21.1.1
  sqlalchemy==2.0.29
  psycopg2-binary==2.9.9
  stripe==9.9.0
  ...
  Locked 24 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "support-bot"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "python-telegram-bot>=21.1",
    "sqlalchemy>=2.0",
    "psycopg2-binary>=2.9",
    "stripe>=9.9",
]

[project.scripts]
support-bot = "support_bot.cli:main"

[tool.molt.tasks]
dev     = { module = "support_bot.main" }
test    = { module = "pytest", args = ["tests/", "-v", "--asyncio-mode=auto"] }
webhook = { module = "support_bot.webhook" }
```

---

## 3. Project Layout

```
support_bot/
├── main.py            # Bot application setup + polling mode
├── webhook.py         # Aiohttp server for Telegram + Stripe webhooks
├── cli.py             # Click CLI: dev, webhook, migrate
├── config.py          # Pydantic settings
├── db.py              # SQLAlchemy engine + session
├── models.py          # User, Subscription, Ticket ORM models
└── handlers/
    ├── __init__.py
    ├── start.py       # /start, /help
    ├── subscription.py # /status, /upgrade, /cancel
    ├── alerts.py      # Proactive usage alert sender
    └── tickets.py     # /ticket, /mytickets
```

**support_bot/models.py**
```python
from sqlalchemy import Column, Integer, String, DateTime, Boolean, ForeignKey
from sqlalchemy.orm import declarative_base, relationship
from datetime import datetime

Base = declarative_base()

class User(Base):
    __tablename__ = "users"
    id              = Column(Integer, primary_key=True)
    telegram_id     = Column(Integer, unique=True, nullable=False, index=True)
    username        = Column(String(64))
    stripe_customer = Column(String(32))
    plan            = Column(String(16), default="free")
    api_calls_this_month = Column(Integer, default=0)
    created_at      = Column(DateTime, default=datetime.utcnow)
    tickets         = relationship("Ticket", back_populates="user")

class Ticket(Base):
    __tablename__ = "tickets"
    id         = Column(Integer, primary_key=True)
    user_id    = Column(Integer, ForeignKey("users.id"), nullable=False)
    subject    = Column(String(256), nullable=False)
    body       = Column(String(4096))
    status     = Column(String(16), default="open")
    created_at = Column(DateTime, default=datetime.utcnow)
    user       = relationship("User", back_populates="tickets")
```

**support_bot/handlers/subscription.py**
```python
from telegram import Update
from telegram.ext import ContextTypes
from support_bot.db import get_session
from support_bot.models import User
import stripe
from support_bot.config import settings

async def status(update: Update, context: ContextTypes.DEFAULT_TYPE):
    telegram_id = update.effective_user.id
    with get_session() as db:
        user = db.query(User).filter_by(telegram_id=telegram_id).first()
    if user is None:
        await update.message.reply_text("You don't have an account yet. Use /start to register.")
        return
    msg = (
        f"*Subscription Status*\n"
        f"Plan: `{user.plan.upper()}`\n"
        f"API calls this month: `{user.api_calls_this_month:,}`\n"
    )
    if user.plan == "free":
        msg += "\nUpgrade to Pro for unlimited calls: /upgrade"
    await update.message.reply_text(msg, parse_mode="Markdown")

async def upgrade(update: Update, context: ContextTypes.DEFAULT_TYPE):
    telegram_id = update.effective_user.id
    with get_session() as db:
        user = db.query(User).filter_by(telegram_id=telegram_id).first()
    session = stripe.checkout.Session.create(
        customer=user.stripe_customer,
        mode="subscription",
        line_items=[{"price": settings.stripe_pro_price_id, "quantity": 1}],
        success_url="https://app.example.com/success",
        cancel_url="https://app.example.com/cancel",
    )
    await update.message.reply_text(
        f"Complete your upgrade here:\n{session.url}",
        disable_web_page_preview=False,
    )
```

**support_bot/handlers/tickets.py**
```python
from telegram import Update
from telegram.ext import ContextTypes, ConversationHandler, CommandHandler, MessageHandler, filters
from support_bot.db import get_session
from support_bot.models import User, Ticket

SUBJECT, BODY = range(2)

async def new_ticket_start(update: Update, context: ContextTypes.DEFAULT_TYPE):
    await update.message.reply_text("What is the subject of your ticket?")
    return SUBJECT

async def receive_subject(update: Update, context: ContextTypes.DEFAULT_TYPE):
    context.user_data["subject"] = update.message.text
    await update.message.reply_text("Describe the issue in detail:")
    return BODY

async def receive_body(update: Update, context: ContextTypes.DEFAULT_TYPE):
    telegram_id = update.effective_user.id
    with get_session() as db:
        user = db.query(User).filter_by(telegram_id=telegram_id).first()
        ticket = Ticket(user_id=user.id,
                        subject=context.user_data["subject"],
                        body=update.message.text)
        db.add(ticket)
        db.commit()
        db.refresh(ticket)
    await update.message.reply_text(
        f"Ticket #{ticket.id} created. Our team will respond within 24 h."
    )
    return ConversationHandler.END
```

---

## 4. Running in Development (Polling Mode)

```
$ cp .env.example .env
# Edit .env:
# TELEGRAM_TOKEN=7123456789:AAF...
# DATABASE_URL=postgresql+psycopg2://bot:secret@localhost/supportbot
# STRIPE_API_KEY=sk_test_...
# STRIPE_PRO_PRICE_ID=price_1Oxx...
# STRIPE_WEBHOOK_SECRET=whsec_...

$ molt run dev
INFO  Starting bot (polling mode)
INFO  Allowed updates: message, callback_query
INFO  Bot @support_bot_dev started

# In Telegram, send /start
# Bot replies: "Welcome to ExampleSaaS support. Commands: /status /ticket /mytickets /upgrade"

# Send /status
# Bot replies:
# Subscription Status
# Plan: FREE
# API calls this month: 1,247
# Upgrade to Pro for unlimited calls: /upgrade
```

Run the webhook server (for Stripe events in production):

```
$ molt run webhook
INFO  Starting webhook server on 0.0.0.0:8443
INFO  Telegram webhook set to https://bot.example.com/telegram
INFO  Stripe webhook listening at /stripe
```

Run tests:

```
$ molt run test
========================= test session starts ==========================
collected 14 items

tests/test_subscription.py::test_status_free_user     PASSED
tests/test_subscription.py::test_status_pro_user      PASSED
tests/test_subscription.py::test_upgrade_generates_url PASSED
tests/test_tickets.py::test_create_ticket             PASSED
tests/test_tickets.py::test_list_my_tickets           PASSED
tests/test_stripe.py::test_subscription_created_event PASSED
tests/test_stripe.py::test_subscription_cancelled_event PASSED
...

========================= 14 passed in 3.87s ==========================
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: support-bot
  entry: support_bot.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: start
      args: ["start"]
      description: "Start bot in webhook mode"
    - name: poll
      args: ["poll"]
      description: "Start bot in polling mode (dev/debug)"
    - name: migrate
      args: ["migrate"]
      description: "Run database migrations"

  env:
    LOG_LEVEL: "INFO"
    BOT_MODE: "webhook"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling support_bot + 24 dependencies...
  Output: dist/support-bot  (17.8 MB)
```

---

## 6. Deploy to Another Machine

```
$ scp dist/support-bot deploy@bot-01.prod:/opt/support-bot/
support-bot                           100%   18MB  31.4MB/s   00:00
```

```
deploy@bot-01$ cat /etc/support-bot/env
TELEGRAM_TOKEN=7123456789:AAF...
DATABASE_URL=postgresql+psycopg2://bot:secret@db.internal/supportbot
STRIPE_API_KEY=sk_live_...
STRIPE_WEBHOOK_SECRET=whsec_...
STRIPE_PRO_PRICE_ID=price_1Oxx...

# Run database migration first
deploy@bot-01$ /opt/support-bot/support-bot migrate
Applying migration 001_create_users ... OK
Applying migration 002_create_tickets ... OK

# Start the bot
deploy@bot-01$ /opt/support-bot/support-bot start
INFO  Webhook registered: https://bot.example.com/telegram
INFO  Bot @support_bot started
```

### systemd unit

```ini
[Unit]
Description=support-bot Telegram Bot
After=network.target postgresql.service

[Service]
User=deploy
EnvironmentFile=/etc/support-bot/env
ExecStart=/opt/support-bot/support-bot start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```
$ sudo systemctl enable --now support-bot
$ sudo systemctl status support-bot
● support-bot.service - support-bot Telegram Bot
     Active: active (running) since Sun 2024-05-04 13:00:00 UTC; 2s ago
```

---

## Tips

- **Polling vs webhook**: Use polling (`dev` task) locally — no public URL needed. Use webhook mode in production for lower latency and no polling overhead.
- **Webhook certificate**: Telegram requires HTTPS. Use a valid TLS certificate (Let's Encrypt) or pass a self-signed cert path to `set_webhook(certificate=...)`.
- **ConversationHandler timeouts**: Set `conversation_timeout=300` to auto-cancel stale multi-step conversations (e.g. ticket creation left halfway).
- **Stripe idempotency**: Store processed Stripe event IDs in the DB before acting on them. Stripe retries failed webhook deliveries up to 72 h.
- **Database connection pool**: Set `pool_size=5, max_overflow=10` on the SQLAlchemy engine. The bot is async but SQLAlchemy Core is sync — use `run_in_executor` or switch to `asyncpg` + SQLAlchemy async for high-throughput bots.
- **Usage alerts**: Run a scheduled task (APScheduler or cron) that queries users near their plan limit and calls `bot.send_message(user.telegram_id, ...)` proactively.
