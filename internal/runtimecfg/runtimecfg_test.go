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
	out := cfg.Apply(parent)

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
	out := cfg.Apply([]string{"PATH=/usr/bin"})

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
	out := Config{}.Apply(parent)
	if len(out) != len(parent) {
		t.Errorf("expected no change, got %v", out)
	}
}
