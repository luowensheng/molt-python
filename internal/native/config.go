package native

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"molt/internal/kernelbuilder"
)

// readConfigFile reads the project configuration file, preferring
// moltproject.toml (non-Python / polyglot projects) over pyproject.toml.
func readConfigFile(projectDir string) ([]byte, error) {
	for _, name := range []string{"moltproject.toml", "pyproject.toml"} {
		if data, err := os.ReadFile(filepath.Join(projectDir, name)); err == nil {
			return data, nil
		}
	}
	return nil, os.ErrNotExist
}

// NativeModuleConfig is one [[tool.molt.native]] entry from the project config.
type NativeModuleConfig struct {
	Module      string
	Src         string   // project-relative path to source folder or file
	Preset      string   // global preset name (optional)
	Build       string   // explicit build command (overrides preset)
	Output      string   // explicit output template (overrides preset)
	SrcPatterns []string // explicit src patterns for hashing (overrides preset)
}

// loadNativeModuleConfigs reads all [[tool.molt.native]] array-of-table entries
// from pyproject.toml. Returns nil if none are found.
func loadNativeModuleConfigs(projectDir string) []NativeModuleConfig {
	data, err := readConfigFile(projectDir)
	if err != nil {
		return nil
	}

	const header = "[[tool.molt.native]]"
	var out []NativeModuleConfig
	var cur *NativeModuleConfig

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Strip inline comments.
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}

		if line == header {
			if cur != nil && cur.Module != "" {
				out = append(out, *cur)
			}
			cur = &NativeModuleConfig{}
			continue
		}

		// Any other section header ends the current entry.
		if strings.HasPrefix(line, "[") {
			if cur != nil && cur.Module != "" {
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
		case "module":
			cur.Module = strings.Trim(val, `"'`)
		case "src":
			cur.Src = strings.Trim(val, `"'`)
		case "preset":
			cur.Preset = strings.Trim(val, `"'`)
		case "build":
			cur.Build = strings.Trim(val, `"'`)
		case "output":
			cur.Output = strings.Trim(val, `"'`)
		case "src_patterns":
			cur.SrcPatterns = parseTOMLStringArray(val)
		}
	}
	if cur != nil && cur.Module != "" {
		out = append(out, *cur)
	}
	return out
}

// CythonConfig holds optional overrides read from [tool.molt.cython] in
// pyproject.toml. All fields have sensible defaults applied by
// LoadCythonConfig so callers never need to check for zero values.
type CythonConfig struct {
	// Paths are project-relative roots scanned for *.pyx files.
	// Default: [".", "src"] (src is silently skipped when absent).
	Paths []string

	// ExtraCompileArgs are appended to the cc invocation after the fixed flags.
	ExtraCompileArgs []string

	// Directives are Cython language-level settings passed as
	// --directive key=value. E.g. {"boundscheck": "false"}.
	Directives map[string]string

	// PkgConfig is a list of pkg-config package names. For each, molt
	// runs `pkg-config --cflags --libs <name>` at build time and merges
	// the resulting flags into the cc invocation. Lets you use
	// brew/system/Nix-installed C libraries without hand-coding -I/-L/-l.
	// Example: ["libsodium", "openssl"].
	PkgConfig []string

	// IncludeC turns on automatic C/header bundling. When true:
	//   1. Each Paths root is added to -I, so headers anywhere in the
	//      project are includable as `#include "A/B/C.h"`.
	//   2. Every .c file found under Paths (excluding ones generated next
	//      to a .pyx of the same basename) is compiled in alongside the
	//      Cython-generated C and linked into the .so.
	IncludeC bool

	// IncludeDirs are extra -I directories. Each is appended verbatim to
	// the cc invocation. Project-relative paths are resolved against the
	// project dir at build time.
	IncludeDirs []string

	// Sources is an explicit allow-list of .c / .cpp / .cc / .cxx files to
	// compile in. Used when IncludeC is too greedy, or to bring in C++
	// code (which IncludeC won't pick up). Project-relative paths.
	// Glob patterns supported: "src/**/*.c", "vendor/*/lib.cpp", etc.
	Sources []string

	// Libraries is a list of library names to link, sugar for "-l<name>".
	// Order is preserved so dependent libs can come before dependencies.
	// Example: ["opencv_core", "opencv_imgproc", "opencv_highgui"].
	Libraries []string

	// LibraryDirs is a list of dirs to add as "-L<dir>". Glob patterns
	// supported (resolved against project dir, kept only if dir exists).
	LibraryDirs []string

	// Defines is a map of preprocessor macros: each entry becomes
	// "-DKEY=VALUE", or "-DKEY" when the value is "" or "1". Iterated in
	// sorted order so the cache hash is deterministic.
	Defines map[string]string

	// Std selects the language standard: passed as "-std=<value>".
	// Examples: "c11", "c17", "c++17", "c++20", "gnu++17". Empty means
	// no -std flag.
	Std string

	// Language overrides the auto-detected language. "c" or "c++".
	// When unset, molt picks C++ if any source matches *.cpp / *.cc /
	// *.cxx OR the .pyx contains `# distutils: language = c++`.
	Language string

	// Compiler is a shortcut for picking a toolchain.
	//   "" / "auto" / "system"   → $CC/$CXX or cc/clang/gcc on PATH
	//   "zig"                     → "zig cc" / "zig c++"
	// Anything else is treated as the literal name of a binary on PATH
	// (e.g. "clang-17"); the C++ counterpart adds "++" if missing.
	Compiler string

	// CC is an explicit C compiler command, with args, whitespace-split.
	// Overrides Compiler. Examples: "zig cc", "ccache clang -O2".
	CC string

	// CXX is an explicit C++ compiler command, with args, whitespace-split.
	// Overrides Compiler. Examples: "zig c++", "ccache clang++ -O2".
	CXX string
}

