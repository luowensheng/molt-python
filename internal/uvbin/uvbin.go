// internal/uvbin/uvbin.go
// Package uvbin manages molt's private uv installation.
//
// Resolution order (first match wins):
//
//  1. MOLT_UV env var — absolute path to a uv binary
//  2. ~/.molt/uv/bin/uv  — molt's self-managed installation
//  3. uv on PATH         — system uv (convenience, not relied on in CI)
//
// The self-managed binary is downloaded from the official uv GitHub release
// for the current platform. The version is pinned in [PinnedVersion] so
// every developer and CI runner uses the same uv regardless of what is
// installed on the host.
//
// Use [Ensure] to guarantee the binary exists before calling [Find].
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
// Bump this intentionally; it is the single source of truth.
const PinnedVersion = "0.4.18"

// EnvOverride is the environment variable users can set to point molt at a
// specific uv binary, bypassing both the self-managed and PATH copies.
const EnvOverride = "MOLT_UV"

// installBase returns ~/.molt/uv
func installBase() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "uv")
}

// managedBinPath returns the path where molt's own uv lives.
func managedBinPath() string {
	bin := "uv"
	if runtime.GOOS == "windows" {
		bin = "uv.exe"
	}
	return filepath.Join(installBase(), "bin", bin)
}

// Find returns the path to a usable uv binary.
//
// It does NOT download uv; call [Ensure] first if you want automatic
// installation.  Returns an error if no binary is found.
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

	// 3. System PATH (convenience fallback — not guaranteed in CI).
	if p, err := exec.LookPath("uv"); err == nil {
		return p, nil
	}

	return "", fmt.Errorf(
		"uv not found — run 'molt uv install' or set %s to an existing uv binary",
		EnvOverride,
	)
}

// MustFind calls [Find] and panics on error. Useful in test helpers.
func MustFind() string {
	p, err := Find()
	if err != nil {
		panic(err)
	}
	return p
}

// Ensure guarantees that a uv binary is available, downloading the pinned
// version into ~/.molt/uv/bin/ if neither the override nor a managed copy
// exists. Returns the path to the binary.
func Ensure() (string, error) {
	// If override is set, trust the caller completely.
	if override := os.Getenv(EnvOverride); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("%s=%q: file not found", EnvOverride, override)
	}

	// Already installed?
	managed := managedBinPath()
	if _, err := os.Stat(managed); err == nil {
		return managed, nil
	}

	// Download and install.
	if err := Install(PinnedVersion, false); err != nil {
		return "", err
	}
	return managed, nil
}

// Install downloads the given uv version into ~/.molt/uv.
// If force is true the existing binary is replaced even if already present.
func Install(version string, force bool) error {
	managed := managedBinPath()
	if !force {
		if _, err := os.Stat(managed); err == nil {
			return nil // already present
		}
	}

	url, sha256Expected, err := releaseURL(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	fmt.Printf("Downloading uv %s for %s/%s...\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  %s\n", url)

	// Download to a temp file.
	tmp, err := os.CreateTemp("", "molt-uv-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := downloadVerified(url, tmp, sha256Expected); err != nil {
		tmp.Close()
		return fmt.Errorf("download uv: %w", err)
	}
	tmp.Close()

	// Extract into ~/.molt/uv/
	destDir := installBase()
	if err := os.MkdirAll(filepath.Join(destDir, "bin"), 0o755); err != nil {
		return err
	}

	if err := extractUV(tmpName, destDir, runtime.GOOS); err != nil {
		return fmt.Errorf("extract uv: %w", err)
	}

	// Verify the extracted binary runs.
	out, err := exec.Command(managed, "version").Output()
	if err != nil {
		return fmt.Errorf("extracted uv does not run: %w", err)
	}
	fmt.Printf("  ✓ uv %s installed at %s\n", strings.TrimSpace(string(out)), managed)
	return nil
}

// Remove deletes molt's self-managed uv installation.
// Has no effect if the override env var is set.
func Remove() error {
	if os.Getenv(EnvOverride) != "" {
		return fmt.Errorf("MOLT_UV is set; not removing externally-managed uv")
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

// Version returns the version string reported by the resolved uv binary.
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

// Status prints a human-readable status of the uv resolution.
func Status() {
	if override := os.Getenv(EnvOverride); override != "" {
		fmt.Printf("uv source:   %s=%s\n", EnvOverride, override)
	} else if _, err := os.Stat(managedBinPath()); err == nil {
		fmt.Printf("uv source:   molt-managed (%s)\n", managedBinPath())
	} else if p, err := exec.LookPath("uv"); err == nil {
		fmt.Printf("uv source:   system PATH (%s)\n", p)
	} else {
		fmt.Println("uv source:   NOT FOUND")
		return
	}

	if v, err := Version(); err == nil {
		fmt.Printf("uv version:  %s\n", v)
	}
	fmt.Printf("uv pinned:   %s\n", PinnedVersion)
}

// ── Download / extract ────────────────────────────────────────────────────────

// downloadVerified fetches url into dst and optionally verifies the SHA-256.
// sha256hex may be empty to skip verification (during bootstrap).
func downloadVerified(url string, dst *os.File, sha256hex string) error {
	resp, err := http.Get(url) //nolint:gosec // URL is constructed from a pinned constant
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

// extractUV unpacks the downloaded archive and places the uv binary at
// <destDir>/bin/uv (or uv.exe on Windows).
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

		// We only want the uv binary itself (ignore uvx, licenses, etc.)
		base := filepath.Base(hdr.Name)
		if base != "uv" {
			continue
		}

		destPath := filepath.Join(destDir, "bin", "uv")
		if err := writeExecutable(destPath, tr); err != nil {
			return err
		}
		return nil // done
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

// ── Platform → release URL ────────────────────────────────────────────────────

// releaseURL returns the download URL and expected SHA-256 for a given uv
// release, OS, and architecture.
//
// SHA-256 values are left empty here because they change with every release.
// In production you would either:
//
//	a) embed a checksum table keyed by version+platform, or
//	b) fetch the upstream sha256sums file and parse it.
//
// For now we skip checksum verification on the first install (the binary
// is verified by running `uv version` after extraction).
func releaseURL(version, goos, goarch string) (url, sha256hex string, err error) {
	// Map Go arch names to the names used in uv release filenames.
	archMap := map[string]string{
		"amd64": "x86_64",
		"arm64": "aarch64",
		"386":   "i686",
	}
	uvArch, ok := archMap[goarch]
	if !ok {
		return "", "", fmt.Errorf("unsupported architecture for uv self-install: %s", goarch)
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
		return "", "", fmt.Errorf("unsupported OS for uv self-install: %s", goos)
	}

	url = fmt.Sprintf("%s/%s/%s", base, version, filename)
	// sha256hex intentionally empty — see doc comment above.
	return url, "", nil
}
