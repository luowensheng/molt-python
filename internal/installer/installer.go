package installer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"pyexec/internal/cache"
	"pyexec/internal/downloader"
	"pyexec/internal/manifest"
	"pyexec/internal/platform"
	"pyexec/internal/verifier"
	"pyexec/pkg/types"
)

// Installer materialises an environment from a Manifest.
type Installer struct {
	cfg      types.InstallConfig
	cache    *cache.Cache
	dl       *downloader.Downloader
	verifier *verifier.Verifier
}

// New creates an Installer.
func New(cfg types.InstallConfig) (*Installer, error) {
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		var err error
		cacheDir, err = platform.DefaultCacheDir()
		if err != nil {
			return nil, fmt.Errorf("cache dir: %w", err)
		}
	}
	c, err := cache.New(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("init cache: %w", err)
	}
	dl := downloader.New(c, cfg.Parallel, cfg.Verbose)
	return &Installer{
		cfg:      cfg,
		cache:    c,
		dl:       dl,
		verifier: verifier.New(),
	}, nil
}

// Install sets up the environment described by m under targetDir.
func (i *Installer) Install(ctx context.Context, m *types.Manifest, targetDir string) error {
	if i.cfg.DryRun {
		fmt.Println("[dry-run] would install to", targetDir)
		return nil
	}
	if i.cfg.Verbose {
		fmt.Printf("Installing %s v%s (%s mode)...\n", m.AppName, m.Version, i.cfg.Mode)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	// Phase 1: download missing components.
	if !i.cfg.Offline && !i.cfg.NoDownload {
		if err := i.downloadMissing(ctx, m); err != nil {
			return fmt.Errorf("download: %w", err)
		}
	}

	// Phase 2: install standalone Python.
	if err := i.installPython(targetDir, m); err != nil {
		return fmt.Errorf("install python: %w", err)
	}

	// Phase 3: install system deps (Linux standalone/exact mode).
	if runtime.GOOS == platform.Linux &&
		(i.cfg.Mode == types.ModeStandalone || i.cfg.Mode == types.ModeExact) {
		if err := i.installSystemDeps(targetDir, m.SystemDeps); err != nil {
			return fmt.Errorf("install system deps: %w", err)
		}
	}

	// Phase 4: macOS @rpath patching for any copied dylibs.
	if runtime.GOOS == platform.Darwin {
		i.patchRpathDarwin(targetDir)
	}

	// Phase 5: create virtual environment.
	if err := i.createVenv(targetDir); err != nil {
		return fmt.Errorf("create venv: %w", err)
	}

	// Phase 6: install Python packages.
	if err := i.installPackages(targetDir, m.PyPackages); err != nil {
		return fmt.Errorf("install packages: %w", err)
	}

	// Phase 7: save manifest.
	if err := manifest.Save(targetDir, m); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}

	// Phase 8: verify (exact mode).
	if i.cfg.Mode == types.ModeExact {
		if err := i.verifier.Verify(targetDir, m); err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}
		if i.cfg.AuditLog != "" {
			if err := i.writeAuditLog(targetDir, m); err != nil {
				return fmt.Errorf("audit log: %w", err)
			}
		}
	}

	fmt.Printf("  Installation complete: %s\n", targetDir)
	return nil
}

// downloadMissing fetches everything that is not already cached or embedded.
func (i *Installer) downloadMissing(ctx context.Context, m *types.Manifest) error {
	var items []downloader.Item
	if !m.Python.Embedded && m.Python.URL != "" {
		items = append(items, downloader.Item{
			URL:    m.Python.URL,
			SHA256: m.Python.SHA256,
			Name:   "Python " + m.Python.Version,
		})
	}
	for _, dep := range m.SystemDeps {
		if dep.Embedded || dep.URL == "" {
			continue
		}
		items = append(items, downloader.Item{URL: dep.URL, SHA256: dep.SHA256, Name: dep.Name})
	}
	for _, pkg := range m.PyPackages {
		if pkg.Embedded || pkg.URL == "" {
			continue
		}
		items = append(items, downloader.Item{URL: pkg.URL, SHA256: pkg.SHA256, Name: pkg.Name + " " + pkg.Version})
	}
	if len(items) == 0 {
		return nil
	}
	if i.cfg.Verbose {
		fmt.Printf("  Downloading %d components...\n", len(items))
	}
	return i.dl.DownloadAll(ctx, items)
}

// installPython installs the standalone Python build into <targetDir>/python/.
func (i *Installer) installPython(targetDir string, m *types.Manifest) error {
	pythonDir := platform.InstallPythonDir(targetDir)
	pyBin := filepath.Join(pythonDir, "bin", platform.PythonBinaryName(runtime.GOOS))
	if runtime.GOOS == platform.Windows {
		pyBin = filepath.Join(pythonDir, "python.exe")
	}

	if _, err := os.Stat(pyBin); err == nil {
		if i.cfg.Verbose {
			fmt.Printf("  Python %s already present\n", m.Python.Version)
		}
		return nil
	}

	// If embedded in the binary the payload extraction already handled it.
	if m.Python.Embedded {
		return nil
	}

	// Download from cache.
	cachedPath := i.cache.Path(m.Python.SHA256)
	if cachedPath == "" {
		if i.cfg.Offline {
			// Try system Python as fallback.
			return i.symlinkSystemPython(targetDir, m.Python.Version)
		}
		if m.Python.URL == "" {
			return i.symlinkSystemPython(targetDir, m.Python.Version)
		}
		if i.cfg.Verbose {
			fmt.Printf("  Downloading Python %s...\n", m.Python.Version)
		}
		if err := i.downloadToCache(m.Python.URL, m.Python.SHA256); err != nil {
			return fmt.Errorf("download python: %w", err)
		}
		cachedPath = i.cache.Path(m.Python.SHA256)
	}

	if err := os.MkdirAll(pythonDir, 0o755); err != nil {
		return err
	}
	if i.cfg.Verbose {
		fmt.Printf("  Extracting Python %s...\n", m.Python.Version)
	}
	return extractStandalonePython(cachedPath, pythonDir)
}

