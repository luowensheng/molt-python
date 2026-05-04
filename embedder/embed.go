// embedder/embed.go
//
// The embedder walks the project tree and produces a deterministic,
// reproducible tar.gz payload for embedding into a molt binary.
//
// Two modes of file selection:
//
//  1. LEGACY (no molt.yaml, or molt.yaml without `include`):
//     A denylist. Everything is included except patterns in DefaultIgnoreList
//     (plus any user additions from .moltignore). This matches the original
//     behaviour and keeps backward compatibility.
//
//  2. ALLOWLIST (molt.yaml has `include:`):
//     Only files matching an `include:` glob are packaged. Plus any
//     explicitly-declared `assets:`. Plus the .molt/manifest.json that
//     the builder writes. Minus `exclude:` patterns. Minus SensitivePatterns
//     (always a safety net, regardless of mode).
//
// Allowlist mode is safer for existing projects: you KNOW what's shipping,
// rather than hoping the default ignore list caught your fixtures dir.
//
// The embedder returns a []PackagedFile alongside the archive, carrying
// path/size/sha256/source for every file. The caller (builder.go) feeds
// this straight into the integrity package.

package embedder

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"molt/pkg/types"
)

// Config controls the embedding process.
type Config struct {
	RootDir     string // Project root to scan
	OutputFile  string // Destination tar.gz path
	PrefixInTar string // e.g. "src/" — must match launcher's extraction logic
	IgnoreFile  string // Optional .moltignore

	// FailOnSensitive aborts when a file matches SensitivePatterns. Default on.
	FailOnSensitive bool

	// IncludeGlobs enables allowlist mode when non-empty. Patterns use
	// doublestar syntax — so "src/**/*.py" works as expected.
	IncludeGlobs []string

	// ExcludeGlobs subtracts from whatever the include set produces. Applied
	// in both legacy and allowlist modes.
	ExcludeGlobs []string

	// Assets are explicitly-declared non-code files that the application
	// needs at runtime. Each AssetFile.Path may be literal or a glob.
	Assets []types.MoltAssetFile

	// MaxFileSizeMB warns (does not fail) if any single file exceeds this.
	// 0 disables the check.
	MaxFileSizeMB int

	// MaxTotalSizeMB aborts the build if the total payload exceeds this.
	// 0 disables the check.
	MaxTotalSizeMB int
}

// Result describes what was packaged, for handoff to the integrity layer.
type Result struct {
	Files         []types.PackagedFile
	TotalBytes    int64
	SizeWarnings  []string // files over MaxFileSizeMB, for surfacing to the user
}

// DefaultIgnoreList covers the files you almost never want in a shipped
// binary. Used only in legacy (denylist) mode.
var DefaultIgnoreList = []string{
	// Version control & CI
	".git/", ".github/", ".gitlab/", ".svn/", ".hg/",
	".circleci/", ".travis.yml", ".gitlab-ci.yml",
	// IDE & editors
	".vscode/", ".idea/", "*.swp", "*.swo", "*~", "*.bak",
	// Python caches & artifacts
	"__pycache__/", "*.pyc", "*.pyo", "*.egg-info/",
	".pytest_cache/", ".mypy_cache/", ".ruff_cache/", ".tox/", ".nox/",
	// Virtual envs & package managers
	".venv/", "venv/", "env/", ".env",
	"node_modules/", "vendor/",
	// Build & dist
	"build/", "dist/", "target/", "*.egg", "*.whl",
	// OS junk
	".DS_Store", "Thumbs.db", "desktop.ini",
	// Docs, tests & dev tooling
	"docs/", "test/", "tests/", "conftest.py",
	"pytest.ini", "tox.ini", "Makefile", "Dockerfile",
	"docker-compose*.yml", "README*", "LICENSE*", "CHANGELOG*",

	// molt's own artifacts
	"molt.yaml", ".moltignore",
}

// SensitivePatterns are matched in BOTH modes. These always fail the build
// in strict mode — the idea is that shipping a private key is never a
// legitimate intent, even if the user opts into allowlist mode and
// explicitly globs `**/*`.
var SensitivePatterns = []string{
	"*.pem", "*.key", "*.p12", "*.pfx", "*.crt",
	"*password*", "*secret*", "*credential*", "*token*",
	"aws_credentials", "id_rsa", "id_ed25519", "id_ecdsa",
	".env.local", ".env.*.local", "credentials.json",
}

