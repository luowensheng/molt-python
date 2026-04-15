package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"pyexec/pkg/types"
)

// Verifier checks that an installation matches its manifest.
type Verifier struct{}

// New creates a Verifier.
func New() *Verifier { return &Verifier{} }

// Verify checks that the installed environment matches the manifest.
func (v *Verifier) Verify(installDir string, m *types.Manifest) error {
	// Check Python binary exists.
	pythonBin := filepath.Join(installDir, "bin", "python3")
	if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
		// Accept venv Python as well.
		pythonBin = filepath.Join(installDir, ".venv", "bin", "python3")
		if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
			return fmt.Errorf("python3 not found in installation")
		}
	}

	// Verify system deps that have checksums and are present.
	libDir := filepath.Join(installDir, "lib")
	for _, dep := range m.SystemDeps {
		if dep.SHA256 == "" {
			continue
		}
		libPath := filepath.Join(libDir, dep.Name)
		if _, err := os.Stat(libPath); os.IsNotExist(err) {
			continue // system library; not copied, that's OK
		}
		got, err := hashFile(libPath)
		if err != nil {
			return fmt.Errorf("hash %s: %w", dep.Name, err)
		}
		if dep.SHA256 != "" && got != dep.SHA256 {
			return fmt.Errorf("checksum mismatch for %s: expected %s got %s",
				dep.Name, dep.SHA256, got)
		}
	}

	// Check that manifest file exists.
	manifestPath := filepath.Join(installDir, ".pyexec", "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("manifest not found: %s", manifestPath)
	}

	return nil
}

// SBOM generates a Software Bill of Materials.
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

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
