package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Cache is a content-addressable cache keyed by SHA-256.
type Cache struct {
	Dir string
}

// New creates (or opens) the cache at dir.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &Cache{Dir: dir}, nil
}

// DefaultDir returns the default user cache directory.
func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "pyexec")
}

// path returns the filesystem path for a given checksum key.
func (c *Cache) path(key string) string {
	return filepath.Join(c.Dir, key)
}

// Has reports whether the entry exists.
func (c *Cache) Has(key string) bool {
	_, err := os.Stat(c.path(key))
	return err == nil
}

// Get returns the cached bytes, or false if absent.
func (c *Cache) Get(key string) ([]byte, bool) {
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Put stores data under key and verifies the SHA-256.
func (c *Cache) Put(key string, data []byte) error {
	// Verify checksum.
	h := sha256.Sum256(data)
	got := hex.EncodeToString(h[:])
	if key != got {
		return fmt.Errorf("checksum mismatch: expected %s got %s", key, got)
	}
	return os.WriteFile(c.path(key), data, 0o644)
}

// PutStream writes from r, computes its SHA-256, verifies it equals expected,
// saves to cache and returns the path.
func (c *Cache) PutStream(expected string, r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(c.Dir, "dl-")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if expected != "" && got != expected {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("sha256 mismatch: expected %s got %s", expected, got)
	}
	dest := c.path(got)
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("commit cache entry: %w", err)
	}
	return dest, nil
}

// Path returns the cached file path for key, or empty string if absent.
func (c *Cache) Path(key string) string {
    p := c.path(key)
    info, err := os.Stat(p)
    // Return empty if: error occurred, OR path is a directory (cache stores files, not dirs)
    if err != nil || info.IsDir() {
        return ""
    }
    return p
}

// Size returns total cache size in bytes.
func (c *Cache) Size() (int64, error) {
	var total int64
	err := filepath.Walk(c.Dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
