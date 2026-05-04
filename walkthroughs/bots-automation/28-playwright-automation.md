# Playwright E2E Suite — SaaS App Automation

This walkthrough builds `e2e-suite`, a Playwright end-to-end test suite and automation toolkit for a SaaS web application. It covers login flows, subscription checkout, dashboard interactions, and API-driven setup. Tests run headless in CI, headed locally for debugging. Rich produces a beautiful terminal report. Structlog emits structured logs for CI log aggregators. The finished binary ships with Chromium so it can run on a fresh Linux runner without any pre-installed browser.

---

## 1. Project Init and molt sync

```
$ mkdir e2e-suite && cd e2e-suite
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  playwright==1.44.0
  pytest-playwright==0.5.0
  rich==13.7.1
  structlog==24.1.0
  pytest==8.1.1
  pytest-html==4.1.1
  ...
  Locked 20 packages.
  Created .venv

$ playwright install chromium
Downloading Chromium 124.0.6367.60...
Chromium downloaded to ~/.cache/ms-playwright/chromium-1117
```

---

## 2. pyproject.toml

```toml
[project]
name = "e2e-suite"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "playwright>=1.44",
    "pytest-playwright>=0.5",
    "rich>=13.7",
    "structlog>=24.1",
    "pytest>=8.1",
    "pytest-html>=4.1",
]

[project.scripts]
e2e-suite = "e2e_suite.cli:main"

[tool.molt.tasks]
test         = "pytest tests/ -v --tb=short"
test-headed  = "pytest tests/ -v --headed --slowmo=500"
record       = "playwright codegen https://app.example.com"
report       = "python -m e2e_suite.report"

[tool.pytest.ini_options]
testpaths = ["tests"]
addopts   = "--html=reports/report.html --self-contained-html"
```

---

## 3. Project Layout

```
e2e_suite/
├── cli.py
├── report.py          # Rich-powered terminal summary
├── conftest.py        # Fixtures: page, auth, api_client
└── tests/
    ├── test_auth.py
    ├── test_dashboard.py
    ├── test_checkout.py
    └── test_settings.py
pages/
├── base.py            # BasePage with shared selectors
├── login.py
├── dashboard.py
└── checkout.py
```

**conftest.py**
```python
import pytest
from playwright.sync_api import Page, expect
from pages.login import LoginPage

BASE_URL = "https://app.example.com"

@pytest.fixture(scope="session")
def browser_context_args(browser_context_args):
    return {**browser_context_args, "base_url": BASE_URL,
            "record_video_dir": "reports/videos/"}

@pytest.fixture
def auth_page(page: Page) -> Page:
    """Return a page already logged in as the test user."""
    lp = LoginPage(page)
    lp.navigate()
    lp.login("test@example.com", "TestP@ss1!")
    expect(page).to_have_url(f"{BASE_URL}/dashboard")
    return page
```

**pages/login.py**
```python
from playwright.sync_api import Page

class LoginPage:
    def __init__(self, page: Page):
        self.page = page

    def navigate(self):
        self.page.goto("/login")

    def login(self, email: str, password: str):
        self.page.get_by_label("Email").fill(email)
        self.page.get_by_label("Password").fill(password)
        self.page.get_by_role("button", name="Sign in").click()
        self.page.wait_for_url("**/dashboard")
```

**tests/test_auth.py**
```python
from playwright.sync_api import Page, expect

def test_login_success(page: Page):
    page.goto("/login")
    page.get_by_label("Email").fill("test@example.com")
    page.get_by_label("Password").fill("TestP@ss1!")
    page.get_by_role("button", name="Sign in").click()
    expect(page).to_have_url("**/dashboard")
    expect(page.get_by_text("Welcome back")).to_be_visible()

def test_login_invalid_password(page: Page):
    page.goto("/login")
    page.get_by_label("Email").fill("test@example.com")
    page.get_by_label("Password").fill("wrong")
    page.get_by_role("button", name="Sign in").click()
    expect(page.get_by_text("Invalid credentials")).to_be_visible()

def test_forgot_password_flow(page: Page):
    page.goto("/login")
    page.get_by_role("link", name="Forgot password").click()
    expect(page).to_have_url("**/forgot-password")
    page.get_by_label("Email").fill("test@example.com")
    page.get_by_role("button", name="Send reset link").click()
    expect(page.get_by_text("Check your email")).to_be_visible()
```

**tests/test_checkout.py**
```python
from playwright.sync_api import Page, expect

def test_upgrade_to_pro(auth_page: Page):
    auth_page.goto("/billing")
    auth_page.get_by_role("button", name="Upgrade to Pro").click()
    # Stripe test card
    frame = auth_page.frame_locator("iframe[name='__privateStripeFrame']").first
    frame.get_by_placeholder("Card number").fill("4242424242424242")
    frame.get_by_placeholder("MM / YY").fill("12/28")
    frame.get_by_placeholder("CVC").fill("123")
    auth_page.get_by_role("button", name="Subscribe").click()
    expect(auth_page).to_have_url("**/billing/success")
    expect(auth_page.get_by_text("You're now on Pro")).to_be_visible()
```

---

## 4. Running the Suite

Headless (default):

