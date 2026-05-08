package native

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"molt/internal/progress"
)

// ZigDefaultVersion is the version molt installs when no `[tool.molt.zig]
// version` is set. Bumped occasionally; users override it in pyproject.
const ZigDefaultVersion = "0.14.1"

// zigInstallMu serialises the install path of EnsureZig so concurrent
// callers (e.g. parallel kernel builds) don't race to download +
// extract the toolchain. The fast paths (env var, PATH, already-cached
// install dir) don't take the lock — only the slow "needs install"
// path does, with a re-check after acquisition so the second caller
// just observes the file the first one wrote.
var zigInstallMu sync.Mutex

// EnsureZig returns an absolute path to a usable `zig` binary, in this
// order of preference:
//
//   1. $ZIG environment variable (if it points to an executable)
//   2. `zig` already on PATH
//   3. ~/.molt/toolchains/zig/<version>/zig — previously installed by molt
//   4. download + extract → ~/.molt/toolchains/zig/<version>/
//
// `version` is the version to install if none is found. Existing PATH /
// $ZIG entries are accepted regardless of their version — we don't try
// to swap out a working zig the user already has. The download path is
// only taken when nothing else works.
//
// Safe to call from multiple goroutines: the install path is mutex-
// protected with a double-checked-locking pattern so concurrent callers
// share one install rather than racing N downloads.
func EnsureZig(version string) (string, error) {
	if version == "" {
		version = ZigDefaultVersion
	}

	// Fast paths — no lock needed, all read-only filesystem checks.
	if env := os.Getenv("ZIG"); env != "" {
		if p, err := exec.LookPath(env); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("zig"); err == nil {
		return p, nil
	}

	dir, err := zigInstallDir(version)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, zigBinaryName())
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}

	// Slow path: actually need to download + extract. Serialise so
	// parallel kernel-build workers don't all race to install at once.
	zigInstallMu.Lock()
	defer zigInstallMu.Unlock()

	// Re-check after acquiring the lock — another goroutine may have
	// just finished the install we were about to start.
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}

	if err := installZig(version, dir); err != nil {
		return "", err
	}
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("zig install completed but %s is missing: %w", bin, err)
	}
	return bin, nil
}

// ZigInstallExplicit forces a download/extract of the requested version,
// even if `zig` is already on PATH. Returns the absolute path to the
// installed binary. Used by the `molt install zig` command path so users
// can pin a project's toolchain regardless of what's globally installed.
func ZigInstallExplicit(version string) (string, error) {
	if version == "" {
		version = ZigDefaultVersion
	}
	dir, err := zigInstallDir(version)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, zigBinaryName())
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := installZig(version, dir); err != nil {
		return "", err
	}
	return bin, nil
}

func zigInstallDir(version string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "toolchains", "zig", version), nil
}

func zigBinaryName() string {
	if runtime.GOOS == "windows" {
		return "zig.exe"
	}
	return "zig"
}

// zigPlatform maps Go's GOOS/GOARCH to ziglang.org's release naming.
func zigPlatform() (osName, archName string, err error) {
	switch runtime.GOOS {
	case "darwin":
		osName = "macos"
	case "linux", "windows", "freebsd":
		osName = runtime.GOOS
	default:
		return "", "", fmt.Errorf("unsupported OS for zig install: %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64":
		archName = "x86_64"
	case "arm64":
		archName = "aarch64"
	case "386":
		archName = "x86"
	default:
		return "", "", fmt.Errorf("unsupported arch for zig install: %s", runtime.GOARCH)
	}
	return osName, archName, nil
}

func installZig(version, targetDir string) error {
	osName, archName, err := zigPlatform()
	if err != nil {
		return err
	}
	ext := "tar.xz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}

	// Zig flipped its release filename convention: pre-0.14 used
	// "zig-<os>-<arch>-<ver>", 0.14+ uses "zig-<arch>-<os>-<ver>". We
	// try the modern layout first; if that 404s, fall back to the old
	// one so users on either side of the boundary work.
	candidates := []string{
		fmt.Sprintf("zig-%s-%s-%s", archName, osName, version), // 0.14+
		fmt.Sprintf("zig-%s-%s-%s", osName, archName, version), // legacy
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "molt: installing zig %s for %s/%s …\n", version, osName, archName)

	tmp, err := os.CreateTemp("", "molt-zig-*."+ext)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	var lastErr error
	var url string
	for _, stem := range candidates {
		url = fmt.Sprintf("https://ziglang.org/download/%s/%s.%s", version, stem, ext)
		label := fmt.Sprintf("downloading %s.%s", stem, ext)
		if err := httpDownloadWithProgress(url, tmpPath, label); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("download zig %s for %s/%s: %w (tried %d url shapes)",
			version, osName, archName, lastErr, len(candidates))
	}

	// Extract directly into targetDir, stripping the top-level
	// `zig-<os>-<arch>-<version>/` directory the archive ships with.
	if runtime.GOOS == "windows" {
		if err := extractZigZip(tmpPath, targetDir); err != nil {
			return fmt.Errorf("extract %s: %w", url, err)
		}
	} else {
		if err := extractZigTarXZ(tmpPath, targetDir); err != nil {
			return fmt.Errorf("extract %s: %w", url, err)
		}
	}
	return nil
}

func httpDownload(url, dst string) error {
	return httpDownloadWithProgress(url, dst, "")
}

// httpDownloadWithProgress downloads url to dst, rendering a progress
// bar on stderr when label != "". An empty label runs silently.
func httpDownloadWithProgress(url, dst, label string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	src := io.Reader(resp.Body)
	if label != "" {
		dl := progress.NewDownload(label, resp.ContentLength)
		defer dl.Finish()
		src = dl.Wrap(src)
	}
	_, err = io.Copy(out, src)
	return err
}

// extractZigTarXZ shells out to `tar -xJ` because the Go stdlib doesn't
// have an xz decompressor. tar is universally available on macOS / Linux
// / FreeBSD where this code runs.
func extractZigTarXZ(archive, dst string) error {
	cmd := exec.Command("tar", "-xJf", archive, "-C", dst, "--strip-components=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar -xJf failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// extractZigZip handles the Windows .zip layout, stripping the
// single top-level `zig-<os>-<arch>-<version>/` directory.
func extractZigZip(archive, dst string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		// Strip first path component.
		rel := f.Name
		if i := strings.IndexAny(rel, "/\\"); i >= 0 {
			rel = rel[i+1:]
		}
		if rel == "" {
			continue
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if !strings.HasPrefix(out, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes target dir: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
			w.Close()
			return err
		}
		rc.Close()
		w.Close()
	}
	return nil
}

// zigArchiveSHA256 returns the hex-encoded SHA256 of a downloaded
// archive. Currently unused (left as a hook for future checksum
// verification against ziglang.org's index.json).
func zigArchiveSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// errZigNotFound is returned when no zig binary could be located or
// installed. Reserved for future API surfaces.
var errZigNotFound = errors.New("zig not available")
