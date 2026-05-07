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
)

// kernelRecipeVersion is mixed into the cache hash; bump on any change
// to how the build pipeline drives the compilers / generators.
const kernelRecipeVersion = "v1"

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
//   - python include dir (encodes Python version via headers)
//   - recipe version
func hashKernel(manifestData, sourceData []byte, abiTag, plat, pyInclude string) string {
	h := sha256.New()
	h.Write(manifestData)
	h.Write([]byte{0})
	h.Write(sourceData)
	h.Write([]byte{0})
	h.Write([]byte(abiTag))
	h.Write([]byte{0})
	h.Write([]byte(plat))
	h.Write([]byte{0})
	h.Write([]byte(pyInclude))
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
func BuildKernelModule(s Source, pyExe, abiTag, plat, extSuffix string, verbose bool, zigCfg ZigConfig, pyInclude string) (Artifact, error) {
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

	// Resolve the sibling source file — required for MVP (pre-built .so
	// + manifest path is a future extension).
	srcPath, lang, ok := ResolveSource(s.Path, manifest,
		[]string{".zig", ".c", ".cpp", ".cc", ".cxx"})
	if !ok {
		return Artifact{}, fmt.Errorf(
			"%s: no source file found (looked for %s.{zig,c,cpp,cc,cxx} alongside)",
			s.Path, s.Basename)
	}
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return Artifact{}, fmt.Errorf("read source %s: %w", srcPath, err)
	}

	hash := hashKernel(manifestData, srcData, abiTag, plat, pyInclude)
	soName := s.Basename + extSuffix

	if hit, cachedPath, err := hasCacheEntry(hash, soName); err != nil {
		return Artifact{}, err
	} else if hit {
		if verbose {
			fmt.Printf("  ✓ kernel  %s  (cached)\n", s.Module)
		}
		return cachedKernelArtifact(s, hash, cachedPath, soName, abiTag, plat), nil
	}

	if verbose {
		fmt.Printf("  ↻ kernel  %s  [%s]\n", s.Module, lang)
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
	if err := compileKernelSource(lang, srcPath, objPath, zigCfg); err != nil {
		return Artifact{}, err
	}

	// Link glue + object into a shared library.
	soStaging := filepath.Join(buildDir, soName)
	if err := linkKernelSO(gluePath, objPath, soStaging, pyInclude, zigCfg); err != nil {
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

// compileKernelSource compiles srcPath (in language `lang`) to a
// position-independent object file at objPath. Per-language adapters
// pick the right toolchain.
func compileKernelSource(lang, srcPath, objPath string, zigCfg ZigConfig) error {
	switch lang {
	case "zig":
		zigBin, err := EnsureZig(zigCfg.Version)
		if err != nil {
			return fmt.Errorf("locate zig: %w", err)
		}
		cmd := exec.Command(zigBin, "build-obj",
			"-O", "ReleaseFast",
			"-fPIC",
			"-femit-bin="+objPath,
			srcPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("zig build-obj %s:\n%s", srcPath, string(out))
		}
		return nil

	case "c":
		cc, err := CCompiler()
		if err != nil {
			return err
		}
		cmd := exec.Command(cc, "-c", "-O2", "-fPIC", "-o", objPath, srcPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cc -c %s:\n%s", srcPath, string(out))
		}
		return nil

	case "cpp":
		cxx, err := CXXCompiler()
		if err != nil {
			return err
		}
		cmd := exec.Command(cxx, "-c", "-O2", "-fPIC", "-o", objPath, srcPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("c++ -c %s:\n%s", srcPath, string(out))
		}
		return nil
	}
	return fmt.Errorf("unsupported source language %q for kernel module", lang)
}

// linkKernelSO compiles glue.c (with Python.h) and links it together
// with the user's pre-compiled object file into the final .so. We use
// `zig cc` because (a) it auto-installs and (b) it accepts macOS's
// -undefined dynamic_lookup uniformly across hosts.
func linkKernelSO(gluePath, objPath, soOut, pyInclude string, zigCfg ZigConfig) error {
	zigBin, err := EnsureZig(zigCfg.Version)
	if err != nil {
		return fmt.Errorf("locate zig (used for linking): %w", err)
	}
	args := []string{"cc",
		"-shared", "-fPIC", "-O2",
		"-I", pyInclude,
		"-o", soOut,
		gluePath, objPath,
	}
	if runtime.GOOS == "darwin" {
		args = append(args, "-undefined", "dynamic_lookup")
	}
	cmd := exec.Command(zigBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("link kernel .so:\n%s", string(out))
	}
	return nil
}
