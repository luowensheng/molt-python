# molt MCP Server

molt ships a built-in [Model Context Protocol](https://spec.modelcontextprotocol.io/) (MCP) server. Running `molt mcp` starts a JSON-RPC 2.0 stdio server that exposes nine molt operations as AI tools, letting Claude Code, Cursor, and any other MCP-compatible client call them directly.

---

## Quick start (Claude Code)

**1. Install molt** (see README → Installation).

**2. Add `.claude/mcp.json` to your molt project:**

```json
{
  "mcpServers": {
    "molt": {
      "command": "molt",
      "args": ["mcp"],
      "description": "molt Python toolchain"
    }
  }
}
```

**3. Open the project in Claude Code.** The `molt_*` tools appear in the tool list. You can now ask Claude to add packages, run tasks, etc.:

> "Add numpy and pandas to the project"  
> "Run the tests"  
> "Build a release binary"

---

## Available tools

| Tool | Equivalent command | Description |
|---|---|---|
| `molt_init` | `molt init [name]` | Scaffold a new molt project |
| `molt_sync` | `molt sync` | Install all dependencies |
| `molt_add` | `molt add <pkgs>` | Add one or more packages |
| `molt_remove` | `molt remove <pkgs>` | Remove packages |
| `molt_run` | `molt run <task>` | Run a task, script, or binary |
| `molt_exec` | `molt exec <cmd>` | Run an arbitrary command in the project env |
| `molt_build` | `molt build` | Build a self-contained binary |
| `molt_info` | `molt info` | Show project summary |
| `molt_python` | `molt python <subcmd>` | Manage Python versions |

---

## Tool input schemas

### `molt_init`
```json
{
  "name":     "my-app",      // optional
  "template": "fastapi",     // optional
  "python":   "3.12"         // optional
}
```

### `molt_add`
```json
{
  "packages": ["numpy", "pandas>=2.0"],  // required
  "dev":      false                       // optional
}
```

### `molt_remove`
```json
{ "packages": ["numpy"] }
```

### `molt_run`
```json
{
  "task": "test",              // required — task name, script path, or binary
  "args": ["--verbose", "-k", "auth"]  // optional extra args
}
```

### `molt_exec`
```json
{ "command": "python -m pytest -x" }
```

### `molt_build`
```json
{ "output": "dist/my-app" }  // optional
```

### `molt_python`
```json
{ "subcommand": "use 3.12" }
// Also: "list", "install 3.13", "which", "remove 3.11"
```

---

## Testing the server manually

```bash
# Initialize
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}' \
  | molt mcp

# List tools
echo '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | molt mcp

# Call a tool
echo '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"molt_info","arguments":{}}}' \
  | molt mcp
```

---

## Other MCP clients

Any client that supports the MCP stdio transport works. The server config format varies by client:

**Cursor** (`~/.cursor/mcp.json` or project `.cursor/mcp.json`):
```json
{
  "mcpServers": {
    "molt": { "command": "molt", "args": ["mcp"] }
  }
}
```

**Generic stdio client**: pass `molt mcp` as the command, send JSON-RPC 2.0 messages on stdin, read responses on stdout.

---

## Architecture note

`molt mcp` re-invokes the molt binary for each tool call (`os.Args[0] <subcommand>`). This means:
- The MCP server inherits the working directory of the calling process (your project root)
- Environment variables like `MOLT_PROJECT` override the working directory if needed
- Tool errors are returned as MCP content with `isError: true`, not as JSON-RPC protocol errors
