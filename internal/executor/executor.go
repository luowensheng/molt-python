package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"molt/pkg/types"
	"runtime"
)

type Executor struct{}

func New(installDir string, cfg types.ExecutionConfig) (*Executor, error) {
	return &Executor{}, nil
}

func (e *Executor) Run(args []string) error {
	return fmt.Errorf("not implemented")
}

func DefaultInstallDir(appName, version string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = os.Getenv("APPDATA")
	default:
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, appName, version), nil
}
