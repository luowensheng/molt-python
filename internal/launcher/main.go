// Command launcher is the target-side runner embedded inside every molt binary.
// It reads molt_INSTALL_BASE to determine where to install.
//
// Environment variables:
//
//	molt_INSTALL_BASE   Override the base installation directory.
//	                      e.g. molt_INSTALL_BASE=/opt ./myapp-v1.0.0 install
//	                      installs to /opt/myapp/1.0.0/
//
//	molt_INSTALL_DIR    Override the full installation directory (skips appName/version suffix).
//	                      e.g. molt_INSTALL_DIR=/opt/myapp ./myapp-v1.0.0 install
//
//	molt_CACHE_DIR      Override the cache directory for downloaded artifacts.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"molt/internal/uvbin"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Manifest mirrors types.Manifest but is self-contained (no imports from main molt).
type Manifest struct {
	AppName    string      `json:"app_name"`
	Version    string      `json:"version"`
	MainModule string      `json:"main_module"`
	Python     PythonSpec  `json:"python"`
	SystemDeps []SystemDep `json:"system_deps"`
	PyPackages []PyPackage `json:"py_packages"`
	Profile    string      `json:"profile"`
}

type PythonSpec struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Embedded bool   `json:"embedded"`
}

type SystemDep struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Embedded bool   `json:"embedded"`
}

type PyPackage struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Embedded bool   `json:"embedded"`
}

const trailerSize = 8

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "install":
		err = cmdInstall(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "uninstall":
		err = cmdUninstall(os.Args[2:])
	case "version":
		err = cmdVersion()
	case "info":
		err = cmdInfo()
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	self := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `Usage: %s <command> [flags]

Commands:
  install    [--prefix DIR] [--mode MODE] [--offline] [--verbose]
  run        [-- args...]
  verify
  uninstall
  version
  info

Environment variables:
  molt_INSTALL_BASE   Base directory for installation (default: platform data dir)
                        The app is installed to $molt_INSTALL_BASE/<app>/<version>/
                        Example: molt_INSTALL_BASE=/opt %s install

  molt_INSTALL_DIR    Full installation directory (overrides molt_INSTALL_BASE).
                        Example: molt_INSTALL_DIR=/opt/myapp %s install

  molt_CACHE_DIR      Directory for downloaded artifacts cache.
                        Example: molt_CACHE_DIR=/var/cache/molt %s install

`, self, self, self, self)
}

// ── Install ──────────────────────────────────────────────────────────────────

func cmdInstall(args []string) error {
	verbose := hasFlag(args, "--verbose", "-v")
	offline := hasFlag(args, "--offline")
	mode := flagValue(args, "--mode", "standalone")

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	m, err := readManifest(self)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	// Resolve installation directory — three sources in priority order:
	//   1. --prefix flag (highest)
	//   2. molt_INSTALL_DIR env (full path)
	//   3. molt_INSTALL_BASE env + app/version suffix
	//   4. Platform default base + app/version suffix (lowest)
	installDir := flagValue(args, "--prefix", "")
	if installDir == "" {
		installDir = resolveInstallDir(m.AppName, m.Version)
	}

	if verbose {
		fmt.Printf("Install base: %s\n", filepath.Dir(filepath.Dir(installDir)))
		fmt.Printf("Install dir:  %s\n", installDir)
	}

	fmt.Printf("Installing %s v%s (%s mode) to %s...\n",
		m.AppName, m.Version, mode, installDir)

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	if err := extractPayload(self, installDir, verbose); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	if err := installPython(installDir, m, offline, verbose); err != nil {
		return fmt.Errorf("install python: %w", err)
	}

	if err := createVenv(installDir, verbose); err != nil {
		return fmt.Errorf("create venv: %w", err)
	}

	if len(m.PyPackages) > 0 && !offline {
		if err := installPackages(installDir, m.PyPackages, verbose); err != nil {
			if verbose {
				fmt.Printf("  Warning: package install: %v\n", err)
			}
		}
	}

	// Write a receipt file recording how/where this was installed.
	writeReceipt(installDir, m, mode)

	fmt.Printf("  ✓ Installation complete: %s\n", installDir)
	fmt.Printf("\nTo run: %s run\n", os.Args[0])
	fmt.Printf("To uninstall: %s uninstall\n", os.Args[0])
	return nil
}

