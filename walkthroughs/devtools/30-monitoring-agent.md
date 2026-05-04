# Monitoring Agent — Infrastructure Watchdog

This walkthrough builds `watchdog`, a lightweight infrastructure monitoring agent that runs on each server. It collects CPU, memory, disk, and network metrics using psutil; checks the health of HTTP services, databases, and message brokers; and fires alerts to Slack or PagerDuty when thresholds are exceeded. Metrics are exported in Prometheus format for Grafana. Configuration is managed by pydantic-settings so every threshold and endpoint is controlled via environment variables or a `.env` file. A single binary is deployed to each server via `scp` or Ansible.

---

## 1. Project Init and molt sync

```
$ mkdir watchdog && cd watchdog
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  click==8.1.7
  httpx==0.27.0
  psutil==5.9.8
  prometheus-client==0.20.0
  structlog==24.1.0
  pydantic-settings==2.2.1
  ...
  Locked 18 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "watchdog"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "click>=8.1",
    "httpx>=0.27",
    "psutil>=5.9",
    "prometheus-client>=0.20",
    "structlog>=24.1",
    "pydantic-settings>=2.2",
]

[project.scripts]
watchdog = "watchdog.cli:main"

[tool.molt.tasks]
start  = "python -m watchdog.cli start"
test   = "python -m pytest tests/ -v"
status = "python -m watchdog.cli status"
```

---

## 3. Project Layout

```
watchdog/
├── cli.py             # Click CLI: start, status, report, check
├── config.py          # Pydantic settings with all thresholds
├── collectors/
│   ├── __init__.py
│   ├── system.py      # CPU, memory, disk, network
│   └── services.py    # HTTP health checks, DB ping, Redis ping
├── alerting/
│   ├── __init__.py
│   ├── slack.py       # Post to Slack webhook
│   └── pagerduty.py   # PagerDuty Events API v2
└── metrics.py         # Prometheus registry + gauges
```

**watchdog/config.py**
```python
from pydantic_settings import BaseSettings
from pydantic import Field

class Settings(BaseSettings):
    # Thresholds
    cpu_warn_pct: float = 80.0
    cpu_crit_pct: float = 95.0
    mem_warn_pct: float = 85.0
    mem_crit_pct: float = 95.0
    disk_warn_pct: float = 80.0
    disk_crit_pct: float = 90.0

    # Services to monitor (comma-separated URLs)
    http_endpoints: list[str] = Field(default_factory=list)
    http_timeout_s: float = 5.0

    # Alerting
    slack_webhook_url: str = ""
    pagerduty_routing_key: str = ""

    # Prometheus
    metrics_port: int = 9100
    scrape_interval_s: int = 15

    # Server identity
    hostname: str = ""

    class Config:
        env_file = ".env"
        env_list_separator = ","

settings = Settings()
```

**watchdog/collectors/system.py**
```python
import psutil
from watchdog.config import settings
import structlog

log = structlog.get_logger(__name__)

def collect() -> dict:
    cpu = psutil.cpu_percent(interval=1)
    mem = psutil.virtual_memory()
    disk = psutil.disk_usage("/")
    net = psutil.net_io_counters()

    result = {
        "cpu_pct":       cpu,
        "mem_pct":       mem.percent,
        "mem_used_gb":   mem.used / 1e9,
        "mem_total_gb":  mem.total / 1e9,
        "disk_pct":      disk.percent,
        "disk_used_gb":  disk.used / 1e9,
        "disk_total_gb": disk.total / 1e9,
        "net_sent_mb":   net.bytes_sent / 1e6,
        "net_recv_mb":   net.bytes_recv / 1e6,
    }
    _check_thresholds(result)
    return result

def _check_thresholds(m: dict):
    from watchdog.alerting import fire_alert
    if m["cpu_pct"] >= settings.cpu_crit_pct:
        fire_alert("critical", f"CPU at {m['cpu_pct']:.1f}% (threshold {settings.cpu_crit_pct}%)")
    elif m["cpu_pct"] >= settings.cpu_warn_pct:
        fire_alert("warning", f"CPU at {m['cpu_pct']:.1f}% (threshold {settings.cpu_warn_pct}%)")
    if m["disk_pct"] >= settings.disk_crit_pct:
        fire_alert("critical", f"Disk at {m['disk_pct']:.1f}% on /")
```

