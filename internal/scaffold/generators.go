package scaffold

import (
	"fmt"
	"strings"

	"molt/pkg/types"
)

// testModuleFile generates a test file.
// importPath is the full dotted import path (e.g. "myapp.models")
// module is used to derive the class name and is NOT appended to importPath.
func testModuleFile(module, importPath string) string {
	class := toClassName(module)
	return fmt.Sprintf(`import pytest
from %s import %s  # noqa: F401


class Test%s:
    def test_basic(self):
        pass

    def test_raises_on_invalid(self):
        with pytest.raises(Exception):
            pass
`, importPath, class, class)
}

func testAsync(module, pkg string) string {
	class := toClassName(module)
	return fmt.Sprintf(`import asyncio

import pytest


@pytest.fixture
def event_loop():
    loop = asyncio.new_event_loop()
    yield loop
    loop.close()


class Test%s:
    @pytest.mark.asyncio
    async def test_basic(self):
        pass
`, class)
}

func pluginSystem(name string) string {
	class := toClassName(name)
	return fmt.Sprintf(`from __future__ import annotations
import importlib
import pkgutil
from pathlib import Path
from typing import Protocol, runtime_checkable


@runtime_checkable
class Plugin(Protocol):
    name: str
    version: str

    def setup(self) -> None: ...
    def teardown(self) -> None: ...


class %sPluginRegistry:
    def __init__(self) -> None:
        self._plugins: dict[str, Plugin] = {}

    def register(self, plugin: Plugin) -> None:
        self._plugins[plugin.name] = plugin

    def get(self, name: str) -> Plugin:
        return self._plugins[name]

    def all(self) -> list[Plugin]:
        return list(self._plugins.values())

    def load_directory(self, directory: str = "plugins") -> None:
        path = Path(directory)
        if not path.exists():
            return
        for finder, mod_name, _ in pkgutil.iter_modules([str(path)]):
            module = importlib.import_module(f"{directory}.{mod_name}")
            if hasattr(module, "plugin") and isinstance(module.plugin, Plugin):
                self.register(module.plugin)


registry = %sPluginRegistry()
`, class, class)
}

func seedScript(pkg string) string {
	return fmt.Sprintf(`#!/usr/bin/env python3
"""Seed script — populate development data."""
from __future__ import annotations
from %s.config import config
from %s.logging import get_logger

logger = get_logger(__name__)


def main() -> None:
    if config.is_production():
        raise RuntimeError("do not run seed in production")
    logger.info("seeding development data (env=%%s)", config.env)
    # TODO: add seed logic here
    logger.info("done")


if __name__ == "__main__":
    main()
`, pkg, pkg)
}

func scriptFile(name, pkg string, scheduled bool) string {
	if scheduled {
		return fmt.Sprintf(`#!/usr/bin/env python3
"""
%s — scheduled task.
Usage: molt run %s
       python scripts/%s.py
"""
from __future__ import annotations
import schedule
import time
from %s.config import config
from %s.logging import get_logger

logger = get_logger(__name__)


def run() -> None:
    logger.info("running %s (env=%%s)", config.env)
    # TODO: implement task logic


def main() -> None:
    schedule.every().hour.do(run)
    logger.info("scheduler started")
    while True:
        schedule.run_pending()
        time.sleep(60)


if __name__ == "__main__":
    main()
`, name, name, name, pkg, pkg, name)
	}
	return fmt.Sprintf(`#!/usr/bin/env python3
"""
%s — utility script.
Usage: molt run %s
       python scripts/%s.py
"""
from __future__ import annotations
from %s.config import config
from %s.logging import get_logger

logger = get_logger(__name__)


def main() -> None:
    logger.info("starting %s (env=%%s)", config.env)
    # TODO: implement script logic
    logger.info("done")


if __name__ == "__main__":
    main()
`, name, name, name, pkg, pkg, name)
}

