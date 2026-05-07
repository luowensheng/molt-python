package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveCythonFlags expands cfg into concrete cc flags + extra source
// files to compile alongside the Cython-generated C. All returned paths
// are absolute. Inputs:
//
//   projectDir   absolute path to the project root.
//   pyxPath      absolute path to the .pyx currently being compiled,
//                used so we never re-include its sibling generated .c.
//
// Returns flags, sources (.c / .cpp / .cc / .cxx), and a `cxx` bool which
// is true when any C++ source was picked up OR the user asked for C++
// explicitly. Callers use that to switch the linker to a C++ compiler.
//
// Order: include / define / std flags → user extra_compile_args →
// pkg-config flags → -L lib_dirs → -l libraries. Library flags go last
// so they're available when objects in front of them reference symbols.
func resolveCythonFlags(projectDir, pyxPath string, cfg CythonConfig) (
	flags []string, sources []string, cxx bool, err error,
) {
	pyxBase := strings.TrimSuffix(filepath.Base(pyxPath), ".pyx")
	pyxDir := filepath.Dir(pyxPath)

	addedSrc := map[string]bool{} // dedupe
	addedInc := map[string]bool{}

	addFlag := func(fs ...string) { flags = append(flags, fs...) }
	addInc := func(dir string) {
		if dir == "" || addedInc[dir] {
			return
		}
		addedInc[dir] = true
		addFlag("-I", dir)
	}
	addSrc := func(p string) {
		if p == "" || addedSrc[p] {
			return
		}
		addedSrc[p] = true
		sources = append(sources, p)
	}

	// 1. include_c: walk Paths, gather native sources (.c/.cpp/.cc/.cxx)
	//    and -I roots.
	if cfg.IncludeC {
		for _, rel := range cfg.Paths {
			root := absPath(projectDir, rel)
			if _, statErr := os.Stat(root); statErr != nil {
				continue
			}
			addInc(root)
			walkErr := filepath.Walk(root, func(p string, info os.FileInfo, werr error) error {
				if werr != nil {
					return werr
				}
				if info.IsDir() {
					name := info.Name()
					if name == "__pycache__" || name == "target" ||
						name == "build" || strings.HasPrefix(name, ".") {
						return filepath.SkipDir
					}
					return nil
				}
				if !isNativeSource(p) {
					return nil
				}
				// Skip Cython's own generated artifact for the sibling .pyx
				// (same basename, same dir) — it's already being compiled.
				if filepath.Dir(p) == pyxDir &&
					trimNativeExt(filepath.Base(p)) == pyxBase {
					return nil
				}
				addSrc(p)
				return nil
			})
			if walkErr != nil {
				return nil, nil, false, fmt.Errorf("include_c walk %s: %w", root, walkErr)
			}
		}
	}

	// 2. Explicit include_dirs (always honoured, even without include_c).
	//    Glob patterns expand against the project dir; non-glob entries
	//    pass through unchanged. Results are filtered to directories.
	for _, d := range cfg.IncludeDirs {
		matches, mErr := expandGlob(projectDir, d, modeDir)
		if mErr != nil {
			return nil, nil, false, fmt.Errorf("include_dirs %q: %w", d, mErr)
		}
		for _, m := range matches {
			addInc(m)
		}
	}

	// 3. Explicit sources allow-list. Glob patterns supported; results
	//    are filtered to regular files.
	for _, s := range cfg.Sources {
		matches, mErr := expandGlob(projectDir, s, modeFile)
		if mErr != nil {
			return nil, nil, false, fmt.Errorf("sources %q: %w", s, mErr)
		}
		for _, m := range matches {
			addSrc(m)
		}
	}

	// 4. Defines: -DKEY=VALUE (or -DKEY when value is "" / "1").
	for _, k := range sortedKeys(cfg.Defines) {
		v := cfg.Defines[k]
		if v == "" || v == "1" {
			addFlag("-D" + k)
		} else {
			addFlag("-D" + k + "=" + v)
		}
	}

	// 5. Language standard.
	if cfg.Std != "" {
		addFlag("-std=" + cfg.Std)
	}

	// 6. User-provided extra args, verbatim.
	flags = append(flags, cfg.ExtraCompileArgs...)

	// 7. pkg-config: each name → its --cflags --libs output, tokenised.
	for _, pkg := range cfg.PkgConfig {
		out, pcErr := exec.Command("pkg-config", "--cflags", "--libs", pkg).Output()
		if pcErr != nil {
			return nil, nil, false, fmt.Errorf("pkg-config %s: %w", pkg, pcErr)
		}
		flags = append(flags, splitShellTokens(string(out))...)
	}

	// 8. library_dirs: -L<dir>, glob-aware.
	for _, d := range cfg.LibraryDirs {
		matches, mErr := expandGlob(projectDir, d, modeDir)
		if mErr != nil {
			return nil, nil, false, fmt.Errorf("library_dirs %q: %w", d, mErr)
		}
		for _, m := range matches {
			addFlag("-L" + m)
		}
	}

	// 9. libraries: -l<name>, in the order given.
	for _, lib := range cfg.Libraries {
		addFlag("-l" + lib)
	}

	// Decide C++ mode. Explicit setting wins; otherwise auto-detect from
	// either bundled C++ sources or a `# distutils: language = c++`
	// directive at the top of the .pyx (Cython's own convention).
	switch strings.ToLower(cfg.Language) {
	case "c++", "cpp", "cxx":
		cxx = true
	case "c", "":
		cxx = false
	}
	if !cxx {
		for _, s := range sources {
			if isCPlusPlusSource(s) {
				cxx = true
				break
			}
		}
	}
	if !cxx && cfg.Language == "" {
		if pyxLanguageIsCPP(pyxPath) {
			cxx = true
		}
	}

	return flags, sources, cxx, nil
}

