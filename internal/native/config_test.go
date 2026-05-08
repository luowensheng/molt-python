package native

import (
	"os"
	"path/filepath"
	"testing"

	"molt/internal/kernelbuilder"
)

// TestLoadKernelConfig_AutoExtendsFromGlobalBuilders verifies that adding a
// builder for a new extension to ~/.molt/kernel-builders.yaml automatically
// makes that extension a watched source extension — so users never have to
// edit pyproject.toml's source_extensions list manually.
func TestLoadKernelConfig_AutoExtendsFromGlobalBuilders(t *testing.T) {
	// Sandbox the global YAML to a tmp HOME so the test is hermetic.
	origHome := os.Getenv("HOME")
	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	defer os.Setenv("HOME", origHome)

	// 1. Defaults only.
	dir := t.TempDir()
	cfg := LoadKernelConfig(dir)
	for _, ext := range []string{".zig", ".c", ".cpp"} {
		if !contains(cfg.SourceExtensions, ext) {
			t.Errorf("default SourceExtensions missing %q: %v", ext, cfg.SourceExtensions)
		}
	}
	if contains(cfg.SourceExtensions, ".odin") {
		t.Errorf("expected no .odin in defaults: %v", cfg.SourceExtensions)
	}

	// 2. Add an odin builder globally → .odin should be watched.
	if err := kernelbuilder.Add(kernelbuilder.Builder{
		Ext:     "odin",
		Command: "odin build {source} -file -build-mode:obj -out:{output}",
	}); err != nil {
		t.Fatal(err)
	}
	cfg = LoadKernelConfig(dir)
	if !contains(cfg.SourceExtensions, ".odin") {
		t.Errorf("after adding odin builder, expected .odin in SourceExtensions: %v",
			cfg.SourceExtensions)
	}
}

func TestLoadKernelConfig_PerProjectBuildersAlsoExtend(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	defer os.Setenv("HOME", origHome)

	// Project pyproject with a nim builder block.
	dir := t.TempDir()
	pp := `[project]
name = "demo"

[tool.molt.native_kernel.build.nim]
command = "nim c --app:staticlib --out:{output} {source}"
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pp), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadKernelConfig(dir)
	if !contains(cfg.SourceExtensions, ".nim") {
		t.Errorf("project-level nim builder should auto-watch .nim: %v",
			cfg.SourceExtensions)
	}
	if cfg.Builders["nim"] == "" {
		t.Errorf("project Builders missing nim: %v", cfg.Builders)
	}
}

func contains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}
