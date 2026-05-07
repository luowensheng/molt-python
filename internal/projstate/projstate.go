// Package projstate locates and manages molt's per-project state directory.
//
// State used to live in `<project>/.molt/` — this polluted user project
// trees and made every IDE see molt's plumbing. State now lives entirely
// under the user's home, keyed by a hash of the absolute project path:
//
//	~/.molt/projects/<basename>-<hash16>/
//	├── syspath.json       # env spec
//	├── sitecustomize.py   # .pth processing
//	├── bin/               # python + console-script shims
//	├── uv-env/            # uv's hidden venv (UV_PROJECT_ENVIRONMENT target)
//	└── meta.json          # back-pointer to the original project + timestamps
//
// The hash uses sha256 of the absolute, cleaned project path. Same path →
// same dir across runs. Different paths → different dirs. The basename
// prefix makes `ls ~/.molt/projects/` browsable.
package projstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	binDirName     = "bin"
	syspathFile    = "syspath.json"
	siteCustomFile = "sitecustomize.py"
	uvEnvDir       = "uv-env"
	metaFile       = "meta.json"
)

// Root returns ~/.molt/projects, the parent of every project's state dir.
func Root() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to /tmp; better to keep working than to crash.
		return filepath.Join(os.TempDir(), ".molt", "projects")
	}
	return filepath.Join(home, ".molt", "projects")
}

// Hash returns a stable 16-hex-char identifier for projectDir.
// Computed from the absolute, cleaned (and EvalSymlinks'd, best-effort)
// project path so the same checkout always maps to the same state dir.
func Hash(projectDir string) string {
	abs := canonical(projectDir)
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16]
}

// Dir returns Root()/<basename>-<hash16> — the per-project state directory.
func Dir(projectDir string) string {
	abs := canonical(projectDir)
	return filepath.Join(Root(), filepath.Base(abs)+"-"+Hash(projectDir))
}

// Bin returns Dir()/bin (where shims are written by syncplan).
func Bin(projectDir string) string { return filepath.Join(Dir(projectDir), binDirName) }

// Syspath returns Dir()/syspath.json (the env spec written by syncplan).
func Syspath(projectDir string) string { return filepath.Join(Dir(projectDir), syspathFile) }

// SiteCustomize returns Dir()/sitecustomize.py (loaded via PYTHONPATH).
func SiteCustomize(projectDir string) string {
	return filepath.Join(Dir(projectDir), siteCustomFile)
}

// UvEnv returns Dir()/uv-env (UV_PROJECT_ENVIRONMENT target — uv's hidden venv).
func UvEnv(projectDir string) string { return filepath.Join(Dir(projectDir), uvEnvDir) }

// Meta returns Dir()/meta.json (back-pointer to the original project).
func Meta(projectDir string) string { return filepath.Join(Dir(projectDir), metaFile) }

// Native returns Dir()/native.json — the per-project manifest of compiled
// native artefacts (cython now, multipy in future). Drives `molt build`'s
// bundling step and `molt gc`'s reachability analysis for the global
// ~/.molt/native cache.
func Native(projectDir string) string { return filepath.Join(Dir(projectDir), "native.json") }

// MetaInfo is the on-disk schema for meta.json.
type MetaInfo struct {
	ProjectDir  string    `json:"project_dir"`
	Created     time.Time `json:"created"`
	LastSync    time.Time `json:"last_sync"`
	MoltVersion string    `json:"molt_version,omitempty"`
}

// WriteMeta creates or updates meta.json. Sets Created on first call,
// updates LastSync on every call. Caller is responsible for ensuring
// Dir(projectDir) exists.
func WriteMeta(projectDir, moltVersion string) error {
	abs := canonical(projectDir)
	if err := os.MkdirAll(Dir(projectDir), 0o755); err != nil {
		return err
	}
	now := time.Now().UTC()
	mi := MetaInfo{ProjectDir: abs, Created: now, LastSync: now, MoltVersion: moltVersion}
	if existing, err := readMeta(projectDir); err == nil && !existing.Created.IsZero() {
		mi.Created = existing.Created
	}
	data, err := json.MarshalIndent(mi, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Meta(projectDir), data, 0o644)
}

func readMeta(projectDir string) (MetaInfo, error) {
	data, err := os.ReadFile(Meta(projectDir))
	if err != nil {
		return MetaInfo{}, err
	}
	var mi MetaInfo
	if err := json.Unmarshal(data, &mi); err != nil {
		return MetaInfo{}, err
	}
	return mi, nil
}

// Entry describes one project state dir found under Root() during ListAll.
type Entry struct {
	Dir          string   // absolute path of the state dir
	ProjectDir   string   // original project path from meta.json (may not exist)
	Hash         string   // the 16-char suffix
	ProjectAlive bool     // whether ProjectDir/pyproject.toml currently exists
	Meta         MetaInfo // parsed meta.json (zero value if missing/malformed)
}

// ListAll walks Root() and returns one Entry per immediate sub-directory.
// Entries with malformed or missing meta.json still appear so `molt gc`
// can offer to clean them up.
func ListAll() ([]Entry, error) {
	root := Root()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	es, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(es))
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		// Hash is the trailing 16 chars after the last '-'; if absent,
		// surface the whole name so gc can still report it.
		hash := ""
		if i := lastDash(e.Name()); i >= 0 && len(e.Name())-i-1 == 16 {
			hash = e.Name()[i+1:]
		}
		entry := Entry{Dir: dir, Hash: hash}
		if data, err := os.ReadFile(filepath.Join(dir, metaFile)); err == nil {
			var mi MetaInfo
			if json.Unmarshal(data, &mi) == nil {
				entry.Meta = mi
				entry.ProjectDir = mi.ProjectDir
			}
		}
		if entry.ProjectDir != "" {
			if _, err := os.Stat(filepath.Join(entry.ProjectDir, "pyproject.toml")); err == nil {
				entry.ProjectAlive = true
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// LegacyInTreeDir returns (<projectDir>/.molt, true) if that directory
// still exists from before this refactor. Used by sync/info to nudge users
// to clean up; never auto-deleted.
func LegacyInTreeDir(projectDir string) (string, bool) {
	p := filepath.Join(projectDir, ".molt")
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return p, true
	}
	return "", false
}

// canonical normalises projectDir to an absolute, cleaned path. EvalSymlinks
// is best-effort: failure (e.g. nonexistent dir) falls back to the absolute
// non-resolved form, so Dir() still returns something deterministic.
func canonical(projectDir string) string {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func lastDash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '-' {
			return i
		}
	}
	return -1
}

// debug helper — present so callers can format paths without re-importing fmt.
var _ = fmt.Sprintf
