// Package store implements a global content-addressed store of unpacked
// Python wheels under ~/.molt/pkg/. Layout:
//
//	~/.molt/pkg/{name}/{version}/{py_tag}-{abi_tag}-{platform_tag}/
//	   <wheel contents — what would normally land in site-packages>
//	   .ok                  written last; presence => entry is complete
//	   .meta.json           {entry_points, source_sha256, wheel_filename}
//
// Installs are atomic: a wheel is unpacked into ~/.molt/pkg/.tmp/{rand}/
// and only renamed into its final location once .ok has been written.
package store

import (
	"archive/zip"
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Store struct{ Root string }

type Key struct {
	Name        string // PEP 503 normalized lowercase
	Version     string
	PyTag       string // e.g. cp312, py3
	AbiTag      string // e.g. cp312, abi3, none
	PlatformTag string // e.g. macosx_11_0_arm64, any
}

func (k Key) tagDir() string { return k.PyTag + "-" + k.AbiTag + "-" + k.PlatformTag }

type Meta struct {
	WheelFilename string                       `json:"wheel_filename"`
	SourceSHA256  string                       `json:"source_sha256"`
	EntryPoints   map[string]map[string]string `json:"entry_points,omitempty"`
}

func Default() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".molt", "pkg")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

func (s *Store) Path(k Key) string {
	return filepath.Join(s.Root, NormalizeName(k.Name), k.Version, k.tagDir())
}

func (s *Store) Has(k Key) bool {
	_, err := os.Stat(filepath.Join(s.Path(k), ".ok"))
	return err == nil
}

func (s *Store) Meta(k Key) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(s.Path(k), ".meta.json"))
	if err != nil {
		return nil, err
	}
	m := &Meta{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Install unpacks wheelPath into the store entry for k. If sha256Hex is non-empty
// it is verified before unpacking. Idempotent: if the entry already has .ok, returns nil.
func (s *Store) Install(k Key, wheelPath, sha256Hex string) error {
	if s.Has(k) {
		return nil
	}
	if sha256Hex != "" {
		got, err := fileSHA256(wheelPath)
		if err != nil {
			return err
		}
		if got != sha256Hex {
			return fmt.Errorf("sha256 mismatch for %s: want %s got %s", filepath.Base(wheelPath), sha256Hex, got)
		}
	}
	dest := s.Path(k)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmpRoot := filepath.Join(s.Root, ".tmp")
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return err
	}
	tmp, err := mkRandDir(tmpRoot)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := unzipWheel(wheelPath, tmp); err != nil {
		return fmt.Errorf("unzip %s: %w", filepath.Base(wheelPath), err)
	}
	meta := &Meta{
		WheelFilename: filepath.Base(wheelPath),
		SourceSHA256:  sha256Hex,
		EntryPoints:   readEntryPoints(tmp),
	}
	if err := writeJSON(filepath.Join(tmp, ".meta.json"), meta); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".ok"), []byte("ok"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		// Race: someone else won. If the destination now exists with .ok, that's fine.
		if _, statErr := os.Stat(filepath.Join(dest, ".ok")); statErr == nil {
			return nil
		}
		return fmt.Errorf("publish %s: %w", dest, err)
	}
	return nil
}

// Lock acquires a process-level exclusive lock on ~/.molt/pkg.lock.
// Use around batches of Install calls when multiple molt processes may run
// concurrently. The returned release function unlocks and closes the file.
// The platform-specific implementation lives in lock_unix.go / lock_windows.go.
func (s *Store) Lock() (release func(), err error) {
	parent := filepath.Dir(s.Root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	lp := filepath.Join(parent, "pkg.lock")
	return acquireLock(lp)
}

// NormalizeName applies the relaxed PEP 503 normalization: lowercase, runs of
// _ . - collapsed to single -.
func NormalizeName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		if r == '_' || r == '.' || r == '-' {
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	return strings.Trim(b.String(), "-")
}

// ParseWheelFilename decodes {dist}-{ver}(-{build})?-{py}-{abi}-{plat}.whl
// per PEP 427. Compressed tags ("cp311.cp312") are kept verbatim so the cache
// key matches uniquely back to the source wheel.
func ParseWheelFilename(fn string) (Key, error) {
	base := strings.TrimSuffix(filepath.Base(fn), ".whl")
	parts := strings.Split(base, "-")
	if len(parts) != 5 && len(parts) != 6 {
		return Key{}, fmt.Errorf("not a wheel filename: %s", fn)
	}
	off := 0
	if len(parts) == 6 {
		off = 1 // skip build tag
	}
	return Key{
		Name:        NormalizeName(parts[0]),
		Version:     parts[1],
		PyTag:       parts[2+off],
		AbiTag:      parts[3+off],
		PlatformTag: parts[4+off],
	}, nil
}

// ── internals ────────────────────────────────────────────────────────────────

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

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func mkRandDir(parent string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, hex.EncodeToString(buf[:]))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// unzipWheel extracts a .whl into destDir. It rejects entries whose path would
// escape destDir (zip-slip). File mode is taken from the zip header; entries
// recorded in <dist-info>/RECORD with the executable bit get 0o755.
func unzipWheel(src, destDir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	cleanRoot, err := filepath.Abs(destDir)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		// Normalize to forward-slash, reject leading / or .. components.
		name := f.Name
		if strings.Contains(name, "\\") {
			name = strings.ReplaceAll(name, "\\", "/")
		}
		if strings.HasPrefix(name, "/") {
			return fmt.Errorf("zip-slip: absolute path %q", name)
		}
		clean := filepath.Clean(name)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("zip-slip: %q escapes root", name)
		}
		dest := filepath.Join(destDir, clean)
		absDest, err := filepath.Abs(dest)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(absDest, cleanRoot+string(filepath.Separator)) && absDest != cleanRoot {
			return fmt.Errorf("zip-slip: %q escapes root", name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		rc.Close()
		out.Close()
	}
	return nil
}

// readEntryPoints parses every <pkg>-<ver>.dist-info/entry_points.txt found in
// dest. Returns map[group]map[name]target. Quietly skips anything malformed.
func readEntryPoints(dest string) map[string]map[string]string {
	out := map[string]map[string]string{}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".dist-info") {
			continue
		}
		path := filepath.Join(dest, e.Name(), "entry_points.txt")
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		group := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				group = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
				if _, ok := out[group]; !ok {
					out[group] = map[string]string{}
				}
				continue
			}
			if group == "" {
				continue
			}
			i := strings.Index(line, "=")
			if i < 0 {
				continue
			}
			name := strings.TrimSpace(line[:i])
			target := strings.TrimSpace(line[i+1:])
			out[group][name] = target
		}
		f.Close()
	}
	return out
}
