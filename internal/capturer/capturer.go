package capturer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"pyexec/internal/platform"
	"pyexec/pkg/types"
)

// Capturer inspects a Python project and builds a Snapshot.
type Capturer struct {
	Verbose bool
	// TargetOS and TargetArch are the intended deployment platform.
	// Defaults to the current machine's OS/arch.
	TargetOS   string
	TargetArch string
}

// New creates a Capturer targeting the current OS/arch.
func New(verbose bool) *Capturer {
	return &Capturer{
		Verbose:    verbose,
		TargetOS:   runtime.GOOS,
		TargetArch: runtime.GOARCH,
	}
}

// NewCross creates a Capturer targeting a different OS/arch.
func NewCross(verbose bool, targetOS, targetArch string) *Capturer {
	return &Capturer{
		Verbose:    verbose,
		TargetOS:   targetOS,
		TargetArch: targetArch,
	}
}

// Capture analyses projectPath and returns a complete environment snapshot.
func (c *Capturer) Capture(projectPath string) (*types.Snapshot, error) {
	snap := &types.Snapshot{}

	if c.Verbose {
		fmt.Println("  Capturing Python binary...")
	}
	py, err := c.capturePython(projectPath)
	if err != nil {
		return nil, fmt.Errorf("capture python: %w", err)
	}
	snap.Python = py

	if c.Verbose {
		fmt.Println("  Capturing system dependencies...")
	}
	deps, err := c.captureSystemDeps(projectPath)
	if err != nil {
		// Non-fatal: system dep tracing is best-effort.
		if c.Verbose {
			fmt.Printf("  Warning: system dep capture: %v\n", err)
		}
	}
	snap.SystemDeps = deps

	if c.Verbose {
		fmt.Println("  Capturing Python packages...")
	}
	pkgs, err := c.capturePyPackages(projectPath)
	if err != nil {
		return nil, fmt.Errorf("capture packages: %w", err)
	}
	snap.PyPackages = pkgs

	if c.Verbose {
		fmt.Println("  Capturing source files...")
	}
	src, err := c.captureSource(projectPath)
	if err != nil {
		return nil, fmt.Errorf("capture source: %w", err)
	}
	snap.Source = src

	return snap, nil
}

// capturePython finds the Python binary in the venv and records its version.
func (c *Capturer) capturePython(projectPath string) (types.PythonSpec, error) {
	pyName := platform.PythonBinaryName(runtime.GOOS)
	candidates := []string{
		filepath.Join(projectPath, ".venv", "bin", pyName),
		filepath.Join(projectPath, ".venv", "Scripts", pyName), // Windows venv
		pyName,
	}
	var pythonBin string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			pythonBin = p
			break
		}
		if out, err := exec.LookPath(p); err == nil {
			pythonBin = out
			break
		}
	}
	if pythonBin == "" {
		return types.PythonSpec{}, fmt.Errorf("python not found")
	}

	out, err := exec.Command(pythonBin, "--version").Output()
	if err != nil {
		return types.PythonSpec{}, fmt.Errorf("python --version: %w", err)
	}
	version := strings.TrimSpace(strings.TrimPrefix(string(out), "Python "))

	sha, size, _ := hashFile(pythonBin)

	// Build the download URL for the target platform's standalone Python.
	standaloneURL, _ := platform.PythonStandaloneURL(version, c.TargetOS, c.TargetArch)

	return types.PythonSpec{
		Version: version,
		URL:     standaloneURL,
		SHA256:  sha,
		Size:    size,
	}, nil
}

// captureSystemDeps traces shared library dependencies via the platform package.
func (c *Capturer) captureSystemDeps(projectPath string) ([]types.SystemDep, error) {
	pyName := platform.PythonBinaryName(runtime.GOOS)
	pythonBin := filepath.Join(projectPath, ".venv", "bin", pyName)
	if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
		var err2 error
		pythonBin, err2 = exec.LookPath(pyName)
		if err2 != nil {
			return nil, nil // best-effort
		}
	}

	deps, err := platform.TraceDeps(pythonBin)
	if err != nil {
		return nil, err
	}

	// Also trace .so/.dylib/.dll files inside the venv for native extensions.
	ext := platform.LibExtension(runtime.GOOS)
	sitePackages := filepath.Join(projectPath, ".venv", "lib")
	soFiles, _ := findSharedLibs(sitePackages, ext)
	for _, so := range soFiles {
		moreDeps, err := platform.TraceDeps(so)
		if err != nil {
			continue
		}
		deps = append(deps, moreDeps...)
	}

	return deduplicateDeps(deps), nil
}

