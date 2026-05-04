# molt — Walkthroughs

End-to-end examples for 33 different project types. Each walkthrough covers:

- `molt init` / `molt sync` — set up the project and populate the global store
- `pyproject.toml` — dependencies and `[tool.molt.tasks]`
- `molt run` — develop locally without activating a venv
- `molt.yaml` — describe the distributable binary
- `molt build` — produce the self-installing binary
- Deploy — copy the binary to another machine and run it

---

## Web Servers & APIs

| # | File | Stack |
|---|---|---|
| 01 | [FastAPI REST API](web-servers/01-fastapi-rest-api.md) | FastAPI · SQLAlchemy · Alembic · PostgreSQL |
| 02 | [Django Web App](web-servers/02-django-web-app.md) | Django 5 · Celery · WhiteNoise · S3 · Redis |
| 03 | [Flask Microservice](web-servers/03-flask-microservice.md) | Flask · JWT · Redis · Gunicorn |
| 04 | [gRPC Server](web-servers/04-grpc-server.md) | grpcio · protobuf · SQLAlchemy |
| 05 | [WebSocket Live Dashboard](web-servers/05-websocket-server.md) | FastAPI · WebSockets · Redis Pub/Sub |
| 33 | [Multi-service Monorepo](web-servers/33-multi-service-monorepo.md) | FastAPI · Celery · Click · shared editable lib |

## AI / ML

| # | File | Stack |
|---|---|---|
| 06 | [LLM Inference Server](ai-ml/06-llm-inference-server.md) | llama-cpp-python · FastAPI · model weights as assets |
| 07 | [RAG Pipeline](ai-ml/07-rag-pipeline.md) | LangChain · ChromaDB · OpenAI embeddings |
| 08 | [HuggingFace Classifier](ai-ml/08-huggingface-classifier.md) | Transformers · PyTorch · DistilBERT |
| 09 | [ML Training Pipeline](ai-ml/09-ml-training-pipeline.md) | scikit-learn · MLflow · Optuna |
| 10 | [LangChain Agent](ai-ml/10-langchain-agent.md) | LangChain · ReAct · FAISS · DuckDuckGo |

## CLI Tools

| # | File | Stack |
|---|---|---|
| 11 | [Cloud Tag Manager](cli-tools/11-click-cli.md) | Click · boto3 · Rich · Pydantic |
| 12 | [Developer Productivity Kit](cli-tools/12-typer-cli.md) | Typer · Rich · Jinja2 · GitPython |
| 13 | [Data Processing CLI](cli-tools/13-data-processing-cli.md) | Click · Pandas · Polars · PyArrow |
| 14 | [Git Workflow Helper](cli-tools/14-git-helper-cli.md) | Click · GitPython · OpenAI · Rich |
| 15 | [Database CLI](cli-tools/15-database-cli.md) | Click · SQLAlchemy · psycopg2 · Rich |

## Data & ETL

| # | File | Stack |
|---|---|---|
| 16 | [ETL Pipeline](data-etl/16-etl-pipeline.md) | Pandas · SQLAlchemy · boto3 · Great Expectations |
| 17 | [Kafka Consumer](data-etl/17-kafka-consumer.md) | confluent-kafka · ClickHouse · Prometheus |
| 18 | [Airflow DAGs](data-etl/18-airflow-dags.md) | Apache Airflow · custom operators · MWAA |
| 19 | [DB Migration Tool](data-etl/19-db-migration-tool.md) | Alembic · SQLAlchemy · Click · multi-tenant |
| 20 | [dbt Runner](data-etl/20-dbt-runner.md) | dbt-core · dbt-postgres · boto3 · CI |

## Background Workers

| # | File | Stack |
|---|---|---|
| 21 | [Celery Worker](workers/21-celery-worker.md) | Celery · Redis · Pillow · boto3 |
| 22 | [RQ Worker](workers/22-rq-worker.md) | RQ · Redis · Rich · Structlog |
| 23 | [APScheduler Cron](workers/23-apscheduler-cron.md) | APScheduler · SQLAlchemy · SendGrid · HTTPX |
| 24 | [Web Scraper Worker](workers/24-webscraper-worker.md) | Playwright · SQLAlchemy · Pydantic |

## Bots & Automation

| # | File | Stack |
|---|---|---|
| 25 | [Discord Bot](bots-automation/25-discord-bot.md) | discord.py · aiohttp · gidgethub · Redis |
| 26 | [Telegram Bot](bots-automation/26-telegram-bot.md) | python-telegram-bot · SQLAlchemy · Stripe |
| 27 | [Slack Bot](bots-automation/27-slack-bot.md) | Slack Bolt · httpx · Pydantic |
| 28 | [Playwright E2E Suite](bots-automation/28-playwright-automation.md) | Playwright · pytest-playwright · Rich |

## Developer Tools

| # | File | Stack |
|---|---|---|
| 29 | [MCP Server](devtools/29-mcp-server.md) | MCP · FastAPI · SQLAlchemy · Claude Desktop |
| 30 | [Monitoring Agent](devtools/30-monitoring-agent.md) | psutil · Prometheus · httpx · Pydantic-settings |
| 31 | [API Mock Server](devtools/31-api-mock-server.md) | FastAPI · OpenAPI · Faker · record/replay |
| 32 | [Code Quality Runner](devtools/32-code-quality-runner.md) | Ruff · mypy · Bandit · Vulture · Rich |

---

## Common patterns

### First sync is the only download

```bash
# Project A: cold populate
cd ~/projects/api
molt sync
# → ↓ install requests 2.32.3
# → ↓ install fastapi 0.115.0
# → ✓ 24 packages installed

# Project B: same deps — instant
cd ~/projects/api-v2
molt sync
# → ✓ cached  requests 2.32.3
# → ✓ cached  fastapi 0.115.0
# → ✓ 24 packages cached (0 bytes downloaded)
```

### Running without thinking about environments

```bash
molt run dev          # task from [tool.molt.tasks]
molt run python       # project's pinned interpreter
molt run pytest -v    # any binary, store PYTHONPATH applied automatically
```

### One binary, any machine

```bash
molt build                          # → myapp-v1.0.0
scp myapp-v1.0.0 user@server:~/
ssh user@server './myapp-v1.0.0 install && ./myapp-v1.0.0 run'
# No Python. No pip. No venv. It just runs.
```

### Switching Python versions

```bash
molt python install 3.13
molt python use 3.13
molt sync                           # repopulates ABI-specific store entries
molt run pytest                     # now running under 3.13
```

### Cleaning up old entries

```bash
molt gc --dry-run                   # preview
molt gc                             # remove entries no project references
```