func dockerfileMultiStage(pkg, pythonVersion string) string {
	return fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM python:%s-slim AS builder
WORKDIR /app
RUN pip install uv
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev
COPY %s ./%s

FROM python:%s-slim AS runtime
WORKDIR /app
COPY --from=builder /app/.venv /app/.venv
COPY --from=builder /app/%s ./%s
ENV PATH="/app/.venv/bin:$PATH" \
    PYTHONNOUSERSITE=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    ENV=production
HEALTHCHECK --interval=30s --timeout=3s CMD python -c "import %s" || exit 1
ENTRYPOINT ["python", "-m", "%s"]
`, pythonVersion, pkg, pkg, pythonVersion, pkg, pkg, pkg, pkg)
}

func dockerfileDistroless(pkg, pythonVersion string) string {
	return fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM python:%s-slim AS builder
WORKDIR /app
RUN pip install uv
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev
COPY %s ./%s

FROM gcr.io/distroless/python3-debian12 AS runtime
WORKDIR /app
COPY --from=builder /app/.venv /app/.venv
COPY --from=builder /app/%s ./%s
ENV PYTHONNOUSERSITE=1
ENTRYPOINT ["/app/.venv/bin/python", "-m", "%s"]
`, pythonVersion, pkg, pkg, pkg, pkg, pkg)
}

func dockerignore() string {
	return `.git
.venv
__pycache__
*.pyc
*.pyo
.pytest_cache
.mypy_cache
dist/
build/
*.egg-info
.env
.molt-deps.lock
`
}

func githubActionsCI(pythonVersion string) string {
	return fmt.Sprintf(`name: CI
on:
  push:
    branches: [main, develop]
  pull_request:

jobs:
  check:
    name: Check & Test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "%s"
      - name: Install uv
        run: pip install uv
      - name: Install deps
        run: uv sync --frozen
      - name: Lint
        run: uv run ruff check .
      - name: Type check
        run: uv run mypy . || true
      - name: Test
        run: uv run pytest tests/ -v --tb=short --cov

  build:
    name: Build (${{ matrix.os }})
    needs: check
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - name: Install molt
        run: pip install molt || curl -sSf https://molt.dev/install.sh | sh
      - name: Build binary
        run: molt build
      - name: Upload binary
        uses: actions/upload-artifact@v4
        with:
          name: binary-${{ matrix.os }}
          path: "*-v*"
          if-no-files-found: warn
`, pythonVersion)
}

func githubActionsRelease() string {
	return `name: Release
on:
  push:
    tags:
      - "v*"

jobs:
  release:
    name: Release (${{ matrix.os }})
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - name: Install molt
        run: pip install molt || curl -sSf https://molt.dev/install.sh | sh
      - name: Build binary
        run: molt build --profile full
      - name: Upload to release
        uses: softprops/action-gh-release@v1
        with:
          files: "*-v*"
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
`
}

func envExample(name string) string {
	return fmt.Sprintf(`# %s environment variables
# Copy to .env and fill in values. Never commit .env.

ENV=development
DEBUG=false
LOG_LEVEL=INFO
`, name)
}

func gitignore() string {
	return `# Python
__pycache__/
*.py[cod]
*.pyo
*.pyd
.Python
*.egg
*.egg-info/
dist/
build/
.eggs/
*.so

# Virtual environments
.venv/
venv/
env/

# Testing
.pytest_cache/
.coverage
htmlcov/
.tox/

# Type checking
.mypy_cache/
.dmypy.json

# Editors
.vscode/
.idea/
*.swp
*.swo

# molt
.env
dist/

# OS
.DS_Store
Thumbs.db
`
}

func readme(name, description string) string {
	if description == "" {
		description = "A Python project."
	}
	return fmt.Sprintf(`# %s

%s

## Quick Start

`+"```bash"+`
# Install dependencies
molt sync

# Run
molt run dev

# Test
molt run test

# Build distributable binary
molt build
`+"```"+`

## Development

`+"```bash"+`
molt run lint      # lint
molt run format    # format
molt run check     # typecheck
`+"```"+`

## Project Structure

`+"```"+`
%s/
  __init__.py    version
  __main__.py    python -m %s entry point
  main.py        application entry point
  config.py      configuration (reads .env)
  logging.py     logging setup
  exceptions.py  exception hierarchy
tests/
  conftest.py    shared fixtures
  test_main.py   main tests
`+"```"+`
`, name, description, strings.ReplaceAll(name, "-", "_"), name)
}

func toClassName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	result := ""
	for _, p := range parts {
		if len(p) > 0 {
			result += strings.ToUpper(p[:1]) + p[1:]
		}
	}
	if result == "" {
		return name
	}
	return result
}