func createVenv(installDir string, verbose bool) error {
	venvDir := filepath.Join(installDir, ".venv")
	if _, err := os.Stat(venvDir); err == nil {
		return nil
	}

	uv, err := findUV()
	if err != nil {
		// Fall back to python -m venv if uv is unavailable.
		python := findPython(installDir)
		if python == "" {
			var lookErr error
			python, lookErr = exec.LookPath(pythonBinaryName())
			if lookErr != nil {
				return fmt.Errorf("neither uv nor python found")
			}
		}
		if verbose {
			fmt.Println("  uv not found — creating venv via python -m venv...")
		}
		return exec.Command(python, "-m", "venv", venvDir).Run()
	}

	if verbose {
		fmt.Println("  Creating virtual environment via uv venv...")
	}
	cmd := exec.Command(uv, "venv", venvDir)
	cmd.Dir = installDir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uv venv: %w", err)
	}
	return nil
}

// installPackages installs Python packages using `uv pip install`.
func installPackages(installDir string, pkgs []PyPackage, verbose bool) error {
	uv, err := findUV()
	if err != nil {
		return fmt.Errorf("uv not found — cannot install packages: %w", err)
	}

	if verbose {
		fmt.Printf("  Installing %d packages via uv pip install...\n", len(pkgs))
	}

	venvPath := filepath.Join(installDir, ".venv")
	args := []string{"pip", "install", "--quiet"}
	for _, p := range pkgs {
		args = append(args, fmt.Sprintf("%s==%s", p.Name, p.Version))
	}

	cmd := exec.Command(uv, args...)
	cmd.Dir = installDir
	// Tell uv which venv to target.
	cmd.Env = append(os.Environ(), "VIRTUAL_ENV="+venvPath)
	return cmd.Run()
}

// resolveInstallDir returns the installation directory using env var overrides.
//
//	Priority:
//	  molt_INSTALL_DIR  →  use as-is
//	  molt_INSTALL_BASE →  base/<appName>/<version>
//	  platform default    →  ~/.local/share/<appName>/<version>  (Linux)
func resolveInstallDir(appName, version string) string {
	// Highest priority: full directory override.
	if dir := os.Getenv("molt_INSTALL_DIR"); dir != "" {
		return dir
	}

	// Second priority: base directory override.
	base := os.Getenv("molt_INSTALL_BASE")
	if base == "" {
		base = defaultInstallBase()
	}
	return filepath.Join(base, appName, version)
}

// defaultInstallBase returns the platform-appropriate base directory.
func defaultInstallBase() string {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			return d
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Roaming")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support")
	default: // linux and others
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share")
	}
}

// resolveCacheDir returns the cache directory, respecting molt_CACHE_DIR.
func resolveCacheDir() string {
	if dir := os.Getenv("molt_CACHE_DIR"); dir != "" {
		return dir
	}
	switch runtime.GOOS {
	case "windows":
		d := os.Getenv("LOCALAPPDATA")
		if d == "" {
			home, _ := os.UserHomeDir()
			d = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(d, "molt", "cache")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Caches", "molt")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".cache", "molt")
	}
}

// writeReceipt writes a small JSON file recording install metadata.
func writeReceipt(installDir string, m *Manifest, mode string) {
	type Receipt struct {
		AppName     string `json:"app_name"`
		Version     string `json:"version"`
		Mode        string `json:"mode"`
		InstallDir  string `json:"install_dir"`
		InstalledBy string `json:"installed_by"`
	}
	r := Receipt{
		AppName:     m.AppName,
		Version:     m.Version,
		Mode:        mode,
		InstallDir:  installDir,
		InstalledBy: os.Args[0],
	}
	data, _ := json.MarshalIndent(r, "", "  ")
	os.WriteFile(filepath.Join(installDir, ".molt", "receipt.json"), data, 0o644)
}

// ── Run ──────────────────────────────────────────────────────────────────────

// ── Verify ───────────────────────────────────────────────────────────────────

func cmdVerify(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := readManifest(self)
	if err != nil {
		return err
	}

	installDir := resolveInstallDir(m.AppName, m.Version)

	// Override with --prefix if given.
	if p := flagValue(args, "--prefix", ""); p != "" {
		installDir = p
	}

	manifestPath := filepath.Join(installDir, ".molt", "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("not installed at %s\nRun: %s install", installDir, os.Args[0])
	}
	fmt.Printf("  ✓ Installation verified: %s\n", installDir)
	return nil
}

// ── Uninstall ────────────────────────────────────────────────────────────────

func cmdUninstall(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := readManifest(self)
	if err != nil {
		return err
	}

	installDir := resolveInstallDir(m.AppName, m.Version)
	if p := flagValue(args, "--prefix", ""); p != "" {
		installDir = p
	}

	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		return fmt.Errorf("not installed at %s", installDir)
	}

	fmt.Printf("Uninstalling %s v%s from %s...\n", m.AppName, m.Version, installDir)
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	fmt.Printf("  ✓ Uninstalled\n")
	return nil
}