```
$ molt run test
========================= test session starts ==========================
platform darwin -- Python 3.12.3
collected 18 items

tests/test_auth.py::test_login_success              PASSED [  5%]
tests/test_auth.py::test_login_invalid_password     PASSED [ 11%]
tests/test_auth.py::test_forgot_password_flow       PASSED [ 17%]
tests/test_dashboard.py::test_dashboard_loads       PASSED [ 22%]
tests/test_dashboard.py::test_create_project        PASSED [ 27%]
tests/test_checkout.py::test_upgrade_to_pro         PASSED [ 33%]
tests/test_settings.py::test_update_profile         PASSED [ 77%]
...

========================= 18 passed in 47.23s =========================
HTML report written to: reports/report.html
```

Headed (with slow motion for debugging):

```
$ molt run test-headed
# Browser window opens, each action executes at 500ms/step
```

Record new test with Playwright codegen:

```
$ molt run record
Listening on: http://localhost:9222
# Browser opens, interactions are recorded and printed as Python code
# Ctrl+C to stop recording
```

Generate Rich terminal summary:

```
$ molt run report
┌─────────────────────────────────────────────┐
│  E2E Suite Report — 2024-05-04 09:00:00     │
├──────────────┬────────┬────────┬────────────┤
│ Suite        │ Passed │ Failed │ Duration   │
├──────────────┼────────┼────────┼────────────┤
│ auth         │   3    │   0    │   8.2s     │
│ dashboard    │   5    │   0    │  14.7s     │
│ checkout     │   2    │   0    │  12.1s     │
│ settings     │   8    │   0    │  12.3s     │
├──────────────┼────────┼────────┼────────────┤
│ TOTAL        │  18    │   0    │  47.3s     │
└──────────────┴────────┴────────┴────────────┘
All 18 tests passed.
```

---

## 5. molt.yaml — Building a Deployable Binary with Chromium

```yaml
# molt.yaml
build:
  name: e2e-suite
  entry: e2e_suite.cli:main
  python: "3.12"
  target: linux/amd64

  playwright_browsers:
    - chromium

  commands:
    - name: test
      args: ["test"]
      description: "Run all tests headless"
    - name: test-headed
      args: ["test-headed"]
      description: "Run tests in headed browser (requires display)"
    - name: report
      args: ["report"]
      description: "Print Rich test summary"

  env:
    BASE_URL: "https://app.example.com"
    PLAYWRIGHT_BROWSERS_PATH: "/opt/e2e-suite/browsers"
    PYTEST_ADDOPTS: "--html=/tmp/report.html --self-contained-html"
```

```
$ molt build
  Resolving platform: linux/amd64
  Bundling e2e_suite + 20 dependencies...
  Bundling Chromium (playwright build v1117)...
  Output: dist/e2e-suite     (22.4 MB)
  Output: dist/browsers/     (142.5 MB)
```

---

## 6. Running in CI (GitHub Actions)

```yaml
# .github/workflows/e2e.yml
name: E2E Tests
on:
  push:
    branches: [main]
  pull_request:

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Download e2e-suite binary
        run: |
          aws s3 cp s3://my-artifacts/e2e-suite/latest/e2e-suite ./e2e-suite
          aws s3 cp s3://my-artifacts/e2e-suite/latest/browsers.tar.gz ./browsers.tar.gz
          chmod +x ./e2e-suite
          tar xzf browsers.tar.gz

      - name: Run E2E tests
        env:
          BASE_URL: https://staging.example.com
          PLAYWRIGHT_BROWSERS_PATH: ${{ github.workspace }}/browsers
          TEST_USER_EMAIL: ${{ secrets.TEST_USER_EMAIL }}
          TEST_USER_PASSWORD: ${{ secrets.TEST_USER_PASSWORD }}
        run: ./e2e-suite test -- --junit-xml=results.xml

      - name: Upload report
        uses: actions/upload-artifact@v4
        if: always()
        with:
          name: e2e-report
          path: /tmp/report.html
```

### Deploying the binary to a dedicated test runner

```
$ rsync -az dist/ deploy@e2e-runner-01.ci:/opt/e2e-suite/
$ ssh deploy@e2e-runner-01.ci "/opt/e2e-suite/e2e-suite test"
========================= test session starts ==========================
...
========================= 18 passed in 52.1s ==========================
```

---

## Tips

- **Parallel test execution**: Install `pytest-xdist` and add `-n auto` to `PYTEST_ADDOPTS`. Each worker gets its own browser context.
- **Video on failure**: Set `record_video_dir` in `browser_context_args` and delete the video in a `request.addfinalizer` that only runs on PASS. Failed tests keep their video.
- **Network mocking**: Use `page.route("**/api/**", handler)` to intercept and mock API calls in unit-style UI tests, making them deterministic without hitting real backends.
- **Retries**: Add `@pytest.mark.flaky(reruns=2)` (requires `pytest-rerunfailures`) to tests that occasionally fail due to timing. Fix the root cause when you have time.
- **CI caching**: The 142 MB Chromium bundle changes only when the playwright version bumps. Cache `dist/browsers/` in CI using a key based on the playwright version pin.
- **Secrets**: Never hardcode credentials. Pass via env vars — the binary reads them at runtime, not bake-time.
