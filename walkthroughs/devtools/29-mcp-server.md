# MCP Server — Developer Tools for LLMs

This walkthrough builds `mcp-devtools`, a Model Context Protocol (MCP) server that exposes three tool categories to LLMs like Claude: filesystem operations (read, write, list), sandboxed code execution (Python snippets), and database queries (read-only SQL against a PostgreSQL database). The server is built with the `mcp` Python SDK, backed by FastAPI for the HTTP transport layer, and uses SQLAlchemy for database access. Once packaged as a binary, it can be pointed to directly from Claude Desktop's configuration.

---

## 1. Project Init and molt sync

```
$ mkdir mcp-devtools && cd mcp-devtools
$ molt init
Created pyproject.toml
Created .python-version (3.12.3)

$ molt sync
  Resolving dependencies...
  mcp==1.2.0
  fastapi==0.111.0
  uvicorn[standard]==0.29.0
  sqlalchemy==2.0.29
  psycopg2-binary==2.9.9
  ...
  Locked 29 packages.
  Created .venv
```

---

## 2. pyproject.toml

```toml
[project]
name = "mcp-devtools"
version = "0.1.0"
requires-python = ">=3.12"
dependencies = [
    "mcp>=1.2",
    "fastapi>=0.111",
    "uvicorn[standard]>=0.29",
    "sqlalchemy>=2.0",
    "psycopg2-binary>=2.9",
]

[project.scripts]
mcp-devtools = "mcp_devtools.cli:main"

[tool.molt.tasks]
dev     = "python -m mcp_devtools.server --transport stdio"
test    = "python -m pytest tests/ -v --asyncio-mode=auto"
inspect = "npx @modelcontextprotocol/inspector python -m mcp_devtools.server"
```

---

## 3. Project Layout

```
mcp_devtools/
├── server.py          # MCP server + tool definitions
├── cli.py             # Click CLI: serve (stdio/http), inspect
├── config.py          # Pydantic settings
├── tools/
│   ├── __init__.py
│   ├── filesystem.py  # read_file, write_file, list_directory
│   ├── execution.py   # run_python (sandboxed)
│   └── database.py    # query_db (read-only SQL)
└── db.py              # SQLAlchemy engine
```

**mcp_devtools/server.py**
```python
import mcp.server.stdio
from mcp.server import Server
from mcp.server.models import InitializationOptions
from mcp.types import Tool, TextContent
from mcp_devtools.tools import filesystem, execution, database

server = Server("mcp-devtools")

@server.list_tools()
async def list_tools() -> list[Tool]:
    return [
        Tool(
            name="read_file",
            description="Read the contents of a file at the given path",
            inputSchema={
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Absolute or relative file path"},
                },
                "required": ["path"],
            },
        ),
        Tool(
            name="list_directory",
            description="List files and directories at a given path",
            inputSchema={
                "type": "object",
                "properties": {
                    "path": {"type": "string"},
                    "recursive": {"type": "boolean", "default": False},
                },
                "required": ["path"],
            },
        ),
        Tool(
            name="run_python",
            description="Execute a Python code snippet and return stdout/stderr",
            inputSchema={
                "type": "object",
                "properties": {
                    "code": {"type": "string", "description": "Python code to execute"},
                    "timeout": {"type": "integer", "default": 10},
                },
                "required": ["code"],
            },
        ),
        Tool(
            name="query_db",
            description="Run a read-only SQL SELECT query and return JSON rows",
            inputSchema={
                "type": "object",
                "properties": {
                    "sql": {"type": "string", "description": "SQL SELECT statement"},
                    "limit": {"type": "integer", "default": 100},
                },
                "required": ["sql"],
            },
        ),
    ]

@server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[TextContent]:
    match name:
        case "read_file":
            result = filesystem.read_file(arguments["path"])
        case "list_directory":
            result = filesystem.list_directory(
                arguments["path"], arguments.get("recursive", False)
            )
        case "run_python":
            result = execution.run_python(
                arguments["code"], arguments.get("timeout", 10)
            )
        case "query_db":
            result = database.query_db(
                arguments["sql"], arguments.get("limit", 100)
            )
        case _:
            result = f"Unknown tool: {name}"
    return [TextContent(type="text", text=str(result))]

async def run():
    async with mcp.server.stdio.stdio_server() as (read_stream, write_stream):
        await server.run(read_stream, write_stream,
                         InitializationOptions(server_name="mcp-devtools",
                                               server_version="0.1.0"))
```

**mcp_devtools/tools/execution.py**
```python
import subprocess, sys, textwrap

BLOCKED = ["import os", "import subprocess", "__import__"]

def run_python(code: str, timeout: int = 10) -> str:
    for blocked in BLOCKED:
        if blocked in code:
            return f"Error: '{blocked}' is not allowed in sandboxed execution"
    result = subprocess.run(
        [sys.executable, "-c", textwrap.dedent(code)],
        capture_output=True, text=True, timeout=timeout
    )
    output = result.stdout
    if result.stderr:
        output += f"\nSTDERR:\n{result.stderr}"
    return output or "(no output)"
```

