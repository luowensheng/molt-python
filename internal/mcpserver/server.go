// Package mcpserver implements a Model Context Protocol (MCP) stdio server
// for molt. Running `molt mcp` starts this server, exposing core molt
// operations as AI tools that Claude Code, Cursor, and other MCP clients can
// call directly from a coding session.
//
// Protocol: JSON-RPC 2.0 over stdin/stdout, one message per line.
// Spec: https://spec.modelcontextprotocol.io/
package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ── JSON-RPC types ────────────────────────────────────────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── MCP types ─────────────────────────────────────────────────────────────────

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ── Tool definitions ──────────────────────────────────────────────────────────

var tools = []tool{
	{
		Name:        "molt_init",
		Description: "Scaffold a new molt Python project in the current directory.",
		InputSchema: schema(props{
			"name":     strProp("Project name (optional; defaults to directory name)"),
			"template": strProp("Template name, e.g. 'fastapi', 'cli' (optional)"),
			"python":   strProp("Python version to pin, e.g. '3.12' (optional)"),
		}, nil),
	},
	{
		Name:        "molt_sync",
		Description: "Install all dependencies from the lockfile into the project environment. Run after editing pyproject.toml or after molt_add/molt_remove.",
		InputSchema: schema(props{}, nil),
	},
	{
		Name:        "molt_add",
		Description: "Add one or more Python packages to the project and sync the environment.",
		InputSchema: schema(props{
			"packages": {
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Package names to add, e.g. [\"numpy\", \"requests>=2.31\"]",
			},
			"dev": boolProp("Add as dev dependency (default false)"),
		}, []string{"packages"}),
	},
	{
		Name:        "molt_remove",
		Description: "Remove one or more Python packages from the project.",
		InputSchema: schema(props{
			"packages": {
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Package names to remove",
			},
		}, []string{"packages"}),
	},
	{
		Name:        "molt_run",
		Description: "Run a named task, a Python script (.py), or a Mojo script (.mojo) inside the project environment.",
		InputSchema: schema(props{
			"task": strProp("Task name, script path (e.g. main.py), or binary name"),
			"args": {
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Extra arguments passed to the task/script (optional)",
			},
		}, []string{"task"}),
	},
	{
		Name:        "molt_exec",
		Description: "Run an arbitrary command inside the project environment (correct PYTHONPATH, activated venv, etc.).",
		InputSchema: schema(props{
			"command": strProp("Command to execute, e.g. \"python -c 'import numpy; print(numpy.__version__)'\""),
		}, []string{"command"}),
	},
	{
		Name:        "molt_build",
		Description: "Build a single self-contained binary that embeds the Python project and all its dependencies. No Python required on the target machine.",
		InputSchema: schema(props{
			"output": strProp("Output binary path (optional; defaults to project name)"),
		}, nil),
	},
	{
		Name:        "molt_info",
		Description: "Show a summary of the current molt project: dependencies, Python version, tasks, environment.",
		InputSchema: schema(props{}, nil),
	},
	{
		Name:        "molt_python",
		Description: "Manage Python versions for the project (list, install, use, which).",
		InputSchema: schema(props{
			"subcommand": strProp("One of: list, install <ver>, use <ver>, which, remove <ver>"),
		}, []string{"subcommand"}),
	},
}

// ── Schema helpers ────────────────────────────────────────────────────────────

type props = map[string]map[string]any

func schema(properties props, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// ── Server ────────────────────────────────────────────────────────────────────

// RunMCPServer starts the stdio MCP server loop. It reads one JSON-RPC
// request per line from stdin and writes responses to stdout. It runs
// until stdin is closed. ver is the molt version string (from -X main.version).
func RunMCPServer(ver string) error {
	mcpVersion = ver

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024) // 4 MB line buffer
	enc := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		// Notifications (method starts with "notifications/") must not receive
		// a response per JSON-RPC 2.0 spec.
		if strings.HasPrefix(req.Method, "notifications/") {
			continue
		}

		resp := handle(req)
		_ = enc.Encode(resp)
	}
	return scanner.Err()
}

// mcpVersion holds the version string passed from main at startup.
var mcpVersion string

