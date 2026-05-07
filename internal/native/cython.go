package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"molt/internal/pyabi"
)

// Compile runs the appropriate compiler for each source not already in cache.
// Dispatches on s.Lang: "cython" → Cython+cc pipeline, "rust" → cargo.
// Returns one Artifact per input source (cache hits inclusive).
func Compile(sources []Source, abi pyabi.Info, pyExe, cc, includeDir, extSuffix string, syspathDirs []string, verbose bool, cfg CythonConfig, rust RustConfig) ([]Artifact, error) {
	plat := platTag(abi)
	abiTag := abi.AbiTag
	out := make([]Artifact, 0, len(sources))
	for _, s := range sources {
		if s.Lang == "rust" {
			art, err := BuildRustFile(s, pyExe, abiTag, plat, extSuffix, verbose, rust)
			if err != nil {
				return nil, err
			}
			out = append(out, art)
			continue
		}

		content, err := os.ReadFile(s.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", s.Path, err)
		}
		hash := hashSource(content, abiTag, plat, cfg)
		soName := s.Basename + extSuffix

		hit, cachedPath, err := hasCacheEntry(hash, soName)
		if err != nil {
			return nil, err
		}
		if hit {
			if verbose {
				fmt.Printf("  ✓ cython  %s  (cached)\n", s.Module)
			}
			out = append(out, Artifact{
				Source: s, Hash: hash, Path: cachedPath, SoName: soName,
				AbiTag: abiTag, Plat: plat,
			})
			continue
		}

		if verbose {
			fmt.Printf("  ↻ cython  %s\n", s.Module)
		}
		// Pre-create the cache dir and compile straight into it. Avoids
		// the cross-defer dance of "move to cache after temp cleanup".
		cdir, err := cacheDir(hash)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(cdir, 0o755); err != nil {
			return nil, err
		}
		dest := filepath.Join(cdir, soName)
		if err := compileOneInto(s, dest, pyExe, cc, includeDir, syspathDirs, cfg); err != nil {
			return nil, err
		}
		if err := writeCacheMeta(hash, CacheEntry{
			Lang:       "cython",
			SoName:     soName,
			Source:     s.Path,
			AbiTag:     abiTag,
			Platform:   plat,
			CompiledAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
		out = append(out, Artifact{
			Source: s, Hash: hash, Path: dest, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		})
	}
	return out, nil
}

// compileOneInto runs the two-step Cython pipeline for a single source
// and writes the resulting shared library directly to soDest. The .c
// intermediate lives in a temp dir that's cleaned up on exit; the .so
// itself is committed to its final destination atomically only when both
// cython and cc succeed.
func compileOneInto(s Source, soDest, pyExe, cc, includeDir string, syspathDirs []string, cfg CythonConfig) error {
	work, err := os.MkdirTemp("", "molt-cython-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	cFile := filepath.Join(work, s.Basename+".c")
	soStaging := filepath.Join(work, filepath.Base(soDest))

	// Step 1: cython → .c. Pass any user-defined language directives.
	// Honours the project's syspath so `python -m cython` can import Cython
	// from the global store (it's a normal dep, not built into the interpreter).
	cyArgs := []string{"-m", "cython", "--3str"}
	for _, k := range sortedKeys(cfg.Directives) {
		cyArgs = append(cyArgs, "--directive", k+"="+cfg.Directives[k])
	}
	cyArgs = append(cyArgs, "-o", cFile, s.Path)
	cy := exec.Command(pyExe, cyArgs...)
	cy.Env = append(envWithPYTHONPATH(syspathDirs), filterPythonEnv(os.Environ())...)
	if out, err := cy.CombinedOutput(); err != nil {
		return fmt.Errorf("cython %s:\n%s", s.Path, string(out))
	}

	// Step 2: cc → .so (in temp). We then move into place so a half-built
	// cache entry never lingers if cc fails between writes.
	args := []string{"-O2", "-shared", "-fPIC", "-I", includeDir, "-o", soStaging, cFile}
	if runtime.GOOS == "darwin" {
		args = append([]string{"-undefined", "dynamic_lookup"}, args...)
	}
	args = append(args, cfg.ExtraCompileArgs...)
	build := exec.Command(cc, args...)
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("cc %s:\n%s", s.Path, string(out))
	}

	if err := os.Rename(soStaging, soDest); err != nil {
		// Cross-device rename can fail; fall back to copy.
		if cpErr := copyFile(soStaging, soDest); cpErr != nil {
			return cpErr
		}
		_ = os.Remove(soStaging)
	}
	return nil
}