// ── Content generators ────────────────────────────────────────────────────────

func pyprojectTOML(name, pkg, python string, projectType types.ProjectType) string {
	if python == "" {
		python = "3.12"
	}
	deps := `dependencies = []`
	switch projectType {
	case types.ProjectAPI:
		deps = `dependencies = [
    "fastapi>=0.100",
    "uvicorn[standard]>=0.23",
    "python-dotenv>=1.0",
]`
	case types.ProjectCLI:
		deps = `dependencies = [
    "python-dotenv>=1.0",
]`
	case types.ProjectWorker:
		deps = `dependencies = [
    "python-dotenv>=1.0",
]`
	}
	return fmt.Sprintf(`[project]
name = "%s"
version = "0.1.0"
description = ""
requires-python = ">=%s"
%s

[project.optional-dependencies]
dev = [
    "pytest>=7",
    "pytest-cov>=4",
    "ruff>=0.1",
    "mypy>=1.5",
]

[tool.molt.tasks]
dev    = "python -m %s"
test   = "pytest tests/ -v --tb=short"
lint   = "ruff check ."
format = "ruff format ."

[tool.pytest.ini_options]
testpaths = ["tests"]
`, name, python, deps, pkg)
}

func initPy(name, version string) string {
	ver := version
	if ver == "" {
		ver = "0.1.0"
	}
	return fmt.Sprintf(`"""%s"""
__version__ = "%s"
`, name, ver)
}

func mainPy(pkg string) string {
	return fmt.Sprintf(`from %s.main import main

if __name__ == "__main__":
    main()
`, pkg)
}

func mainModule(pkg string, projectType types.ProjectType) string {
	switch projectType {
	case types.ProjectAPI:
		return fmt.Sprintf(`from %s.app import app
import uvicorn


def main() -> None:
    uvicorn.run(app, host="0.0.0.0", port=8000)

if __name__ == "__main__":
    main()
`, pkg)
	case types.ProjectWorker:
		return fmt.Sprintf(`import asyncio
from %s.worker import Worker
from %s.logging import get_logger

logger = get_logger(__name__)


def main() -> None:
    logger.info("starting")
    asyncio.run(Worker().start())

if __name__ == "__main__":
    main()
`, pkg, pkg)
	default:
		return fmt.Sprintf(`from %s.config import config
from %s.logging import get_logger

logger = get_logger(__name__)


def main() -> None:
    logger.info("starting %s (env=%%s)", config.env)

if __name__ == "__main__":
    main()
`, pkg, pkg, pkg)
	}
}

func configPy() string {
	return `from __future__ import annotations
from dataclasses import dataclass
from pathlib import Path
import os
from dotenv import load_dotenv

load_dotenv(Path(__file__).parent.parent / ".env", override=False)


@dataclass(frozen=True)
class Config:
    env:       str  = os.getenv("ENV",       "development")
    debug:     bool = os.getenv("DEBUG",     "false").lower() == "true"
    log_level: str  = os.getenv("LOG_LEVEL", "INFO")

    def is_production(self) -> bool:
        return self.env == "production"

config = Config()
`
}

func configPydantic() string {
	return `from pydantic_settings import BaseSettings


class Config(BaseSettings):
    env:       str  = "development"
    debug:     bool = False
    log_level: str  = "INFO"

    class Config:
        env_file = ".env"

config = Config()
`
}

func configLayered() string {
	return `from __future__ import annotations
from dataclasses import dataclass
import os
from dotenv import load_dotenv

_env = os.getenv("ENV", "development")
load_dotenv(f".env.{_env}", override=False)
load_dotenv(".env", override=False)


@dataclass(frozen=True)
class Config:
    env:       str  = os.getenv("ENV",       "development")
    debug:     bool = os.getenv("DEBUG",     "false").lower() == "true"
    log_level: str  = os.getenv("LOG_LEVEL", "INFO")

config = Config()
`
}

func loggingPy(pkg string) string {
	return fmt.Sprintf(`from __future__ import annotations
import logging
import sys
from %s.config import config


def get_logger(name: str) -> logging.Logger:
    logger = logging.getLogger(name)
    if not logger.handlers:
        handler = logging.StreamHandler(sys.stdout)
        handler.setFormatter(logging.Formatter(
            "%%(asctime)s %%(levelname)-8s %%(name)s %%(message)s"
        ))
        logger.addHandler(handler)
        logger.propagate = False
    logger.setLevel(config.log_level)
    return logger
`, pkg)
}

