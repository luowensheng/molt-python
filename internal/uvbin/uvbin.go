// internal/uvbin/uvbin.go
package uvbin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PinnedVersion is the uv release molt downloads when self-installing.
const PinnedVersion = "0.4.18"

// EnvOverride is the ONLY environment variable allowed to point to an external uv binary.
const EnvOverride = "MOLT_UV"

func installBase() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "uv")
}

func managedBinPath() string {
	bin := "uv"
	if runtime.GOOS == "windows" {
		bin = "uv.exe"
	}
	return filepath.Join(installBase(), "bin", bin)
}

// Find returns the path to a usable uv binary.
// STRICT POLICY: Only MOLT_UV or molt-managed uv are allowed.
// System PATH is IGNORED to guarantee hermetic builds.
func Find() (string, error) {
	// 1. Explicit override.
	if override := os.Getenv(EnvOverride); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("%s=%q: file not found", EnvOverride, override)
	}
	// 2. Molt-managed copy.
	managed := managedBinPath()
	if _, err := os.Stat(managed); err == nil {
		return managed, nil
	}
	return "", fmt.Errorf("uv not found — run 'molt init' or set %s", EnvOverride)
}

// Ensure guarantees that a uv binary is available.
// Auto-downloads the pinned version into ~/.molt/uv/bin/ if missing.
func Ensure() (string, error) {
	if override := os.Getenv(EnvOverride); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("%s=%q: file not found", EnvOverride, override)
	}
	managed := managedBinPath()
	if _, err := os.Stat(managed); err == nil {
		return managed, nil
	}
	// Auto-download
	if err := Install(PinnedVersion, false); err != nil {
		return "", err
	}
	return managed, nil
}

// Install downloads the given uv version into ~/.molt/uv.
func Install(version string, force bool) error {
	managed := managedBinPath()
	if !force {
		if _, err := os.Stat(managed); err == nil {
			return nil
		}
	}
	url, _, err := releaseURL(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	fmt.Printf("Downloading uv %s for %s/%s... ", version, runtime.GOOS, runtime.GOARCH)
	tmp, err := os.CreateTemp("", "molt-uv-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := downloadVerified(url, tmp, ""); err != nil {
		tmp.Close()
		return fmt.Errorf("download uv: %w", err)
	}
	tmp.Close()

	destDir := installBase()
	if err := os.MkdirAll(filepath.Join(destDir, "bin"), 0o755); err != nil {
		return err
	}
	if err := extractUV(tmpName, destDir, runtime.GOOS); err != nil {
		return fmt.Errorf("extract uv: %w", err)
	}

	out, err := exec.Command(managed, "version").Output()
	if err != nil {
		return fmt.Errorf("extracted uv does not run: %w", err)
	}
	fmt.Printf("✓ uv %s installed at %s\n", strings.TrimSpace(string(out)), managed)
	return nil
}

func Remove() error {
	if os.Getenv(EnvOverride) != "" {
		return fmt.Errorf("%s is set; not removing externally-managed uv", EnvOverride)
	}
	dir := installBase()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Println("No molt-managed uv installation found.")
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Printf("✓ Removed %s\n", dir)
	return nil
}

func Version() (string, error) {
	p, err := Find()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(p, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func Status() {
	if override := os.Getenv(EnvOverride); override != "" {
		fmt.Printf("uv source:   %s=%s\n", EnvOverride, override)
	} else if _, err := os.Stat(managedBinPath()); err == nil {
		fmt.Printf("uv source:   molt-managed (%s)\n", managedBinPath())
	} else {
		fmt.Println("uv source:   NOT FOUND (will auto-download on next use)")
		return
	}
	if v, err := Version(); err == nil {
		fmt.Printf("uv version:  %s\n", v)
	}
	fmt.Printf("uv pinned:   %s\n", PinnedVersion)
}

func downloadVerified(url string, dst *os.File, sha256hex string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	h := sha256.New()
	w := io.MultiWriter(dst, h)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}
	if sha256hex != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != sha256hex {
			return fmt.Errorf("SHA-256 mismatch: want %s got %s", sha256hex, got)
		}
	}
	return nil
}

func extractUV(archivePath, destDir, goos string) error {
	if goos == "windows" {
		return extractZip(archivePath, destDir)
	}
	return extractTarGz(archivePath, destDir)
}

func extractTarGz(src, destDir string) error {
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
		base := filepath.Base(hdr.Name)
		if base != "uv" {
			continue
		}
		destPath := filepath.Join(destDir, "bin", "uv")
		if err := writeExecutable(destPath, tr); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("uv binary not found inside archive")
}

func extractZip(src, destDir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if filepath.Base(f.Name) != "uv.exe" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		destPath := filepath.Join(destDir, "bin", "uv.exe")
		err = writeExecutable(destPath, rc)
		rc.Close()
		return err
	}
	return fmt.Errorf("uv.exe not found inside zip archive")
}

func writeExecutable(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func releaseURL(version, goos, goarch string) (url, sha256hex string, err error) {
	archMap := map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
		"386":   "i686",
	}
	uvArch, ok := archMap[goarch]
	if !ok {
		return "", "", fmt.Errorf("unsupported architecture: %s", goarch)
	}
	const base = "https://github.com/astral-sh/uv/releases/download"
	var filename string
	switch goos {
	case "linux":
		filename = fmt.Sprintf("uv-%s-unknown-linux-musl.tar.gz", uvArch)
	case "darwin":
		filename = fmt.Sprintf("uv-%s-apple-darwin.tar.gz", uvArch)
	case "windows":
		filename = fmt.Sprintf("uv-%s-pc-windows-msvc.zip", uvArch)
	default:
		return "", "", fmt.Errorf("unsupported OS: %s", goos)
	}
	url = fmt.Sprintf("%s/%s/%s", base, version, filename)
	return url, "", nil
}
