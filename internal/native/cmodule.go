// Package native — manifest-free C extension build pipeline.
//
// [[tool.molt.c.modules]] entries in pyproject.toml declare C source files
// and (optionally) header files. Headers are parsed by ParseCHeader to
// extract exported function signatures; those become a synthetic Manifest
// which is fed through the existing kernel codegen + zig cc pipeline to
// produce a .so + .pyi exactly like a manifest-driven kernel module.
//
// No .molt.toml is required: the header IS the manifest.
package native

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"molt/internal/progress"
	"molt/internal/pyabi"
)

// CConfig holds all [[tool.molt.c.modules]] entries from pyproject.toml.
type CConfig struct {
	Modules []CModuleConfig
}

// CModuleConfig is one [[tool.molt.c.modules]] entry.
type CModuleConfig struct {
	Name    string   // Python module name (required)
	Src     []string // .c source files (required)
	Headers []string // .h files to parse; defaults to <name>.h if it exists
	Flags   []string // extra -D/-I/-l flags passed to zig cc
}

// LoadCConfig reads [[tool.molt.c.modules]] from pyproject.toml in projectDir.
// Returns an empty CConfig (no error) if the section is absent or the file
// does not exist.
func LoadCConfig(projectDir string) CConfig {
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return CConfig{}
	}

	const header = "[[tool.molt.c.modules]]"
	var out []CModuleConfig
	var cur *CModuleConfig

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Strip inline comments.
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}

		if line == header {
			if cur != nil && cur.Name != "" {
				out = append(out, *cur)
			}
			cur = &CModuleConfig{}
			continue
		}

		// Any other section header ends the current entry.
		if strings.HasPrefix(line, "[") {
			if cur != nil && cur.Name != "" {
				out = append(out, *cur)
				cur = nil
			}
			continue
		}

		if cur == nil || line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch key {
		case "name":
			cur.Name = strings.Trim(val, `"'`)
		case "src":
			// Accept both a single string and a string array.
			if strings.HasPrefix(val, "[") {
				if arr := parseTOMLStringArray(val); arr != nil {
					cur.Src = arr
				}
			} else {
				if s := strings.Trim(val, `"'`); s != "" {
					cur.Src = []string{s}
				}
			}
		case "headers":
			if arr := parseTOMLStringArray(val); arr != nil {
				cur.Headers = arr
			}
		case "flags":
			if arr := parseTOMLStringArray(val); arr != nil {
				cur.Flags = arr
			}
		}
	}
	if cur != nil && cur.Name != "" {
		out = append(out, *cur)
	}
	return CConfig{Modules: out}
}

// BuildCModules compiles all [[tool.molt.c.modules]] entries.
// Uses the existing kernel build infrastructure (emitGlueC, zig cc).
// Returns one Artifact per successfully built module.
func BuildCModules(
	projectDir, pyExe, includeDir, extSuffix string,
	abi pyabi.Info,
	zigCfg ZigConfig,
	cfg CConfig,
	verbose bool,
) ([]Artifact, error) {
	var allArts []Artifact
	for _, mod := range cfg.Modules {
		art, err := buildOneCModule(projectDir, pyExe, includeDir, extSuffix, abi, zigCfg, mod, verbose)
		if err != nil {
			// Non-fatal: warn and continue with next module.
			fmt.Fprintf(stderrSink, "warn: C module %q: %v\n", mod.Name, err)
			continue
		}
		allArts = append(allArts, art)
	}
	return allArts, nil
}