func loggingJSON(pkg string) string {
	return fmt.Sprintf(`from __future__ import annotations
import json
import logging
import sys
from %s.config import config


class _JSON(logging.Formatter):
    def format(self, r: logging.LogRecord) -> str:
        return json.dumps({"time": self.formatTime(r), "level": r.levelname,
                           "logger": r.name, "msg": r.getMessage()})


def get_logger(name: str) -> logging.Logger:
    logger = logging.getLogger(name)
    if not logger.handlers:
        h = logging.StreamHandler(sys.stdout)
        h.setFormatter(_JSON())
        logger.addHandler(h)
        logger.propagate = False
    logger.setLevel(config.log_level)
    return logger
`, pkg)
}

func exceptionsPy(name string) string {
	class := toClassName(name)
	return fmt.Sprintf(`class %sError(Exception):
    """Base exception for %s."""


class ConfigError(%sError):
    """Configuration is invalid."""


class NotFoundError(%sError):
    """Resource not found."""


class ValidationError(%sError):
    """Input validation failed."""


class AuthError(%sError):
    """Authentication or authorisation failed."""


class ServiceError(%sError):
    """Downstream service call failed."""
`, class, name, class, class, class, class, class)
}

func cliPy(name, pkg string) string {
	return fmt.Sprintf(`import argparse
from %s.logging import get_logger

logger = get_logger(__name__)


def main() -> None:
    parser = argparse.ArgumentParser(prog="%s")
    parser.add_argument("--verbose", "-v", action="store_true")
    parser.add_argument("--version", action="version", version="0.1.0")
    subparsers = parser.add_subparsers(dest="command", required=True)
    run_p = subparsers.add_parser("run", help="run the application")
    run_p.add_argument("target", nargs="?")
    args = parser.parse_args()
    if args.command == "run":
        logger.info("running")

if __name__ == "__main__":
    main()
`, pkg, name)
}

func cliTyper(name, pkg string) string {
	return fmt.Sprintf(`import typer
from %s.logging import get_logger

logger = get_logger(__name__)
app = typer.Typer(name="%s")


@app.command()
def run(target: str = typer.Argument(...)) -> None:
    logger.info("running %%s", target)


def main() -> None:
    app()
`, pkg, name)
}

func cliClick(name, pkg string) string {
	return fmt.Sprintf(`import click
from %s.logging import get_logger

logger = get_logger(__name__)


@click.group()
def cli(): """%s CLI."""


@cli.command()
@click.argument("target")
def run(target: str) -> None:
    logger.info("running %%s", target)


def main() -> None:
    cli()
`, pkg, name)
}

func appPy(pkg string) string {
	return fmt.Sprintf(`from contextlib import asynccontextmanager
from fastapi import FastAPI
from %s.routers import health
from %s.logging import get_logger

logger = get_logger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("starting up")
    yield
    logger.info("shutting down")

app = FastAPI(lifespan=lifespan)
app.include_router(health.router)
`, pkg, pkg)
}

func healthRouter() string {
	return `from fastapi import APIRouter

router = APIRouter(tags=["health"])


@router.get("/health")
async def health():
    return {"status": "ok"}
`
}

func workerPy(name, pkg string) string {
	return fmt.Sprintf(`from __future__ import annotations
import asyncio
import signal
from %s.logging import get_logger

logger = get_logger(__name__)


class Worker:
    def __init__(self) -> None:
        self._running = False

    async def start(self) -> None:
        self._running = True
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, self.stop)
        logger.info("worker started")
        await self._loop()

    def stop(self) -> None:
        logger.info("stopping")
        self._running = False

    async def _loop(self) -> None:
        while self._running:
            try:
                await self._process()
            except Exception as exc:
                logger.error("error: %%s", exc)
            await asyncio.sleep(1)

    async def _process(self) -> None:
        pass
`, pkg)
}

