package editor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"molt/internal/syspath"
)

// WritePyright writes <project>/pyrightconfig.json. Unlike the VS Code
// settings file, pyrightconfig is treated as molt-managed: every call
// overwrites it wholesale with the current spec. Pyright/Pylance read
// pythonPath and extraPaths to find the interpreter and the store dirs
// for type-checking and IntelliSense.
func WritePyright(projectDir string, spec *syspath.Spec) error {
	if spec == nil {
		return fmt.Errorf("nil spec")
	}
	cfg := map[string]any{
		"pythonPath": spec.Python,
		"extraPaths": spec.Syspath,
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectDir, "pyrightconfig.json"), append(out, '\n'), 0o644)
}