// isNativeSource reports whether p has a recognised C/C++ extension.
func isNativeSource(p string) bool {
	return strings.HasSuffix(p, ".c") || isCPlusPlusSource(p)
}

// isCPlusPlusSource reports whether p has a C++ extension.
func isCPlusPlusSource(p string) bool {
	return strings.HasSuffix(p, ".cpp") ||
		strings.HasSuffix(p, ".cc") ||
		strings.HasSuffix(p, ".cxx") ||
		strings.HasSuffix(p, ".C")
}

// trimNativeExt strips one of {.c,.cpp,.cc,.cxx,.C} from a basename.
func trimNativeExt(name string) string {
	for _, ext := range []string{".cpp", ".cxx", ".cc", ".c", ".C"} {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(name, ext)
		}
	}
	return name
}

// pyxLanguageIsCPP looks for `# distutils: language = c++` near the top
// of a .pyx file (the Cython convention). Stops scanning after 64 lines.
func pyxLanguageIsCPP(pyxPath string) bool {
	data, err := os.ReadFile(pyxPath)
	if err != nil {
		return false
	}
	lines := strings.SplitN(string(data), "\n", 65)
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "#") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(ln, "#"))
		if !strings.HasPrefix(body, "distutils:") {
			continue
		}
		body = strings.TrimSpace(strings.TrimPrefix(body, "distutils:"))
		// Expect: language = c++
		if eq := strings.IndexByte(body, '='); eq >= 0 {
			k := strings.TrimSpace(body[:eq])
			v := strings.TrimSpace(body[eq+1:])
			if k == "language" && (v == "c++" || v == "cpp") {
				return true
			}
		}
	}
	return false
}

// absPath joins projectDir/rel when rel is relative; returns rel unchanged
// if already absolute. Cleans the result.
func absPath(projectDir, rel string) string {
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(projectDir, rel))
}