// ── Version ──────────────────────────────────────────────────────────────────

func cmdVersion() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := readManifest(self)
	if err != nil {
		return err
	}
	fmt.Printf("%s v%s\n", m.AppName, m.Version)
	return nil
}

// ── Info ─────────────────────────────────────────────────────────────────────

func cmdInfo() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := readManifest(self)
	if err != nil {
		return err
	}

	installDir := resolveInstallDir(m.AppName, m.Version)
	installed := "no"
	if _, err := os.Stat(filepath.Join(installDir, ".molt", "manifest.json")); err == nil {
		installed = "yes"
	}

	fmt.Printf("App:          %s\n", m.AppName)
	fmt.Printf("Version:      %s\n", m.Version)
	fmt.Printf("Profile:      %s\n", m.Profile)
	fmt.Printf("Python:       %s\n", m.Python.Version)
	fmt.Printf("Packages:     %d\n", len(m.PyPackages))
	fmt.Printf("System deps:  %d\n", len(m.SystemDeps))
	fmt.Println()
	fmt.Printf("Install dir:  %s\n", installDir)
	fmt.Printf("Installed:    %s\n", installed)
	fmt.Println()
	fmt.Println("Override install location:")
	fmt.Printf("  molt_INSTALL_BASE=/opt %s install\n", filepath.Base(os.Args[0]))
	fmt.Printf("  molt_INSTALL_DIR=/opt/%s %s install\n", m.AppName, filepath.Base(os.Args[0]))
	return nil
}

// ── Payload extraction ───────────────────────────────────────────────────────

func readManifest(binaryPath string) (*Manifest, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	offset, err := readPayloadOffset(f)
	if err != nil {
		return nil, fmt.Errorf("read payload offset: %w", err)
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name == ".molt/manifest.json" {
			var m Manifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return nil, err
			}
			return &m, nil
		}
	}
	return nil, fmt.Errorf("manifest not found in payload")
}

func extractPayload(binaryPath, installDir string, verbose bool) error {
	f, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer f.Close()

	offset, err := readPayloadOffset(f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(installDir, filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}
			os.Chmod(target, os.FileMode(hdr.Mode))
			if verbose {
				fmt.Printf("  extracted: %s\n", hdr.Name)
			}
		}
	}
	return nil
}

func readPayloadOffset(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() < trailerSize {
		return 0, fmt.Errorf("binary too small")
	}
	if _, err := f.Seek(-trailerSize, io.SeekEnd); err != nil {
		return 0, err
	}
	var offset int64
	if err := binary.Read(f, binary.LittleEndian, &offset); err != nil {
		return 0, err
	}
	return offset, nil
}

// ── Python management ────────────────────────────────────────────────────────

func installPython(installDir string, m *Manifest, offline, verbose bool) error {
	pythonDir := filepath.Join(installDir, "python")
	pyBin := filepath.Join(pythonDir, "bin", pythonBinaryName())
	if runtime.GOOS == "windows" {
		pyBin = filepath.Join(pythonDir, "python.exe")
	}
	if _, err := os.Stat(pyBin); err == nil {
		if verbose {
			fmt.Printf("  Python %s already present\n", m.Python.Version)
		}
		return nil
	}
	if m.Python.Embedded {
		return nil
	}
	if offline {
		return symlinkSystemPython(installDir)
	}
	if m.Python.URL == "" {
		return symlinkSystemPython(installDir)
	}
	if verbose {
		fmt.Printf("  Downloading Python %s...\n", m.Python.Version)
	}
	tmp, err := os.CreateTemp(resolveCacheDir(), "python-*.tar.gz")
	if err != nil {
		// Fall back to system temp if cache dir doesn't exist.
		os.MkdirAll(resolveCacheDir(), 0o755)
		tmp, err = os.CreateTemp("", "python-*.tar.gz")
		if err != nil {
			return err
		}
	}
	defer os.Remove(tmp.Name())
	if err := downloadFile(m.Python.URL, tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("download python: %w", err)
	}
	tmp.Close()
	os.MkdirAll(pythonDir, 0o755)
	return extractTarGz(tmp.Name(), pythonDir)
}

