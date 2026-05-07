package native

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"molt/internal/nativepreset"
	"molt/internal/projstate"
)

// ExternalModule is a fully-resolved [[tool.molt.native]] entry.
type ExternalModule struct {
	Module  string
	SrcDir  string   // absolute path to the source folder
	Src     []string // glob patterns (relative to SrcDir) for change detection
	Build   string   // fully-resolved shell command
	Output  string   // fully-resolved absolute output path
	IsRust  bool     // Rust cargo project → use BuildRustProject
}

// extHashEntry records the last-known state of one external module.
type extHashEntry struct {
	SrcHash string `json:"src_hash"`
	Build   string `json:"build"`
}

func extHashesPath(projectDir string) string {
	return filepath.Join(projstate.Dir(projectDir), "ext-hashes.json")
}

func loadExtHashes(projectDir string) (map[string]extHashEntry, error) {
	out := map[string]extHashEntry{}
	data, err := os.ReadFile(extHashesPath(projectDir))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(data, &out)
}

func saveExtHashes(projectDir string, hashes map[string]extHashEntry) error {
	data, err := json.MarshalIndent(hashes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(extHashesPath(projectDir), data, 0o644)
}

// PlatformExt returns the shared library file extension for the current OS.
// Used to substitute the {ext} token in build/output templates.
func PlatformExt() string {
	switch runtime.GOOS {
	case "windows":
		return "pyd"
	case "darwin":
		return "dylib"
	default:
		return "so"
	}
}

// resolveTokens substitutes template tokens in s.
func resolveTokens(s, module, outputPath, pythonInclude string, srcFiles []string) string {
	r := strings.NewReplacer(
		"{module}", module,
		"{ext}", PlatformExt(),
		"{output}", outputPath,
		"{python_include}", pythonInclude,
		"{src_files}", strings.Join(srcFiles, " "),
	)
	return r.Replace(s)
}

// collectSrcFiles walks srcDir returning source file paths relevant for building.
func collectSrcFiles(srcDir string) []string {
	var files []string
	_ = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == "target" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return err
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".c", ".cpp", ".cc", ".cxx", ".rs":
			files = append(files, p)
		}
		return nil
	})
	return files
}

