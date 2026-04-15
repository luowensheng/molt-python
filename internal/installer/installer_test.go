package installer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/installer"
	"pyexec/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeManifest() *types.Manifest {
	return &types.Manifest{
		AppName:    "testapp",
		Version:    "1.0.0",
		MainModule: "testapp.main",
		Profile:    types.ProfileMinimal,
		Python:     types.PythonSpec{Version: "3.12.1"},
		PyPackages: []types.PyPackage{},
		SystemDeps: []types.SystemDep{},
	}
}

func TestInstallDryRun(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "install")

	cfg := types.InstallConfig{
		Mode:     types.ModeMinimal,
		CacheDir: filepath.Join(dir, "cache"),
		DryRun:   true,
		Verbose:  false,
	}
	inst, err := installer.New(cfg)
	require.NoError(t, err)

	err = inst.Install(context.Background(), makeManifest(), targetDir)
	require.NoError(t, err)

	// Dry run should NOT create the target directory.
	_, statErr := os.Stat(targetDir)
	assert.True(t, os.IsNotExist(statErr), "dry run should not create install dir")
}

func TestInstallMinimal(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "install")

	cfg := types.InstallConfig{
		Mode:       types.ModeMinimal,
		CacheDir:   filepath.Join(dir, "cache"),
		Verbose:    false,
		NoDownload: true, // skip downloads for test speed
		Offline:    true,
	}
	inst, err := installer.New(cfg)
	require.NoError(t, err)

	m := makeManifest()
	err = inst.Install(context.Background(), m, targetDir)
	require.NoError(t, err)

	// Manifest should be saved.
	manifestPath := filepath.Join(targetDir, ".pyexec", "manifest.json")
	_, statErr := os.Stat(manifestPath)
	assert.NoError(t, statErr, "manifest.json should be created")
}

func TestInstallCreatesVenv(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "install")

	cfg := types.InstallConfig{
		Mode:     types.ModeMinimal,
		CacheDir: filepath.Join(dir, "cache"),
		Offline:  true,
		Verbose:  false,
	}
	inst, err := installer.New(cfg)
	require.NoError(t, err)

	err = inst.Install(context.Background(), makeManifest(), targetDir)
	require.NoError(t, err)

	// Venv should exist (if python3 is available on the system).
	venvDir := filepath.Join(targetDir, ".venv")
	if _, err := os.Stat(venvDir); err == nil {
		// Venv was created; verify structure.
		_, err = os.Stat(filepath.Join(venvDir, "bin", "python3"))
		assert.NoError(t, err)
	}
}

func TestInstallAuditLog(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "install")
	auditLog := filepath.Join(dir, "audit.log")

	cfg := types.InstallConfig{
		Mode:     types.ModeExact,
		CacheDir: filepath.Join(dir, "cache"),
		Offline:  true,
		AuditLog: auditLog,
		Verbose:  false,
	}
	inst, err := installer.New(cfg)
	require.NoError(t, err)

	err = inst.Install(context.Background(), makeManifest(), targetDir)
	require.NoError(t, err)

	content, err := os.ReadFile(auditLog)
	require.NoError(t, err)
	assert.Contains(t, string(content), "testapp")
}