func corePy(name, pkg string) string {
	return fmt.Sprintf(`from __future__ import annotations
from %s.logging import get_logger
from %s.exceptions import NotFoundError

logger = get_logger(__name__)


class %sCore:
    def __init__(self) -> None:
        pass

    def get(self, id: int) -> dict:
        raise NotFoundError(f"not found: {id}")
`, pkg, pkg, toClassName(name))
}

func modelsPy(name string) string {
	return fmt.Sprintf(`from __future__ import annotations
from dataclasses import dataclass, field
from datetime import datetime
from typing import Optional


@dataclass(frozen=True)
class %s:
    id: Optional[int] = None
    created_at: datetime = field(default_factory=datetime.utcnow)
`, toClassName(name))
}

func modelDataclass(name, fields string) string {
	fieldLines := "    name: str\n"
	if fields != "" {
		fieldLines = ""
		for _, f := range strings.Split(fields, ",") {
			parts := strings.SplitN(strings.TrimSpace(f), ":", 2)
			fname := strings.TrimSpace(parts[0])
			ftype := "str"
			if len(parts) > 1 {
				ftype = strings.TrimSpace(parts[1])
			}
			fieldLines += fmt.Sprintf("    %s: %s\n", fname, ftype)
		}
	}
	return fmt.Sprintf(`from __future__ import annotations
from dataclasses import dataclass


@dataclass(frozen=True)
class %s:
%s`, toClassName(name), fieldLines)
}

func modelPydantic(name, fields string) string {
	fieldLines := "    name: str\n"
	if fields != "" {
		fieldLines = ""
		for _, f := range strings.Split(fields, ",") {
			parts := strings.SplitN(strings.TrimSpace(f), ":", 2)
			fname := strings.TrimSpace(parts[0])
			ftype := "str"
			if len(parts) > 1 {
				ftype = strings.TrimSpace(parts[1])
			}
			fieldLines += fmt.Sprintf("    %s: %s\n", fname, ftype)
		}
	}
	return fmt.Sprintf(`from pydantic import BaseModel


class %s(BaseModel):
%s`, toClassName(name), fieldLines)
}

func modelSQLAlchemy(name, fields string) string {
	return fmt.Sprintf(`from sqlalchemy import Column, Integer, String
from sqlalchemy.orm import DeclarativeBase


class Base(DeclarativeBase):
    pass


class %s(Base):
    __tablename__ = "%ss"
    id = Column(Integer, primary_key=True)
    name = Column(String, nullable=False)
`, toClassName(name), strings.ToLower(name))
}

func moduleFile(name, pkg string, opts ModuleOptions) string {
	if opts.Async {
		return fmt.Sprintf(`from __future__ import annotations
import asyncio

from %s.logging import get_logger

logger = get_logger(__name__)


async def main() -> None:
    logger.info("running %s")


if __name__ == "__main__":
    asyncio.run(main())
`, pkg, name)
	}
	className := opts.ClassName
	if opts.Dataclass || className != "" {
		if className == "" {
			className = toClassName(name)
		}
		return fmt.Sprintf(`from __future__ import annotations
from dataclasses import dataclass
from %s.logging import get_logger

logger = get_logger(__name__)


@dataclass
class %s:
    def __post_init__(self) -> None:
        pass
`, pkg, className)
	}
	return fmt.Sprintf(`from __future__ import annotations
from %s.logging import get_logger

logger = get_logger(__name__)


def main() -> None:
    logger.info("running %s")
`, pkg, name)
}

// conftest generates a root-level conftest.py that has access to config.
func conftest(pkg string) string {
	return fmt.Sprintf(`import pytest
from %s.config import config


@pytest.fixture
def app_config():
    return config
`, pkg)
}

// subpkgConftest generates a conftest.py for a subpackage that has no config.
func subpkgConftest(importPrefix string) string {
	return fmt.Sprintf(`import pytest


@pytest.fixture
def subject():
    """Override this fixture in individual test files."""
    return None
`)
}

func testMain(pkg string) string {
	return fmt.Sprintf(`from %s.main import main


class TestMain:
    def test_imports(self):
        assert main is not None
`, pkg)
}


// testFunctionModule generates a test for a plain function-based module.
func testFunctionModule(module, importPath string) string {
	return fmt.Sprintf(`import pytest


def test_%s_imports():
    """Smoke test: verify module imports without error."""
    import %s  # noqa: F401


def test_%s_basic():
    pass
`, module, importPath, module)
}