// LoadCythonConfig reads [tool.molt.cython] and [tool.molt.cython.directives]
// from pyproject.toml in projectDir. Missing file or missing section both
// return defaults silently — callers should always get a usable config.
func LoadCythonConfig(projectDir string) CythonConfig {
	cfg := CythonConfig{
		Paths:      []string{".", "src"},
		Directives: map[string]string{},
		Defines:    map[string]string{},
	}

	data, err := readConfigFile(projectDir)
	if err != nil {
		return cfg
	}

	const (
		sectionNone       = 0
		sectionCython     = 1
		sectionDirectives = 2
		sectionDefines    = 3
	)

	section := sectionNone
	var pathsSet, argsSet bool

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Strip inline comments.
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}

		// Detect section headers.
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			switch line {
			case "[tool.molt.cython]":
				section = sectionCython
			case "[tool.molt.cython.directives]":
				section = sectionDirectives
			case "[tool.molt.cython.defines]":
				section = sectionDefines
			default:
				section = sectionNone
			}
			continue
		}

		if section == sectionNone || line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch section {
		case sectionCython:
			switch key {
			case "paths":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.Paths = arr
					pathsSet = true
				}
			case "extra_compile_args":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.ExtraCompileArgs = arr
					argsSet = true
				}
			case "pkg_config":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.PkgConfig = arr
				}
			case "include_c":
				v := strings.ToLower(strings.Trim(val, `"'`))
				cfg.IncludeC = (v == "true" || v == "1" || v == "yes")
			case "include_dirs":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.IncludeDirs = arr
				}
			case "sources":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.Sources = arr
				}
			case "libraries":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.Libraries = arr
				}
			case "library_dirs":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.LibraryDirs = arr
				}
			case "std":
				if v := strings.Trim(val, `"'`); v != "" {
					cfg.Std = v
				}
			case "language":
				if v := strings.Trim(val, `"'`); v != "" {
					cfg.Language = v
				}
			case "compiler":
				cfg.Compiler = strings.Trim(val, `"'`)
			case "cc":
				cfg.CC = strings.Trim(val, `"'`)
			case "cxx":
				cfg.CXX = strings.Trim(val, `"'`)
			}
		case sectionDirectives:
			cfg.Directives[key] = strings.Trim(val, `"'`)
		case sectionDefines:
			cfg.Defines[key] = strings.Trim(val, `"'`)
		}
	}

	_ = pathsSet
	_ = argsSet
	return cfg
}

