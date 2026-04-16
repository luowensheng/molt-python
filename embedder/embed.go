package embedder

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config controls the embedding process
type Config struct {
	RootDir         string // project root to scan
	OutputFile      string // destination binary/tar.gz
	PrefixInTar     string // e.g., "src/" (must match launcher expectations)
	IgnoreFile      string // path to .moltignore (optional)
	FailOnSensitive bool   // strict mode: abort if secrets found
}

// DefaultIgnoreList covers 95% of bloat/dev files
var DefaultIgnoreList = []string{
	// Version control & CI
	".git/", ".github/", ".gitlab/", ".svn/", ".hg/",
	".circleci/", ".travis.yml", ".gitlab-ci.yml",
	// IDE & Editors
	".vscode/", ".idea/", "*.swp", "*.swo", "*~", "*.bak",
	// Python caches & artifacts
	"__pycache__/", "*.pyc", "*.pyo", "*.egg-info/",
	".pytest_cache/", ".mypy_cache/", ".ruff_cache/", ".tox/", ".nox/",
	// Virtual envs & package managers
	".venv/", "venv/", "env/", ".env",
	"node_modules/", "vendor/", "go.mod", "go.sum",
	// Build & dist
	"build/", "dist/", "target/", "*.egg", "*.whl",
	// OS junk
	".DS_Store", "Thumbs.db", "desktop.ini",
	// Docs, tests & dev tooling
	"docs/", "test/", "tests/", "conftest.py",
	"pytest.ini", "tox.ini", "Makefile", "Dockerfile",
	"docker-compose*.yml", "README*", "LICENSE*", "CHANGELOG*",
}

// SensitivePatterns will FAIL the build by default
var SensitivePatterns = []string{
	"*.pem", "*.key", "*.p12", "*.pfx", "*.crt",
	"*password*", "*secret*", "*credential*", "*token*",
	"aws_credentials", "id_rsa", "id_ed25519", "id_ecdsa",
	".env.local", ".env.*.local", "credentials.json",
}

// Run executes the strict embedding process
func Run(cfg Config) error {
	ignores := append([]string(nil), DefaultIgnoreList...)
	if cfg.IgnoreFile != "" {
		if extra, err := readIgnoreFile(cfg.IgnoreFile); err == nil {
			ignores = append(ignores, extra...)
		}
	}

	files, err := collectFiles(cfg.RootDir, ignores, cfg.FailOnSensitive)
	if err != nil {
		return fmt.Errorf("collect files: %w", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("no files matched inclusion criteria (check .moltignore)")
	}

	return writeTarGz(files, cfg.RootDir, cfg.PrefixInTar, cfg.OutputFile)
}

func collectFiles(root string, ignores []string, strict bool) ([]string, error) {
	var matched []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		if isIgnored(rel, ignores) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if strict && isSensitive(rel) {
			return fmt.Errorf("SECURITY BLOCK: sensitive file matched: %s", rel)
		}

		matched = append(matched, rel)
		return nil
	})
	return matched, err
}

func isIgnored(path string, patterns []string) bool {
	// Directory patterns (e.g., ".git/", "__pycache__/")
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") {
			dirName := p[:len(p)-1]
			if path == dirName || strings.HasPrefix(path, dirName+"/") {
				return true
			}
			// Match at any depth
			parts := strings.Split(path, "/")
			for _, seg := range parts {
				if seg == dirName {
					return true
				}
			}
		}
	}
	// File glob patterns (e.g., "*.pyc", ".env")
	for _, p := range patterns {
		if !strings.HasSuffix(p, "/") {
			if matched, _ := filepath.Match(p, filepath.Base(path)); matched {
				return true
			}
		}
	}
	return false
}

func isSensitive(path string) bool {
	base := filepath.Base(path)
	for _, p := range SensitivePatterns {
		if strings.HasSuffix(p, "/") {
			continue // skip dirs in sensitive check
		}
		if matched, _ := filepath.Match(p, base); matched {
			return true
		}
		// Substring match for secret-like names
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

func writeTarGz(files []string, root, prefix, outPath string) error {
	sort.Strings(files) // Deterministic build

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Fixed timestamp for reproducible builds
	fixedTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, rel := range files {
		abs := filepath.Join(root, rel)
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}

		archiveName := prefix + filepath.ToSlash(rel)
		if info.IsDir() {
			archiveName += "/"
		}

		hdr, err := tar.FileInfoHeader(info, info.Mode().String())
		if err != nil {
			return err
		}
		hdr.Name = archiveName
		hdr.ModTime = fixedTime
		hdr.Uid = 0
		hdr.Gid = 0
		hdr.Uname = ""
		hdr.Gname = ""

		// Normalize permissions
		if info.IsDir() {
			hdr.Mode = 0o755
		} else {
			hdr.Mode = 0o644
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(abs)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, f)
			f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
	return nil
}
