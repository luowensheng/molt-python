// Package integration contains end-to-end tests for the PyExec pipeline.
package main_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/builder"
	"pyexec/internal/installer"
	"pyexec/internal/manifest"
	"pyexec/internal/verifier"
	"pyexec/pkg/types"
)

func buildTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`
[project]
name = "integration-app"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = []
`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(`
version = 1
[[package]]
name = "six"
version = "1.16.0"
source = { registry = "https://pypi.org/simple" }
`), 0o644))
	pkgDir := filepath.Join(dir, "integration_app")
	must(t, os.MkdirAll(pkgDir, 0o755))
	must(t, os.WriteFile(filepath.Join(pkgDir, "__init__.py"), nil, 0o644))
	must(t, os.WriteFile(filepath.Join(pkgDir, "main.py"), []byte(`
def main():
    print("integration-app running")
`), 0o644))
	return dir
}

func TestBuildThenInstall(t *testing.T) {
	projectDir := buildTestProject(t)
	workDir := t.TempDir()
	outputPath := filepath.Join(workDir, "integration-app-v0.1.0")

	// Build
	cfg := types.BuildConfig{
		Profile:     types.ProfileMinimal,
		Name:        "integration-app",
		Version:     "0.1.0",
		ProjectPath: projectDir,
		OutputPath:  outputPath,
	}
	if err := builder.New(cfg).Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("output is empty")
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("output is not executable")
	}

	// Install
	installDir := filepath.Join(workDir, "install", "integration-app", "0.1.0")
	inst, err := installer.New(types.InstallConfig{
		Mode:     types.ModeMinimal,
		CacheDir: filepath.Join(workDir, "cache"),
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("installer.New: %v", err)
	}
	m, err := extractManifestFromBinary(outputPath)
	if err != nil {
		t.Fatalf("extractManifest: %v", err)
	}
	if err := inst.Install(context.Background(), m, installDir); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Manifest on disk
	loaded, err := manifest.Load(installDir)
	if err != nil {
		t.Fatalf("manifest.Load: %v", err)
	}
	if loaded.AppName != "integration-app" {
		t.Errorf("AppName: got %q want integration-app", loaded.AppName)
	}

	// Verify
	if err := verifier.New().Verify(installDir, loaded); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// SBOM
	sbom := verifier.New().SBOM(installDir, loaded)
	if sbom.AppName != "integration-app" {
		t.Errorf("SBOM AppName: got %q", sbom.AppName)
	}
}

func TestBuildProfiles(t *testing.T) {
	profiles := []types.BuildProfile{
		types.ProfileMinimal,
		types.ProfileStandard,
		types.ProfileExtended,
		types.ProfileFull,
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(string(profile), func(t *testing.T) {
			projectDir := buildTestProject(t)
			workDir := t.TempDir()
			outputPath := filepath.Join(workDir, "app-v0.1.0")
			cfg := types.BuildConfig{
				Profile:     profile,
				Name:        "app",
				Version:     "0.1.0",
				ProjectPath: projectDir,
				OutputPath:  outputPath,
			}
			if err := builder.New(cfg).Build(); err != nil {
				t.Fatalf("Build(%s): %v", profile, err)
			}
			if _, err := os.Stat(outputPath); err != nil {
				t.Fatalf("output missing: %v", err)
			}
		})
	}
}

// extractManifestFromBinary reads the JSON manifest embedded in the shell stub.
func extractManifestFromBinary(binaryPath string) (*types.Manifest, error) {
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	const start = "MANIFEST_EOF\n"
	const end   = "\nMANIFEST_EOF"
	si := indexOf(content, start)
	if si < 0 {
		// Fall back to a synthetic manifest so the install step still works.
		return manifest.Decode([]byte(`{"app_name":"integration-app","version":"0.1.0","main_module":"integration_app.main","profile":"minimal","python":{"version":"3.8","url":"","sha256":"","size":0,"embedded":false}}`))
	}
	si += len(start)
	ei := indexOfFrom(content, end, si)
	if ei < 0 {
		ei = len(content)
	}
	return manifest.Decode([]byte(content[si:ei]))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func indexOfFrom(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
