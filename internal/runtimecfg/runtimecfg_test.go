package runtimecfg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	cfg := Load(t.TempDir())
	if len(cfg.ExtraPaths) != 0 {
		t.Errorf("expected empty ExtraPaths, got %v", cfg.ExtraPaths)
	}
}

func TestLoad_ParsesAndResolves(t *testing.T) {
	dir := t.TempDir()
	pp := `[project]
name = "demo"

[tool.molt.runtime]
extra_paths = ["vendor/lib", "/abs/dir", "vendor/bin"]
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pp), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load(dir)
	if len(cfg.ExtraPaths) != 3 {
		t.Fatalf("got %d paths, want 3: %v", len(cfg.ExtraPaths), cfg.ExtraPaths)
	}
	// Relative resolved against project dir.
	wantRel := filepath.Join(dir, "vendor", "lib")
	if cfg.ExtraPaths[0] != wantRel {
		t.Errorf("ExtraPaths[0] = %q, want %q", cfg.ExtraPaths[0], wantRel)
	}
	// Absolute kept as-is.
	if cfg.ExtraPaths[1] != "/abs/dir" {
		t.Errorf("ExtraPaths[1] = %q, want /abs/dir", cfg.ExtraPaths[1])
	}
}

func TestApply_PrependsToPATH(t *testing.T) {
	cfg := Config{ExtraPaths: []string{"/a", "/b"}}
	parent := []string{"PATH=/usr/bin:/bin", "FOO=bar"}
	out := cfg.Apply(parent, nil)

	pathVal := ""
	for _, kv := range out {
		if strings.HasPrefix(kv, "PATH=") {
			pathVal = strings.TrimPrefix(kv, "PATH=")
			break
		}
	}
	want := "/a" + string(os.PathListSeparator) + "/b" + string(os.PathListSeparator) + "/usr/bin:/bin"
	if pathVal != want {
		t.Errorf("PATH = %q, want %q", pathVal, want)
	}
}

func TestApply_AddsLinkerVarPerPlatform(t *testing.T) {
	cfg := Config{ExtraPaths: []string{"/lib"}}
	out := cfg.Apply([]string{"PATH=/usr/bin"}, nil)

	var dynamicVar string
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		dynamicVar = "LD_LIBRARY_PATH"
	case "darwin":
		dynamicVar = "DYLD_FALLBACK_LIBRARY_PATH"
	}
	if dynamicVar == "" {
		t.Skip("no dynamic-linker env var to check on " + runtime.GOOS)
	}
	found := false
	for _, kv := range out {
		if strings.HasPrefix(kv, dynamicVar+"=") && strings.Contains(kv, "/lib") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %s containing /lib in env, got %v", dynamicVar, out)
	}
}

func TestApply_NoExtraPathsIsNoop(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "FOO=bar"}
	out := Config{}.Apply(parent, nil)
	if len(out) != len(parent) {
		t.Errorf("expected no change, got %v", out)
	}
}

func TestApply_GlobalEnvSetsButDoesntOverride(t *testing.T) {
	cfg := Config{}
	parent := []string{"PATH=/usr/bin", "EXISTING=already-set"}
	global := map[string]string{
		"NEW_VAR":  "new",
		"EXISTING": "should-not-win",
	}
	out := cfg.Apply(parent, global)

	got := envToMap(out)
	if got["NEW_VAR"] != "new" {
		t.Errorf("NEW_VAR = %q, want new", got["NEW_VAR"])
	}
	if got["EXISTING"] != "already-set" {
		t.Errorf("global should not override parent: EXISTING = %q", got["EXISTING"])
	}
}

func TestApply_ProjectEnvOverridesEverything(t *testing.T) {
	cfg := Config{Env: map[string]string{
		"FOO":      "from-project",
		"NEW":      "project-only",
	}}
	parent := []string{"PATH=/usr/bin", "FOO=from-shell"}
	global := map[string]string{
		"FOO":   "from-global",
		"OTHER": "from-global",
	}
	out := cfg.Apply(parent, global)
	got := envToMap(out)

	// Project beats both shell and global.
	if got["FOO"] != "from-project" {
		t.Errorf("FOO = %q, want from-project", got["FOO"])
	}
	// Global value still wins for vars not in shell or project.
	if got["OTHER"] != "from-global" {
		t.Errorf("OTHER = %q, want from-global", got["OTHER"])
	}
	// Project-only var present.
	if got["NEW"] != "project-only" {
		t.Errorf("NEW = %q, want project-only", got["NEW"])
	}
}

func TestApply_ReservedNamesSkipped(t *testing.T) {
	cfg := Config{Env: map[string]string{
		"PYTHONPATH":  "/should/be/dropped",
		"VIRTUAL_ENV": "/should/be/dropped",
		"PYTHONHOME":  "/should/be/dropped",
		"OK_VAR":      "kept",
	}}
	out := cfg.Apply([]string{"PATH=/usr/bin"}, nil)
	got := envToMap(out)
	for _, k := range []string{"PYTHONPATH", "VIRTUAL_ENV", "PYTHONHOME"} {
		if v, ok := got[k]; ok {
			t.Errorf("reserved %s leaked: %q", k, v)
		}
	}
	if got["OK_VAR"] != "kept" {
		t.Errorf("OK_VAR = %q, want kept", got["OK_VAR"])
	}
}

func TestLoad_ParsesEnvSection(t *testing.T) {
	dir := t.TempDir()
	pp := `[project]
name = "demo"

[tool.molt.runtime.env]
DATABASE_URL = "postgresql://localhost/dev"
LOG_LEVEL    = "DEBUG"
PYTHONPATH   = "should-be-ignored"
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pp), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Load(dir)
	if cfg.Env["DATABASE_URL"] != "postgresql://localhost/dev" {
		t.Errorf("DATABASE_URL not parsed: %q", cfg.Env["DATABASE_URL"])
	}
	if cfg.Env["LOG_LEVEL"] != "DEBUG" {
		t.Errorf("LOG_LEVEL not parsed: %q", cfg.Env["LOG_LEVEL"])
	}
	if _, ok := cfg.Env["PYTHONPATH"]; ok {
		t.Errorf("PYTHONPATH should not be parsed (reserved)")
	}
}

func envToMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		m[kv[:i]] = kv[i+1:]
	}
	return m
}
