package pkgbackend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ── InferLang ────────────────────────────────────────────────────────────────

func TestInferLang(t *testing.T) {
	cases := []struct {
		pkg  string
		want string
		ok   bool
	}{
		// Go module paths (domain/path)
		{"github.com/spf13/cobra", "go", true},
		{"golang.org/x/net", "go", true},
		{"k8s.io/client-go", "go", true},

		// Node scoped packages
		{"@types/node", "node", true},
		{"@scope/pkg", "node", true},
		{"@babel/core", "node", true},

		// C library convention (lib* lowercase)
		{"libpng", "c", true},
		{"libcurl", "c", true},
		{"libsodium", "c", true},
		{"libssl", "c", true},

		// Rust sys crates
		{"openssl-sys", "rust", true},
		{"libz-sys", "rust", true},

		// Rust naming hint (-rs suffix)
		{"tokio-rs", "rust", true},

		// Go naming hint (-go suffix)
		{"go-chi-go", "go", true},

		// Ambiguous — no inference possible
		{"serde", "", false},
		{"requests", "", false},
		{"numpy", "", false},
		{"express", "", false},
		{"cobra", "", false}, // no domain — could be Go or anything
	}
	for _, c := range cases {
		got, ok := InferLang(c.pkg)
		if ok != c.ok {
			t.Errorf("InferLang(%q): ok = %v, want %v", c.pkg, ok, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("InferLang(%q) = %q, want %q", c.pkg, got, c.want)
		}
	}
}

// ── FormatVersionFlag ────────────────────────────────────────────────────────

func TestFormatVersionFlag(t *testing.T) {
	cases := []struct {
		lang, version, want string
	}{
		// Python: comparators pass through; bare version gets ==
		{"python", "2.0", "==2.0"},
		{"python", ">=2.0", ">=2.0"},
		{"python", "<=1.9", "<=1.9"},
		{"python", "^1.6", "^1.6"},
		{"python", "~=1.4", "~=1.4"},
		{"python", "!=1.5", "!=1.5"},
		{"python", "", ""},

		// Rust: --version prefix
		{"rust", "^1.0", "--version ^1.0"},
		{"rust", "1.80.0", "--version 1.80.0"},
		{"rust", "", ""},

		// Go: @v prefix, adding v if absent
		{"go", "1.8.1", "@v1.8.1"},
		{"go", "v1.8.1", "@v1.8.1"},
		{"go", "", ""},

		// Node/Bun/pnpm/Yarn: @ prefix
		{"node", "18.0.0", "@18.0.0"},
		{"bun", "3.0.0", "@3.0.0"},
		{"pnpm", "latest", "@latest"},
		{"yarn", "2.1.0", "@2.1.0"},

		// Swift: --exact prefix
		{"swift", "5.10.0", "--exact 5.10.0"},

		// Unknown lang: space-separated
		{"nim", "1.2.3", "1.2.3"},
		{"zig", "0.13.0", "0.13.0"},
	}
	for _, c := range cases {
		got := FormatVersionFlag(c.lang, c.version)
		if got != c.want {
			t.Errorf("FormatVersionFlag(%q, %q) = %q, want %q", c.lang, c.version, got, c.want)
		}
	}
}

// ── Expand ───────────────────────────────────────────────────────────────────

func TestExpand(t *testing.T) {
	cases := []struct {
		cmd    string
		tokens map[string]string
		want   string
	}{
		{
			"cargo add {package} {version_flag} {flags}",
			map[string]string{"package": "serde", "version_flag": "--version ^1.0", "flags": "--features derive"},
			"cargo add serde --version ^1.0 --features derive",
		},
		{
			"uv add {package}{version_flag} {flags}",
			map[string]string{"package": "numpy", "version_flag": ">=2.0", "flags": ""},
			"uv add numpy>=2.0 ",
		},
		{
			"go get {package}{version_flag} {flags}",
			map[string]string{"package": "github.com/spf13/cobra", "version_flag": "@v1.8.1", "flags": ""},
			"go get github.com/spf13/cobra@v1.8.1 ",
		},
		{
			// Unknown tokens pass through unchanged
			"cmd {package} {unknown_token}",
			map[string]string{"package": "foo"},
			"cmd foo {unknown_token}",
		},
		{
			// Empty tokens
			"npm install {package}{version_flag}",
			map[string]string{"package": "express", "version_flag": ""},
			"npm install express",
		},
	}
	for _, c := range cases {
		got := Expand(c.cmd, c.tokens)
		if got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

// ── Resolve (op + goos) ──────────────────────────────────────────────────────

func TestResolve(t *testing.T) {
	b := Backend{
		Lang:        "node",
		Add:         "npm install {package}",
		Sync:        "npm install",
		AddWindows:  "npm.cmd install {package}",
		SyncWindows: "npm.cmd install",
	}

	if got := b.Resolve(OpAdd, "linux"); got != "npm install {package}" {
		t.Errorf("Resolve(add, linux) = %q", got)
	}
	if got := b.Resolve(OpAdd, "darwin"); got != "npm install {package}" {
		t.Errorf("Resolve(add, darwin) = %q", got)
	}
	if got := b.Resolve(OpAdd, "windows"); got != "npm.cmd install {package}" {
		t.Errorf("Resolve(add, windows) = %q", got)
	}
	if got := b.Resolve(OpSync, "windows"); got != "npm.cmd install" {
		t.Errorf("Resolve(sync, windows) = %q", got)
	}
	// No windows override for remove — falls back to Remove field
	b.Remove = "npm uninstall {package}"
	if got := b.Resolve(OpRemove, "windows"); got != "npm uninstall {package}" {
		t.Errorf("Resolve(remove, windows) = %q", got)
	}
}

// ── ProjectLang ──────────────────────────────────────────────────────────────

func TestProjectLang(t *testing.T) {
	dir := t.TempDir()

	// No config file → default python
	if got := ProjectLang(dir); got != "python" {
		t.Errorf("no config: ProjectLang = %q, want %q", got, "python")
	}

	// pyproject.toml without lang → python
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"myapp\"\n")
	if got := ProjectLang(dir); got != "python" {
		t.Errorf("pyproject no lang: ProjectLang = %q, want %q", got, "python")
	}

	// pyproject.toml with lang = "rust"
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"myapp\"\nlang = \"rust\"\n")
	if got := ProjectLang(dir); got != "rust" {
		t.Errorf("pyproject lang=rust: ProjectLang = %q, want %q", got, "rust")
	}

	// moltproject.toml takes priority over pyproject.toml
	writeFile(t, dir, "moltproject.toml", "[project]\nname = \"native\"\nlang = \"go\"\n")
	if got := ProjectLang(dir); got != "go" {
		t.Errorf("moltproject.toml priority: ProjectLang = %q, want %q", got, "go")
	}

	// moltproject.toml without lang → "mixed"
	writeFile(t, dir, "moltproject.toml", "[project]\nname = \"native\"\n")
	os.Remove(filepath.Join(dir, "pyproject.toml"))
	if got := ProjectLang(dir); got != "mixed" {
		t.Errorf("moltproject no lang: ProjectLang = %q, want %q", got, "mixed")
	}
}

// ── extractLangField ─────────────────────────────────────────────────────────

func TestExtractLangField(t *testing.T) {
	cases := []struct {
		toml string
		want string
	}{
		{"[project]\nlang = \"rust\"\n", "rust"},
		{"[project]\nlang = 'go'\n", "go"},
		{"[project]\nname = \"x\"\n", ""},
		// lang in another section doesn't count
		{"[project]\nname = \"x\"\n\n[tool]\nlang = \"python\"\n", ""},
	}
	for _, c := range cases {
		got := extractLangField([]byte(c.toml))
		if got != c.want {
			t.Errorf("extractLangField(%q) = %q, want %q", c.toml, got, c.want)
		}
	}
}

// ── extractBackendOverride ───────────────────────────────────────────────────

func TestExtractBackendOverride(t *testing.T) {
	toml := `
[project]
name = "myapp"
lang = "node"

[tool.molt.backend]
add     = "bun add {package}{version_flag}"
remove  = "bun remove {package}"
sync    = "bun install"
list    = "bun pm ls"
upgrade = "bun update {package}"
`
	b := extractBackendOverride([]byte(toml))
	if b.Add != "bun add {package}{version_flag}" {
		t.Errorf("Add = %q", b.Add)
	}
	if b.Sync != "bun install" {
		t.Errorf("Sync = %q", b.Sync)
	}
	if b.List != "bun pm ls" {
		t.Errorf("List = %q", b.List)
	}

	// No backend section → zero value
	b2 := extractBackendOverride([]byte("[project]\nname = \"x\"\n"))
	if b2 != (Backend{}) {
		t.Errorf("no section: expected zero Backend, got %+v", b2)
	}
}

// ── Load / Save / Find / Add / Remove / Reset ────────────────────────────────

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pkg-backends.yaml")

	backends := []Backend{
		{Lang: "testlang", Add: "test add {package}", Sync: "test sync"},
	}
	// Save directly to a temp path
	data, err := marshalBackends(backends)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Lang != "testlang" {
		t.Errorf("round-trip: got %+v", loaded)
	}
}

