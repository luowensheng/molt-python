// Command launcher is the target-side runner embedded inside every PyExec binary.
// It is compiled as a separate binary per OS/arch and concatenated with the
// payload archive during `pyexec build`. It uses only the Go standard library.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Manifest mirrors types.Manifest but is self-contained (no imports from main pyexec).
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

// trailerSize is the 8-byte little-endian offset appended at the end of the binary.
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
	fmt.Fprintf(os.Stderr, "Usage: %s {install|run|verify|uninstall|version} [args]\n", os.Args[0])
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

	installDir := defaultInstallDir(m.AppName, m.Version)
	if v := flagValue(args, "--prefix", ""); v != "" {
		installDir = v
	}

	fmt.Printf("Installing %s v%s (%s mode)...\n", m.AppName, m.Version, mode)

	// Extract the payload archive.
	if err := extractPayload(self, installDir, verbose); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Install Python (standalone build).
	if err := installPython(installDir, m, offline, verbose); err != nil {
		return fmt.Errorf("install python: %w", err)
	}

	// Create virtual environment.
	if err := createVenv(installDir, verbose); err != nil {
		return fmt.Errorf("create venv: %w", err)
	}

	// Install Python packages.
	if len(m.PyPackages) > 0 && !offline {
		if err := installPackages(installDir, m.PyPackages, verbose); err != nil {
			if verbose {
				fmt.Printf("  Warning: package install: %v\n", err)
			}
		}
	}

	fmt.Printf("  Installation path: %s\n", installDir)
	fmt.Println("  Installation complete")
	return nil
}

// ── Run ──────────────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := readManifest(self)
	if err != nil {
		return err
	}

	installDir := defaultInstallDir(m.AppName, m.Version)

	pythonBin := findPython(installDir)
	if pythonBin == "" {
		return fmt.Errorf("python not found in %s — run install first", installDir)
	}

	mainModule := m.MainModule
	if mainModule == "" {
		mainModule = m.AppName + ".main"
	}

	cmdArgs := append([]string{"-m", mainModule}, args...)
	cmd := exec.Command(pythonBin, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = buildEnv(installDir)

	// Linux namespace isolation (no-op on other platforms).
	applyIsolation(cmd)

	return cmd.Run()
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
	installDir := defaultInstallDir(m.AppName, m.Version)
	manifestPath := filepath.Join(installDir, ".pyexec", "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return fmt.Errorf("not installed (manifest missing): %s", manifestPath)
	}
	fmt.Printf("  Installation verified: %s\n", installDir)
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
	installDir := defaultInstallDir(m.AppName, m.Version)
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	fmt.Printf("Uninstalled %s v%s\n", m.AppName, m.Version)
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

// ── Payload extraction ───────────────────────────────────────────────────────

// readManifest reads the manifest embedded in the binary's payload archive.
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
		if hdr.Name == ".pyexec/manifest.json" {
			var m Manifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return nil, err
			}
			return &m, nil
		}
	}
	return nil, fmt.Errorf("manifest not found in payload")
}

// extractPayload extracts the gzipped tar payload into installDir.
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
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
			os.Chmod(target, os.FileMode(hdr.Mode))
			if verbose {
				fmt.Printf("  extracted: %s\n", hdr.Name)
			}
		}
	}
	return nil
}

// readPayloadOffset reads the 8-byte little-endian offset from the binary trailer.
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
		// Embedded Python would have been extracted from the payload already.
		return nil
	}

	if offline {
		// Fall back to system Python.
		return nil
	}

	if m.Python.URL == "" {
		return nil // best-effort
	}

	if verbose {
		fmt.Printf("  Downloading Python %s...\n", m.Python.Version)
	}

	tmpFile, err := os.CreateTemp("", "python-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if err := downloadFile(m.Python.URL, tmpFile); err != nil {
		tmpFile.Close()
		return fmt.Errorf("download python: %w", err)
	}
	tmpFile.Close()

	if err := os.MkdirAll(pythonDir, 0o755); err != nil {
		return err
	}
	return extractTarGz(tmpFile.Name(), pythonDir)
}

