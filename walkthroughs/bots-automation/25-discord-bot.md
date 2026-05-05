# Discord Bot — Developer Community Bot

This walkthrough builds `dev-bot`, a Discord bot for a developer community server. It handles three feature areas: automated role assignment based on GitHub contributions, notifications when PRs are opened or merged in a watched repository, and code snippet sharing with syntax highlighting rendered as embedded messages. The bot uses discord.py for the Discord gateway, gidgethub for GitHub webhook parsing, aiohttp for serving the webhook endpoint, and Redis for rate-limiting and deduplication.

---

## 1. Project Init and molt sync

```
$ mkdir dev-bot && cd dev-bot
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  discord.py==2.3.2
  aiohttp==3.9.5
  gidgethub==5.3.0
  redis==5.0.3
  jinja2==3.1.3
  ...
  Locked 18 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "dev-bot"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "discord.py>=2.3",
    "aiohttp>=3.9",
    "gidgethub>=5.3",
    "redis>=5.0",
    "jinja2>=3.1",
]

[project.scripts]
dev-bot = "dev_bot.cli:main"

[tool.molt.tasks]
dev  = { module = "dev_bot.main" }
test = { module = "pytest", args = ["tests/", "-v", "--asyncio-mode=auto"] }
```

---

## 3. Project Layout

```
dev_bot/
├── main.py            # Bot + webhook server startup
├── cli.py             # Click CLI wrapping main
├── config.py          # Pydantic settings
├── cogs/
│   ├── __init__.py
│   ├── roles.py       # Role assignment commands
│   ├── github.py      # PR notification handler
│   └── snippets.py    # Code snippet rendering
├── webhooks/
│   ├── __init__.py
│   └── github.py      # aiohttp webhook route
└── templates/
    └── pr_embed.html.j2
```

**dev_bot/config.py**
```python
from pydantic_settings import BaseSettings

class Settings(BaseSettings):
    discord_token: str
    discord_guild_id: int
    github_webhook_secret: str
    github_watched_repos: list[str] = ["myorg/backend", "myorg/frontend"]
    redis_url: str = "redis://localhost:6379/0"
    webhook_port: int = 8080
    pr_notifications_channel_id: int = 0

    class Config:
        env_file = ".env"

settings = Settings()
```

**dev_bot/cogs/roles.py**
```python
import discord
from discord.ext import commands
from discord import app_commands

ROLE_MAP = {
    "python":     "Python Dev",
    "typescript": "TS Dev",
    "rust":       "Rustacean",
    "devops":     "DevOps",
}

class RolesCog(commands.Cog):
    def __init__(self, bot):
        self.bot = bot

    @app_commands.command(name="role", description="Assign yourself a language role")
    @app_commands.choices(lang=[
        app_commands.Choice(name=k, value=k) for k in ROLE_MAP
    ])
    async def assign_role(self, interaction: discord.Interaction, lang: str):
        role_name = ROLE_MAP[lang]
        guild = interaction.guild
        role = discord.utils.get(guild.roles, name=role_name)
        if role is None:
            role = await guild.create_role(name=role_name, mentionable=True)
        await interaction.user.add_roles(role)
        await interaction.response.send_message(
            f"You now have the **{role_name}** role!", ephemeral=True
        )
```

**dev_bot/cogs/github.py**
```python
import discord
from discord.ext import commands

class GitHubCog(commands.Cog):
    def __init__(self, bot):
        self.bot = bot

    async def post_pr_notification(self, payload: dict):
        pr = payload["pull_request"]
        channel_id = self.bot.settings.pr_notifications_channel_id
        channel = self.bot.get_channel(channel_id)
        if channel is None:
            return
        embed = discord.Embed(
            title=f"PR #{pr['number']}: {pr['title']}",
            url=pr["html_url"],
            color=discord.Color.green() if payload["action"] == "opened" else discord.Color.purple(),
        )
        embed.set_author(name=pr["user"]["login"], icon_url=pr["user"]["avatar_url"])
        embed.add_field(name="Repo", value=payload["repository"]["full_name"])
        embed.add_field(name="Action", value=payload["action"].title())
        embed.set_footer(text=f"+{pr['additions']} / -{pr['deletions']} lines")
        await channel.send(embed=embed)
```

