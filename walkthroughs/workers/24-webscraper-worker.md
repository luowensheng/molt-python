# Playwright Web Scraper — Scheduled Price Tracker

This walkthrough builds `price-scraper`, a headless browser scraper that runs on a schedule, extracts product prices from several e-commerce sites, and persists results in a PostgreSQL database using SQLAlchemy. Playwright drives Chromium. Pydantic validates raw scraped data before it is written. Structlog produces structured JSON logs for ingestion into a log aggregator. The finished binary ships with Chromium bundled so it runs on any Linux server without a separate browser install.

---

## 1. Project Init and molt sync

```
$ mkdir price-scraper && cd price-scraper
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  playwright==1.44.0
  sqlalchemy==2.0.29
  psycopg2-binary==2.9.9
  pydantic==2.7.1
  structlog==24.1.0
  ...
  Locked 22 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "price-scraper"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "playwright>=1.44",
    "sqlalchemy>=2.0",
    "psycopg2-binary>=2.9",
    "pydantic>=2.7",
    "structlog>=24.1",
]

[project.scripts]
price-scraper = "price_scraper.cli:main"

[tool.molt.tasks]
scrape            = "python -m price_scraper.cli scrape"
install-browsers  = "playwright install chromium"
test              = "python -m pytest tests/ -v"
```

---

## 3. Project Layout

```
price_scraper/
├── cli.py             # Click CLI: scrape, status, backfill
├── browser.py         # Playwright context manager
├── scrapers/
│   ├── __init__.py
│   ├── amazon.py
│   ├── bestbuy.py
│   └── newegg.py
├── models.py          # SQLAlchemy ORM models
├── schemas.py         # Pydantic validation schemas
├── db.py              # Engine + session factory
└── scheduler.py       # APScheduler wrapper
```

**price_scraper/schemas.py**
```python
from pydantic import BaseModel, HttpUrl, validator
from decimal import Decimal

class ScrapedPrice(BaseModel):
    sku: str
    retailer: str
    url: HttpUrl
    price: Decimal
    currency: str = "USD"
    in_stock: bool
    scraped_at: str

    @validator("price")
    def price_must_be_positive(cls, v):
        if v <= 0:
            raise ValueError("price must be positive")
        return v
```

**price_scraper/browser.py**
```python
from contextlib import asynccontextmanager
from playwright.async_api import async_playwright

@asynccontextmanager
async def get_browser():
    async with async_playwright() as p:
        browser = await p.chromium.launch(
            headless=True,
            args=["--no-sandbox", "--disable-dev-shm-usage"],
        )
        context = await browser.new_context(
            user_agent="Mozilla/5.0 (compatible; PriceScraper/1.0)",
            viewport={"width": 1280, "height": 800},
        )
        try:
            yield context
        finally:
            await browser.close()
```

**price_scraper/scrapers/amazon.py**
```python
import asyncio, re
from decimal import Decimal
from datetime import datetime, timezone
from price_scraper.browser import get_browser
from price_scraper.schemas import ScrapedPrice
import structlog

log = structlog.get_logger(__name__)

SKUS = {
    "B0CHX3QBWW": "https://www.amazon.com/dp/B0CHX3QBWW",
    "B0BDJH5F1X": "https://www.amazon.com/dp/B0BDJH5F1X",
}

async def scrape_all() -> list[ScrapedPrice]:
    results = []
    async with get_browser() as ctx:
        for sku, url in SKUS.items():
            page = await ctx.new_page()
            try:
                await page.goto(url, wait_until="domcontentloaded", timeout=30_000)
                price_el = await page.query_selector("#priceblock_ourprice, .a-price .a-offscreen")
                raw = await price_el.inner_text() if price_el else None
                if not raw:
                    log.warning("price_not_found", sku=sku, url=url)
                    continue
                price_str = re.sub(r"[^\d.]", "", raw)
                in_stock_el = await page.query_selector("#availability .a-color-success")
                results.append(ScrapedPrice(
                    sku=sku, retailer="amazon", url=url,
                    price=Decimal(price_str), in_stock=bool(in_stock_el),
                    scraped_at=datetime.now(timezone.utc).isoformat(),
                ))
                log.info("scraped", sku=sku, price=price_str)
            finally:
                await page.close()
    return results
```

---

## 4. Running Locally

Install browsers first (only needed once):

```
$ molt run install-browsers
Downloading Chromium 124.0.6367.60 (playwright build v1117) from ...
|████████████████████████████████| 142.5 MB
Chromium 124.0.6367.60 (playwright build v1117) downloaded to:
/Users/user/Library/Caches/ms-playwright/chromium-1117
```

Run a scrape:

```
$ molt run scrape
{"event": "scrape_started", "retailers": ["amazon", "bestbuy", "newegg"]}
{"event": "scraped", "retailer": "amazon", "sku": "B0CHX3QBWW", "price": "399.99"}
{"event": "scraped", "retailer": "amazon", "sku": "B0BDJH5F1X", "price": "249.00"}
{"event": "scraped", "retailer": "bestbuy", "sku": "6565221",   "price": "399.99"}
{"event": "scraped", "retailer": "newegg",  "sku": "N82E16834234246", "price": "379.00"}
{"event": "scrape_complete", "records_written": 4, "duration_s": 18.4}
```

Run tests (uses `pytest-playwright` with a real Chromium context against a local mock server):

```
$ molt run test
========================= test session starts ==========================
platform darwin -- Python 3.12.3 -- pytest-8.1.1
collected 11 items

tests/test_schemas.py::test_valid_price        PASSED
tests/test_schemas.py::test_invalid_price      PASSED
tests/test_scrapers.py::test_amazon_scraper    PASSED
tests/test_scrapers.py::test_bestbuy_scraper   PASSED
tests/test_scrapers.py::test_newegg_scraper    PASSED
tests/test_db.py::test_write_and_read          PASSED
...

========================= 11 passed in 14.71s =========================
```

---

## 5. molt.yaml — Building a Deployable Binary (with Chromium)

```yaml
# molt.yaml
build:
  name: price-scraper
  entry: price_scraper.cli:main
  python: "3.12"
  target: linux/amd64

  # Bundle Chromium alongside the binary
  playwright_browsers:
    - chromium

  commands:
    - name: scrape
      args: ["scrape"]
      description: "Run a full scrape across all configured retailers"
    - name: status
      args: ["status"]
      description: "Print last scrape results per retailer"

  env:
    DATABASE_URL: "postgresql+psycopg2://scraper:secret@db:5432/prices"
    LOG_FORMAT: "json"
    PLAYWRIGHT_BROWSERS_PATH: "/opt/price-scraper/browsers"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling price_scraper + 22 dependencies...
  Bundling Chromium (playwright build v1117)...
  Output: dist/price-scraper        (24.1 MB)
  Output: dist/browsers/            (142.5 MB — Chromium)
  Total:  166.6 MB

$ ls dist/
browsers/    price-scraper
```

---

## 6. Deploy to Another Machine

```
$ rsync -az --progress dist/ deploy@scraper-01.prod:/opt/price-scraper/
sending incremental file list
price-scraper
browsers/chromium-1117/chrome-linux/chrome
...
sent 166.6 MB in 4.2s
```

```
deploy@scraper-01$ export DATABASE_URL=postgresql+psycopg2://scraper:secret@db.internal:5432/prices
deploy@scraper-01$ export PLAYWRIGHT_BROWSERS_PATH=/opt/price-scraper/browsers
deploy@scraper-01$ export LOG_FORMAT=json

# Test the binary
deploy@scraper-01$ /opt/price-scraper/price-scraper scrape
{"event": "scrape_started", "retailers": ["amazon", "bestbuy", "newegg"]}
{"event": "scraped", "retailer": "amazon", "sku": "B0CHX3QBWW", "price": "399.99"}
...
{"event": "scrape_complete", "records_written": 4, "duration_s": 21.3}
```

### cron + systemd timer

```ini
# /etc/systemd/system/price-scraper.service
[Unit]
Description=price-scraper single run

[Service]
User=deploy
EnvironmentFile=/etc/price-scraper/env
ExecStart=/opt/price-scraper/price-scraper scrape
Type=oneshot

# /etc/systemd/system/price-scraper.timer
[Unit]
Description=Run price-scraper every 4 hours

[Timer]
OnCalendar=*-*-* 00,04,08,12,16,20:00:00
Persistent=true

[Install]
WantedBy=timers.target
```

```
$ sudo systemctl enable --now price-scraper.timer
$ systemctl list-timers price-scraper*
NEXT                          LEFT     LAST                          PASSED  UNIT
Sun 2024-05-05 04:00:00 UTC   2h left  Sun 2024-05-04 00:00:10 UTC   21h ago price-scraper.timer
```

---

## Tips

- **`--no-sandbox`**: Required when running Chromium as root (e.g. in Docker). Add `--disable-gpu` on headless Linux servers without GPU.
- **Rate limiting**: Add `asyncio.sleep(random.uniform(1, 3))` between page loads to avoid triggering anti-bot measures.
- **Stealth**: Consider `playwright-stealth` to patch common browser fingerprinting signals.
- **Retries**: Wrap each scraper call in a retry loop with exponential backoff for `TimeoutError` and `NetworkError`.
- **Change detection**: Compare new prices against the previous row in the DB and only write rows on change to keep table size manageable.
- **Binary + browsers size**: 167 MB is large for frequent deploys. Push the `browsers/` dir once, then only redeploy the 24 MB binary on code changes.