// symlinkSystemPython creates a symlink to the system Python as a fallback.
func (i *Installer) symlinkSystemPython(targetDir, version string) error {
	pyName := platform.PythonBinaryName(runtime.GOOS)
	systemPython, err := exec.LookPath(pyName)
	if err != nil {
		return fmt.Errorf("python not found (tried standalone download and system lookup)")
	}
	if i.cfg.Verbose {
		fmt.Printf("  Using system Python at %s\n", systemPython)
	}
	binDir := filepath.Join(targetDir, "python", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(binDir, pyName)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	return os.Symlink(systemPython, dest)
}

// installSystemDeps copies system libraries into <targetDir>/lib (Linux only).
func (i *Installer) installSystemDeps(targetDir string, deps []types.SystemDep) error {
	libDir := platform.LibDir(targetDir)
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return err
	}
	for _, dep := range deps {
		if dep.SHA256 == "" || dep.Embedded {
			continue
		}
		dest := filepath.Join(libDir, dep.Name)
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := i.dl.CopyToDir(dep.SHA256, dep.Name, libDir); err != nil {
			if i.cfg.Verbose {
				fmt.Printf("  Warning: could not install %s: %v\n", dep.Name, err)
			}
		}
	}
	return nil
}

// patchRpathDarwin uses install_name_tool to fix @rpath references in dylibs.
// This is a best-effort step; failure is non-fatal.
func (i *Installer) patchRpathDarwin(targetDir string) {
	if _, err := exec.LookPath("install_name_tool"); err != nil {
		return // tool not available, skip
	}
	libDir := platform.LibDir(targetDir)
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".dylib") {
			continue
		}
		path := filepath.Join(libDir, e.Name())
		// Add the lib dir as an rpath so extensions find their deps.
		exec.Command("install_name_tool", "-add_rpath", libDir, path).Run()
	}
}

// createVenv sets up a Python virtual environment.
func (i *Installer) createVenv(targetDir string) error {
	venvDir := platform.VenvDir(targetDir)
	if _, err := os.Stat(venvDir); err == nil {
		return nil
	}

	pyName := platform.PythonBinaryName(runtime.GOOS)
	// Prefer the standalone Python we just installed.
	pythonBin := filepath.Join(targetDir, "python", "bin", pyName)
	if runtime.GOOS == platform.Windows {
		pythonBin = filepath.Join(targetDir, "python", pyName)
	}
	if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
		var err2 error
		pythonBin, err2 = exec.LookPath(pyName)
		if err2 != nil {
			return fmt.Errorf("no python available to create venv")
		}
	}

	if i.cfg.Verbose {
		fmt.Println("  Creating virtual environment...")
	}
	return exec.Command(pythonBin, "-m", "venv", venvDir).Run()
}

// installPackages installs Python packages into the venv using pip.
func (i *Installer) installPackages(targetDir string, pkgs []types.PyPackage) error {
	if len(pkgs) == 0 {
		return nil
	}
	pipName := "pip"
	if runtime.GOOS == platform.Windows {
		pipName = "pip.exe"
	}
	pip := filepath.Join(targetDir, ".venv", "bin", pipName)
	if runtime.GOOS == platform.Windows {
		pip = filepath.Join(targetDir, ".venv", "Scripts", pipName)
	}
	if _, err := os.Stat(pip); os.IsNotExist(err) {
		return nil
	}

	if i.cfg.Verbose {
		fmt.Printf("  Installing %d Python packages...\n", len(pkgs))
	}
	args := []string{"install", "--quiet"}
	for _, p := range pkgs {
		args = append(args, fmt.Sprintf("%s==%s", p.Name, p.Version))
	}
	cmd := exec.Command(pip, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && i.cfg.Verbose {
		fmt.Printf("  pip output: %s\n", string(out))
	}
	return err
}

// downloadToCache downloads a URL and stores it under its SHA256 in the cache.
func (i *Installer) downloadToCache(url, expectedSHA256 string) error {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	_, err = i.cache.PutStream(expectedSHA256, resp.Body)
	return err
}

// extractStandalonePython extracts a python-build-standalone tarball,
// stripping the top-level directory prefix.
func extractStandalonePython(tarGzPath, destDir string) error {
	f, err := os.Open(tarGzPath)
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

		// Strip top-level directory (e.g., "python/bin/..." → "bin/...").
		parts := strings.SplitN(filepath.ToSlash(hdr.Name), "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		rel := parts[1]
		target := filepath.Join(destDir, filepath.FromSlash(rel))

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

// writeAuditLog records the installation details to a file.
func (i *Installer) writeAuditLog(targetDir string, m *types.Manifest) error {
	content := fmt.Sprintf(`PyExec Installation Audit Log
==============================
App:         %s
Version:     %s
Mode:        %s
OS:          %s
Arch:        %s
Install Dir: %s
Date:        %s
Python:      %s
Packages:    %d
SystemDeps:  %d
`,
		m.AppName, m.Version, i.cfg.Mode,
		runtime.GOOS, runtime.GOARCH,
		targetDir, time.Now().UTC().Format(time.RFC3339),
		m.Python.Version, len(m.PyPackages), len(m.SystemDeps),
	)
	return os.WriteFile(i.cfg.AuditLog, []byte(content), 0o644)
}