func handle(req request) response {
	switch req.Method {
	case "initialize":
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: initResult{
				ProtocolVersion: "2024-11-05",
				Capabilities:    map[string]any{"tools": map[string]any{}},
				ServerInfo:      serverInfo{Name: "molt", Version: serverVersion()},
			},
		}

	case "tools/list":
		return response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"tools": tools},
		}

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, -32602, "invalid params: "+err.Error())
		}
		return callTool(req.ID, p.Name, p.Arguments)

	default:
		return errResp(req.ID, -32601, "method not found: "+req.Method)
	}
}

func callTool(id json.RawMessage, name string, args map[string]any) response {
	argv, err := buildArgv(name, args)
	if err != nil {
		return toolResp(id, "", err)
	}

	molt := os.Args[0] // re-invoke the molt binary
	cmd := exec.Command(molt, argv...)
	cmd.Dir, _ = os.Getwd()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	runErr := cmd.Run()
	return toolResp(id, out.String(), runErr)
}

// buildArgv converts a tool name + arguments map into a molt CLI argv slice.
func buildArgv(name string, args map[string]any) ([]string, error) {
	str := func(key string) string {
		v, _ := args[key].(string)
		return v
	}
	boolVal := func(key string) bool {
		v, _ := args[key].(bool)
		return v
	}
	strs := func(key string) []string {
		v, _ := args[key].([]any)
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}

	switch name {
	case "molt_init":
		argv := []string{"init"}
		if t := str("template"); t != "" {
			argv = append(argv, "--template", t)
		}
		if py := str("python"); py != "" {
			argv = append(argv, "--python", py)
		}
		if n := str("name"); n != "" {
			argv = append(argv, n)
		}
		return argv, nil

	case "molt_sync":
		return []string{"sync"}, nil

	case "molt_add":
		pkgs := strs("packages")
		if len(pkgs) == 0 {
			return nil, fmt.Errorf("packages is required")
		}
		argv := []string{"add"}
		if boolVal("dev") {
			argv = append(argv, "--dev")
		}
		return append(argv, pkgs...), nil

	case "molt_remove":
		pkgs := strs("packages")
		if len(pkgs) == 0 {
			return nil, fmt.Errorf("packages is required")
		}
		return append([]string{"remove"}, pkgs...), nil

	case "molt_run":
		task := str("task")
		if task == "" {
			return nil, fmt.Errorf("task is required")
		}
		argv := []string{"run", task}
		if extra := strs("args"); len(extra) > 0 {
			argv = append(argv, extra...)
		}
		return argv, nil

	case "molt_exec":
		command := str("command")
		if command == "" {
			return nil, fmt.Errorf("command is required")
		}
		// Split simple commands; for shell features users can wrap in sh -c.
		parts := strings.Fields(command)
		return append([]string{"exec"}, parts...), nil

	case "molt_build":
		argv := []string{"build"}
		if o := str("output"); o != "" {
			argv = append(argv, "--output", o)
		}
		return argv, nil

	case "molt_info":
		return []string{"info"}, nil

	case "molt_python":
		sub := str("subcommand")
		if sub == "" {
			return nil, fmt.Errorf("subcommand is required")
		}
		return append([]string{"python"}, strings.Fields(sub)...), nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// toolResp constructs a tools/call result. If runErr is non-nil the result
// is marked isError=true but still returns a 200-level JSON-RPC response
// (per MCP spec — tool errors are content, not protocol errors).
func toolResp(id json.RawMessage, output string, runErr error) response {
	text := output
	isErr := false
	if runErr != nil {
		isErr = true
		if text == "" {
			text = runErr.Error()
		}
	}
	if text == "" {
		text = "done"
	}
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Result: toolResult{
			Content: []toolContent{{Type: "text", Text: text}},
			IsError: isErr,
		},
	}
}

func errResp(id json.RawMessage, code int, msg string) response {
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

// serverVersion returns the molt version string passed at startup via RunMCPServer.
// Falls back to "dev" if called before the server has been started.
func serverVersion() string {
	if mcpVersion != "" {
		return mcpVersion
	}
	return "dev"
}
