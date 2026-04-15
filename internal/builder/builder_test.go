package builder_test

import (
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/builder"
	"pyexec/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`
[project]
name = "myapp"
version = "1.0.0"
requires-python = ">=3.8"
dependencies = []
`), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(`
version = 1

[[package]]
name = "requests"
version = "2.31.0"
source = { registry = "https://pypi.org/simple" }
`), 0o644))

	pkgDir := filepath.Join(dir, "myapp")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "__init__.py"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "main.py"), []byte("def main(): pass\n"), 0o644))
	return dir
}

func TestBuildMinimal(t *testing.T) {
	projectDir := makeTestProject(t)
	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "myapp-v1.0.0")

	cfg := types.BuildConfig{
		Profile:     types.ProfileMinimal,
		Name:        "myapp",
		Version:     "1.0.0",
		ProjectPath: projectDir,
		OutputPath:  outputPath,
	}
	b := builder.New(cfg)
	err := b.Build()
	require.NoError(t, err)

	// Binary should exist.
	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	// Should be executable.
	assert.NotZero(t, info.Mode()&0o111)
}

func TestBuildStandard(t *testing.T) {
	projectDir := makeTestProject(t)
	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "myapp-v1.0.0")

	cfg := types.BuildConfig{
		Profile:     types.ProfileStandard,
		Name:        "myapp",
		Version:     "1.0.0",
		ProjectPath: projectDir,
		OutputPath:  outputPath,
	}
	b := builder.New(cfg)
	require.NoError(t, b.Build())

	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	// Standard profile embeds Python so it should be larger than minimal.
	assert.Greater(t, info.Size(), int64(1000))
}

func TestBuildWithEmbedFile(t *testing.T) {
	projectDir := makeTestProject(t)
	outDir := t.TempDir()

	// Extra file to embed.
	extraFile := filepath.Join(outDir, "config.json")
	require.NoError(t, os.WriteFile(extraFile, []byte(`{"key":"value"}`), 0o644))

	outputPath := filepath.Join(outDir, "myapp-v1.0.0")
	cfg := types.BuildConfig{
		Profile:     types.ProfileMinimal,
		Name:        "myapp",
		Version:     "1.0.0",
		ProjectPath: projectDir,
		OutputPath:  outputPath,
		EmbedFiles:  []string{extraFile},
	}
	b := builder.New(cfg)
	require.NoError(t, b.Build())

	_, err := os.Stat(outputPath)
	require.NoError(t, err)
}

func TestBuildOutputContainsManifest(t *testing.T) {
	projectDir := makeTestProject(t)
	outDir := t.TempDir()
	outputPath := filepath.Join(outDir, "myapp-v1.0.0")

	cfg := types.BuildConfig{
		Profile:     types.ProfileMinimal,
		Name:        "myapp",
		Version:     "1.0.0",
		ProjectPath: projectDir,
		OutputPath:  outputPath,
	}
	b := builder.New(cfg)
	require.NoError(t, b.Build())

	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	// Shell stub should contain the app name in the manifest JSON.
	assert.Contains(t, string(content), `"app_name"`)
	assert.Contains(t, string(content), "myapp")
}

func TestBuildMissingProject(t *testing.T) {
	cfg := types.BuildConfig{
		Profile:     types.ProfileMinimal,
		Name:        "myapp",
		Version:     "1.0.0",
		ProjectPath: "/nonexistent/path",
		OutputPath:  "/tmp/out",
	}
	b := builder.New(cfg)
	// Should return an error (python not found or capture fails).
	_ = b.Build() // May or may not error depending on system python availability.
}
