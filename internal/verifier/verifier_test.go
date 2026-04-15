package verifier_test

import (
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/manifest"
	"pyexec/internal/verifier"
	"pyexec/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeInstallation(t *testing.T) (string, *types.Manifest) {
	t.Helper()
	dir := t.TempDir()

	m := &types.Manifest{
		AppName:    "testapp",
		Version:    "1.0.0",
		MainModule: "testapp.main",
		Profile:    types.ProfileMinimal,
		Python:     types.PythonSpec{Version: "3.12.1"},
	}
	require.NoError(t, manifest.Save(dir, m))

	// Create a fake python binary.
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "python3"), []byte("#!/bin/sh\npython3 $@\n"), 0o755))

	return dir, m
}

func TestVerifySuccess(t *testing.T) {
	dir, m := makeInstallation(t)
	v := verifier.New()
	err := v.Verify(dir, m)
	require.NoError(t, err)
}

func TestVerifyMissingManifest(t *testing.T) {
	dir := t.TempDir()
	m := &types.Manifest{AppName: "testapp", Version: "1.0.0"}

	v := verifier.New()
	err := v.Verify(dir, m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifest")
}

func TestVerifyChecksumMismatch(t *testing.T) {
	dir, _ := makeInstallation(t)

	m := &types.Manifest{
		AppName: "testapp",
		Version: "1.0.0",
		SystemDeps: []types.SystemDep{
			{Name: "libfake.so", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
	// Place the lib file with wrong content.
	libDir := filepath.Join(dir, "lib")
	require.NoError(t, os.MkdirAll(libDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(libDir, "libfake.so"), []byte("wrong content"), 0o644))

	v := verifier.New()
	err := v.Verify(dir, m)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestSBOM(t *testing.T) {
	dir, m := makeInstallation(t)
	v := verifier.New()
	sbom := v.SBOM(dir, m)

	assert.Equal(t, "testapp", sbom.AppName)
	assert.Equal(t, "1.0.0", sbom.Version)
	assert.Equal(t, dir, sbom.Installation)
	assert.Equal(t, "3.12.1", sbom.Python.Version)
}