**watchdog/collectors/services.py**
```python
import httpx
from watchdog.config import settings
import structlog

log = structlog.get_logger(__name__)

def check_http_endpoints() -> list[dict]:
    results = []
    for url in settings.http_endpoints:
        try:
            r = httpx.get(url, timeout=settings.http_timeout_s, follow_redirects=True)
            ok = r.status_code < 500
        except Exception as exc:
            ok = False
            log.warning("http_check_failed", url=url, error=str(exc))
        results.append({"url": url, "up": ok})
    return results
```

**watchdog/alerting/slack.py**
```python
import httpx
from watchdog.config import settings
import structlog

log = structlog.get_logger(__name__)
_sent: set[str] = set()   # simple dedup

def send(severity: str, message: str):
    key = f"{severity}:{message}"
    if key in _sent:
        return
    _sent.add(key)
    if not settings.slack_webhook_url:
        return
    color = "#ff0000" if severity == "critical" else "#ffaa00"
    host = settings.hostname or "unknown"
    httpx.post(settings.slack_webhook_url, json={
        "attachments": [{
            "color": color,
            "title": f"[{severity.upper()}] {host}",
            "text": message,
        }]
    }, timeout=5)
    log.info("alert_sent", severity=severity, message=message)
```

**watchdog/cli.py**
```python
import click, time, threading
from prometheus_client import start_http_server, Gauge
from watchdog.collectors import system, services
from watchdog.config import settings
import structlog

log = structlog.get_logger(__name__)

CPU_GAUGE  = Gauge("watchdog_cpu_percent",  "CPU usage percent")
MEM_GAUGE  = Gauge("watchdog_mem_percent",  "Memory usage percent")
DISK_GAUGE = Gauge("watchdog_disk_percent", "Disk usage percent on /")

@click.group()
def main():
    pass

@main.command()
def start():
    """Start the monitoring agent."""
    structlog.configure(processors=[structlog.processors.JSONRenderer()])
    start_http_server(settings.metrics_port)
    log.info("agent_started", metrics_port=settings.metrics_port,
             scrape_interval=settings.scrape_interval_s)
    while True:
        m = system.collect()
        CPU_GAUGE.set(m["cpu_pct"])
        MEM_GAUGE.set(m["mem_pct"])
        DISK_GAUGE.set(m["disk_pct"])
        svc = services.check_http_endpoints()
        log.info("metrics", **m, services=svc)
        time.sleep(settings.scrape_interval_s)

@main.command()
def status():
    """Print current system status to stdout."""
    m = system.collect()
    svc = services.check_http_endpoints()
    click.echo(f"CPU:  {m['cpu_pct']:.1f}%")
    click.echo(f"MEM:  {m['mem_pct']:.1f}%  ({m['mem_used_gb']:.1f}/{m['mem_total_gb']:.1f} GB)")
    click.echo(f"DISK: {m['disk_pct']:.1f}%  ({m['disk_used_gb']:.0f}/{m['disk_total_gb']:.0f} GB)")
    click.echo(f"SERVICES:")
    for s in svc:
        status = "UP  " if s["up"] else "DOWN"
        click.echo(f"  [{status}] {s['url']}")
```

---

## 4. Running Locally