// deduplicateDeps removes duplicate library names.
func deduplicateDeps(deps []types.SystemDep) []types.SystemDep {
	seen := make(map[string]bool)
	var out []types.SystemDep
	for _, d := range deps {
		if !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d)
		}
	}
	return out
}

// capturePyPackages reads uv.lock to collect package metadata.
func (c *Capturer) capturePyPackages(projectPath string) ([]types.PyPackage, error) {
	lockPath := filepath.Join(projectPath, "uv.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return c.capturePipFreeze(projectPath)
	}
	return parseUvLock(data, c.TargetOS, c.TargetArch), nil
}

// parseUvLock extracts package info from uv.lock TOML.
func parseUvLock(data []byte, targetOS, targetArch string) []types.PyPackage {
	var pkgs []types.PyPackage
	var current *types.PyPackage

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[[package]]" {
			if current != nil {
				pkgs = append(pkgs, *current)
			}
			current = &types.PyPackage{}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "name = ") {
			current.Name = unquote(strings.TrimPrefix(line, "name = "))
		} else if strings.HasPrefix(line, "version = ") {
			current.Version = unquote(strings.TrimPrefix(line, "version = "))
			current.URL = pypiSourceURL(current.Name, current.Version)
		}
	}
	if current != nil {
		pkgs = append(pkgs, *current)
	}
	return pkgs
}

// pypiSourceURL builds a PyPI source dist URL (platform-neutral fallback).
func pypiSourceURL(name, version string) string {
	return fmt.Sprintf(
		"https://files.pythonhosted.org/packages/source/%s/%s/%s-%s.tar.gz",
		strings.ToLower(name[:1]), name, name, version,
	)
}

func unquote(s string) string { return strings.Trim(s, `"'`) }

// capturePipFreeze falls back to pip freeze.
func (c *Capturer) capturePipFreeze(projectPath string) ([]types.PyPackage, error) {
	pipName := "pip"
	if runtime.GOOS == "windows" {
		pipName = "pip.exe"
	}
	pipBin := filepath.Join(projectPath, ".venv", "bin", pipName)
	if runtime.GOOS == "windows" {
		pipBin = filepath.Join(projectPath, ".venv", "Scripts", pipName)
	}
	if _, err := os.Stat(pipBin); os.IsNotExist(err) {
		return nil, nil
	}
	out, err := exec.Command(pipBin, "freeze").Output()
	if err != nil {
		return nil, nil
	}
	var pkgs []types.PyPackage
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "==", 2)
		if len(parts) != 2 {
			continue
		}
		name, version := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		pkgs = append(pkgs, types.PyPackage{
			Name:    name,
			Version: version,
			URL:     pypiSourceURL(name, version),
		})
	}
	return pkgs, nil
}

// captureSource walks the project and returns source files.
func (c *Capturer) captureSource(projectPath string) ([]types.SourceFile, error) {
	var files []types.SourceFile
	skip := map[string]bool{
		".venv": true, "__pycache__": true, ".git": true,
		".mypy_cache": true, "dist": true, "build": true,
	}
	exts := map[string]bool{
		".py": true, ".toml": true, ".lock": true,
		".json": true, ".cfg": true, ".ini": true, ".txt": true, ".md": true,
	}

	err := filepath.WalkDir(projectPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectPath, path)
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if skip[part] {
				return filepath.SkipDir
			}
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !exts[ext] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Normalise path separator for cross-platform archives.
		files = append(files, types.SourceFile{
			RelPath: filepath.ToSlash(rel),
			Data:    data,
		})
		return nil
	})
	return files, err
}

// findSharedLibs walks dir looking for files with the given extension.
func findSharedLibs(dir, ext string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		// Handle walk errors gracefully (best-effort)
		if err != nil {
			return nil
		}
		// d can be nil when err is non-nil; guard against it
		if d == nil {
			return fmt.Errorf("dir is nil")
		}
		
		if !d.IsDir() && strings.Contains(path, ext) {
			out = append(out, path)
		}
		return nil
	})
	// Optionally return the walk error if you want strict behavior
	_ = err // or: return out, err
	return out, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// CaptureSourceOnly walks the project and returns source files only,
// without tracing Python or system dependencies.
// Used by the assemble step when loading a pre-captured manifest.
func (c *Capturer) CaptureSourceOnly(projectPath string) ([]types.SourceFile, error) {
	return c.captureSource(projectPath)
}
