package capturer_test

import (
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/capturer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeProject creates a minimal valid Python project in dir.
func makeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`
[project]
name = "testapp"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = []
`), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(`
version = 1

[[package]]
name = "requests"
version = "2.31.0"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "urllib3"
version = "2.0.7"
source = { registry = "https://pypi.org/simple" }
`), 0o644))

	pkgDir := filepath.Join(dir, "testapp")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "__init__.py"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "main.py"), []byte(`
def main():
    print("hello")
`), 0o644))

	return dir
}

func TestCapture(t *testing.T) {
	dir := makeProject(t)
	c := capturer.New(false)
	snap, err := c.Capture(dir)
	require.NoError(t, err)

	// Python version should be populated.
	assert.NotEmpty(t, snap.Python.Version)

	// Should have captured the source files.
	assert.NotEmpty(t, snap.Source)
	var foundMain bool
	for _, sf := range snap.Source {
		if sf.RelPath == "testapp/main.py" {
			foundMain = true
			break
		}
	}
	assert.True(t, foundMain, "main.py should be in source files")

	// Packages parsed from uv.lock.
	assert.Len(t, snap.PyPackages, 2)
	names := make(map[string]bool)
	for _, p := range snap.PyPackages {
		names[p.Name] = true
	}
	assert.True(t, names["requests"])
	assert.True(t, names["urllib3"])
}

func TestCaptureSourceFiltering(t *testing.T) {
	dir := makeProject(t)

	// Add some files that should be ignored.
	venvDir := filepath.Join(dir, ".venv", "lib")
	require.NoError(t, os.MkdirAll(venvDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(venvDir, "ignore.py"), []byte("skip"), 0o644))

	cacheDir := filepath.Join(dir, "__pycache__")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "ignore.pyc"), []byte("skip"), 0o644))

	c := capturer.New(false)
	snap, err := c.Capture(dir)
	require.NoError(t, err)

	for _, sf := range snap.Source {
		assert.NotContains(t, sf.RelPath, ".venv")
		assert.NotContains(t, sf.RelPath, "__pycache__")
	}
}

func TestCaptureUvLockParsing(t *testing.T) {
	dir := makeProject(t)
	c := capturer.New(false)
	snap, err := c.Capture(dir)
	require.NoError(t, err)

	for _, pkg := range snap.PyPackages {
		assert.NotEmpty(t, pkg.Name)
		assert.NotEmpty(t, pkg.Version)
		assert.NotEmpty(t, pkg.URL)
	}
}
