// Package native — manifest-driven kernel modules.
//
// A "kernel module" is a Python extension built from:
//   - a TOML manifest (<basename>.molt.toml) describing the exported fns
//   - a sibling source file (.zig, .c, .cpp, ...) implementing them
//
// Discovery walks for manifests; the manifest's basename is matched to
// a sibling source file via KernelConfig.SourceExtensions. The build
// auto-generates a C glue layer (PyInit_<name> + PyMethodDef) from the
// manifest, compiles the source per its language, links them together,
// and emits a .pyi alongside for IDE/typechecker support.
//
// This makes molt language-agnostic at the discovery layer: adding
// support for a new C-ABI language just means adding a build adapter
// in BuildKernelModule's switch — no parser or per-language framework.

package native

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"molt/internal/kernelbuilder"
	"molt/internal/progress"
)

// kernelRecipeVersion is mixed into the cache hash; bump on any change
// to how the build pipeline drives the compilers / generators.
const kernelRecipeVersion = "v4"

// DiscoverKernels walks projectDir + cfg.Paths (and cfg.ManifestDir if
// set) for files ending in any of cfg.ManifestSuffixes. Each manifest
// becomes a Source{Lang: "kernel", Path: <manifest>, Module: <basename>}.
//
// The Source is what the rest of the molt pipeline already understands;
// BuildKernelModule consumes it and parses the manifest at compile time.
func DiscoverKernels(projectDir string, cfg KernelConfig) ([]Source, error) {
	seen := map[string]bool{}
	var out []Source

	walk := func(absRoot string) error {
		if _, err := os.Stat(absRoot); err != nil {
			return nil // missing root → silent skip
		}
		return filepath.Walk(absRoot, func(p string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				name := info.Name()
				if name == "__pycache__" || name == "target" ||
					name == "build" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !hasAnySuffix(p, cfg.ManifestSuffixes) {
				return nil
			}
			absP, err := filepath.Abs(p)
			if err != nil {
				return err
			}
			if seen[absP] {
				return nil
			}
			seen[absP] = true

			// Module name is the basename of the manifest with the
			// suffix stripped. PackagePath is its directory relative
			// to the search root, dotted-form.
			rel, _ := filepath.Rel(absRoot, absP)
			dir, _ := filepath.Split(rel)
			pkgPath := strings.TrimSuffix(filepath.ToSlash(dir), "/")
			base := manifestBasename(absP)
			mod := base
			if pkgPath != "" {
				mod = strings.ReplaceAll(pkgPath, "/", ".") + "." + base
			}
			out = append(out, Source{
				Lang:        "kernel",
				Path:        absP,
				Module:      mod,
				PackagePath: pkgPath,
				Basename:    base,
			})
			return nil
		})
	}

	// Walk most-specific paths first. With paths = [".", "src"] and a
	// manifest at src/crypto.molt.toml, we want the module to be
	// "crypto" (relative to "src"), not "src.crypto" (relative to ".").
	// Sorting by descending path length puts deeper roots first; the
	// dedup map then prevents the shallow root from re-claiming it.
	ordered := append([]string(nil), cfg.Paths...)
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if len(ordered[j]) > len(ordered[i]) {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	for _, rel := range ordered {
		absRoot, err := filepath.Abs(filepath.Join(projectDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if err := walk(absRoot); err != nil {
			return nil, err
		}
	}
	if cfg.ManifestDir != "" {
		absRoot, err := filepath.Abs(filepath.Join(projectDir, filepath.FromSlash(cfg.ManifestDir)))
		if err != nil {
			return nil, err
		}
		if err := walk(absRoot); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func hasAnySuffix(name string, suffixes []string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// hashKernel produces the cache key for a kernel module. Mixes:
//   - manifest content
//   - source content (if a source file exists)
//   - ABI tag, platform
//   - effective build target (host platform when no cross-compile target set)
//   - python include dir (encodes Python version via headers)
//   - resolved build command (so editing global YAML or project override
//     forces a rebuild)
//   - recipe version
func hashKernel(manifestData, sourceData []byte, abiTag, plat, effectiveTarget, pyInclude, buildCmd string) string {
	h := sha256.New()
	h.Write(manifestData)
	h.Write([]byte{0})
	h.Write(sourceData)
	h.Write([]byte{0})
	h.Write([]byte(abiTag))
	h.Write([]byte{0})
	h.Write([]byte(plat))
	h.Write([]byte{0})
	h.Write([]byte(effectiveTarget))
	h.Write([]byte{0})
	h.Write([]byte(pyInclude))
	h.Write([]byte{0})
	h.Write([]byte(buildCmd))
	h.Write([]byte{0})
	h.Write([]byte(kernelRecipeVersion))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// BuildKernelModule compiles a manifest + source file pair into an
// importable Python extension. Generates the C glue from the manifest,
// invokes the per-language source compiler, links them together, and
// writes a .pyi stub alongside.
//
// The Source.Path here is the manifest path, not the source file —
// DiscoverKernels writes it that way so the rest of molt's pipeline
// (caching, staging) treats the manifest as the unit of work.
func BuildKernelModule(s Source, pyExe, abiTag, plat, extSuffix string, verbose bool, zigCfg ZigConfig, kernCfg KernelConfig, pyInclude string) (Artifact, error) {
	manifestData, err := os.ReadFile(s.Path)
	if err != nil {
		return Artifact{}, fmt.Errorf("read manifest %s: %w", s.Path, err)
	}
	manifest, err := ParseManifest(s.Path)
	if err != nil {
		return Artifact{}, err
	}
	if manifest.Module == "" {
		manifest.Module = s.Basename
	}

	// Resolve the sibling source-or-library file. Returns lang="prebuilt"
	// when the match is a .so/.dylib/.dll (or manifest.Library is set).
	exts := kernCfg.SourceExtensions
	srcPath, lang, ok := ResolveSource(s.Path, manifest, exts)
	if !ok {
		return Artifact{}, fmt.Errorf(
			"%s: no source or library file found (tried %s and .so/.dylib/.dll)",
			s.Path, strings.Join(exts, " "))
	}
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return Artifact{}, fmt.Errorf("read %s: %w", srcPath, err)
	}

	soName := s.Basename + extSuffix

	if lang == "prebuilt" {
		return buildPrebuiltKernel(s, manifestData, srcPath, srcData,
			manifest, soName, abiTag, plat, pyInclude, verbose)
	}

	// Resolve the build-command template *before* hashing so the cache
	// key depends on which builder will run (project override / global
	// YAML / built-in). The template still has tokens like {source} —
	// that's fine: the template, not the resolved command, is what
	// matters for "did the recipe change?". Token substitution happens
	// later inside compileKernelSource.
	buildTemplate, err := lookupBuilder(lang, kernCfg)
	if err != nil {
		return Artifact{}, err
	}

	// effectiveTarget is the os_arch string used both as the {target_flags}
	// lookup key and as a cache-key component. When no cross-compile target
	// is set, fall back to the host platform so host rebuilds are still
	// separated from cross-compile artifacts in the cache.
	effectiveTarget := kernCfg.Target
	if effectiveTarget == "" {
		effectiveTarget = runtime.GOOS + "_" + runtime.GOARCH
	}

	hash := hashKernel(manifestData, srcData, abiTag, plat, effectiveTarget, pyInclude, buildTemplate)

	if hit, cachedPath, err := hasCacheEntry(hash, soName); err != nil {
		return Artifact{}, err
	} else if hit {
		if verbose {
			progressLine("  ✓ kernel  %s  (cached)\n", s.Module)
		}
		return cachedKernelArtifact(s, hash, cachedPath, soName, abiTag, plat), nil
	}

	if verbose {
		progressLine("  ↻ kernel  %s  [%s]\n", s.Module, lang)
	}

	// Build directory under the cache root.
	root, err := CacheRoot()
	if err != nil {
		return Artifact{}, err
	}
	buildDir := filepath.Join(root, "kernel-build", s.Module+"-"+hash)
	_ = os.RemoveAll(buildDir)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return Artifact{}, err
	}

	// Generate glue.c from the manifest.
	gluePath := filepath.Join(buildDir, "glue.c")
	if err := os.WriteFile(gluePath, []byte(emitGlueC(manifest)), 0o644); err != nil {
		return Artifact{}, err
	}

	// Compile the user's source to a position-independent object.
	objPath := filepath.Join(buildDir, s.Basename+".o")
	if _, err := compileKernelSource(lang, srcPath, objPath, zigCfg, kernCfg, pyInclude, kernCfg.Target); err != nil {
		return Artifact{}, err
	}

	// Link glue + object into a shared library.
	soStaging := filepath.Join(buildDir, soName)
	if err := linkKernelSO(gluePath, objPath, soStaging, pyInclude, zigCfg, kernCfg.Target, kernCfg.TargetFlags); err != nil {
		return Artifact{}, err
	}

	// Emit .pyi stub.
	pyiContent := emitPyiStub(manifest, srcPath)
	pyiStaging := filepath.Join(buildDir, s.Basename+".pyi")
	if err := os.WriteFile(pyiStaging, []byte(pyiContent), 0o644); err != nil {
		return Artifact{}, err
	}

	// Move into the cache.
	cdir, err := cacheDir(hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return Artifact{}, err
	}
	cachedSo := filepath.Join(cdir, soName)
	cachedPyi := filepath.Join(cdir, s.Basename+".pyi")
	if err := copyFile(soStaging, cachedSo); err != nil {
		return Artifact{}, fmt.Errorf("cache .so: %w", err)
	}
	if err := copyFile(pyiStaging, cachedPyi); err != nil {
		return Artifact{}, fmt.Errorf("cache .pyi: %w", err)
	}
	if err := writeCacheMeta(hash, CacheEntry{
		Lang: "kernel", SoName: soName, Source: s.Path,
		AbiTag: abiTag, Platform: plat, CompiledAt: time.Now().UTC(),
	}); err != nil {
		return Artifact{}, err
	}

	return Artifact{
		Source: s, Hash: hash, Path: cachedSo, SoName: soName,
		AbiTag: abiTag, Plat: plat, Siblings: []string{cachedPyi},
	}, nil
}

// buildPrebuiltKernel handles the dlopen-wrapper path: the user has a
// pre-built .so/.dylib/.dll and wants Python to import it via molt.
// We generate a tiny wrapper that dlopens the lib and forwards calls
// through function pointers, then compile and link the wrapper as a
// CPython extension.
//
// Cache hash mixes manifest content + library content (so vendor
// updates trigger rebuild) + library path + ABI/platform.
func buildPrebuiltKernel(
	s Source,
	manifestData []byte,
	libPath string,
	libData []byte,
	manifest *Manifest,
	soName, abiTag, plat, pyInclude string,
	verbose bool,
) (Artifact, error) {
	hash := hashKernel(manifestData, libData, abiTag, plat,
		runtime.GOOS+"_"+runtime.GOARCH, pyInclude, "prebuilt:"+libPath)

	if hit, cachedPath, err := hasCacheEntry(hash, soName); err != nil {
		return Artifact{}, err
	} else if hit {
		if verbose {
			progressLine("  ✓ kernel  %s  (cached, prebuilt)\n", s.Module)
		}
		return cachedKernelArtifact(s, hash, cachedPath, soName, abiTag, plat), nil
	}

	if verbose {
		progressLine("  ↻ kernel  %s  [prebuilt]\n", s.Module)
	}

	root, err := CacheRoot()
	if err != nil {
		return Artifact{}, err
	}
	buildDir := filepath.Join(root, "kernel-build", s.Module+"-"+hash)
	_ = os.RemoveAll(buildDir)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return Artifact{}, err
	}

	// Generate dlopen-wrapper glue.c.
	gluePath := filepath.Join(buildDir, "glue.c")
	if err := os.WriteFile(gluePath, []byte(emitGlueDlopenC(manifest, libPath)), 0o644); err != nil {
		return Artifact{}, err
	}

	// Link via zig cc — same as the source path, just no user object
	// to link in. The wrapper depends only on libdl + Python; the impl
	// is loaded at import time, not link time.
	soStaging := filepath.Join(buildDir, soName)
	if err := linkPrebuiltSO(gluePath, soStaging, pyInclude, "", nil); err != nil {
		return Artifact{}, err
	}

	// .pyi stub.
	pyiContent := emitPyiStub(manifest, libPath)
	pyiStaging := filepath.Join(buildDir, s.Basename+".pyi")
	if err := os.WriteFile(pyiStaging, []byte(pyiContent), 0o644); err != nil {
		return Artifact{}, err
	}

	cdir, err := cacheDir(hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return Artifact{}, err
	}
	cachedSo := filepath.Join(cdir, soName)
	cachedPyi := filepath.Join(cdir, s.Basename+".pyi")
	if err := copyFile(soStaging, cachedSo); err != nil {
		return Artifact{}, fmt.Errorf("cache .so: %w", err)
	}
	if err := copyFile(pyiStaging, cachedPyi); err != nil {
		return Artifact{}, fmt.Errorf("cache .pyi: %w", err)
	}
	if err := writeCacheMeta(hash, CacheEntry{
		Lang: "kernel-prebuilt", SoName: soName, Source: s.Path,
		AbiTag: abiTag, Platform: plat, CompiledAt: time.Now().UTC(),
	}); err != nil {
		return Artifact{}, err
	}

	return Artifact{
		Source: s, Hash: hash, Path: cachedSo, SoName: soName,
		AbiTag: abiTag, Plat: plat, Siblings: []string{cachedPyi},
	}, nil
}

// linkPrebuiltSO compiles glue.c into a shared library. No user object
// to link in — the impl is loaded at runtime via dlopen.
// target and targetFlags are optional cross-compile parameters: when target
// is non-empty, the zig linker flags for "zig"[target] are prepended.
func linkPrebuiltSO(gluePath, soOut, pyInclude, target string, targetFlags map[string]map[string]string) error {
	zigBin, err := EnsureZig("")
	if err != nil {
		return fmt.Errorf("locate zig (used for linking): %w", err)
	}
	args := []string{"cc",
		"-shared", "-fPIC", "-O2",
	}
	if flags := resolveTargetFlags("zig", target, targetFlags); flags != "" {
		args = append(args, strings.Fields(flags)...)
	}
	args = append(args,
		"-I", pyInclude,
		"-o", soOut,
		gluePath,
	)
	if runtime.GOOS == "darwin" {
		args = append(args, "-undefined", "dynamic_lookup")
	} else if runtime.GOOS == "linux" {
		args = append(args, "-ldl")
	}
	cmd := exec.Command(zigBin, args...)
	if out, err := progress.Stream(cmd, "    [link] ", streamingEnabled()); err != nil {
		return fmt.Errorf("link prebuilt-wrapper .so:\n%s", string(out))
	}
	return nil
}

func cachedKernelArtifact(s Source, hash, soPath, soName, abiTag, plat string) Artifact {
	cdir := filepath.Dir(soPath)
	pyiPath := filepath.Join(cdir, s.Basename+".pyi")
	var siblings []string
	if fileExists(pyiPath) {
		siblings = []string{pyiPath}
	}
	return Artifact{
		Source: s, Hash: hash, Path: soPath, SoName: soName,
		AbiTag: abiTag, Plat: plat, Siblings: siblings,
	}
}

// compileKernelSource compiles srcPath to a position-independent object
// file at objPath using the per-extension build command resolved from
// (highest priority first):
//
//   1. project's [tool.molt.native_kernel.build.<ext>]
//   2. ~/.molt/kernel-builders.yaml
//   3. built-in defaults (kernelbuilder.DefaultBuilders())
//
// `lang` is the extension without the leading dot ("zig", "c", "odin", …).
//
// Returns the resolved command string (after token substitution) so the
// caller can mix it into the cache hash — that way changing the project
// override or the global YAML invalidates the cache automatically.
func compileKernelSource(lang, srcPath, objPath string, zigCfg ZigConfig, kernCfg KernelConfig, pyInclude, target string) (string, error) {
	template, err := lookupBuilder(lang, kernCfg)
	if err != nil {
		return "", err
	}

	tokens, err := buildTokens(template, srcPath, objPath, zigCfg, pyInclude, lang, target, kernCfg.TargetFlags)
	if err != nil {
		return "", err
	}

	resolved := kernelbuilder.Resolve(template, tokens)

	// Catch unresolved {tokens} before we hand the command to exec —
	// otherwise the user gets a confusing "no such file" error pointing
	// at a literal "{zig}" path. This usually means a toolchain
	// dependency couldn't be resolved (e.g. zig auto-install failed).
	if leftover := unresolvedTokens(resolved); len(leftover) > 0 {
		return "", fmt.Errorf(
			"unresolved tokens in build command for .%s: %s\n"+
				"  template: %s\n"+
				"  resolved: %s\n"+
				"  (a required toolchain may have failed to install — check earlier output)",
			lang, strings.Join(leftover, ", "), template, resolved)
	}

	argv := kernelbuilder.SplitCommand(resolved)
	if len(argv) == 0 {
		return "", fmt.Errorf("empty build command for .%s", lang)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	prefix := streamPrefix(srcPath)
	if out, err := progress.Stream(cmd, prefix, streamingEnabled()); err != nil {
		return "", fmt.Errorf("%s build (.%s) failed:\n  %s\n%s",
			argv[0], lang, resolved, string(out))
	}
	return resolved, nil
}

// streamPrefix returns the per-line prefix for streamed build output.
// The basename (without extension) attributes lines from parallel
// builds: "  [crypto] " for /abs/.../crypto.zig.
func streamPrefix(srcPath string) string {
	base := filepath.Base(srcPath)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return fmt.Sprintf("    [%s] ", base)
}

// streamingEnabled gates real-time output of build subprocess stdout/
// stderr. Off by default; opt in via MOLT_STREAM_BUILDS=1 because for
// small/cached projects the silent CombinedOutput path is cleaner.
// Errors always include the captured output regardless of this flag.
func streamingEnabled() bool {
	v := os.Getenv("MOLT_STREAM_BUILDS")
	return v == "1" || v == "true" || v == "yes"
}

// unresolvedTokens returns any "{name}" placeholders left in s after
// substitution. Used to catch silent toolchain-resolution failures
// before we hand a malformed command to exec.
func unresolvedTokens(s string) []string {
	var out []string
	seen := map[string]bool{}
	for {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			return out
		}
		tok := s[i : i+j+1]
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
		s = s[i+j+1:]
	}
}

// lookupBuilder finds the build-command template for the given extension,
// honouring the project → global → built-in priority order.
func lookupBuilder(ext string, kernCfg KernelConfig) (string, error) {
	ext = strings.TrimPrefix(ext, ".")
	// 1. Per-project override
	if cmd, ok := kernCfg.Builders[ext]; ok && cmd != "" {
		return cmd, nil
	}
	// 2. Global ~/.molt/kernel-builders.yaml
	b, found, err := kernelbuilder.Find(ext)
	if err != nil {
		return "", err
	}
	if found {
		return b.Command, nil
	}
	// 3. Built-in defaults (also seeded into the global YAML on first
	// load, but available here for the case where the file is unreachable)
	for _, d := range kernelbuilder.DefaultBuilders() {
		if d.Ext == ext {
			return d.Command, nil
		}
	}
	return "", fmt.Errorf(
		"no kernel builder for .%s — add one with `molt kernel-builder add %s '<command>'`",
		ext, ext)
}

// buildTokens populates the substitution map for one build invocation.
// Resolves toolchain tokens only when the template actually references
// them (no point installing zig if the template only uses {cc}), and
// propagates resolution errors so the user sees the real cause instead
// of a confusing "unresolved {zig}" downstream.
//
// ext is the file extension without dot ("zig", "c", …); target is the
// active cross-compile target ("linux_amd64") or "" for host builds.
func buildTokens(template, srcPath, objPath string, zigCfg ZigConfig, pyInclude, ext, target string, targetFlags map[string]map[string]string) (map[string]string, error) {
	t := map[string]string{
		"source":       srcPath,
		"output":       objPath,
		"include_dir":  pyInclude,
		"target_flags": resolveTargetFlags(ext, target, targetFlags),
	}
	if strings.Contains(template, "{zig}") {
		zigBin, err := EnsureZig(zigCfg.Version)
		if err != nil {
			return nil, fmt.Errorf("zig required by build template but unavailable: %w", err)
		}
		t["zig"] = zigBin
	}
	if strings.Contains(template, "{cc}") {
		cc, err := CCompiler()
		if err != nil {
			return nil, fmt.Errorf("cc required by build template but unavailable: %w", err)
		}
		t["cc"] = cc
	}
	if strings.Contains(template, "{cxx}") {
		cxx, err := CXXCompiler()
		if err != nil {
			return nil, fmt.Errorf("c++ required by build template but unavailable: %w", err)
		}
		t["cxx"] = cxx
	}
	return t, nil
}

// resolveTargetFlags looks up the compiler flag string for ext + target in the
// target_flags map. Returns "" when target is empty or no entry exists — callers
// substitute the empty string into the template so host builds are unaffected.
func resolveTargetFlags(ext, target string, targetFlags map[string]map[string]string) string {
	if target == "" || targetFlags == nil {
		return ""
	}
	if osMap, ok := targetFlags[ext]; ok {
		return osMap[target]
	}
	return ""
}

// linkKernelSO compiles glue.c (with Python.h) and links it together
// with the user's pre-compiled object file into the final .so. We use
// `zig cc` because (a) it auto-installs and (b) it accepts macOS's
// -undefined dynamic_lookup uniformly across hosts.
// target and targetFlags enable cross-compilation: when target is non-empty,
// zig linker flags from targetFlags["zig"][target] are prepended to the args.
func linkKernelSO(gluePath, objPath, soOut, pyInclude string, zigCfg ZigConfig, target string, targetFlags map[string]map[string]string) error {
	zigBin, err := EnsureZig(zigCfg.Version)
	if err != nil {
		return fmt.Errorf("locate zig (used for linking): %w", err)
	}
	args := []string{"cc",
		"-shared", "-fPIC", "-O2",
	}
	if flags := resolveTargetFlags("zig", target, targetFlags); flags != "" {
		args = append(args, strings.Fields(flags)...)
	}
	args = append(args,
		"-I", pyInclude,
		"-o", soOut,
		gluePath, objPath,
	)
	if runtime.GOOS == "darwin" {
		args = append(args, "-undefined", "dynamic_lookup")
	}
	cmd := exec.Command(zigBin, args...)
	if out, err := progress.Stream(cmd, "    [link] ", streamingEnabled()); err != nil {
		return fmt.Errorf("link kernel .so:\n%s", string(out))
	}
	return nil
}
