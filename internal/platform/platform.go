// Package platform provides OS-specific behaviour behind a uniform interface.
// All other packages import this instead of calling syscall or exec.Command("ldd") directly.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// OS constants mirroring runtime.GOOS for readability.
const (
	Linux   = "linux"
	Darwin  = "darwin"
	Windows = "windows"
)

// Current returns the running OS.
func Current() string { return runtime.GOOS }

// CurrentArch returns the running CPU architecture.
func CurrentArch() string { return runtime.GOARCH }

// PythonBinaryName returns the name of the Python executable for the given OS.
func PythonBinaryName(goos string) string {
	if goos == Windows {
		return "python.exe"
	}
	return "python3"
}

// LibExtension returns the shared library extension for the given OS.
func LibExtension(goos string) string {
	switch goos {
	case Darwin:
		return ".dylib"
	case Windows:
		return ".dll"
	default:
		return ".so"
	}
}

// ExeSuffix returns ".exe" on Windows, "" elsewhere.
func ExeSuffix(goos string) string {
	if goos == Windows {
		return ".exe"
	}
	return ""
}

// DefaultInstallBase returns the per-user application data directory.
func DefaultInstallBase() (string, error) {
	switch runtime.GOOS {
	case Windows:
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		return appdata, nil
	case Darwin:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	default: // linux and others
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

// DefaultCacheDir returns the per-user cache directory for PyExec.
func DefaultCacheDir() (string, error) {
	switch runtime.GOOS {
	case Windows:
		localappdata := os.Getenv("LOCALAPPDATA")
		if localappdata == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			localappdata = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localappdata, "pyexec", "cache"), nil
	case Darwin:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Caches", "pyexec"), nil
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cache", "pyexec"), nil
	}
}

// PythonStandaloneURL returns the python-build-standalone download URL
// for the given Python version, target OS, and architecture.
func PythonStandaloneURL(version, goos, goarch string) (string, error) {
	// Map Go arch names to python-build-standalone arch names.
	archMap := map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
	}
	pbsArch, ok := archMap[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported arch: %s", goarch)
	}

	// Map GOOS to python-build-standalone platform strings.
	var platform string
	switch goos {
	case Linux:
		platform = pbsArch + "-unknown-linux-gnu"
	case Darwin:
		platform = pbsArch + "-apple-darwin"
	case Windows:
		platform = pbsArch + "-pc-windows-msvc"
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}

	suffix := "install_only.tar.gz"
	// Use a fixed release tag; in production this would be looked up dynamically.
	const releaseTag = "20240107"
	filename := fmt.Sprintf("cpython-%s+%s-%s-%s.tar.gz", version, releaseTag, platform, suffix)

	return fmt.Sprintf(
		"https://github.com/indygreg/python-build-standalone/releases/download/%s/%s",
		releaseTag, filename,
	), nil
}

// EnvVars returns the environment variables needed to run Python from installDir
// on the current OS.
func EnvVars(installDir, goos string) []string {
	pythonHome := filepath.Join(installDir, ".venv")
	binDir := filepath.Join(installDir, "python", "bin")
	venvBin := filepath.Join(installDir, ".venv", "bin")
	libDir := filepath.Join(installDir, "lib")
	srcDir := filepath.Join(installDir, "src")

	switch goos {
	case Windows:
		pythonScripts := filepath.Join(installDir, ".venv", "Scripts")
		pythonBin := filepath.Join(installDir, "python")
		return []string{
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"PATH=" + pythonBin + ";" + pythonScripts + ";C:\\Windows\\system32;C:\\Windows",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	case Darwin:
		return []string{
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"DYLD_LIBRARY_PATH=" + libDir,
			"PATH=" + binDir + ":" + venvBin + ":/usr/bin:/bin",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	default: // linux
		return []string{
			"LD_LIBRARY_PATH=" + libDir,
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"PATH=" + binDir + ":" + venvBin + ":/usr/bin:/bin",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	}
}

// IsolationSupported reports whether Linux namespace isolation is available.
// This is determined at runtime, not compile time, because the platform package
// is shared across all OS builds.
func IsolationSupported() bool {
	if runtime.GOOS != Linux {
		return false
	}
	_, err := os.Stat("/proc/self/ns/mnt")
	return err == nil
}

// InstallPythonDir returns the path where Python should be installed
// inside an install directory.
func InstallPythonDir(installDir string) string {
	return filepath.Join(installDir, "python")
}

// VenvDir returns the path of the venv inside an install directory.
func VenvDir(installDir string) string {
	return filepath.Join(installDir, ".venv")
}

// LibDir returns the path of the private library directory inside an install directory.
func LibDir(installDir string) string {
	return filepath.Join(installDir, "lib")
}