// ZigConfig holds optional overrides read from [tool.molt.zig] in
// pyproject.toml. Zig is auto-installed under
// ~/.molt/toolchains/zig/<version>/ when needed (i.e. when a user opts in
// via `[tool.molt.cython] compiler = "zig"`).
type ZigConfig struct {
	// Version pins the zig release to install/use. Default is
	// ZigDefaultVersion. Honoured only when EnsureZig has to download —
	// an existing zig on PATH is used regardless of its version.
	Version string

	// AutoInstall, when false, makes molt fail loudly if `zig` isn't on
	// PATH instead of downloading. Defaults to true. Useful in offline
	// CI where you want all toolchain installs done up front.
	AutoInstall bool
}

// LoadZigConfig reads [tool.molt.zig] from pyproject.toml.
func LoadZigConfig(projectDir string) ZigConfig {
	cfg := ZigConfig{
		Version:     ZigDefaultVersion,
		AutoInstall: true,
	}
	data, err := readConfigFile(projectDir)
	if err != nil {
		return cfg
	}
	inSection := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = (line == "[tool.molt.zig]")
			continue
		}
		if !inSection || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		switch key {
		case "version":
			if v := strings.Trim(val, `"'`); v != "" {
				cfg.Version = v
			}
		case "auto_install":
			v := strings.ToLower(strings.Trim(val, `"'`))
			cfg.AutoInstall = !(v == "false" || v == "0" || v == "no")
		}
	}
	return cfg
}

// KernelConfig holds settings for manifest-driven kernel modules,
// read from [tool.molt.native_kernel] in pyproject.toml.
//
// A "kernel" is a manifest file (`<name>.molt.toml`) that describes a
// Python module's exported functions. molt finds the manifest, looks
// for a sibling source file matching one of `source_extensions`,
// generates a C glue layer from the manifest, compiles the source,
// and links them into an importable .so plus a .pyi stub.
//
// The discovery is manifest-first and language-agnostic: molt doesn't
// scan for .zig or .rs etc. directly — it scans for manifests and the
// configured source extensions only resolve siblings.
type KernelConfig struct {
	// Paths is project-relative dirs to scan for manifest files.
	// Default: [".", "src"]. Mirrors CythonConfig.Paths.
	Paths []string

	// ManifestDir is an optional centralised directory for manifest
	// files. When set, molt looks here in addition to scanning Paths.
	// Sibling manifests still win on basename collision.
	ManifestDir string

	// SourceExtensions is the list of file extensions molt will try
	// when matching a manifest to its source file. Default:
	// [".zig", ".c", ".cpp", ".cc", ".cxx"]. The first one that has
	// a sibling matching the manifest's basename wins.
	SourceExtensions []string

	// ManifestSuffixes is the list of manifest filename suffixes to
	// recognise. Default: [".molt.toml"]. Future formats (.molt.json,
	// .molt.yaml) plug in here.
	ManifestSuffixes []string

	// Builders is the per-extension build-command override read from
	// [tool.molt.native_kernel.build.<ext>] sections. Keys are extension
	// names without the leading dot ("zig", "odin", "c", ...). Values are
	// command templates with the same {token} vocabulary as
	// ~/.molt/kernel-builders.yaml. Project-level overrides beat the
	// global YAML which beats the built-in defaults.
	Builders map[string]string

	// TargetFlags maps compiler extension → os_arch → compiler flag string.
	// Read from [tool.molt.native_kernel.target_flags.<ext>] in
	// pyproject.toml, merged with ~/.molt/kernel.yaml (project wins on
	// conflict). Used to resolve the {target_flags} token in build templates.
	//
	// Example entry: TargetFlags["zig"]["linux_amd64"] = "-target x86_64-linux-gnu"
	TargetFlags map[string]map[string]string

	// Target is the active cross-compile target in Go-style os_arch notation
	// ("linux_amd64", "darwin_arm64", …). Empty means host build — no
	// {target_flags} substitution occurs. Set from the CLI --target flag.
	Target string
}

