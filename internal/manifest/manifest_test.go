package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/manifest"
	"pyexec/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeManifest() *types.Manifest {
	return &types.Manifest{
		AppName:    "testapp",
		Version:    "1.0.0",
		MainModule: "testapp.main",
		Profile:    types.ProfileStandard,
		Python: types.PythonSpec{
			Version: "3.12.1",
			URL:     "https://example.com/python.tar.gz",
			SHA256:  "abc123",
		},
		SystemDeps: []types.SystemDep{
			{Name: "libssl.so.3", SHA256: "def456"},
		},
		PyPackages: []types.PyPackage{
			{Name: "requests", Version: "2.31.0", URL: "https://pypi.org/requests", SHA256: "ghi789"},
		},
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	m := makeManifest()

	require.NoError(t, manifest.Save(dir, m))
	loaded, err := manifest.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, m.AppName, loaded.AppName)
	assert.Equal(t, m.Version, loaded.Version)
	assert.Equal(t, m.Python.Version, loaded.Python.Version)
	assert.Len(t, loaded.SystemDeps, 1)
	assert.Len(t, loaded.PyPackages, 1)
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	m := makeManifest()
	require.NoError(t, manifest.Save(dir, m))
	_, err := os.Stat(filepath.Join(dir, ".pyexec", "manifest.json"))
	require.NoError(t, err)
}

func TestLoadNotFound(t *testing.T) {
	_, err := manifest.Load("/nonexistent/path")
	assert.Error(t, err)
}

func TestEncodeAndDecode(t *testing.T) {
	m := makeManifest()
	data, err := manifest.Encode(m)
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	decoded, err := manifest.Decode(data)
	require.NoError(t, err)
	assert.Equal(t, m.AppName, decoded.AppName)
	assert.Equal(t, m.MainModule, decoded.MainModule)
}

func TestDecodeInvalid(t *testing.T) {
	_, err := manifest.Decode([]byte("not json"))
	assert.Error(t, err)
}

func TestFilterNotCached(t *testing.T) {
	dir := t.TempDir()
	m := makeManifest()
	// Mark dep as embedded (should be excluded).
	m.SystemDeps[0].Embedded = true

	filtered := manifest.FilterNotCached(m, dir)
	// Embedded dep should not be in filtered.
	assert.Empty(t, filtered.SystemDeps)

	// Package is not cached and not embedded.
	assert.Len(t, filtered.PyPackages, 1)
}

func TestFilterNotCachedWithCachedFile(t *testing.T) {
	dir := t.TempDir()
	m := makeManifest()
	key := m.PyPackages[0].SHA256

	// Simulate the package being in cache.
	require.NoError(t, os.WriteFile(filepath.Join(dir, key), []byte("data"), 0o644))

	filtered := manifest.FilterNotCached(m, dir)
	assert.Empty(t, filtered.PyPackages)
}
