package native

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// CacheRoot returns ~/.molt/native. The native compile cache, content-keyed,
// shared across every project on the machine.
func CacheRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "native"), nil
}

// CacheEntry is the on-disk meta.json sitting next to each compiled
// artefact under ~/.molt/native/<hash>/.
type CacheEntry struct {
	Lang       string    `json:"lang"`
	SoName     string    `json:"so_name"`
	Source     string    `json:"source"`     // original .pyx path (informational)
	AbiTag     string    `json:"abi"`
	Platform   string    `json:"platform"`
	CompiledAt time.Time `json:"compiled_at"`
}

// cacheDir returns ~/.molt/native/<hash>.
func cacheDir(hash string) (string, error) {
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, hash), nil
}

// hasCacheEntry reports whether <hash>/<soName> exists. We check for the .so
// rather than the directory so a half-written cache (compile failed mid-way)
// is correctly treated as a miss.
func hasCacheEntry(hash, soName string) (bool, string, error) {
	d, err := cacheDir(hash)
	if err != nil {
		return false, "", err
	}
	p := filepath.Join(d, soName)
	if _, err := os.Stat(p); err == nil {
		return true, p, nil
	} else if !os.IsNotExist(err) {
		return false, "", err
	}
	return false, p, nil
}

// writeCacheMeta writes meta.json next to a previously-compiled .so under
// ~/.molt/native/<hash>/. The .so itself is written directly by
// compileOneInto, so this only records the sidecar.
func writeCacheMeta(hash string, ce CacheEntry) error {
	d, err := cacheDir(hash)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ce, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "meta.json"), data, 0o644)
}

// ListCacheEntries enumerates ~/.molt/native/<hash>/ for `molt gc`.
type ListedEntry struct {
	Dir   string
	Hash  string
	Meta  CacheEntry
}

func ListCacheEntries() ([]ListedEntry, error) {
	root, err := CacheRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ListedEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		le := ListedEntry{Dir: dir, Hash: e.Name()}
		if data, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
			_ = json.Unmarshal(data, &le.Meta)
		}
		out = append(out, le)
	}
	return out, nil
}
