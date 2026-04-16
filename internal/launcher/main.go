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
	"io/fs"
	"molt/internal/uvbin"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

// ── Logger ────────────────────────────────────────────────────────────────────

// logger is a small helper that gates verbose output and tracks timing.
type logger struct {
	verbose bool
	start   time.Time
}

func newLogger(verbose bool) *logger {
	return &logger{verbose: verbose, start: time.Now()}
}

// log prints a message only when verbose is enabled.
func (l *logger) log(format string, args ...any) {
	if l.verbose {
		fmt.Printf("  "+format+"\n", args...)
	}
}

// logCmd prints the exact command that is about to be executed.
// Format:  $ /path/to/binary arg1 arg2  (cwd: /some/dir)
func (l *logger) logCmd(cmd *exec.Cmd) {
	if !l.verbose {
		return
	}
	parts := make([]string, 0, len(cmd.Args))
	for _, a := range cmd.Args {
		if strings.ContainsAny(a, " \t\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
		} else {
			parts = append(parts, a)
		}
	}
	cwd := cmd.Dir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	fmt.Printf("  $ %s  (cwd: %s)\n", strings.Join(parts, " "), cwd)
}

// elapsed prints the wall-clock time since the logger was created.
func (l *logger) elapsed(label string) {
	if l.verbose {
		fmt.Printf("  ✓ %s (%.1fs)\n", label, time.Since(l.start).Seconds())
	}
}

// step prints a named step header regardless of verbose level.
func step(format string, args ...any) {
	fmt.Printf("[molt] "+format+"\n", args...)
}

// execCmd runs cmd, optionally streaming stdout/stderr, and always logs the
// exact invocation when verbose is enabled.
func execCmd(cmd *exec.Cmd, l *logger) error {
	l.logCmd(cmd)
	if l.verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command %q failed: %w", cmd.Args[0], err)
	}
	return nil
}

// ── Main ──────────────────────────────────────────────────────────────────────

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
	l := newLogger(hasFlag(args, "--verbose", "-v"))
	offline := hasFlag(args, "--offline")
	mode := flagValue(args, "--mode", "standalone")

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	l.log("binary path: %s", self)

	m, err := readManifest(self)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	l.log("manifest loaded: app=%s version=%s profile=%s", m.AppName, m.Version, m.Profile)

	installDir := flagValue(args, "--prefix", "")
	if installDir == "" {
		installDir = resolveInstallDir(m.AppName, m.Version)
	}
	installDir, err = filepath.Abs(installDir)
	if err != nil {
		return fmt.Errorf("resolve absolute install dir: %w", err)
	}

	l.log("install base: %s", filepath.Dir(filepath.Dir(installDir)))
	l.log("install dir:  %s", installDir)
	l.log("mode: %s  offline: %v", mode, offline)

	step("Installing %s v%s (%s mode)", m.AppName, m.Version, mode)
	fmt.Printf("  → %s\n", installDir)

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}
	l.log("created install dir")

	// CRITICAL: Install UV first for all Python operations.
	if !offline {
		step("Ensuring uv is installed...")
		if err := ensureUVInstalled(installDir, l); err != nil {
			return fmt.Errorf("install uv: %w", err)
		}
	} else {
		l.log("offline mode — skipping uv download")
	}

	step("Extracting payload...")
	if err := extractPayload(self, installDir, l); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	step("Installing Python %s...", m.Python.Version)
	if err := installPython(installDir, m, offline, l); err != nil {
		return fmt.Errorf("install python: %w", err)
	}

	staleVenv := filepath.Join(installDir, ".venv")
	if _, err := os.Stat(staleVenv); err == nil {
		l.log("removing stale .venv extracted from payload")
		if err := os.RemoveAll(staleVenv); err != nil {
			return fmt.Errorf("remove stale venv: %w", err)
		}
	}

	step("Creating virtual environment...")
	if err := createVenv(installDir, l); err != nil {
		return fmt.Errorf("create venv: %w", err)
	}

	step("Syncing Python dependencies...")
	if !offline {
		if err := syncPythonDeps(installDir, l); err != nil {
			// Non-fatal — warn and continue (packages are best-effort)
			fmt.Fprintf(os.Stderr, "  warning: dependency sync failed: %v\n", err)
		}
	} else {
		l.log("offline mode — skipping dependency sync")
	}

	writeReceipt(installDir, m, mode)

	l.elapsed("total install time")
	fmt.Printf("\n✓ Installation complete: %s\n", installDir)
	fmt.Printf("\nTo run:       %s run\n", os.Args[0])
	fmt.Printf("To uninstall: %s uninstall\n", os.Args[0])
	return nil
}

