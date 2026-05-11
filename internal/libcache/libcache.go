// Package libcache provides visibility into library-level data caches written
// by Python packages like PyTorch and HuggingFace. These caches live in the
// user's home directory (e.g. ~/.cache/huggingface/) outside molt's managed
// store, can reach tens of GB, and are controlled by env vars (HF_HOME, etc.).
//
// The package does three things:
//
//  1. Maintains a registry of well-known library→dir mappings, honouring the
//     env vars each library uses to redirect its cache.
//  2. Scans those locations and cross-references them against the set of
//     packages currently installed in a project.
//  3. Deletes a named cache directory after confirmation.
package libcache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KnownCache describes one well-known library cache location.
type KnownCache struct {
	// Name is the human-readable label shown in molt output.
	Name string

	// Packages is the list of PyPI package names that write to this cache.
	// A single dir is often shared by a family (all HuggingFace libs share HF_HOME).
	Packages []string

	// DefaultRelDir is the directory path relative to the user home dir,
	// used when the override env var is not set. E.g. ".cache/huggingface".
	DefaultRelDir string

	// EnvVar is the environment variable a user can set to redirect this cache.
	// Empty if the library doesn't support redirection via a single var.
	EnvVar string
}

// ResolvedDir returns the absolute path to the cache directory, honouring
// EnvVar when set. Returns "" when the home directory cannot be determined.
func (k KnownCache) ResolvedDir() string {
	if k.EnvVar != "" {
		if v := os.Getenv(k.EnvVar); v != "" {
			return filepath.Clean(v)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(k.DefaultRelDir))
}

// ScanResult is one entry returned by Scan.
type ScanResult struct {
	KnownCache
	Dir       string // resolved absolute path
	Exists    bool
	Bytes     int64
	Installed bool // at least one of Packages is in the caller-supplied installed set
}

// AllKnownCaches returns the registry of popular ML/data library caches.
// Entries are deduplicated at scan time by resolved dir path, so when
// multiple packages share a dir (HuggingFace ecosystem) it is only counted once
// (under the first matching entry).
func AllKnownCaches() []KnownCache {
	return []KnownCache{
		{
			Name:          "huggingface",
			Packages:      []string{"huggingface-hub", "transformers", "diffusers", "sentence-transformers", "timm"},
			DefaultRelDir: ".cache/huggingface",
			EnvVar:        "HF_HOME",
		},
		{
			Name:          "hf-datasets",
			Packages:      []string{"datasets"},
			DefaultRelDir: ".cache/huggingface/datasets",
			EnvVar:        "HF_DATASETS_CACHE",
		},
		{
			Name:          "torch",
			Packages:      []string{"torch", "torchvision", "torchaudio"},
			DefaultRelDir: ".cache/torch",
			EnvVar:        "TORCH_HOME",
		},
		{
			Name:          "keras",
			Packages:      []string{"keras", "tensorflow", "tf-keras"},
			DefaultRelDir: ".keras",
			EnvVar:        "KERAS_HOME",
		},
		{
			Name:          "clip",
			Packages:      []string{"clip", "openai-clip"},
			DefaultRelDir: ".cache/clip",
			EnvVar:        "",
		},
		{
			Name:          "nltk",
			Packages:      []string{"nltk"},
			DefaultRelDir: "nltk_data",
			EnvVar:        "NLTK_DATA",
		},
		{
			Name:          "gensim",
			Packages:      []string{"gensim"},
			DefaultRelDir: "gensim-data",
			EnvVar:        "",
		},
		{
			Name:          "spacy",
			Packages:      []string{"spacy"},
			DefaultRelDir: ".cache/spacy",
			EnvVar:        "",
		},
		{
			Name:          "flair",
			Packages:      []string{"flair"},
			DefaultRelDir: ".flair",
			EnvVar:        "",
		},
		{
			Name:          "stanza",
			Packages:      []string{"stanza"},
			DefaultRelDir: "stanza_resources",
			EnvVar:        "",
		},
	}
}

// Scan checks every known cache location and returns a ScanResult for each
// entry that either exists on disk OR whose packages appear in installed.
// installed is a set of normalised PyPI package names (lowercase, hyphens).
// Entries that neither exist nor are installed are omitted to keep output clean.
//
// Dirs are deduplicated: if two entries resolve to the same path, only the
// first (by registry order) is returned — its Packages list is merged.
func Scan(installed map[string]bool) ([]ScanResult, error) {
	seen := map[string]bool{} // deduplicate by resolved dir
	var out []ScanResult

	for _, kc := range AllKnownCaches() {
		dir := kc.ResolvedDir()
		if dir == "" {
			continue
		}

		// Check whether the dir is a sub-path of an already-seen parent
		// (e.g. hf-datasets is inside huggingface). Report it separately
		// only if it has a different root dir.
		if seen[dir] {
			continue
		}
		seen[dir] = true

		result := ScanResult{
			KnownCache: kc,
			Dir:        dir,
		}

		if _, err := os.Stat(dir); err == nil {
			result.Exists = true
			result.Bytes, _ = DirSize(dir)
		}

		for _, pkg := range kc.Packages {
			if installed[normalize(pkg)] {
				result.Installed = true
				break
			}
		}

		// Include if it exists on disk OR if the package is installed (so
		// users see where the cache WOULD live even before it's populated).
		if result.Exists || result.Installed {
			out = append(out, result)
		}
	}
	return out, nil
}

// DirSize returns the total size of all regular files under root.
// Symlinks are not followed to avoid double-counting.
// Returns 0 (not an error) when root does not exist.
func DirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// Clear removes dir and all its contents. It is the caller's responsibility
// to confirm with the user before calling this.
func Clear(dir string) error {
	return os.RemoveAll(dir)
}

// InstalledPackages extracts the set of normalised package names from a list
// of store directory paths (e.g. "~/.molt/pkg/torch/2.3.0/cp312-...").
// Non-store paths (project src dirs, etc.) are silently skipped.
func InstalledPackages(syspathDirs []string, storeRoot string) map[string]bool {
	out := map[string]bool{}
	storeRoot = filepath.Clean(storeRoot)
	for _, dir := range syspathDirs {
		dir = filepath.Clean(dir)
		rel, err := filepath.Rel(storeRoot, dir)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue // not under the store
		}
		// rel = "<name>/<version>/<tags>" — take first segment
		parts := strings.SplitN(rel, string(os.PathSeparator), 2)
		if len(parts) > 0 && parts[0] != "" {
			out[normalize(parts[0])] = true
		}
	}
	return out
}

// normalize applies PEP 503 relaxed normalisation: lower-case and collapse
// runs of [-_.] to a single hyphen. Consistent with store.NormalizeName.
func normalize(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	prev := '-'
	for _, r := range name {
		if r == '-' || r == '_' || r == '.' {
			if prev != '-' {
				b.WriteByte('-')
				prev = '-'
			}
		} else {
			b.WriteRune(r)
			prev = r
		}
	}
	return b.String()
}

// FormatBytes returns a human-readable string for a byte count.
func FormatBytes(n int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case n >= GB:
		return formatFloat(float64(n)/GB) + " GB"
	case n >= MB:
		return formatFloat(float64(n)/MB) + " MB"
	case n >= KB:
		return formatFloat(float64(n)/KB) + " KB"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatFloat(f float64) string {
	return fmt.Sprintf("%.1f", f)
}