// LoadKernelConfig parses [tool.molt.native_kernel] and the per-extension
// [tool.molt.native_kernel.build.<ext>] subsections from pyproject.toml.
//
// SourceExtensions is auto-extended with any extension that has a
// configured builder (global ~/.molt/kernel-builders.yaml or per-project
// override). So `molt kernel-builder add odin --from-template` makes
// .odin a watched extension automatically — no per-project pyproject
// edit needed.
func LoadKernelConfig(projectDir string) KernelConfig {
	cfg := KernelConfig{
		Paths:            []string{".", "src"},
		SourceExtensions: []string{".zig", ".c", ".cpp", ".cc", ".cxx", ".s", ".S"},
		ManifestSuffixes: []string{".molt.toml"},
		Builders:         map[string]string{},
		TargetFlags:      map[string]map[string]string{},
	}
	// Read pyproject.toml if present, otherwise fall through to the
	// auto-extension block (so global builders still extend the
	// watched-extensions list even when no pyproject exists).
	data, err := readConfigFile(projectDir)
	if err != nil {
		mergeGlobalTargetFlags(&cfg)
		return augmentKernelExtensions(cfg)
	}

	// Section state: "" = not in our section, "main" = [tool.molt.native_kernel],
	// "build:<ext>" = [tool.molt.native_kernel.build.<ext>],
	// "flags:<ext>" = [tool.molt.native_kernel.target_flags.<ext>].
	const (
		buildPrefix = "[tool.molt.native_kernel.build."
		flagsPrefix = "[tool.molt.native_kernel.target_flags."
	)
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			switch {
			case line == "[tool.molt.native_kernel]":
				section = "main"
			case strings.HasPrefix(line, buildPrefix) && strings.HasSuffix(line, "]"):
				ext := strings.TrimSuffix(strings.TrimPrefix(line, buildPrefix), "]")
				section = "build:" + ext
			case strings.HasPrefix(line, flagsPrefix) && strings.HasSuffix(line, "]"):
				ext := strings.TrimSuffix(strings.TrimPrefix(line, flagsPrefix), "]")
				section = "flags:" + ext
			default:
				section = ""
			}
			continue
		}
		if section == "" || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		switch {
		case section == "main":
			switch key {
			case "paths":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.Paths = arr
				}
			case "manifest_dir":
				cfg.ManifestDir = strings.Trim(val, `"'`)
			case "source_extensions":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.SourceExtensions = arr
				}
			case "manifest_suffixes":
				if arr := parseTOMLStringArray(val); arr != nil {
					cfg.ManifestSuffixes = arr
				}
			}
		case strings.HasPrefix(section, "build:"):
			if key == "command" {
				ext := strings.TrimPrefix(section, "build:")
				cfg.Builders[ext] = strings.Trim(val, `"'`)
			}
		case strings.HasPrefix(section, "flags:"):
			ext := strings.TrimPrefix(section, "flags:")
			if cfg.TargetFlags[ext] == nil {
				cfg.TargetFlags[ext] = map[string]string{}
			}
			cfg.TargetFlags[ext][key] = strings.Trim(val, `"'`)
		}
	}

	// Merge global ~/.molt/kernel.yaml — project entries win on conflict.
	mergeGlobalTargetFlags(&cfg)
	return augmentKernelExtensions(cfg)
}

// mergeGlobalTargetFlags loads ~/.molt/kernel.yaml and merges its target_flags
// into cfg. Project-level entries take precedence: if both define the same
// ext + os_arch key, the project value is kept unchanged.
func mergeGlobalTargetFlags(cfg *KernelConfig) {
	gs, err := kernelbuilder.LoadGlobalSettings()
	if err != nil {
		return
	}
	for ext, osMap := range gs.TargetFlags {
		if cfg.TargetFlags[ext] == nil {
			cfg.TargetFlags[ext] = map[string]string{}
		}
		for osArch, flags := range osMap {
			if _, exists := cfg.TargetFlags[ext][osArch]; !exists {
				cfg.TargetFlags[ext][osArch] = flags
			}
		}
	}
}