// syncPythonDeps runs uv sync --frozen if uv.lock exists.
// This allows manifests to omit py_packages when bundling uv.lock.
// syncPythonDeps runs uv sync --frozen if uv.lock exists.
func syncPythonDeps(installDir string, l *logger) error {
	uv, err := findUV(installDir)
	if err != nil {
		return fmt.Errorf("uv not found: %w", err)
	}

	lockPath := filepath.Join(installDir, "uv.lock")
	if _, err := os.Stat(lockPath); err == nil {
		l.log("found uv.lock — running uv sync --frozen")
		cmd := exec.Command(uv, "sync", "--frozen")
		cmd.Dir = installDir

		// uv auto-detects .venv in cwd. We explicitly set VIRTUAL_ENV
		// to avoid mismatch warnings from parent shells.
		venvDir := filepath.Join(installDir, ".venv")
		env := os.Environ()
		cleanEnv := make([]string, 0, len(env)+1)
		for _, e := range env {
			if !strings.HasPrefix(e, "VIRTUAL_ENV=") {
				cleanEnv = append(cleanEnv, e)
			}
		}
		cleanEnv = append(cleanEnv, "VIRTUAL_ENV="+venvDir)
		cmd.Env = cleanEnv

		return execCmd(cmd, l)
	}
	return nil
}

func createVenv(installDir string, l *logger) error {
	venvDir := filepath.Join(installDir, ".venv")
	if _, err := os.Stat(venvDir); err == nil {
		l.log("virtual environment already exists at %s — skipping", venvDir)
		return nil
	}

	uv, err := findUV(installDir)
	if err != nil {
		return fmt.Errorf("uv not found: %w", err)
	}
	l.log("uv binary: %s", uv)
	l.log("venv target: %s", venvDir)

	cmd := exec.Command(uv, "venv", venvDir)
	cmd.Dir = installDir
	return execCmd(cmd, l)
}

func installPackages(installDir string, pkgs []PyPackage, l *logger) error {
	uv, err := findUV(installDir)
	if err != nil {
		return fmt.Errorf("uv not found: %w", err)
	}
	l.log("uv binary: %s", uv)

	venvDir := filepath.Join(installDir, ".venv")
	l.log("VIRTUAL_ENV: %s", venvDir)

	cmd := exec.Command(uv, "sync", "--frozen")
	cmd.Dir = installDir
	cmd.Env = append(os.Environ(), "VIRTUAL_ENV="+venvDir)
	if err := execCmd(cmd, l); err != nil {
		return fmt.Errorf("uv sync: %w", err)
	}
	return nil
}