**dev_bot/main.py**
```python
import asyncio, aiohttp
import discord
from discord.ext import commands
from dev_bot.config import settings
from dev_bot.cogs.roles import RolesCog
from dev_bot.cogs.github import GitHubCog
from dev_bot.webhooks.github import make_github_handler

intents = discord.Intents.default()
intents.members = True

bot = commands.Bot(command_prefix="!", intents=intents)
bot.settings = settings

@bot.event
async def on_ready():
    print(f"Logged in as {bot.user} ({bot.user.id})")
    await bot.tree.sync()

async def main():
    await bot.add_cog(RolesCog(bot))
    github_cog = GitHubCog(bot)
    await bot.add_cog(github_cog)

    # Run Discord bot + webhook server concurrently
    app = aiohttp.web.Application()
    app.router.add_post("/webhook/github", make_github_handler(github_cog, settings))
    runner = aiohttp.web.AppRunner(app)
    await runner.setup()
    site = aiohttp.web.TCPSite(runner, "0.0.0.0", settings.webhook_port)
    await site.start()
    print(f"Webhook server on port {settings.webhook_port}")

    async with bot:
        await bot.start(settings.discord_token)

if __name__ == "__main__":
    asyncio.run(main())
```

---

## 4. Running in Development

```
$ cp .env.example .env
# Edit .env with your DISCORD_TOKEN, DISCORD_GUILD_ID, etc.

$ molt run dev
Webhook server on port 8080
Logged in as DevBot#1234 (1234567890)
Synced 3 app commands to guild 987654321

# Test the /role command in Discord — the bot replies:
# "You now have the Python Dev role!"

# Send a test GitHub webhook:
$ curl -X POST http://localhost:8080/webhook/github \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: pull_request" \
    -H "X-Hub-Signature-256: sha256=..." \
    -d @tests/fixtures/pr_opened.json
{"ok": true}
# Bot posts PR embed to #pr-notifications channel
```

Run tests:

```
$ molt run test
========================= test session starts ==========================
collected 9 items

tests/test_roles.py::test_assign_existing_role  PASSED
tests/test_roles.py::test_create_missing_role   PASSED
tests/test_github.py::test_pr_opened_embed      PASSED
tests/test_github.py::test_pr_merged_embed      PASSED
tests/test_snippets.py::test_python_highlight   PASSED
...

========================= 9 passed in 2.14s ===========================
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: dev-bot
  entry: dev_bot.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: start
      args: ["start"]
      description: "Start the Discord bot and GitHub webhook server"

  env:
    REDIS_URL: "redis://redis:6379/0"
    WEBHOOK_PORT: "8080"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling dev_bot + 18 dependencies...
  Output: dist/dev-bot  (16.2 MB)
```

---

## 6. Deploy to a VPS

```
$ scp dist/dev-bot deploy@bot-01.vps.example.com:/opt/dev-bot/
dev-bot                               100%   16MB  22.4MB/s   00:00
```

Create the secrets file on the VPS:

```
deploy@bot-01$ cat /etc/dev-bot/env
DISCORD_TOKEN=MTIz...
DISCORD_GUILD_ID=987654321
GITHUB_WEBHOOK_SECRET=whsec_abc123
PR_NOTIFICATIONS_CHANNEL_ID=1122334455
REDIS_URL=redis://127.0.0.1:6379/0
WEBHOOK_PORT=8080
```

Configure nginx to proxy the webhook endpoint:

```nginx
server {
    listen 443 ssl;
    server_name bot.example.com;

    location /webhook/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
    }
}
```

```
deploy@bot-01$ sudo systemctl start redis
deploy@bot-01$ /opt/dev-bot/dev-bot start
Webhook server on port 8080
Logged in as DevBot#1234 (1234567890)
```

### systemd unit

```ini
[Unit]
Description=dev-bot Discord Bot
After=network.target redis.service

[Service]
User=deploy
EnvironmentFile=/etc/dev-bot/env
ExecStart=/opt/dev-bot/dev-bot start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```
$ sudo systemctl enable --now dev-bot
$ sudo systemctl status dev-bot
● dev-bot.service - dev-bot Discord Bot
     Active: active (running) since Sun 2024-05-04 12:00:00 UTC; 3s ago
```

---

## Tips

- **Gateway intents**: `members=True` requires enabling the Server Members Intent in the Discord Developer Portal. Enabling it also means your bot will receive `on_member_join` events without polling.
- **Slash command sync**: Call `bot.tree.sync(guild=discord.Object(id=GUILD_ID))` during development for instant propagation. Global sync (no guild) can take up to an hour.
- **Webhook signature verification**: Always verify `X-Hub-Signature-256` with `gidgethub.sansio.validate_event` before processing any payload. The binary bakes in the secret at build time via env.
- **Redis deduplication**: Store processed GitHub delivery IDs in Redis with a 24 h TTL (`SETEX delivery:{id} 86400 1`) to handle GitHub's at-least-once delivery.
- **Rate limits**: Discord enforces per-channel message rate limits. Use a queue (asyncio.Queue) to serialize embeds and add jitter between sends.
- **Log levels**: In production set `discord.py` logger to WARNING to suppress verbose gateway heartbeat messages.
