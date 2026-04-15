package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"pyexec/internal/manifest"
	"pyexec/internal/platform"
	"pyexec/pkg/types"
)

// Executor runs Python inside the hermetic environment.
type Executor struct {
	installation types.Installation
	cfg          types.ExecutionConfig
}

// New creates an Executor for the given installation directory.
func New(installDir string, cfg types.ExecutionConfig) (*Executor, error) {
	m, err := manifest.Load(installDir)
	if err != nil {
		return nil, fmt.Errorf("load manifest: %w", err)
	}

	pythonBin := findPython(installDir)

	return &Executor{
		installation: types.Installation{
			AppName:       m.AppName,
			Version:       m.Version,
			Path:          installDir,
			PythonBin:     pythonBin,
			MainModule:    m.MainModule,
			PythonVersion: m.Python.Version,
		},
		cfg: cfg,
	}, nil
}

// Run executes the application with the given arguments.
func (e *Executor) Run(args []string) error {
	manifestPath := filepath.Join(e.installation.Path, ".pyexec", "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("installation corrupted: manifest missing at %s", manifestPath)
	}

	pythonBin := e.installation.PythonBin
	if pythonBin == "" || func() bool { _, err := os.Stat(pythonBin); return os.IsNotExist(err) }() {
		var err error
		pythonBin, err = exec.LookPath(platform.PythonBinaryName(runtime.GOOS))
		if err != nil {
			return fmt.Errorf("python3 not found")
		}
	}

	cmdArgs := append([]string{"-m", e.installation.MainModule}, args...)
	cmd := exec.Command(pythonBin, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = platform.EnvVars(e.installation.Path, runtime.GOOS)

	// Preserve essential host env vars.
	for _, key := range []string{"HOME", "USER", "TERM", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP"} {
		if v := os.Getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}

	// Apply namespace isolation on Linux when requested.
	if e.cfg.UseNamespace {
		if !platform.IsolationSupported() {
			fmt.Fprintf(os.Stderr, "warning: --isolated requested but namespace isolation is not supported on %s\n", runtime.GOOS)
		} else {
			cmd.SysProcAttr = platform.NamespaceSysProcAttr()
		}
	}

	if e.cfg.Debug {
		fmt.Fprintf(os.Stderr, "[debug] exec: %s %v\n", pythonBin, cmdArgs)
		fmt.Fprintf(os.Stderr, "[debug] env: %v\n", cmd.Env)
	}

	return cmd.Run()
}

// findPython searches for the Python binary in the install directory.
func findPython(installDir string) string {
	pyName := platform.PythonBinaryName(runtime.GOOS)
	candidates := []string{
		filepath.Join(installDir, ".venv", "bin", pyName),
		filepath.Join(installDir, ".venv", "Scripts", pyName),
		filepath.Join(installDir, "python", "bin", pyName),
		filepath.Join(installDir, "python", pyName),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// DefaultInstallDir returns the default installation directory for an app/version.
func DefaultInstallDir(appName, version string) (string, error) {
	base, err := platform.DefaultInstallBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, version), nil
}
