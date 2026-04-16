package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

func DefaultInstallBase() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support"), nil
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			return d, nil
		}
		return filepath.Join(home, "AppData", "Roaming"), nil
	default:
		return filepath.Join(home, ".local", "share"), nil
	}
}

func DefaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "molt"), nil
	case "windows":
		d := os.Getenv("LOCALAPPDATA")
		if d == "" {
			d = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(d, "molt", "cache"), nil
	default:
		return filepath.Join(home, ".cache", "molt"), nil
	}
}
