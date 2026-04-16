package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"molt/pkg/types"
)

const manifestFilename = ".molt/manifest.json"

func Save(installDir string, m *types.Manifest) error {
	dir := filepath.Join(installDir, ".molt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(installDir, manifestFilename), data, 0o644)
}

func Load(installDir string) (*types.Manifest, error) {
	path := filepath.Join(installDir, manifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m types.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func Encode(m *types.Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

func Decode(data []byte) (*types.Manifest, error) {
	var m types.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