// augmentKernelExtensions extends cfg.SourceExtensions with any extension
// that has a builder configured (per-project or globally), so adding a
// builder via `molt kernel-builder add <ext>` automatically makes that
// extension a watched source extension. Dedup keys are trimmed-dot.
func augmentKernelExtensions(cfg KernelConfig) KernelConfig {
	seen := map[string]bool{}
	for _, e := range cfg.SourceExtensions {
		seen[strings.TrimPrefix(e, ".")] = true
	}
	for ext := range cfg.Builders {
		if !seen[ext] {
			cfg.SourceExtensions = append(cfg.SourceExtensions, "."+ext)
			seen[ext] = true
		}
	}
	if globalBuilders, err := kernelbuilder.Load(); err == nil {
		for _, b := range globalBuilders {
			if !seen[b.Ext] {
				cfg.SourceExtensions = append(cfg.SourceExtensions, "."+b.Ext)
				seen[b.Ext] = true
			}
		}
	}
	return cfg
}

// RustConfig holds optional overrides read from [tool.molt.rust] in
// pyproject.toml. Used by BuildRustFile to populate the auto-generated
// Cargo.toml for single-.rs PyO3 builds. All fields have defaults so
// callers never need to check for zero values.
type RustConfig struct {
	// Pyo3Version is the version requirement written into the generated
	// Cargo.toml under [dependencies]. Default "0.24" (the first pyo3
	// release supporting Python 3.14). Bump in pyproject.toml when newer
	// Python versions need newer pyo3 support.
	Pyo3Version string

	// Pyo3Features overrides the cargo features enabled on the pyo3
	// dependency. Default ["extension-module"]. If you add e.g. "abi3-py39"
	// the generated cdylib becomes ABI-stable across Python versions.
	Pyo3Features []string

	// AutoPrelude controls whether molt prepends `use pyo3::prelude::*;`
	// (and a couple of related types) to the user's .rs file at build
	// time. Implemented by writing a tiny lib.rs wrapper in the build
	// directory that does the imports. Skipped automatically when the
	// file already contains a `pyo3` reference. Default true.
	AutoPrelude bool

	// AutoAttrs controls whether molt auto-prepends `#[pyfunction]` to
	// every top-level `fn` that lacks an attribute, and `#[pymodule]`
	// to the function whose name matches the module's name (i.e. the
	// .rs file's basename). Functions that already carry any `#[…]`
	// attribute are left untouched, so users can opt out per-fn by
	// adding e.g. `#[allow(dead_code)]`. Default true.
	AutoAttrs bool
}

// LoadRustConfig reads [tool.molt.rust] from pyproject.toml in projectDir.
// Missing file or section returns defaults silently.
func LoadRustConfig(projectDir string) RustConfig {
	cfg := RustConfig{
		Pyo3Version:  "0.24",
		Pyo3Features: []string{"extension-module"},
		AutoPrelude:  true,
		AutoAttrs:    true,
	}

	data, err := readConfigFile(projectDir)
	if err != nil {
		return cfg
	}

	inSection := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = (line == "[tool.molt.rust]")
			continue
		}
		if !inSection || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		switch key {
		case "pyo3_version":
			if v := strings.Trim(val, `"'`); v != "" {
				cfg.Pyo3Version = v
			}
		case "pyo3_features":
			if arr := parseTOMLStringArray(val); arr != nil {
				cfg.Pyo3Features = arr
			}
		case "auto_prelude":
			v := strings.ToLower(strings.Trim(val, `"'`))
			cfg.AutoPrelude = !(v == "false" || v == "0" || v == "no")
		case "auto_attrs":
			v := strings.ToLower(strings.Trim(val, `"'`))
			cfg.AutoAttrs = !(v == "false" || v == "0" || v == "no")
		}
	}
	return cfg
}

// parseTOMLStringArray parses a TOML inline string array like ["a", "b"] or
// ['a', 'b']. Returns nil when the value doesn't look like an array so the
// caller can leave the default in place.
func parseTOMLStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