// Run executes the embedding process and returns a Result describing the
// packaged payload. The tar.gz is written to cfg.OutputFile.
func Run(cfg Config) (*Result, error) {
	ignores := append([]string(nil), DefaultIgnoreList...)
	if cfg.IgnoreFile != "" {
		if extra, err := readIgnoreFile(cfg.IgnoreFile); err == nil {
			ignores = append(ignores, extra...)
		}
	}

	allowlist := len(cfg.IncludeGlobs) > 0

	matched, err := collectFiles(cfg, ignores, allowlist)
	if err != nil {
		return nil, fmt.Errorf("collect files: %w", err)
	}

	// Add assets on top of whatever the primary selector produced. Assets
	// are declared explicitly, so they take priority — but we still run
	// them past the sensitive-file check.
	assetPaths, err := resolveAssets(cfg.RootDir, cfg.Assets)
	if err != nil {
		return nil, fmt.Errorf("resolve assets: %w", err)
	}
	for _, ap := range assetPaths {
		if cfg.FailOnSensitive && isSensitive(ap.RelPath) {
			return nil, fmt.Errorf(
				"SECURITY BLOCK: asset %q matches a sensitive pattern; "+
					"refusing to ship", ap.RelPath,
			)
		}
		matched = upsertFile(matched, ap)
	}

	if len(matched) == 0 {
		return nil, fmt.Errorf(
			"no files matched inclusion criteria — check include/exclude/assets",
		)
	}

	return writeTarGz(cfg, matched)
}

// fileEntry carries everything we need for one packaged file during the
// build. `Source` flows into the integrity manifest.
type fileEntry struct {
	RelPath     string
	Source      types.PackagedFileSource
	Description string
}

// upsertFile inserts or updates a file in the slice, keyed by RelPath.
// If a file appears as both include-matched and an asset, the asset's
// metadata wins because it carries description and intent.
func upsertFile(files []fileEntry, newEntry fileEntry) []fileEntry {
	for i, f := range files {
		if f.RelPath == newEntry.RelPath {
			// Asset wins over include — we want the description preserved.
			if newEntry.Source == types.SourceAsset {
				files[i] = newEntry
			}
			return files
		}
	}
	return append(files, newEntry)
}

// collectFiles walks RootDir and returns every file that should be packaged,
// applying the appropriate mode (allowlist vs denylist).
func collectFiles(cfg Config, ignores []string, allowlist bool) ([]fileEntry, error) {
	var matched []fileEntry

	err := filepath.WalkDir(cfg.RootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == cfg.RootDir {
			return nil
		}
		rel, _ := filepath.Rel(cfg.RootDir, path)
		rel = filepath.ToSlash(rel)

		// Sensitive-file check runs in BOTH modes. Even an allowlist pattern
		// of `**/*` should not ship a private key.
		if cfg.FailOnSensitive && !d.IsDir() && isSensitive(rel) {
			return fmt.Errorf("SECURITY BLOCK: sensitive file matched: %s", rel)
		}

		// Explicit exclusions (from molt.yaml `exclude:`) always apply.
		if matchesAny(cfg.ExcludeGlobs, rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if allowlist {
			// ALLOWLIST MODE: walk everywhere, keep only include matches.
			// We can't skip directories eagerly because an include glob
			// like "**/*.py" needs to descend into everything.
			if d.IsDir() {
				return nil
			}
			if matchesAny(cfg.IncludeGlobs, rel) {
				matched = append(matched, fileEntry{
					RelPath: rel,
					Source:  types.SourceInclude,
				})
			}
			return nil
		}

		// LEGACY DENYLIST MODE.
		if isIgnored(rel, ignores) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		matched = append(matched, fileEntry{
			RelPath: rel,
			Source:  types.SourceDefault,
		})
		return nil
	})
	return matched, err
}

// resolveAssets expands each declared asset (which may be a literal path
// or a glob) into concrete file entries. Required assets that match nothing
// produce a clear error — if you said "this model file MUST exist", we
// should tell you when it doesn't.
func resolveAssets(rootDir string, assets []types.MoltAssetFile) ([]fileEntry, error) {
	var out []fileEntry
	for _, a := range assets {
		// Try as literal path first (fast common case).
		abs := filepath.Join(rootDir, filepath.FromSlash(a.Path))
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			out = append(out, fileEntry{
				RelPath:     filepath.ToSlash(a.Path),
				Source:      types.SourceAsset,
				Description: a.Description,
			})
			continue
		}

		// Glob expansion.
		matches, err := doublestar.Glob(os.DirFS(rootDir), a.Path)
		if err != nil {
			return nil, fmt.Errorf("asset glob %q: %w", a.Path, err)
		}
		// Filter to regular files only — glob can return directories.
		var files []string
		for _, m := range matches {
			info, err := os.Stat(filepath.Join(rootDir, filepath.FromSlash(m)))
			if err == nil && !info.IsDir() {
				files = append(files, m)
			}
		}
		if len(files) == 0 {
			if a.Required {
				return nil, fmt.Errorf(
					"required asset %q matched no files — aborting", a.Path,
				)
			}
			continue
		}
		for _, m := range files {
			out = append(out, fileEntry{
				RelPath:     filepath.ToSlash(m),
				Source:      types.SourceAsset,
				Description: a.Description,
			})
		}
	}
	return out, nil
}