func symlinkSystemPython(installDir string) error {
	systemPython, err := exec.LookPath(pythonBinaryName())
	if err != nil {
		return fmt.Errorf("python not found (tried standalone + system lookup)")
	}
	binDir := filepath.Join(installDir, "python", "bin")
	os.MkdirAll(binDir, 0o755)
	dest := filepath.Join(binDir, pythonBinaryName())
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	return os.Symlink(systemPython, dest)
}

// ── Environment ──────────────────────────────────────────────────────────────

func buildEnv(installDir string) []string {
	pythonHome := filepath.Join(installDir, ".venv")
	srcDir := filepath.Join(installDir, "src")
	binDir := filepath.Join(installDir, "python", "bin")
	venvBin := filepath.Join(installDir, ".venv", "bin")
	libDir := filepath.Join(installDir, "lib")

	var env []string
	switch runtime.GOOS {
	case "windows":
		scripts := filepath.Join(installDir, ".venv", "Scripts")
		pythonBin := filepath.Join(installDir, "python")
		env = []string{
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"PATH=" + pythonBin + ";" + scripts + ";" + os.Getenv("PATH"),
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	case "darwin":
		env = []string{
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"DYLD_LIBRARY_PATH=" + libDir,
			"PATH=" + binDir + ":" + venvBin + ":/usr/bin:/bin",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	default:
		env = []string{
			"LD_LIBRARY_PATH=" + libDir,
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"PATH=" + binDir + ":" + venvBin + ":/usr/bin:/bin",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	}
	for _, key := range []string{"HOME", "USER", "TERM", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// ── Path helpers ──────────────────────────────────────────────────────────────

func findPython(installDir string) string {
	candidates := []string{
		filepath.Join(installDir, ".venv", "bin", pythonBinaryName()),
		filepath.Join(installDir, ".venv", "Scripts", pythonBinaryName()),
		filepath.Join(installDir, "python", "bin", pythonBinaryName()),
		filepath.Join(installDir, "python", pythonBinaryName()),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func findPip(installDir string) string {
	name := "pip"
	if runtime.GOOS == "windows" {
		name = "pip.exe"
	}
	for _, p := range []string{
		filepath.Join(installDir, ".venv", "bin", name),
		filepath.Join(installDir, ".venv", "Scripts", name),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func pythonBinaryName() string {
	if runtime.GOOS == "windows" {
		return "python.exe"
	}
	return "python3"
}

// ── Download / extract helpers ────────────────────────────────────────────────

func downloadFile(url string, dst *os.File) error {
	for _, tool := range []string{"curl", "wget"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		var args []string
		if tool == "curl" {
			args = []string{"-L", "-o", dst.Name(), url}
		} else {
			args = []string{"-O", dst.Name(), url}
		}
		return exec.Command(tool, args...).Run()
	}
	return fmt.Errorf("curl or wget required to download Python")
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		parts := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(parts[1]))
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}
			os.Chmod(target, os.FileMode(hdr.Mode))
		case tar.TypeSymlink:
			os.Symlink(hdr.Linkname, target)
		}
	}
	return nil
}

// ── Argument helpers ──────────────────────────────────────────────────────────

func hasFlag(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}

func flagValue(args []string, flag, def string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return def
}

// internal/launcher/main.go
func findUV() (string, error) {
	// 1. MOLT_UV override (strict)
	if p := os.Getenv(uvbin.EnvOverride); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%s=%q: file not found", uvbin.EnvOverride, p)
	}

	// 2. Managed location
	home, _ := os.UserHomeDir()
	managed := filepath.Join(home, ".molt", "uv", "bin", "uv")
	if runtime.GOOS == "windows" {
		managed = filepath.Join(filepath.Dir(managed), "uv.exe")
	}
	if _, err := os.Stat(managed); err == nil {
		return managed, nil
	}

	// 3. Auto-download via uvbin.Install (handles extraction properly)
	fmt.Println("  Bootstrapping uv...")
	if err := uvbin.Install(uvbin.PinnedVersion, false); err != nil {
		return "", fmt.Errorf("auto-install uv: %w", err)
	}
	return managed, nil
}

// Update cmdRun to use uv run:
func cmdRun(args []string) error {
	self, _ := os.Executable()
	m, _ := readManifest(self)
	installDir := resolveInstallDir(m.AppName, m.Version)

	uv, err := findUV()
	if err != nil {
		return fmt.Errorf("uv required to run: %w", err)
	}

	mainModule := m.MainModule
	if mainModule == "" {
		mainModule = m.AppName + ".main"
	}

	// uv run automatically activates the nearest .venv or uses system python
	cmd := exec.Command(uv, "run", "-m", mainModule)
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = installDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = buildEnv(installDir) // keep env setup
	return cmd.Run()
}