**mcp_devtools/tools/database.py**
```python
from sqlalchemy import text
from mcp_devtools.db import get_engine
import json

def query_db(sql: str, limit: int = 100) -> str:
    sql_lower = sql.strip().lower()
    if not sql_lower.startswith("select"):
        return "Error: only SELECT statements are allowed"
    engine = get_engine()
    with engine.connect() as conn:
        rows = conn.execute(text(sql + f" LIMIT {limit}")).mappings().all()
    return json.dumps([dict(r) for r in rows], indent=2, default=str)
```

---

## 4. Running and Inspecting

Start in stdio mode (Claude Desktop connects via stdio):

```
$ molt run dev
# Server waits for MCP messages on stdin — not interactive
# Connect Claude Desktop to test (see section 6)
```

Inspect the server interactively with the MCP Inspector:

```
$ molt run inspect
Starting MCP inspector...
Server started. Open the inspector at: http://localhost:5173

# In the browser UI:
# → Tools tab shows: read_file, list_directory, run_python, query_db
# → Click "run_python" → enter code "print(2 + 2)" → Execute
# ← Result: "4"
# → Click "query_db" → enter "SELECT id, email FROM users" → Execute
# ← Result: [{"id": 1, "email": "alice@example.com"}, ...]
```

Run tests:

```
$ molt run test
========================= test session starts ==========================
collected 16 items

tests/test_filesystem.py::test_read_existing_file      PASSED
tests/test_filesystem.py::test_read_missing_file       PASSED
tests/test_filesystem.py::test_list_directory          PASSED
tests/test_execution.py::test_run_hello_world          PASSED
tests/test_execution.py::test_blocked_import_os        PASSED
tests/test_execution.py::test_timeout_enforced         PASSED
tests/test_database.py::test_select_query              PASSED
tests/test_database.py::test_non_select_blocked        PASSED
...

========================= 16 passed in 1.94s ==========================
```

---

## 5. molt.yaml — Building a Deployable Binary

```yaml
# molt.yaml
build:
  name: mcp-devtools
  entry: mcp_devtools.cli:main
  python: "3.12"
  target: macos/arm64   # build for the machine running Claude Desktop

  commands:
    - name: serve
      args: ["serve"]
      description: "Start the MCP server in stdio mode"

  env:
    DATABASE_URL: ""    # set per-user in Claude Desktop config
    LOG_LEVEL: "WARNING"
```

```
$ molt build
  Resolving platform: macos/arm64
  Bundling mcp_devtools + 29 dependencies...
  Output: dist/mcp-devtools  (21.7 MB)
```

---

## 6. Connecting from Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "devtools": {
      "command": "/Users/alice/tools/mcp-devtools",
      "args": ["serve"],
      "env": {
        "DATABASE_URL": "postgresql+psycopg2://alice:secret@localhost/mydb",
        "ALLOWED_PATHS": "/Users/alice/projects"
      }
    }
  }
}
```

Restart Claude Desktop. In a new conversation:

```
User: List the Python files in ~/projects/api/

Claude: I'll use the list_directory tool.
[Calls list_directory({"path": "/Users/alice/projects/api", "recursive": false})]

Result:
- main.py
- config.py
- models/
- routes/
- tests/

Here are the Python files in your api project: main.py, config.py...
```

---

## 7. Deploy on a Remote Dev Server

```
$ scp dist/mcp-devtools dev@devserver-01:/usr/local/bin/
mcp-devtools                          100%   22MB  41.3MB/s   00:00

# Claude Desktop can connect via SSH tunneled stdio:
# claude_desktop_config.json
{
  "mcpServers": {
    "remote-devtools": {
      "command": "ssh",
      "args": [
        "dev@devserver-01",
        "/usr/local/bin/mcp-devtools serve"
      ],
      "env": {
        "DATABASE_URL": "postgresql+psycopg2://app:secret@localhost/prod_readonly"
      }
    }
  }
}
```

---

## Tips

- **Path allowlisting**: Restrict `read_file` and `list_directory` to `ALLOWED_PATHS`. Never allow traversal above the project root in production.
- **Sandbox depth**: The `run_python` sandbox blocks common escape patterns but is not a true sandbox. For untrusted code, run in a Docker container with network disabled.
- **Read-only DB**: Create a dedicated Postgres role with `SELECT` only, no `INSERT/UPDATE/DELETE`. The query tool enforces the `SELECT` check in code, but defense in depth is better.
- **Transport modes**: `stdio` is simplest for Claude Desktop (local binary). Use the `sse` transport for remote servers — FastAPI serves `GET /sse` and `POST /messages`.
- **Resource endpoints**: MCP also supports "resources" (file-like content), not just tools. Expose frequently accessed files (e.g. project docs) as resources for faster retrieval.
- **Version pinning**: Pin the `mcp` SDK version tightly — the protocol spec evolves and minor versions can break compatibility with older Claude Desktop releases.
