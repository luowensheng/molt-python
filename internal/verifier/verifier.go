package verifier

import (
	"fmt"
	"os"
	"path/filepath"
	"molt/pkg/types"
)

type Verifier struct{}

func New() *Verifier { return &Verifier{} }

func (v *Verifier) Verify(installDir string, m *types.Manifest) error {
	manifestPath := filepath.Join(installDir, ".molt", "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest not found: %s", manifestPath)
	}
	return nil
}

func (v *Verifier) SBOM(installDir string, m *types.Manifest) *types.SBOM {
	return &types.SBOM{
		AppName:      m.AppName,
		Version:      m.Version,
		Installation: installDir,
		Python:       m.Python,
		SystemDeps:   m.SystemDeps,
		PyPackages:   m.PyPackages,
	}
}