// resolveInstallDir returns the installation directory using env var overrides.
//
//	Priority:
//	  molt_INSTALL_DIR  →  use as-is
//	  molt_INSTALL_BASE →  base/<appName>/<version>
//	  platform default    →  ~/.local/share/<appName>/<version>  (Linux)
func resolveInstallDir(appName, version string) string {
	if dir := os.Getenv("molt_INSTALL_DIR"); dir != "" {
		return dir
	}
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
	default:
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
	receiptPath := filepath.Join(installDir, ".molt", "receipt.json")
	receiptPath, _ = filepath.Abs(receiptPath)
	os.MkdirAll(filepath.Dir(receiptPath), 0o755)
	os.WriteFile(receiptPath, data, 0o644)
}

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

	step("Uninstalling %s v%s", m.AppName, m.Version)
	fmt.Printf("  → removing %s\n", installDir)
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

		// Handle both prefixed (src/.molt/manifest.json) and unprefixed paths
		manifestPath := hdr.Name
		if strings.HasPrefix(manifestPath, "src/") {
			manifestPath = strings.TrimPrefix(manifestPath, "src/")
		}
		if manifestPath == ".molt/manifest.json" {
			var m Manifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return nil, err
			}
			return &m, nil
		}
	}
	return nil, fmt.Errorf("manifest not found in payload")
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

func installPython(installDir string, m *Manifest, offline bool, l *logger) error {
	pythonDir := filepath.Join(installDir, "python")
	pyBin := filepath.Join(pythonDir, "bin", pythonBinaryName())
	if runtime.GOOS == "windows" {
		pyBin = filepath.Join(pythonDir, "python.exe")
	}

	if _, err := os.Stat(pyBin); err == nil {
		l.log("Python %s already present at %s — skipping download", m.Python.Version, pyBin)
		return nil
	}

	if m.Python.Embedded {
		l.log("Python is embedded in payload — no separate install needed")
		return nil
	}

	if offline {
		l.log("offline mode — symlinking system Python")
		return symlinkSystemPython(installDir, l)
	}

	if m.Python.URL == "" {
		l.log("no Python URL in manifest — symlinking system Python")
		return symlinkSystemPython(installDir, l)
	}

	l.log("Python %s not found — downloading from %s", m.Python.Version, m.Python.URL)
	cacheDir := resolveCacheDir()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		l.log("warning: could not create cache dir %s: %v", cacheDir, err)
	}

	tmp, err := os.CreateTemp(cacheDir, "python-*.tar.gz")
	if err != nil {
		l.log("cache dir unavailable, falling back to system temp")
		tmp, err = os.CreateTemp("", "python-*.tar.gz")
		if err != nil {
			return err
		}
	}
	l.log("downloading to temp file: %s", tmp.Name())
	defer os.Remove(tmp.Name())

	if err := downloadFile(m.Python.URL, tmp, l); err != nil {
		tmp.Close()
		return fmt.Errorf("download python: %w", err)
	}
	tmp.Close()

	l.log("extracting Python archive to %s", pythonDir)
	os.MkdirAll(pythonDir, 0o755)
	return extractTarGz(tmp.Name(), pythonDir)
}

func symlinkSystemPython(installDir string, l *logger) error {
	systemPython, err := exec.LookPath(pythonBinaryName())
	if err != nil {
		return fmt.Errorf("python not found (tried standalone + system lookup)")
	}
	l.log("system Python found at %s", systemPython)

	binDir := filepath.Join(installDir, "python", "bin")
	os.MkdirAll(binDir, 0o755)
	dest := filepath.Join(binDir, pythonBinaryName())
	if _, err := os.Stat(dest); err == nil {
		l.log("symlink already exists at %s — skipping", dest)
		return nil
	}
	l.log("creating symlink %s → %s", dest, systemPython)
	return os.Symlink(systemPython, dest)
}

// ── Path helpers ──────────────────────────────────────────────────────────────

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