// resolveCompilerCommands returns the (C, C++) command vectors to invoke,
// honouring config-level overrides. Each result is a non-empty slice
// with the program in [0] and any pre-bound args in the tail (so things
// like `cc = "ccache clang -O2"` or `compiler = "zig"` work uniformly).
//
//   Precedence (highest first):
//     cfg.CC / cfg.CXX  (whitespace-split)
//     cfg.Compiler == "zig"   → "zig cc" / "zig c++"
//     cfg.Compiler == "<bin>" → bin / bin++   (with sanity fixups)
//     fallbackCC + CXXCompiler() probe          (the original behaviour)
//
// fallbackCC is the value passed in by syncplan from CCompiler().
// cxxNeeded controls whether we resolve the C++ command at all — keeps
// pure-C builds from failing if no C++ compiler is on PATH.
func resolveCompilerCommands(cfg CythonConfig, zigCfg ZigConfig, fallbackCC string, cxxNeeded bool) (
	cc []string, cxx []string, err error,
) {
	// Lazily resolve zig only when actually needed. Honours
	// zigCfg.AutoInstall = false by erroring instead of downloading.
	zigBin := func() (string, error) {
		if !zigCfg.AutoInstall {
			if env := os.Getenv("ZIG"); env != "" {
				if p, lpErr := exec.LookPath(env); lpErr == nil {
					return p, nil
				}
			}
			if p, lpErr := exec.LookPath("zig"); lpErr == nil {
				return p, nil
			}
			return "", fmt.Errorf("zig not on PATH and auto_install=false")
		}
		return EnsureZig(zigCfg.Version)
	}

	wantsZigViaCC := containsToken(cfg.CC, "zig")
	wantsZigViaCXX := containsToken(cfg.CXX, "zig")
	wantsZigViaCompiler := strings.EqualFold(cfg.Compiler, "zig")

	// Pre-resolve zig path once if any branch needs it, so a `zig cc`
	// command in cfg.CC gets the absolute path.
	var zigPath string
	if wantsZigViaCC || wantsZigViaCXX || wantsZigViaCompiler {
		p, zErr := zigBin()
		if zErr != nil {
			return nil, nil, zErr
		}
		zigPath = p
	}

	// C side.
	switch {
	case cfg.CC != "":
		cc = strings.Fields(cfg.CC)
		if wantsZigViaCC {
			cc[0] = zigPath
		}
	case wantsZigViaCompiler:
		cc = []string{zigPath, "cc"}
	case cfg.Compiler != "" && !isAutoCompiler(cfg.Compiler):
		cc = []string{cfg.Compiler}
	default:
		if fallbackCC == "" {
			return nil, nil, fmt.Errorf("no C compiler resolved")
		}
		cc = []string{fallbackCC}
	}
	if !cxxNeeded {
		return cc, nil, nil
	}

	// C++ side.
	switch {
	case cfg.CXX != "":
		cxx = strings.Fields(cfg.CXX)
		if wantsZigViaCXX {
			cxx[0] = zigPath
		}
	case wantsZigViaCompiler:
		cxx = []string{zigPath, "c++"}
	case cfg.Compiler != "" && !isAutoCompiler(cfg.Compiler):
		// `compiler = "clang"` → cxx = "clang++". Already-suffixed names
		// (`clang++`, `g++`, `c++`) pass through.
		bin := cfg.Compiler
		if !strings.HasSuffix(bin, "++") &&
			!strings.HasSuffix(bin, "++.exe") &&
			bin != "c++" {
			bin += "++"
		}
		cxx = []string{bin}
	default:
		bin, cxxErr := CXXCompiler()
		if cxxErr != nil {
			return nil, nil, cxxErr
		}
		cxx = []string{bin}
	}
	return cc, cxx, nil
}

// containsToken reports whether `cmd`, split on whitespace, contains tok.
func containsToken(cmd, tok string) bool {
	for _, t := range strings.Fields(cmd) {
		if t == tok {
			return true
		}
	}
	return false
}

func isAutoCompiler(s string) bool {
	switch strings.ToLower(s) {
	case "", "auto", "system":
		return true
	}
	return false
}

