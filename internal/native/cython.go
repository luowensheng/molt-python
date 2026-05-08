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
// Compile builds every source in `sources` in parallel, bounded by the
// number of workers returned by jobsCount(). Build outputs (cargo,
// cython, zig, ...) are captured per-build by exec.CombinedOutput so
// their stdout/stderr never interleaves; the only thing we serialise
// is the one-line progress markers via a stdout mutex.
//
// Result order matches the input source order so callers see a stable
// []Artifact even though builds finish in arbitrary order.
//
// Failure semantics: every queued build runs to completion (whether
// the previous one failed or not), then the first error encountered
// is returned. This keeps the cache populated for any sources that
// did succeed and avoids leaving half-finished work behind.
func Compile(sources []Source, projectDir string, abi pyabi.Info, pyExe, cc, includeDir, extSuffix string, syspathDirs []string, verbose bool, cfg CythonConfig, rust RustConfig, zigCfg ZigConfig, kernCfg KernelConfig) ([]Artifact, error) {
	plat := platTag(abi)
	abiTag := abi.AbiTag

	// One closure per source — shared logic for the three language paths.
	buildOne := func(s Source) (Artifact, error) {
		switch s.Lang {
		case "rust":
			return BuildRustFile(s, pyExe, abiTag, plat, extSuffix, verbose, rust)
		case "kernel":
			return BuildKernelModule(s, pyExe, abiTag, plat, extSuffix, verbose, zigCfg, kernCfg, includeDir)
		}
		return buildCythonOne(s, projectDir, abiTag, plat, pyExe, cc,
			includeDir, syspathDirs, extSuffix, verbose, cfg, zigCfg)
	}

	return runInParallel(sources, jobsCount(), buildOne)
}

// buildCythonOne is the per-source body for Lang == "cython"; factored
// out so Compile's worker can call it without an inline 60-line block.
func buildCythonOne(s Source, projectDir, abiTag, plat, pyExe, cc, includeDir string, syspathDirs []string, extSuffix string, verbose bool, cfg CythonConfig, zigCfg ZigConfig) (Artifact, error) {
	content, err := os.ReadFile(s.Path)
	if err != nil {
		return Artifact{}, fmt.Errorf("read %s: %w", s.Path, err)
	}
	extraFlags, extraSources, cxx, err := resolveCythonFlags(projectDir, s.Path, cfg)
	if err != nil {
		return Artifact{}, fmt.Errorf("resolve cython flags %s: %w", s.Path, err)
	}
	hash := hashSource(content, abiTag, plat, cfg, extraFlags, extraSources)
	soName := s.Basename + extSuffix

	hit, cachedPath, err := hasCacheEntry(hash, soName)
	if err != nil {
		return Artifact{}, err
	}
	if hit {
		if verbose {
			progressLine("  ✓ cython  %s  (cached)\n", s.Module)
		}
		return Artifact{
			Source: s, Hash: hash, Path: cachedPath, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		}, nil
	}

	if verbose {
		progressLine("  ↻ cython  %s\n", s.Module)
	}
	cdir, err := cacheDir(hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return Artifact{}, err
	}
	dest := filepath.Join(cdir, soName)
	if err := compileOneInto(s, dest, pyExe, cc, includeDir, syspathDirs,
		cfg, zigCfg, extraFlags, extraSources, cxx); err != nil {
		return Artifact{}, err
	}
	if err := writeCacheMeta(hash, CacheEntry{
		Lang:       "cython",
		SoName:     soName,
		Source:     s.Path,
		AbiTag:     abiTag,
		Platform:   plat,
		CompiledAt: time.Now().UTC(),
	}); err != nil {
		return Artifact{}, err
	}
	return Artifact{
		Source: s, Hash: hash, Path: dest, SoName: soName,
		AbiTag: abiTag, Plat: plat,
	}, nil
}

// compileOneInto runs the two-step Cython pipeline for a single source
// and writes the resulting shared library directly to soDest. The .c
// intermediate lives in a temp dir that's cleaned up on exit; the .so
// itself is committed to its final destination atomically only when both
// cython and cc succeed.
func compileOneInto(s Source, soDest, pyExe, cc, includeDir string, syspathDirs []string, cfg CythonConfig, zigCfg ZigConfig, extraFlags, extraSources []string, cxx bool) error {
	work, err := os.MkdirTemp("", "molt-cython-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// In C++ mode the Cython output is a .cpp file (not .c), and we use
	// the C++ compiler so the standard library + name mangling are
	// resolved correctly when linking C++ extra-sources.
	cExt := ".c"
	if cxx {
		cExt = ".cpp"
	}
	cFile := filepath.Join(work, s.Basename+cExt)
	soStaging := filepath.Join(work, filepath.Base(soDest))

	// Step 1: cython → .c (or .cpp with --cplus).
	// Honours the project's syspath so `python -m cython` can import Cython
	// from the global store (it's a normal dep, not built into the interpreter).
	cyArgs := []string{"-m", "cython", "--3str"}
	if cxx {
		cyArgs = append(cyArgs, "--cplus")
	}
	for _, k := range sortedKeys(cfg.Directives) {
		cyArgs = append(cyArgs, "--directive", k+"="+cfg.Directives[k])
	}
	cyArgs = append(cyArgs, "-o", cFile, s.Path)
	cy := exec.Command(pyExe, cyArgs...)
	cy.Env = append(envWithPYTHONPATH(syspathDirs), filterPythonEnv(os.Environ())...)
	if out, err := cy.CombinedOutput(); err != nil {
		return fmt.Errorf("cython %s:\n%s", s.Path, string(out))
	}

	// Step 2: cc / c++ → .so (in temp). We then move into place so a
	// half-built cache entry never lingers if compilation fails.
	//
	// resolveCompilerCommands returns a command vector ([prog, args...])
	// so things like `cc = "ccache zig cc"` or `compiler = "zig"` work
	// uniformly with the existing per-source argument list.
	ccCmd, cxxCmd, err := resolveCompilerCommands(cfg, zigCfg, cc, cxx)
	if err != nil {
		return fmt.Errorf("resolve compiler: %w", err)
	}
	cmdVec := ccCmd
	if cxx {
		cmdVec = cxxCmd
	}
	args := []string{"-O2", "-shared", "-fPIC", "-I", includeDir, "-o", soStaging, cFile}
	if runtime.GOOS == "darwin" {
		args = append([]string{"-undefined", "dynamic_lookup"}, args...)
	}
	// Bundled .c/.cpp sources (from include_c / Sources) compile in alongside.
	args = append(args, extraSources...)
	// Resolved flags: include dirs, defines, std, user extra args,
	// pkg-config output, -L, -l.
	args = append(args, extraFlags...)
	full := append([]string{}, cmdVec...)
	full = append(full, args...)
	build := exec.Command(full[0], full[1:]...)
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s:\n%s", filepath.Base(full[0]), s.Path, string(out))
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