// buildOneCModule compiles a single CModuleConfig entry.
func buildOneCModule(
	projectDir, pyExe, includeDir, extSuffix string,
	abi pyabi.Info,
	zigCfg ZigConfig,
	mod CModuleConfig,
	verbose bool,
) (Artifact, error) {
	if mod.Name == "" {
		return Artifact{}, fmt.Errorf("module name is required")
	}
	if len(mod.Src) == 0 {
		return Artifact{}, fmt.Errorf("no source files specified for module %q", mod.Name)
	}

	// Resolve absolute paths for src files.
	absSrcs := make([]string, 0, len(mod.Src))
	for _, s := range mod.Src {
		if filepath.IsAbs(s) {
			absSrcs = append(absSrcs, s)
		} else {
			absSrcs = append(absSrcs, filepath.Join(projectDir, s))
		}
	}

	// Find header files.
	absHeaders := findCHeaders(projectDir, mod, absSrcs)

	// Parse headers to get function signatures.
	var fns []ManifestFn
	for _, h := range absHeaders {
		parsed, err := ParseCHeader(h)
		if err != nil {
			fmt.Fprintf(stderrSink, "warn: C module %q: parse header %s: %v\n", mod.Name, h, err)
			continue
		}
		fns = append(fns, parsed...)
	}
	if len(fns) == 0 {
		return Artifact{}, fmt.Errorf("no supported function declarations found in headers for module %q (headers: %v)", mod.Name, absHeaders)
	}

	// Synthesise a Manifest.
	manifest := &Manifest{
		Module:    mod.Name,
		Functions: fns,
	}

	abiTag := abi.AbiTag
	plat := platTag(abi)
	soName := mod.Name + extSuffix

	// Compute cache hash.
	hash := hashCModule(absSrcs, absHeaders, mod.Name, abiTag, plat, mod.Flags, zigCfg.Version)

	// Check cache.
	if hit, cachedPath, err := hasCacheEntry(hash, soName); err != nil {
		return Artifact{}, err
	} else if hit {
		if verbose {
			progressLine("  ✓ c      %s  (cached)\n", mod.Name)
		}
		cdir := filepath.Dir(cachedPath)
		pyiPath := filepath.Join(cdir, mod.Name+".pyi")
		var siblings []string
		if fileExists(pyiPath) {
			siblings = []string{pyiPath}
		}
		src := Source{
			Lang:     "c",
			Path:     absSrcs[0],
			Module:   mod.Name,
			Basename: mod.Name,
		}
		return Artifact{
			Source: src, Hash: hash, Path: cachedPath, SoName: soName,
			AbiTag: abiTag, Plat: plat, Siblings: siblings,
		}, nil
	}

	if verbose {
		progressLine("  ↻ c      %s\n", mod.Name)
	}

	art, err := buildCModuleFromScratch(projectDir, includeDir, extSuffix, zigCfg, mod, manifest, absSrcs, hash, soName, abiTag, plat)
	if err != nil {
		return Artifact{}, err
	}
	return art, nil
}

// FindCModuleHeaders is the exported form of findCHeaders for use by cmdCList.
func FindCModuleHeaders(projectDir string, mod CModuleConfig, absSrcs []string) []string {
	return findCHeaders(projectDir, mod, absSrcs)
}

// HashCModuleKey is the exported form of hashCModule for use by cmdCList.
func HashCModuleKey(absSrcs, absHeaders []string, name, abiTag, plat string, flags []string, zigVersion string) string {
	return hashCModule(absSrcs, absHeaders, name, abiTag, plat, flags, zigVersion)
}

// HasCacheEntry is the exported form of hasCacheEntry for use by cmdCList.
func HasCacheEntry(hash, soName string) (bool, string, error) {
	return hasCacheEntry(hash, soName)
}

// findCHeaders returns the set of header files to parse for a module.
// If mod.Headers is non-empty, those are used (resolved relative to projectDir).
// Otherwise, we look for <name>.h in the same directories as the source files.
func findCHeaders(projectDir string, mod CModuleConfig, absSrcs []string) []string {
	if len(mod.Headers) > 0 {
		out := make([]string, 0, len(mod.Headers))
		for _, h := range mod.Headers {
			if filepath.IsAbs(h) {
				out = append(out, h)
			} else {
				out = append(out, filepath.Join(projectDir, h))
			}
		}
		return out
	}

	// Default: look for <name>.h in each source file's directory.
	seen := map[string]bool{}
	var out []string
	for _, src := range absSrcs {
		dir := filepath.Dir(src)
		candidate := filepath.Join(dir, mod.Name+".h")
		if !seen[candidate] {
			if _, err := os.Stat(candidate); err == nil {
				out = append(out, candidate)
				seen[candidate] = true
			}
		}
	}
	return out
}