// matchMode controls what expandGlob keeps from the walk.
type matchMode int

const (
	modeFile matchMode = iota
	modeDir
)

// expandGlob expands `pattern` against `projectDir`. Supported:
//
//   *           any sequence of non-separator characters
//   ?           any single non-separator character
//   [abc]       character class (Go's filepath.Match semantics)
//   **          zero or more path components (recursive)
//
// Results are absolute paths, filtered by `kind`. A pattern with no glob
// metacharacters is returned as a single-element slice (after path
// resolution and existence/kind check) — callers can treat literal entries
// and globs uniformly.
//
// Returns an empty slice (not an error) when a glob matches nothing, so a
// best-effort default config doesn't blow up on a project that hasn't
// created the matching dir yet.
func expandGlob(projectDir, pattern string, kind matchMode) ([]string, error) {
	abs := absPath(projectDir, pattern)

	// Fast path: literal — just stat it.
	if !hasGlobMeta(pattern) {
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		if (kind == modeDir) != info.IsDir() {
			return nil, nil
		}
		return []string{abs}, nil
	}

	// Glob path: walk from the longest literal prefix and test each
	// candidate against the full pattern.
	root, rest := splitGlobRoot(abs)
	var out []string
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, werr error) error {
		if werr != nil {
			// Skip dirs we can't read; don't fail the whole build.
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip the usual noise.
		if info.IsDir() {
			name := info.Name()
			if name == "__pycache__" || name == "target" ||
				name == "build" || strings.HasPrefix(name, ".") {
				if p != root {
					return filepath.SkipDir
				}
			}
		}
		if (kind == modeDir) != info.IsDir() {
			return nil
		}
		// Match candidate's path-relative-to-root against `rest`.
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil || rel == "." {
			return nil
		}
		ok, mErr := matchDoublestar(rest, filepath.ToSlash(rel))
		if mErr != nil {
			return mErr
		}
		if ok {
			out = append(out, p)
		}
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, walkErr
	}
	return out, nil
}

// hasGlobMeta reports whether s contains any glob metacharacter.
func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// splitGlobRoot splits an absolute pattern into a literal prefix dir and
// the remaining glob portion (slash-joined). For "/proj/src/**/*.c"
// returns ("/proj/src", "**/*.c").
func splitGlobRoot(abs string) (root, rest string) {
	parts := strings.Split(filepath.ToSlash(abs), "/")
	cut := len(parts)
	for i, p := range parts {
		if hasGlobMeta(p) {
			cut = i
			break
		}
	}
	root = filepath.FromSlash(strings.Join(parts[:cut], "/"))
	if root == "" {
		root = "/"
	}
	rest = strings.Join(parts[cut:], "/")
	return root, rest
}

// matchDoublestar tests `name` (a forward-slash relative path) against
// `pattern`, where ** means zero-or-more components and *,?,[…] keep
// their filepath.Match meaning per single component.
func matchDoublestar(pattern, name string) (bool, error) {
	if pattern == "" {
		return name == "", nil
	}
	pp := strings.Split(pattern, "/")
	np := strings.Split(name, "/")
	return matchParts(pp, np)
}

func matchParts(pp, np []string) (bool, error) {
	if len(pp) == 0 {
		return len(np) == 0, nil
	}
	if pp[0] == "**" {
		// Match zero or more components.
		for i := 0; i <= len(np); i++ {
			ok, err := matchParts(pp[1:], np[i:])
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if len(np) == 0 {
		return false, nil
	}
	ok, err := filepath.Match(pp[0], np[0])
	if err != nil || !ok {
		return false, err
	}
	return matchParts(pp[1:], np[1:])
}

// splitShellTokens splits whitespace-separated tokens, treating a single
// pair of double quotes as one token. Good enough for pkg-config output,
// which is space-separated -I/-L/-l flags with the occasional quoted path.
func splitShellTokens(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
