// Package wheelsrc resolves a wheel filename to a local .whl path,
// preferring uv's wheel cache (~/.cache/uv) when it already has the file.
// On a miss, the wheel is downloaded into ~/.molt/pkg/.dl/{sha256}.whl.
package wheelsrc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type Resolver struct {
	UVCacheDir string // checked first; empty means skip
	Downloads  string // ~/.molt/pkg/.dl
}

func New() (*Resolver, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	uvCache := os.Getenv("UV_CACHE_DIR")
	if uvCache == "" {
		uvCache = filepath.Join(home, ".cache", "uv")
	}
	dl := filepath.Join(home, ".molt", "pkg", ".dl")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		return nil, err
	}
	return &Resolver{UVCacheDir: uvCache, Downloads: dl}, nil
}

// Locate returns a local path to a .whl matching filename+sha256. Strategy:
//  1. Walk the uv wheel cache for an exact filename match whose sha256 equals expected.
//  2. Otherwise download from url, verifying sha256 while streaming.
func (r *Resolver) Locate(filename, sha256Hex, url string) (string, error) {
	if p, ok := r.findInUV(filename, sha256Hex); ok {
		return p, nil
	}
	dest := filepath.Join(r.Downloads, sha256Hex+".whl")
	if got, err := fileSHA256(dest); err == nil && got == sha256Hex {
		return dest, nil
	}
	if url == "" {
		return "", fmt.Errorf("wheel %s missing from cache and no URL provided", filename)
	}
	if err := download(url, dest, sha256Hex); err != nil {
		return "", err
	}
	return dest, nil
}

func (r *Resolver) findInUV(filename, sha256Hex string) (string, bool) {
	if r.UVCacheDir == "" {
		return "", false
	}
	var found string
	_ = filepath.WalkDir(r.UVCacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() != filename {
			return nil
		}
		if sha256Hex != "" {
			got, sErr := fileSHA256(path)
			if sErr != nil || got != sha256Hex {
				return nil
			}
		}
		found = path
		return io.EOF // short-circuit
	})
	return found, found != ""
}

func download(url, dest, sha256Hex string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if sha256Hex != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != sha256Hex {
			os.Remove(tmp)
			return fmt.Errorf("sha256 mismatch downloading %s: want %s got %s", url, sha256Hex, got)
		}
	}
	return os.Rename(tmp, dest)
}

func fileSHA256(path string) (string, error) {
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