func createVenv(installDir string, verbose bool) error {
	venvDir := filepath.Join(installDir, ".venv")
	if _, err := os.Stat(venvDir); err == nil {
		return nil // already exists
	}

	python := findPython(installDir)
	if python == "" {
		var err error
		python, err = exec.LookPath(pythonBinaryName())
		if err != nil {
			return fmt.Errorf("python not found")
		}
	}

	if verbose {
		fmt.Println("  Creating virtual environment...")
	}
	return exec.Command(python, "-m", "venv", venvDir).Run()
}

func installPackages(installDir string, pkgs []PyPackage, verbose bool) error {
	pip := findPip(installDir)
	if pip == "" {
		return nil
	}

	if verbose {
		fmt.Printf("  Installing %d packages...\n", len(pkgs))
	}

	args := []string{"install", "--quiet"}
	for _, p := range pkgs {
		args = append(args, fmt.Sprintf("%s==%s", p.Name, p.Version))
	}
	return exec.Command(pip, args...).Run()
}

// ── Environment ──────────────────────────────────────────────────────────────

func buildEnv(installDir string) []string {
	pythonHome := filepath.Join(installDir, ".venv")
	srcDir := filepath.Join(installDir, "src")

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
		binDir := filepath.Join(installDir, "python", "bin")
		venvBin := filepath.Join(installDir, ".venv", "bin")
		libDir := filepath.Join(installDir, "lib")
		env = []string{
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"DYLD_LIBRARY_PATH=" + libDir,
			"PATH=" + binDir + ":" + venvBin + ":/usr/bin:/bin",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	default: // linux
		binDir := filepath.Join(installDir, "python", "bin")
		venvBin := filepath.Join(installDir, ".venv", "bin")
		libDir := filepath.Join(installDir, "lib")
		env = []string{
			"LD_LIBRARY_PATH=" + libDir,
			"PYTHONHOME=" + pythonHome,
			"PYTHONPATH=" + srcDir,
			"PATH=" + binDir + ":" + venvBin + ":/usr/bin:/bin",
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}
	}

	// Preserve essential host env vars.
	for _, key := range []string{"HOME", "USER", "TERM", "LANG", "LC_ALL", "TMPDIR", "TMP", "TEMP"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// ── Path helpers ─────────────────────────────────────────────────────────────

func defaultInstallDir(appName, version string) string {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, "AppData", "Roaming")
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Library", "Application Support")
	default:
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, appName, version)
}

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
	pipName := "pip"
	if runtime.GOOS == "windows" {
		pipName = "pip.exe"
	}
	candidates := []string{
		filepath.Join(installDir, ".venv", "bin", pipName),
		filepath.Join(installDir, ".venv", "Scripts", pipName),
	}
	for _, p := range candidates {
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

// ── Download / extract helpers ───────────────────────────────────────────────

func downloadFile(url string, dst *os.File) error {
	// Use curl or wget — avoids importing net/http which adds binary size.
	// Falls back to Go's http if neither is found.
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
	// Pure Go fallback.
	return downloadFileGo(url, dst)
}

func downloadFileGo(url string, dst *os.File) error {
	// Minimal HTTP GET without importing net/http at the top level.
	// We use exec to call the system's python3 as a fallback downloader.
	script := fmt.Sprintf(`import urllib.request; urllib.request.urlretrieve(%q, %q)`, url, dst.Name())
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
		if err != nil {
			return fmt.Errorf("no download tool available (curl, wget, or python3 required)")
		}
	}
	return exec.Command(python, "-c", script).Run()
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

		// Strip the top-level directory from python-build-standalone archives.
		parts := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)
		if len(parts) < 2 {
			continue
		}
		rel := parts[1]
		if rel == "" {
			continue
		}

		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
			os.Chmod(target, os.FileMode(hdr.Mode))
		case tar.TypeSymlink:
			os.Symlink(hdr.Linkname, target)
		}
	}
	return nil
}

// ── Argument helpers ─────────────────────────────────────────────────────────

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