func downloadFile(url string, dst *os.File, l *logger) error {
	for _, tool := range []string{"curl", "wget"} {
		if _, err := exec.LookPath(tool); err != nil {
			l.log("%s not found in PATH — trying next", tool)
			continue
		}
		var args []string
		if tool == "curl" {
			args = []string{"-L", "-o", dst.Name(), url}
		} else {
			args = []string{"-O", dst.Name(), url}
		}
		cmd := exec.Command(tool, args...)
		l.logCmd(cmd)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s download failed: %w", tool, err)
		}
		return nil
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

func findUV(installDir string) (string, error) {
	// 1. MOLT_UV override (strict)
	if override := os.Getenv(uvbin.EnvOverride); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("%s=%q: file not found", uvbin.EnvOverride, override)
	}

	// 2. Local install-dir copy (placed there by ensureUVInstalled).
	localUV := filepath.Join(installDir, "uv", "bin", "uv")
	if runtime.GOOS == "windows" {
		localUV += ".exe"
	}
	if _, err := os.Stat(localUV); err == nil {
		return localUV, nil
	}

	// 3. Managed global install via uvbin.Ensure().
	return uvbin.Ensure()
}

// findPython returns the absolute path to the Python binary inside installDir.
// It checks standard locations first, then falls back to a recursive walk.
func findPython(installDir string) string {
	absInstallDir, err := filepath.Abs(installDir)
	if err != nil {
		absInstallDir = installDir
	}

	candidates := []string{
		filepath.Join(absInstallDir, ".venv", "bin", pythonBinaryName()),
		filepath.Join(absInstallDir, ".venv", "Scripts", pythonBinaryName()),
		filepath.Join(absInstallDir, "python", "bin", pythonBinaryName()),
		filepath.Join(absInstallDir, "python", pythonBinaryName()),
	}
	for _, p := range candidates {
		if absP, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(absP); err == nil {
				return absP
			}
		}
	}

	// Fallback: recursive search for python* under bin/ or Scripts/.
	var found string
	filepath.WalkDir(absInstallDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == pythonBinaryName() || base == "python3" || base == "python" {
			dir := filepath.Dir(path)
			if filepath.Base(dir) == "bin" || filepath.Base(dir) == "Scripts" {
				if absPath, err := filepath.Abs(path); err == nil {
					found = absPath
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return found
}

// buildEnv constructs the subprocess environment: no PYTHONHOME, absolute venv paths.
func buildEnv(installDir string) []string {
	absInstallDir, err := filepath.Abs(installDir)
	if err != nil {
		absInstallDir = installDir
	}

	srcDir := filepath.Join(absInstallDir, "src")
	venvDir := filepath.Join(absInstallDir, ".venv")
	venvBin := filepath.Join(venvDir, "bin")

	env := os.Environ()
	filtered := make([]string, 0, len(env)+6)

	for _, e := range env {
		// CRITICAL: Remove PYTHONHOME — breaks venv stdlib discovery.
		if strings.HasPrefix(e, "PYTHONHOME=") {
			continue
		}
		// Remove existing PYTHONPATH — we set our own below.
		if strings.HasPrefix(e, "PYTHONPATH=") {
			continue
		}
		filtered = append(filtered, e)
	}

	filtered = append(filtered, "VIRTUAL_ENV="+venvDir)
	filtered = append(filtered, "PYTHONPATH="+srcDir)

	pathVal := venvBin
	if orig := os.Getenv("PATH"); orig != "" {
		sep := ":"
		if runtime.GOOS == "windows" {
			sep = ";"
		}
		pathVal += sep + orig
	}
	pathSet := false
	for i, e := range filtered {
		if strings.HasPrefix(e, "PATH=") {
			filtered[i] = "PATH=" + pathVal
			pathSet = true
			break
		}
	}
	if !pathSet {
		filtered = append(filtered, "PATH="+pathVal)
	}

	filtered = append(filtered,
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
	)

	return filtered
}

// ── Run ──────────────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	l := newLogger(hasFlag(args, "--verbose", "-v"))

	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := readManifest(self)
	if err != nil {
		return err
	}

	installDir := resolveInstallDir(m.AppName, m.Version)
	installDir, err = filepath.Abs(installDir)
	if err != nil {
		return fmt.Errorf("resolve absolute install dir: %w", err)
	}
	l.log("install dir: %s", installDir)

	manifestPath := filepath.Join(installDir, ".molt", "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Not installed. Run first:\n%s install\n", os.Args[0])
		return fmt.Errorf("not installed at %s", installDir)
	}

	pythonBin := findPython(installDir)
	if pythonBin == "" {
		return fmt.Errorf("python not found in %s — run install first", installDir)
	}
	l.log("python binary: %s", pythonBin)

	mainModule := m.MainModule
	if mainModule == "" {
		mainModule = m.AppName + ".main"
	}
	l.log("main module: %s", mainModule)

	// Strip our own --verbose flag before forwarding args to the app.
	forwardArgs := filterFlags(args, "--verbose", "-v")
	cmdArgs := append([]string{"-m", mainModule}, forwardArgs...)

	cmd := exec.Command(pythonBin, cmdArgs...)
	cmd.Dir = installDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = buildEnv(installDir)

	l.logCmd(cmd)
	if l.verbose {
		fmt.Printf("  env overrides:\n")
		for _, e := range cmd.Env {
			if strings.HasPrefix(e, "VIRTUAL_ENV=") ||
				strings.HasPrefix(e, "PYTHONPATH=") ||
				strings.HasPrefix(e, "PYTHONNOUSERSITE=") ||
				strings.HasPrefix(e, "PYTHONDONTWRITEBYTECODE=") {
				fmt.Printf("    %s\n", e)
			}
		}
	}

	applyIsolation(cmd)
	return cmd.Run()
}

// ── Payload extraction ───────────────────────────────────────────────────────

// extractPayload strips the leading "src/" prefix so files land in installDir.
func extractPayload(binaryPath, installDir string, l *logger) error {
	f, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer f.Close()

	offset, err := readPayloadOffset(f)
	if err != nil {
		return err
	}
	l.log("payload offset: %d bytes", offset)

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	fileCount := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		relPath := filepath.FromSlash(hdr.Name)
		if strings.HasPrefix(relPath, "src"+string(filepath.Separator)) {
			relPath = strings.TrimPrefix(relPath, "src"+string(filepath.Separator))
		} else if relPath == "src" {
			continue
		}

		target := filepath.Join(installDir, relPath)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}
			if err := os.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
			fileCount++
			l.log("extracted: %s", hdr.Name)
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			l.log("symlink:   %s → %s", hdr.Name, hdr.Linkname)
		}
	}
	l.log("extracted %d file(s) to %s", fileCount, installDir)
	return nil
}