func TestDefaultBackendsComplete(t *testing.T) {
	required := []string{"python", "rust", "go", "node", "bun", "zig", "c"}
	defaults := DefaultBackends()
	index := map[string]bool{}
	for _, b := range defaults {
		index[b.Lang] = true
	}
	for _, lang := range required {
		if !index[lang] {
			t.Errorf("missing default backend for lang %q", lang)
		}
	}
}

func TestDefaultBackendsHaveRequiredOps(t *testing.T) {
	for _, b := range DefaultBackends() {
		if b.Add == "" {
			t.Errorf("backend %q: Add is empty", b.Lang)
		}
		if b.Sync == "" {
			t.Errorf("backend %q: Sync is empty", b.Lang)
		}
		if b.Remove == "" {
			t.Errorf("backend %q: Remove is empty", b.Lang)
		}
	}
}

func TestFindBuiltIn(t *testing.T) {
	// Use a temp home so we don't touch the real ~/.molt/pkg-backends.yaml.
	t.Setenv("HOME", t.TempDir())

	b, ok, err := Find("rust")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Find(rust): not found")
	}
	if !strings.Contains(b.Add, "cargo") {
		t.Errorf("rust backend Add should contain 'cargo', got %q", b.Add)
	}
}

func TestAddRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Use a lang name that has no built-in so removal truly disappears it.
	const lang = "testlang999"
	custom := Backend{
		Lang: lang,
		Add:  "testpkg install {package}",
		Sync: "testpkg install",
	}
	if err := Add(custom); err != nil {
		t.Fatal("Add:", err)
	}

	b, ok, err := Find(lang)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("Find(%s) after Add: not found", lang)
	}
	if b.Add != "testpkg install {package}" {
		t.Errorf("Add command = %q", b.Add)
	}

	// Idempotent update
	custom.Add = "testpkg install {package}{version_flag}"
	if err := Add(custom); err != nil {
		t.Fatal("Add (update):", err)
	}
	b, _, _ = Find(lang)
	if b.Add != "testpkg install {package}{version_flag}" {
		t.Errorf("updated Add = %q", b.Add)
	}

	// Remove
	if err := Remove(lang); err != nil {
		t.Fatal("Remove:", err)
	}
	_, ok, _ = Find(lang)
	// No built-in for this lang, so it should be gone.
	if ok {
		t.Errorf("Find(%s) after Remove: should not be found", lang)
	}

	// Remove non-existent user entry → error
	if err := Remove(lang); err == nil {
		t.Errorf("Remove(%s) again: expected error, got nil", lang)
	}
}

func TestReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Add a custom backend then reset
	_ = Add(Backend{Lang: "testonly", Add: "x", Sync: "y"})
	if err := Reset(); err != nil {
		t.Fatal(err)
	}
	_, ok, _ := Find("testonly")
	if ok {
		t.Error("after Reset, custom backend should not be in user file")
	}
	// Built-ins still accessible
	_, ok, _ = Find("python")
	if !ok {
		t.Error("after Reset, python backend should still be found")
	}
}

// ── ProjectBackend inline override ───────────────────────────────────────────

func TestProjectBackendInlineOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	toml := `[project]
name = "myapp"
lang = "node"

[tool.molt.backend]
add  = "bun add {package}{version_flag}"
sync = "bun install"
`
	writeFile(t, dir, "moltproject.toml", toml)

	b, ok, err := ProjectBackend(dir, "node")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ProjectBackend: not found")
	}
	// Override fields win
	if b.Add != "bun add {package}{version_flag}" {
		t.Errorf("Add = %q", b.Add)
	}
	if b.Sync != "bun install" {
		t.Errorf("Sync = %q", b.Sync)
	}
	// Non-overridden fields come from the node built-in
	if !strings.Contains(b.Remove, "npm") || !strings.Contains(b.Remove, "uninstall") {
		t.Errorf("Remove (should come from npm built-in) = %q", b.Remove)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// marshalBackends and loadFrom are package-internal helpers exposed for testing
// via the same package (white-box test).

func marshalBackends(backends []Backend) ([]byte, error) {
	return yaml.Marshal(backends)
}

func loadFrom(path string) ([]Backend, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Backend
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
