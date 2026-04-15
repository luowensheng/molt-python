package types_test

import (
	"testing"

	"pyexec/pkg/types"

	"github.com/stretchr/testify/assert"
)

func TestBuildProfiles(t *testing.T) {
	profiles := []types.BuildProfile{
		types.ProfileMinimal,
		types.ProfileStandard,
		types.ProfileExtended,
		types.ProfileFull,
	}
	for _, p := range profiles {
		assert.NotEmpty(t, string(p))
	}
}

func TestInstallModes(t *testing.T) {
	modes := []types.InstallMode{
		types.ModeMinimal,
		types.ModeStandalone,
		types.ModeExact,
	}
	for _, m := range modes {
		assert.NotEmpty(t, string(m))
	}
}

func TestManifestZeroValue(t *testing.T) {
	var m types.Manifest
	assert.Empty(t, m.AppName)
	assert.Nil(t, m.SystemDeps)
	assert.Nil(t, m.PyPackages)
}

func TestBuildConfigDefaults(t *testing.T) {
	cfg := types.BuildConfig{
		Profile: types.ProfileStandard,
		Name:    "myapp",
		Version: "1.0.0",
	}
	assert.Equal(t, types.ProfileStandard, cfg.Profile)
	assert.False(t, cfg.Offline)
	assert.Nil(t, cfg.EmbedFiles)
}

func TestInstallConfigDefaults(t *testing.T) {
	cfg := types.InstallConfig{
		Mode: types.ModeStandalone,
	}
	assert.Equal(t, types.ModeStandalone, cfg.Mode)
	assert.False(t, cfg.Offline)
	assert.False(t, cfg.DryRun)
}
