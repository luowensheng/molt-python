package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"pyexec/pkg/types"
)

const manifestFilename = ".pyexec/manifest.json"

// Save writes m to <installDir>/.pyexec/manifest.json.
func Save(installDir string, m *types.Manifest) error {
	dir := filepath.Join(installDir, ".pyexec")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create manifest dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(installDir, manifestFilename), data, 0o644)
}

// Load reads the manifest from an installation directory.
func Load(installDir string) (*types.Manifest, error) {
	path := filepath.Join(installDir, manifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m types.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// Encode returns the JSON bytes of m (for embedding in the binary).
func Encode(m *types.Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Decode parses raw JSON bytes into a Manifest.
func Decode(data []byte) (*types.Manifest, error) {
	var m types.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// FilterNotCached returns the subset of deps/packages not present in cacheDir.
func FilterNotCached(m *types.Manifest, cacheDir string) *types.Manifest {
	filtered := *m
	filtered.SystemDeps = filterDeps(m.SystemDeps, cacheDir)
	filtered.PyPackages = filterPkgs(m.PyPackages, cacheDir)
	return &filtered
}

func filterDeps(deps []types.SystemDep, cacheDir string) []types.SystemDep {
	var out []types.SystemDep
	for _, d := range deps {
		if d.Embedded {
			continue
		}
		if _, err := os.Stat(filepath.Join(cacheDir, d.SHA256)); os.IsNotExist(err) {
			out = append(out, d)
		}
	}
	return out
}

func filterPkgs(pkgs []types.PyPackage, cacheDir string) []types.PyPackage {
	var out []types.PyPackage
	for _, p := range pkgs {
		if p.Embedded {
			continue
		}
		if _, err := os.Stat(filepath.Join(cacheDir, p.SHA256)); os.IsNotExist(err) {
			out = append(out, p)
		}
	}
	return out
}