```
$ molt run start
{"event": "agent_started", "metrics_port": 9100, "scrape_interval": 15}
{"event": "metrics", "cpu_pct": 12.4, "mem_pct": 58.3, "disk_pct": 41.2,
 "services": [{"url": "https://api.example.com/health", "up": true}]}

# Prometheus scrape endpoint
$ curl -s http://localhost:9100/metrics | grep watchdog
watchdog_cpu_percent 12.4
watchdog_mem_percent 58.3
watchdog_disk_percent 41.2
```

Quick status check:

```
$ molt run status
CPU:  14.2%
MEM:  59.1%  (9.5/16.0 GB)
DISK: 41.2%  (165/400 GB)
SERVICES:
  [UP  ] https://api.example.com/health
  [UP  ] https://db.example.com:5432
  [DOWN] https://legacy-service.example.com/ping
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: watchdog
  entry: watchdog.cli:main
  python: "3.12"
  target: linux/amd64

  commands:
    - name: start
      args: ["start"]
      description: "Start the monitoring agent"
    - name: status
      args: ["status"]
      description: "Print current system metrics"
    - name: report
      args: ["report"]
      description: "Emit a one-shot JSON metrics report"

  env:
    SCRAPE_INTERVAL_S: "15"
    METRICS_PORT: "9100"
    LOG_LEVEL: "INFO"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling watchdog + 18 dependencies...
  Output: dist/watchdog  (12.8 MB)
```

---

## 6. Deploy to Multiple Servers

### Manual deploy

```
$ for host in web-01 web-02 worker-01 db-01; do
    scp dist/watchdog deploy@${host}.prod:/opt/watchdog/watchdog
    echo "Deployed to $host"
  done
Deployed to web-01
Deployed to web-02
Deployed to worker-01
Deployed to db-01
```

### Ansible playbook

```yaml
# deploy-watchdog.yml
- hosts: all
  tasks:
    - name: Copy watchdog binary
      copy:
        src: dist/watchdog
        dest: /opt/watchdog/watchdog
        mode: "0755"

    - name: Write env file
      template:
        src: templates/watchdog-env.j2
        dest: /etc/watchdog/env
        mode: "0600"

    - name: Install systemd unit
      copy:
        src: templates/watchdog.service
        dest: /etc/systemd/system/watchdog.service

    - name: Enable and start watchdog
      systemd:
        name: watchdog
        enabled: true
        state: restarted
        daemon_reload: true
```

```
$ ansible-playbook -i inventory/prod deploy-watchdog.yml
PLAY [all] ************************************************************
TASK [Copy watchdog binary]     changed: [web-01] changed: [web-02] ...
TASK [Write env file]           changed: [web-01] changed: [web-02] ...
TASK [Enable and start watchdog] ok: [web-01] ok: [web-02] ...
PLAY RECAP: 4 changed, 0 failed
```

### systemd unit

```ini
[Unit]
Description=watchdog monitoring agent
After=network.target

[Service]
User=deploy
EnvironmentFile=/etc/watchdog/env
ExecStart=/opt/watchdog/watchdog start
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

---

## Tips

- **Dedup alerts**: The in-process `_sent` set prevents alert storms. For multi-process or restart-safe dedup, use Redis with a TTL (e.g. 30 min cooldown per alert key).
- **Prometheus + Grafana**: Add the scrape target to `prometheus.yml`, then import the [Node Exporter dashboard](https://grafana.com/grafana/dashboards/1860) as a starting point — the gauge names match.
- **Custom service checks**: Add Redis, PostgreSQL, and RabbitMQ health checks to `services.py`. Each check is a simple function that catches connection errors and returns `{"up": False}`.
- **Binary size**: psutil has no heavy deps. The 12 MB binary is small enough to pull fresh on every deploy without caching.
- **Minimal footprint**: The agent uses < 20 MB RAM. Run it under `nice -n 19` so it never competes with application processes for CPU.
- **Cross-architecture**: Build with `target: linux/arm64` for AWS Graviton or Raspberry Pi servers. Keep the amd64 build for x86 VMs.