// hashExternalSrc hashes all files matching patterns (or all source files if
// no patterns match) plus the build command string.
func hashExternalSrc(srcDir string, patterns []string, build string) (string, error) {
	h := sha256.New()

	var candidates []string
	for _, pat := range patterns {
		// Try as a glob relative to srcDir.
		matches, err := filepath.Glob(filepath.Join(srcDir, pat))
		if err == nil {
			candidates = append(candidates, matches...)
		}
	}
	if len(candidates) == 0 {
		// Fall back to walking the whole srcDir.
		_ = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && (d.Name() == "target" || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return err
			}
			candidates = append(candidates, p)
			return nil
		})
	}
	sort.Strings(candidates)

	for _, f := range candidates {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(srcDir, f)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	h.Write([]byte(build))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// LoadExternalModules reads [[tool.molt.native]] from pyproject.toml, merges
// global presets, and resolves all tokens. pythonInclude is passed for the
// {python_include} token used by C/C++ presets.
func LoadExternalModules(projectDir, pythonInclude string) ([]ExternalModule, error) {
	cfgs := loadNativeModuleConfigs(projectDir)
	if len(cfgs) == 0 {
		return nil, nil
	}

	var out []ExternalModule
	for _, cfg := range cfgs {
		if cfg.Module == "" || cfg.Src == "" {
			continue
		}
		srcDir := filepath.Join(projectDir, filepath.FromSlash(cfg.Src))

		buildCmd := cfg.Build
		outputTmpl := cfg.Output
		srcPatterns := cfg.SrcPatterns
		isRust := false

		// Merge preset if specified.
		if cfg.Preset != "" {
			p, err := nativepreset.Find(cfg.Preset)
			if err != nil {
				return nil, fmt.Errorf("[[tool.molt.native]] module=%s: %w", cfg.Module, err)
			}
			if buildCmd == "" {
				buildCmd = p.Build
			}
			if outputTmpl == "" {
				outputTmpl = p.Output
			}
			if len(srcPatterns) == 0 {
				srcPatterns = p.SrcPatterns
			}
			if p.Name == "rust" {
				isRust = true
			}
		}

		// Auto-detect Rust project by Cargo.toml presence.
		if _, err := os.Stat(filepath.Join(srcDir, "Cargo.toml")); err == nil {
			isRust = true
		}

		// Resolve output path (needs module + ext before build).
		srcFiles := collectSrcFiles(srcDir)
		outputResolved := filepath.Join(srcDir,
			resolveTokens(outputTmpl, cfg.Module, "", pythonInclude, srcFiles))

		// Resolve build command with the now-known output path.
		buildResolved := resolveTokens(buildCmd, cfg.Module, outputResolved, pythonInclude, srcFiles)

		out = append(out, ExternalModule{
			Module:  cfg.Module,
			SrcDir:  srcDir,
			Src:     srcPatterns,
			Build:   buildResolved,
			Output:  outputResolved,
			IsRust:  isRust,
		})
	}
	return out, nil
}

// CheckAndRebuild checks each external module for source changes and rebuilds
// only what changed. Returns all artifacts (including cache hits) and whether
// any module was rebuilt.
func CheckAndRebuild(modules []ExternalModule, projectDir, abiTag, plat, extSuffix string, verbose bool) ([]Artifact, bool, error) {
	if len(modules) == 0 {
		return nil, false, nil
	}

	hashes, err := loadExtHashes(projectDir)
	if err != nil {
		return nil, false, err
	}

	var arts []Artifact
	anyRebuilt := false

	for _, m := range modules {
		// Rust projects use their own internal cache keyed by source hash.
		if m.IsRust {
			art, err := BuildRustProject(m.Module, m.SrcDir, abiTag, plat, extSuffix, verbose)
			if err != nil {
				return nil, false, fmt.Errorf("rust project %s: %w", m.Module, err)
			}
			art.Source.Lang = "external"
			arts = append(arts, art)
			continue
		}

		srcHash, err := hashExternalSrc(m.SrcDir, m.Src, m.Build)
		if err != nil {
			return nil, false, fmt.Errorf("hash %s: %w", m.Module, err)
		}

		soName := m.Module + extSuffix
		cached := hashes[m.Module]

		// Cache hit: same hash + build command + .so still in cache.
		if cached.SrcHash == srcHash && cached.Build == m.Build {
			if hit, cachedPath, herr := hasCacheEntry(srcHash, soName); herr == nil && hit {
				if verbose {
					fmt.Printf("  ✓ native  %s  (cached)\n", m.Module)
				}
				src := Source{Lang: "external", Module: m.Module, Basename: m.Module, Path: m.SrcDir}
				arts = append(arts, Artifact{
					Source: src, Hash: srcHash, Path: cachedPath, SoName: soName,
					AbiTag: abiTag, Plat: plat,
				})
				continue
			}
		}

		if verbose {
			fmt.Printf("  ↻ native  %s\n", m.Module)
		}

		cmd := exec.Command("sh", "-c", m.Build)
		cmd.Dir = m.SrcDir
		if cmdOut, err := cmd.CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("build %s:\n%s", m.Module, string(cmdOut))
		}

		if _, err := os.Stat(m.Output); err != nil {
			return nil, false, fmt.Errorf("build %s: output not found at %s", m.Module, m.Output)
		}

		cdir, err := cacheDir(srcHash)
		if err != nil {
			return nil, false, err
		}
		if err := os.MkdirAll(cdir, 0o755); err != nil {
			return nil, false, err
		}
		dest := filepath.Join(cdir, soName)
		if err := copyFile(m.Output, dest); err != nil {
			return nil, false, fmt.Errorf("cache %s: %w", m.Module, err)
		}

		hashes[m.Module] = extHashEntry{SrcHash: srcHash, Build: m.Build}
		anyRebuilt = true

		src := Source{Lang: "external", Module: m.Module, Basename: m.Module, Path: m.SrcDir}
		arts = append(arts, Artifact{
			Source: src, Hash: srcHash, Path: dest, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		})
	}

	if anyRebuilt {
		_ = saveExtHashes(projectDir, hashes)
	}

	return arts, anyRebuilt, nil
}

// AnyFileNewerThan reports whether any file under dir has a modification time
// after t. Returns false if dir doesn't exist or can't be read.
func AnyFileNewerThan(dir string, t time.Time) bool {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "target" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(t) {
			return fs.ErrExist // sentinel: stop walking
		}
		return nil
	})
	return err == fs.ErrExist
}