// hashCModule computes a 16-char hex cache key mixing source contents,
// header contents, module name, ABI, platform, flags, and zig version.
func hashCModule(absSrcs, absHeaders []string, name, abiTag, plat string, flags []string, zigVersion string) string {
	h := sha256.New()
	for _, s := range absSrcs {
		h.Write([]byte(s))
		h.Write([]byte{0})
		if data, err := os.ReadFile(s); err == nil {
			h.Write(data)
		}
		h.Write([]byte{0})
	}
	for _, hdr := range absHeaders {
		h.Write([]byte(hdr))
		h.Write([]byte{0})
		if data, err := os.ReadFile(hdr); err == nil {
			h.Write(data)
		}
		h.Write([]byte{0})
	}
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(abiTag))
	h.Write([]byte{0})
	h.Write([]byte(plat))
	h.Write([]byte{0})
	for _, f := range flags {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	h.Write([]byte(zigVersion))
	h.Write([]byte{0})
	h.Write([]byte("cmodule:v1"))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildCModuleFromScratch generates glue.c, compiles all source files, and
// links them into a shared library cached under ~/.molt/native/<hash>/.
func buildCModuleFromScratch(
	projectDir, includeDir, extSuffix string,
	zigCfg ZigConfig,
	mod CModuleConfig,
	manifest *Manifest,
	absSrcs []string,
	hash, soName, abiTag, plat string,
) (Artifact, error) {
	// Create a build temp dir under the cache root.
	root, err := CacheRoot()
	if err != nil {
		return Artifact{}, err
	}
	buildDir := filepath.Join(root, "cmodule-build", mod.Name+"-"+hash)
	_ = os.RemoveAll(buildDir)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return Artifact{}, err
	}

	// Generate glue.c.
	gluePath := filepath.Join(buildDir, "glue.c")
	if err := os.WriteFile(gluePath, []byte(emitGlueC(manifest)), 0o644); err != nil {
		return Artifact{}, fmt.Errorf("write glue.c: %w", err)
	}

	// Resolve zig binary.
	zigBin, err := EnsureZig(zigCfg.Version)
	if err != nil {
		return Artifact{}, fmt.Errorf("locate zig: %w", err)
	}

	soStaging := filepath.Join(buildDir, soName)

	// Build command: zig cc -O2 -shared -fPIC -I{includeDir} -o {out} {srcs...} glue.c {flags...}
	args := []string{"cc", "-O2", "-shared", "-fPIC",
		"-I", includeDir,
		"-o", soStaging,
	}
	args = append(args, absSrcs...)
	args = append(args, gluePath)
	args = append(args, mod.Flags...)
	if runtime.GOOS == "darwin" {
		args = append(args, "-undefined", "dynamic_lookup")
	}

	cmd := exec.Command(zigBin, args...)
	if out, err := progress.Stream(cmd, fmt.Sprintf("    [%s] ", mod.Name), streamingEnabled()); err != nil {
		return Artifact{}, fmt.Errorf("compile C module %q:\n%s", mod.Name, string(out))
	}

	// Emit .pyi stub alongside.
	pyiContent := emitPyiStub(manifest, strings.Join(absSrcs, ", "))
	pyiStaging := filepath.Join(buildDir, mod.Name+".pyi")
	if err := os.WriteFile(pyiStaging, []byte(pyiContent), 0o644); err != nil {
		return Artifact{}, fmt.Errorf("write .pyi stub: %w", err)
	}

	// Move to cache.
	cdir, err := cacheDir(hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return Artifact{}, err
	}
	cachedSo := filepath.Join(cdir, soName)
	cachedPyi := filepath.Join(cdir, mod.Name+".pyi")
	if err := copyFile(soStaging, cachedSo); err != nil {
		return Artifact{}, fmt.Errorf("cache .so: %w", err)
	}
	if err := copyFile(pyiStaging, cachedPyi); err != nil {
		return Artifact{}, fmt.Errorf("cache .pyi: %w", err)
	}
	if err := writeCacheMeta(hash, CacheEntry{
		Lang: "c", SoName: soName, Source: absSrcs[0],
		AbiTag: abiTag, Platform: plat, CompiledAt: time.Now().UTC(),
	}); err != nil {
		return Artifact{}, err
	}

	src := Source{
		Lang:     "c",
		Path:     absSrcs[0],
		Module:   mod.Name,
		Basename: mod.Name,
	}
	return Artifact{
		Source: src, Hash: hash, Path: cachedSo, SoName: soName,
		AbiTag: abiTag, Plat: plat, Siblings: []string{cachedPyi},
	}, nil
}