// ensureUVInstalled downloads UV into the install directory for self-containment.
func ensureUVInstalled(installDir string, l *logger) error {
	uvDir := filepath.Join(installDir, "uv")
	uvBin := filepath.Join(uvDir, "bin", "uv")
	if runtime.GOOS == "windows" {
		uvBin = filepath.Join(uvDir, "bin", "uv.exe")
	}

	if _, err := os.Stat(uvBin); err == nil {
		l.log("uv already present at %s — skipping", uvBin)
		return nil
	}

	l.log("uv not found — downloading pinned version %s via uvbin.Ensure()", uvbin.PinnedVersion)

	managedUV, err := uvbin.Ensure()
	if err != nil {
		return fmt.Errorf("ensure uv: %w", err)
	}
	l.log("managed uv binary: %s", managedUV)

	if err := os.MkdirAll(filepath.Dir(uvBin), 0o755); err != nil {
		return err
	}
	l.log("copying uv to local install dir: %s", uvBin)
	data, err := os.ReadFile(managedUV)
	if err != nil {
		return err
	}
	if err := os.WriteFile(uvBin, data, 0o755); err != nil {
		return err
	}
	l.log("✓ uv installed at %s", uvBin)
	return nil
}

// filterFlags removes exact-match flags from a slice without mutating it.
func filterFlags(args []string, flags ...string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		skip := false
		for _, f := range flags {
			if a == f {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, a)
		}
	}
	return out
}
