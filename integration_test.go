//go:build integration

package main

// Integration tests for molt's external-facing behaviour.
// These tests require the molt binary to be built first:
//
//	make build          (or: go build -o molt .)
//	go test -tags integration ./...
//
// The tests spawn molt as a subprocess so they exercise the full CLI surface,
// including the MCP server, without any import-cycle concerns.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// moltBin returns the path to the molt binary under test.
// Prefers ./molt in the repo root; falls back to whatever is on PATH.
func moltBin(t *testing.T) string {
	t.Helper()
	candidates := []string{"./molt", "molt"}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	path, err := exec.LookPath("molt")
	if err != nil {
		t.Skip("molt binary not found — run 'make build' first")
	}
	return path
}

// runMolt runs molt with the given args and returns combined stdout+stderr.
func runMolt(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(moltBin(t), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return out.String(), code
}

// ── Version / help smoke tests ────────────────────────────────────────────────

func TestVersionFlag(t *testing.T) {
	out, code := runMolt(t, "--version")
	if code != 0 {
		t.Fatalf("--version exited %d: %s", code, out)
	}
	// Expect "molt X.Y.Z (...)"
	if !strings.HasPrefix(out, "molt ") {
		t.Errorf("unexpected --version output: %q", out)
	}
	t.Logf("version: %s", strings.TrimSpace(out))
}

func TestHelpFlag(t *testing.T) {
	out, _ := runMolt(t, "--help")
	for _, keyword := range []string{"init", "sync", "add", "run", "build", "mcp"} {
		if !strings.Contains(out, keyword) {
			t.Errorf("help output missing keyword %q", keyword)
		}
	}
}

// ── MCP server tests ──────────────────────────────────────────────────────────

// mcpSession sends one line of JSON to `molt mcp` and returns the parsed response.
func mcpSession(t *testing.T, messages ...string) []map[string]any {
	t.Helper()

	bin := moltBin(t)
	cmd := exec.Command(bin, "mcp")
	cmd.Stdin = strings.NewReader(strings.Join(messages, "\n") + "\n")
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Non-zero exit is OK — stdin closed, server exits cleanly.
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("molt mcp failed to start: %v", err)
		}
	}

	var results []map[string]any
	scanner := bufio.NewScanner(&outBuf)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-JSON from MCP server: %q", line)
		}
		results = append(results, m)
	}
	return results
}

var initMsg = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

func TestMCPInitialize(t *testing.T) {
	responses := mcpSession(t, initMsg)
	if len(responses) == 0 {
		t.Fatal("no response from MCP server")
	}
	resp := responses[0]

	if resp["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", resp["jsonrpc"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got: %v", resp)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "molt" {
		t.Errorf("expected serverInfo.name=molt, got %v", info["name"])
	}
	t.Logf("initialize ok: serverInfo=%v", info)
}

func TestMCPToolsList(t *testing.T) {
	listMsg := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	responses := mcpSession(t, initMsg, listMsg)

	// Find the tools/list response (id=2)
	var listResp map[string]any
	for _, r := range responses {
		if fmt.Sprint(r["id"]) == "2" {
			listResp = r
		}
	}
	if listResp == nil {
		t.Fatalf("no response for tools/list; got %v", responses)
	}

	result, _ := listResp["result"].(map[string]any)
	toolsRaw, _ := result["tools"].([]any)
	if len(toolsRaw) == 0 {
		t.Fatal("tools/list returned no tools")
	}

	wantTools := []string{
		"molt_init", "molt_sync", "molt_add", "molt_remove",
		"molt_run", "molt_exec", "molt_build", "molt_info", "molt_python",
	}
	names := map[string]bool{}
	for _, raw := range toolsRaw {
		m, _ := raw.(map[string]any)
		names[fmt.Sprint(m["name"])] = true
	}
	for _, want := range wantTools {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
	t.Logf("tools/list ok: %d tools", len(toolsRaw))
}

func TestMCPToolCallInfo(t *testing.T) {
	// molt_info requires a synced project; we just check the protocol works —
	// a non-zero exit still returns a valid tools/call response.
	callMsg := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"molt_info","arguments":{}}}`
	responses := mcpSession(t, initMsg, callMsg)

	var callResp map[string]any
	for _, r := range responses {
		if fmt.Sprint(r["id"]) == "3" {
			callResp = r
		}
	}
	if callResp == nil {
		t.Fatalf("no response for tools/call molt_info")
	}
	// Must have a result (not a protocol error)
	if callResp["error"] != nil {
		t.Fatalf("protocol error: %v", callResp["error"])
	}
	result, _ := callResp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("tools/call returned empty content")
	}
	t.Logf("molt_info tool call ok (isError=%v)", result["isError"])
}

func TestMCPUnknownTool(t *testing.T) {
	callMsg := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"molt_does_not_exist","arguments":{}}}`
	responses := mcpSession(t, initMsg, callMsg)

	var callResp map[string]any
	for _, r := range responses {
		if fmt.Sprint(r["id"]) == "4" {
			callResp = r
		}
	}
	if callResp == nil {
		t.Fatalf("no response for unknown tool call")
	}
	result, _ := callResp["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("expected isError=true for unknown tool, got: %v", result)
	}
}

func TestMCPVersionInServerInfo(t *testing.T) {
	responses := mcpSession(t, initMsg)
	if len(responses) == 0 {
		t.Fatal("no MCP response")
	}
	result, _ := responses[0]["result"].(map[string]any)
	info, _ := result["serverInfo"].(map[string]any)
	ver, _ := info["version"].(string)
	if ver == "" {
		t.Error("serverInfo.version is empty")
	}
	semver := regexp.MustCompile(`^\d+\.\d+`)
	if !semver.MatchString(ver) {
		t.Errorf("version %q doesn't look like semver", ver)
	}
}