// matchesAny returns true if path matches any pattern in patterns. Uses
// doublestar so "**/*.py" works; otherwise falls back to a literal segment
// match for backward compatibility with the original isIgnored shape.
func matchesAny(patterns []string, path string) bool {
	for _, p := range patterns {
		// Directory-style pattern: "tests/" means "the tests directory
		// and everything under it". Strip trailing slash and treat as prefix.
		if strings.HasSuffix(p, "/") {
			dir := strings.TrimSuffix(p, "/")
			if path == dir || strings.HasPrefix(path, dir+"/") {
				return true
			}
			// Match that dir at any depth.
			for _, seg := range strings.Split(path, "/") {
				if seg == dir {
					return true
				}
			}
			continue
		}
		ok, _ := doublestar.Match(p, path)
		if ok {
			return true
		}
		// Also match against basename for simple globs like "*.pyc".
		if ok, _ := doublestar.Match(p, filepath.Base(path)); ok {
			return true
		}
	}
	return false
}

// isIgnored preserves the original legacy ignore-list semantics.
func isIgnored(path string, patterns []string) bool {
	return matchesAny(patterns, path)
}

// isSensitive checks the hard-coded sensitive-pattern list.
func isSensitive(path string) bool {
	base := filepath.Base(path)
	for _, p := range SensitivePatterns {
		if strings.HasSuffix(p, "/") {
			continue
		}
		if matched, _ := filepath.Match(p, base); matched {
			return true
		}
		lower := strings.ToLower(base)
		if strings.Contains(lower, strings.TrimPrefix(strings.ToLower(p), "*")) {
			return true
		}
	}
	return false
}

func readIgnoreFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// writeTarGz serialises the matched files into a deterministic tar.gz and
// returns a Result describing what was packaged (for integrity manifest).
func writeTarGz(cfg Config, files []fileEntry) (*Result, error) {
	// Sort for deterministic output (same input → same bytes → same hash).
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})

	f, err := os.Create(cfg.OutputFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	fixedTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	result := &Result{}
	maxFile := int64(cfg.MaxFileSizeMB) * 1024 * 1024
	maxTotal := int64(cfg.MaxTotalSizeMB) * 1024 * 1024

	for _, entry := range files {
		abs := filepath.Join(cfg.RootDir, filepath.FromSlash(entry.RelPath))
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", entry.RelPath, err)
		}

		// We only package regular files. If WalkDir handed us a directory
		// via the legacy path (shouldn't happen, but defensively), skip it.
		if !info.Mode().IsRegular() {
			// Still emit the header so symlinks etc. are preserved — but
			// don't add them to the integrity manifest (no content hash).
			if err := writeNonRegular(tw, info, entry.RelPath, cfg.PrefixInTar, fixedTime); err != nil {
				return nil, err
			}
			continue
		}

		// Size checks.
		if maxFile > 0 && info.Size() > maxFile {
			result.SizeWarnings = append(result.SizeWarnings, fmt.Sprintf(
				"%s: %.1f MB exceeds max_file_size_mb=%d",
				entry.RelPath, float64(info.Size())/(1024*1024), cfg.MaxFileSizeMB,
			))
		}
		if maxTotal > 0 && result.TotalBytes+info.Size() > maxTotal {
			return nil, fmt.Errorf(
				"payload exceeds max_total_size_mb=%d (at %s, running total %.1f MB)",
				cfg.MaxTotalSizeMB, entry.RelPath,
				float64(result.TotalBytes+info.Size())/(1024*1024),
			)
		}

		// Stream the file through sha256 WHILE writing it into the tar —
		// single pass, no double-read.
		archiveName := cfg.PrefixInTar + entry.RelPath
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, err
		}
		hdr.Name = archiveName
		hdr.ModTime = fixedTime
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = ""
		hdr.Gname = ""
		hdr.Mode = 0o644
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}

		hashHex, err := streamAndHash(abs, tw)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", entry.RelPath, err)
		}

		result.Files = append(result.Files, types.PackagedFile{
			Path:        entry.RelPath,
			Size:        info.Size(),
			SHA256:      hashHex,
			Source:      entry.Source,
			Description: entry.Description,
		})
		result.TotalBytes += info.Size()
	}

	return result, nil
}

// streamAndHash reads src once, writes it to w, and returns the sha256 of
// what was read. Hashing during copy avoids a second pass over the file.
func streamAndHash(srcPath string, w io.Writer) (string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	mw := io.MultiWriter(w, h)
	if _, err := io.Copy(mw, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeNonRegular writes a tar header for directories/symlinks without
// content. Returns nil if nothing needs to be written.
func writeNonRegular(tw *tar.Writer, info os.FileInfo, rel, prefix string, t time.Time) error {
	if info.IsDir() {
		// We don't write directory entries explicitly — they're implied by
		// file paths. Skip.
		return nil
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = prefix + rel
	hdr.ModTime = t
	hdr.Uid, hdr.Gid = 0, 0
	hdr.Uname, hdr.Gname = "", ""
	return tw.WriteHeader(hdr)
}
