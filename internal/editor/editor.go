// Package editor writes molt-managed editor / language-server config.
//
// Per-project state used to live at <project>/.molt/, which IDEs would
// auto-discover. Now state lives under ~/.molt/projects/, so editors need
// to be told where the interpreter and store dirs are. This package writes
// the necessary keys to .vscode/settings.json and pyrightconfig.json.
//
// Files are merged where possible (VS Code: only molt-owned keys are
// touched; everything else is preserved). pyrightconfig.json is treated as
// molt-managed and overwritten wholesale.
package editor

import (
	"fmt"
	"os"
	"path/filepath"

	"molt/internal/syspath"
)

// RefreshIfPresent re-runs whichever editor configs already exist. Called
// at the end of every successful sync so that adding/removing deps keeps
// IntelliSense paths fresh. Errors are logged best-effort to stderr —
// sync should never fail because the editor config got weird.
func RefreshIfPresent(projectDir string, spec *syspath.Spec) {
	if spec == nil || projectDir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".vscode", "settings.json")); err == nil {
		if err := WriteVSCode(projectDir, spec, false); err != nil {
			fmt.Fprintf(os.Stderr, "warn: editor: refresh .vscode/settings.json: %v\n", err)
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, "pyrightconfig.json")); err == nil {
		if err := WritePyright(projectDir, spec); err != nil {
			fmt.Fprintf(os.Stderr, "warn: editor: refresh pyrightconfig.json: %v\n", err)
		}
	}
}

// AutoDetect runs whichever writers' target file already exists. Returns
// the list of editors that were updated. Empty slice if neither was found.
// Used by `molt editor` with no name argument.
func AutoDetect(projectDir string, spec *syspath.Spec) ([]string, error) {
	var done []string
	if _, err := os.Stat(filepath.Join(projectDir, ".vscode")); err == nil {
		if err := WriteVSCode(projectDir, spec, false); err != nil {
			return done, fmt.Errorf("vscode: %w", err)
		}
		done = append(done, "vscode")
	}
	if _, err := os.Stat(filepath.Join(projectDir, "pyrightconfig.json")); err == nil {
		if err := WritePyright(projectDir, spec); err != nil {
			return done, fmt.Errorf("pyright: %w", err)
		}
		done = append(done, "pyright")
	}
	return done, nil
}
