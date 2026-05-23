package glue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Config parsing ─────────────────────────────────────────────────────────

const sampleTOML = `
[project]
name = "test"
lang = "python"

[[tool.molt.glue]]
module    = "stats"
lang      = "go"
src       = "./stats"
transport = "stdio"

  [[tool.molt.glue.fn]]
  name    = "mean"
  args    = [{ name = "data", type = "[]f64" }]
  returns = "f64"

  [[tool.molt.glue.fn]]
  name    = "compress"
  args    = [{ name = "payload", type = "bytes" }]
  returns = "bytes"

[[tool.molt.glue]]
module    = "gosha"
lang      = "go"
src       = "crypto/sha256"
transport = "stdio"

  [[tool.molt.glue.fn]]
  name    = "sum256"
  args    = [{ name = "data", type = "bytes" }]
  returns = "bytes"
`

func TestLoadGlueConfig_Parsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moltproject.toml")
	if err := os.WriteFile(path, []byte(sampleTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadGlueConfig(dir)
	if err != nil {
		t.Fatalf("LoadGlueConfig: %v", err)
	}
	if len(cfg.Modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(cfg.Modules))
	}

	stats := cfg.Modules[0]
	if stats.Module != "stats" {
		t.Errorf("module 0: want 'stats', got %q", stats.Module)
	}
	if stats.Lang != "go" {
		t.Errorf("module 0: want lang 'go', got %q", stats.Lang)
	}
	if stats.Src != "./stats" {
		t.Errorf("module 0: want src './stats', got %q", stats.Src)
	}
	if len(stats.Fns) != 2 {
		t.Fatalf("module 0: want 2 fns, got %d", len(stats.Fns))
	}
	if stats.Fns[0].Name != "mean" {
		t.Errorf("fn 0: want 'mean', got %q", stats.Fns[0].Name)
	}
	if len(stats.Fns[0].Args) != 1 || stats.Fns[0].Args[0].Name != "data" || stats.Fns[0].Args[0].Type != "[]f64" {
		t.Errorf("fn 0 args: unexpected %v", stats.Fns[0].Args)
	}
	if stats.Fns[0].Returns != "f64" {
		t.Errorf("fn 0 returns: want 'f64', got %q", stats.Fns[0].Returns)
	}

	sha := cfg.Modules[1]
	if sha.Module != "gosha" {
		t.Errorf("module 1: want 'gosha', got %q", sha.Module)
	}
}

func TestLoadGlueConfig_EmptyDir(t *testing.T) {
	cfg, err := LoadGlueConfig(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Modules) != 0 {
		t.Errorf("expected 0 modules, got %d", len(cfg.Modules))
	}
}

// ── SrcIsLibrary ──────────────────────────────────────────────────────────

func TestSrcIsLibrary_Go(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"./stats", false},
		{"stats/stats.go", false},
		{"/abs/path", false},
		{"crypto/sha256", true},
		{"golang.org/x/crypto/sha3", true},
		{"net/http", true},
		{"github.com/pkg/errors", true},
	}
	for _, c := range cases {
		m := GlueModuleConfig{Lang: "go", Src: c.src}
		if got := m.SrcIsLibrary(); got != c.want {
			t.Errorf("go src=%q: SrcIsLibrary()=%v, want %v", c.src, got, c.want)
		}
	}
}

func TestSrcIsLibrary_Rust(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"compress_glue.rs", false},
		{"./rust-lib", false},
		{"/abs/path", false},
		{"flate2", true},
		{"sha2", true},
	}
	for _, c := range cases {
		m := GlueModuleConfig{Lang: "rust", Src: c.src}
		if got := m.SrcIsLibrary(); got != c.want {
			t.Errorf("rust src=%q: SrcIsLibrary()=%v, want %v", c.src, got, c.want)
		}
	}
}

// ── Driver CRUD ──────────────────────────────────────────────────────────

func TestDriverAddFindRemove(t *testing.T) {
	// Redirect global path to a temp file.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Built-in drivers should always be found.
	d, ok, err := Find("go")
	if err != nil || !ok {
		t.Fatalf("Find('go'): ok=%v err=%v", ok, err)
	}
	if d.BuildCmd == "" {
		t.Error("go driver: empty BuildCmd")
	}

	// Add a custom driver.
	custom := GlueDriver{
		Lang:     "nim",
		BuildCmd: "nim c -d:release -o:{output} {server_file}",
	}
	if err := Add(custom); err != nil {
		t.Fatalf("Add: %v", err)
	}
	d, ok, err = Find("nim")
	if err != nil || !ok {
		t.Fatalf("Find('nim') after Add: ok=%v err=%v", ok, err)
	}
	if d.BuildCmd != custom.BuildCmd {
		t.Errorf("driver BuildCmd mismatch: %q", d.BuildCmd)
	}

	// Remove it.
	if err := Remove("nim"); err != nil {
		t.Fatalf("Remove('nim'): %v", err)
	}
	_, ok, err = Find("nim")
	if err != nil {
		t.Fatalf("Find after Remove: %v", err)
	}
	if ok {
		t.Error("driver should be gone after Remove, but still found")
	}

	// Cannot remove built-ins.
	if err := Remove("go"); err == nil {
		t.Error("Remove('go') should fail (built-in)")
	}
}

func TestDriverReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Add something, then reset.
	_ = Add(GlueDriver{Lang: "nim", BuildCmd: "nim c ..."})
	if err := Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	_, ok, _ := Find("nim")
	if ok {
		t.Error("nim should be gone after Reset")
	}
}

// ── Codegen smoke tests ──────────────────────────────────────────────────

func TestGenerateGoServer_Smoke(t *testing.T) {
	m := GlueModuleConfig{
		Module:    "stats",
		Lang:      "go",
		Src:       "./stats",
		Transport: "stdio",
		Fns: []GlueFn{
			{Name: "mean", Args: []GlueArg{{Name: "data", Type: "[]f64"}}, Returns: "f64"},
			{Name: "compress", Args: []GlueArg{{Name: "payload", Type: "bytes"}}, Returns: "bytes"},
		},
	}

	buildDir := t.TempDir()
	if err := GenerateGoServer(m, buildDir, "user/stats"); err != nil {
		t.Fatalf("GenerateGoServer: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(buildDir, "server.go"))
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	src := string(data)

	checks := []string{
		"package main",
		`"user/stats"`,
		`case "mean":`,
		`case "compress":`,
		"base64.StdEncoding",
	}
	for _, want := range checks {
		if !strings.Contains(src, want) {
			t.Errorf("server.go missing %q", want)
		}
	}
}

func TestGenerateRustServer_Smoke(t *testing.T) {
	m := GlueModuleConfig{
		Module:    "compress",
		Lang:      "rust",
		Transport: "stdio",
		Fns: []GlueFn{
			{Name: "deflate", Args: []GlueArg{{Name: "data", Type: "bytes"}}, Returns: "bytes"},
		},
	}

	buildDir := t.TempDir()
	srcFile := filepath.Join(buildDir, "glue.rs")
	_ = os.WriteFile(srcFile, []byte("pub fn deflate(data: Vec<u8>) -> Vec<u8> { data }\n"), 0o644)

	if err := GenerateRustServer(m, buildDir, srcFile); err != nil {
		t.Fatalf("GenerateRustServer: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(buildDir, "src", "main.rs"))
	if err != nil {
		t.Fatalf("read main.rs: %v", err)
	}
	src := string(data)

	checks := []string{
		"include!(",
		`"deflate"`,
		"general_purpose::STANDARD",
	}
	for _, want := range checks {
		if !strings.Contains(src, want) {
			t.Errorf("main.rs missing %q", want)
		}
	}
}

func TestGeneratePythonClient_Smoke(t *testing.T) {
	m := GlueModuleConfig{
		Module:    "stats",
		Lang:      "go",
		Transport: "stdio",
		Fns: []GlueFn{
			{Name: "mean", Args: []GlueArg{{Name: "data", Type: "[]f64"}}, Returns: "f64"},
			{Name: "compress", Args: []GlueArg{{Name: "payload", Type: "bytes"}}, Returns: "bytes"},
		},
	}

	outDir := t.TempDir()
	if err := GeneratePythonClient(m, outDir); err != nil {
		t.Fatalf("GeneratePythonClient: %v", err)
	}

	pyData, err := os.ReadFile(filepath.Join(outDir, "stats.py"))
	if err != nil {
		t.Fatalf("read stats.py: %v", err)
	}
	py := string(pyData)

	pyChecks := []string{
		"def mean(",
		"def compress(",
		"base64",
		"_call(",
	}
	for _, want := range pyChecks {
		if !strings.Contains(py, want) {
			t.Errorf("stats.py missing %q", want)
		}
	}

	pyiData, err := os.ReadFile(filepath.Join(outDir, "stats.pyi"))
	if err != nil {
		t.Fatalf("read stats.pyi: %v", err)
	}
	pyi := string(pyiData)
	if !strings.Contains(pyi, "def mean(") {
		t.Error("stats.pyi missing 'def mean('")
	}
}

// ── ExpandTokens ─────────────────────────────────────────────────────────

func TestExpandTokens(t *testing.T) {
	cmd := "go build -o {output} {server_dir}"
	result := ExpandTokens(cmd, map[string]string{
		"output":     "/tmp/out",
		"server_dir": "/tmp/build",
	})
	want := "go build -o /tmp/out /tmp/build"
	if result != want {
		t.Errorf("ExpandTokens: got %q, want %q", result, want)
	}
}
