// Package pkgbackend manages the global registry of per-language package-manager
// backends stored at ~/.molt/pkg-backends.yaml.
//
// A "backend" is one entry that says "to add/remove/sync/list/upgrade packages
// in a <lang> project, run these shell commands after substituting tokens."
// molt uses it during `molt add`, `molt remove`, and `molt sync` to dispatch
// to the appropriate package manager.
//
// Resolution order at command time (highest priority first):
//
//  1. [tool.molt.backend] inline override in the project's config file
//  2. ~/.molt/pkg-backends.yaml entry with matching lang
//  3. Built-in default compiled into molt
//
// The Python backend (uv) is always the built-in reference implementation.
// Every other package ecosystem — Cargo, go mod, npm, Zig, Swift PM, Nimble —
// can be described as a backend entry with no molt code change.
package pkgbackend

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Backend is one package-manager recipe, keyed by language name.
type Backend struct {
	Lang    string `yaml:"lang"`                      // e.g. "rust", "go", "node"
	Add     string `yaml:"add"`                       // command to add a package
	Remove  string `yaml:"remove"`                    // command to remove a package
	Sync    string `yaml:"sync"`                      // command to install all deps
	List    string `yaml:"list"`                      // command to list installed packages
	Upgrade string `yaml:"upgrade"`                   // command to upgrade a package
	// Per-OS overrides — same pattern as run-handlers.
	AddWindows     string `yaml:"add_windows,omitempty"`
	RemoveWindows  string `yaml:"remove_windows,omitempty"`
	SyncWindows    string `yaml:"sync_windows,omitempty"`
	UpgradeWindows string `yaml:"upgrade_windows,omitempty"`
}

// Op names the five package operations.
type Op string

const (
	OpAdd     Op = "add"
	OpRemove  Op = "remove"
	OpSync    Op = "sync"
	OpList    Op = "list"
	OpUpgrade Op = "upgrade"
)

// Resolve returns the platform-appropriate command string for the given
// operation. Falls back to the platform-agnostic field when no OS override
// is set.
func (b Backend) Resolve(op Op, goos string) string {
	isWin := goos == "windows"
	switch op {
	case OpAdd:
		if isWin && b.AddWindows != "" {
			return b.AddWindows
		}
		return b.Add
	case OpRemove:
		if isWin && b.RemoveWindows != "" {
			return b.RemoveWindows
		}
		return b.Remove
	case OpSync:
		if isWin && b.SyncWindows != "" {
			return b.SyncWindows
		}
		return b.Sync
	case OpList:
		return b.List
	case OpUpgrade:
		if isWin && b.UpgradeWindows != "" {
			return b.UpgradeWindows
		}
		return b.Upgrade
	}
	return ""
}

// Expand substitutes {tokens} in a command template.
//
// Standard tokens:
//
//	{package}      – package name as typed by the user
//	{version}      – raw version string ("1.6.43", "^1.6", "")
//	{version_flag} – formatted version argument for the backend's CLI
//	{flags}        – extra flags forwarded from the CLI
//	{manifest}     – absolute path to the project's manifest file
//	{lockfile}     – absolute path to the project's lock file
//	{project}      – absolute path to the project root
//
// Unknown tokens are left unchanged.
func Expand(command string, tokens map[string]string) string {
	out := command
	for k, v := range tokens {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// FormatVersionFlag formats a version string into the argument style expected
// by each language's package manager CLI.
//
//	python/uv  : ">=2.0"  → ">=2.0"  (appended directly: uv add numpy>=2.0)
//	rust/cargo : "^1.0"   → "--version ^1.0"
//	go         : "v1.8.1" → "@v1.8.1"
//	node/npm   : "1.6.0"  → "@1.6.0"
//	zig        : version ignored (URL-based)
//
// When version is empty the function returns "".
func FormatVersionFlag(lang, version string) string {
	if version == "" {
		return ""
	}
	switch lang {
	case "python":
		// uv add numpy>=2.0 — version appended to package name, not as a flag.
		// Callers should append this directly after {package}, not as a separate arg.
		// If the version already starts with a comparator, pass through.
		if startsWithComparator(version) {
			return version
		}
		return "==" + version
	case "rust":
		return "--version " + version
	case "go":
		if !strings.HasPrefix(version, "v") {
			return "@v" + version
		}
		return "@" + version
	case "node", "bun", "pnpm", "yarn":
		return "@" + version
	case "swift":
		return "--exact " + version
	default:
		// Generic: space-separated
		return version
	}
}

func startsWithComparator(s string) bool {
	return len(s) > 0 && (s[0] == '>' || s[0] == '<' || s[0] == '=' || s[0] == '!' || s[0] == '^' || s[0] == '~')
}

// InferLang guesses the target language from a package name.
// Returns ("", false) when inference is ambiguous or impossible.
//
// Inference rules (applied in order):
//  1. Contains "/" with a dotted prefix  → Go module (e.g. github.com/pkg)
//  2. Starts with "@"                    → Node scoped package
//  3. Starts with "lib" and is all-lower → C library (e.g. libpng, libcurl)
//  4. Ends with "-sys"                   → Rust sys crate
//  5. Ends with "-rs"                    → Rust crate
//  6. Ends with "-go"                    → Go package
//  7. Otherwise                          → ambiguous; returns ("", false)
func InferLang(pkg string) (string, bool) {
	lower := strings.ToLower(pkg)

	// Go module path: domain/path
	if slashIdx := strings.Index(pkg, "/"); slashIdx > 0 {
		prefix := pkg[:slashIdx]
		if strings.Contains(prefix, ".") {
			return "go", true
		}
	}

	// Node scoped package: @scope/name
	if strings.HasPrefix(pkg, "@") {
		return "node", true
	}

	// Rust sys crate — check before lib* so "libz-sys" → rust, not c.
	if strings.HasSuffix(lower, "-sys") {
		return "rust", true
	}

	// C library convention
	if strings.HasPrefix(lower, "lib") && lower == pkg {
		return "c", true
	}

	// Rust naming hint
	if strings.HasSuffix(lower, "-rs") {
		return "rust", true
	}

	// Go naming hint
	if strings.HasSuffix(lower, "-go") {
		return "go", true
	}

	return "", false
}

// defaultBackends are the built-in backends seeded into a fresh
// ~/.molt/pkg-backends.yaml.
var defaultBackends = []Backend{
	// Python — uv as the resolver (same as existing molt behaviour).
	// {version_flag} is appended directly after {package} (no space separator)
	// because uv uses "numpy>=2.0" style, not "numpy --version >=2.0".
	{
		Lang:    "python",
		Add:     "uv add {package}{version_flag} {flags}",
		Remove:  "uv remove {package} {flags}",
		Sync:    "uv sync",
		List:    "uv pip list",
		Upgrade: "uv add --upgrade {package}",
	},
	// Rust — delegate entirely to Cargo.
	{
		Lang:    "rust",
		Add:     "cargo add {package} {version_flag} {flags}",
		Remove:  "cargo remove {package}",
		Sync:    "cargo fetch",
		List:    "cargo tree --depth 1",
		Upgrade: "cargo update {package}",
	},
	// Go — delegate to go modules.
	{
		Lang:    "go",
		Add:     "go get {package}{version_flag} {flags}",
		Remove:  "go mod edit -droprequire {package} && go mod tidy",
		Sync:    "go mod download",
		List:    "go list -m all",
		Upgrade: "go get -u {package}",
	},
	// Node.js — npm by default; swap to bun/pnpm via project override or
	// a separate backend entry with lang = "bun" / lang = "pnpm".
	{
		Lang:           "node",
		Add:            "npm install {package}{version_flag} {flags}",
		Remove:         "npm uninstall {package}",
		Sync:           "npm install",
		List:           "npm list --depth 0",
		Upgrade:        "npm update {package}",
		AddWindows:     "npm.cmd install {package}{version_flag} {flags}",
		SyncWindows:    "npm.cmd install",
		UpgradeWindows: "npm.cmd update {package}",
	},
	// Bun — alternative Node backend.
	{
		Lang:    "bun",
		Add:     "bun add {package}{version_flag} {flags}",
		Remove:  "bun remove {package}",
		Sync:    "bun install",
		List:    "bun pm ls",
		Upgrade: "bun update {package}",
	},
	// Zig — wraps zig fetch; packages are URLs or registry aliases.
	{
		Lang:    "zig",
		Add:     "zig fetch --save {package}",
		Remove:  "molt-zig-remove {package}",
		Sync:    "zig build --fetch",
		List:    "cat build.zig.zon",
		Upgrade: "zig fetch --save {package}",
	},
	// C/C++ — pkg-config detection + zig-build compilation.
	{
		Lang:    "c",
		Add:     "molt-pkg-resolve {package} {version}",
		Remove:  "molt-pkg-remove {package}",
		Sync:    "molt-pkg-sync",
		List:    "pkg-config --list-all",
		Upgrade: "molt-pkg-resolve {package} latest",
	},
	// Swift Package Manager.
	{
		Lang:    "swift",
		Add:     "swift package add {package} {version_flag} {flags}",
		Remove:  "swift package remove {package}",
		Sync:    "swift package resolve",
		List:    "swift package show-dependencies",
		Upgrade: "swift package update {package}",
	},
	// Nim / Nimble.
	{
		Lang:    "nim",
		Add:     "nimble install {package}{version_flag} {flags}",
		Remove:  "nimble uninstall {package}",
		Sync:    "nimble install",
		List:    "nimble list --installed",
		Upgrade: "nimble install {package}",
	},
}

// DefaultBackends returns a copy of the built-in set.
func DefaultBackends() []Backend {
	out := make([]Backend, len(defaultBackends))
	copy(out, defaultBackends)
	return out
}

// GlobalPath returns the path to ~/.molt/pkg-backends.yaml.
func GlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "pkg-backends.yaml"), nil
}

// Load reads ~/.molt/pkg-backends.yaml. If the file is missing it is seeded
// with the built-in defaults and saved.
func Load() ([]Backend, error) {
	path, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := Save(DefaultBackends()); err != nil {
			return nil, err
		}
		return DefaultBackends(), nil
	}
	if err != nil {
		return nil, err
	}
	var backends []Backend
	if err := yaml.Unmarshal(data, &backends); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return backends, nil
}

// Save writes the backend list to ~/.molt/pkg-backends.yaml.
func Save(backends []Backend) error {
	path, err := GlobalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(backends)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Find returns the Backend for lang, or (Backend{}, false) if not found.
// Searches user file first, then falls back to DefaultBackends.
func Find(lang string) (Backend, bool, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	backends, err := Load()
	if err != nil {
		return Backend{}, false, err
	}
	for _, b := range backends {
		if strings.ToLower(b.Lang) == lang {
			return b, true, nil
		}
	}
	for _, b := range defaultBackends {
		if strings.ToLower(b.Lang) == lang {
			return b, true, nil
		}
	}
	return Backend{}, false, nil
}

// Add inserts or replaces a backend by lang. Idempotent.
func Add(b Backend) error {
	b.Lang = strings.ToLower(strings.TrimSpace(b.Lang))
	backends, err := Load()
	if err != nil {
		return err
	}
	for i, existing := range backends {
		if strings.ToLower(existing.Lang) == b.Lang {
			backends[i] = b
			return Save(backends)
		}
	}
	return Save(append(backends, b))
}

// Remove deletes the backend for lang from the user file.
// Returns an error if not found in the user file (built-ins cannot be removed;
// use Add to override them).
func Remove(lang string) error {
	lang = strings.ToLower(strings.TrimSpace(lang))
	backends, err := Load()
	if err != nil {
		return err
	}
	out := backends[:0]
	found := false
	for _, b := range backends {
		if strings.ToLower(b.Lang) == lang {
			found = true
			continue
		}
		out = append(out, b)
	}
	if !found {
		return fmt.Errorf("backend for lang %q not found in user config\n"+
			"(built-in backends cannot be removed; use 'add' to override them)", lang)
	}
	return Save(out)
}

// Reset overwrites the global YAML with the built-in defaults.
func Reset() error {
	return Save(DefaultBackends())
}

// ProjectLang reads the `lang` field from a project's config file
// (moltproject.toml preferred, then pyproject.toml). Returns "python"
// as the default when the field is absent — preserving backward compatibility
// with all existing pyproject.toml-based projects.
func ProjectLang(projectDir string) string {
	for _, name := range []string{"moltproject.toml", "pyproject.toml"} {
		path := filepath.Join(projectDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lang := extractLangField(data)
		if lang != "" {
			return strings.ToLower(strings.TrimSpace(lang))
		}
		// File exists but has no lang field.
		// moltproject.toml with no lang is a non-Python project — default "mixed".
		// pyproject.toml with no lang is a Python project — default "python".
		if name == "pyproject.toml" {
			return "python"
		}
		return "mixed"
	}
	return "python"
}

// extractLangField is a minimal line-scanner for `lang = "..."` in a TOML
// file. We avoid a full TOML parse to keep the dependency surface small.
func extractLangField(data []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	inProject := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[project]" {
			inProject = true
			continue
		}
		if inProject && strings.HasPrefix(line, "[") {
			inProject = false
		}
		if !inProject {
			continue
		}
		if !strings.HasPrefix(line, "lang") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		if val != "" {
			return val
		}
	}
	return ""
}

// ProjectBackend returns the effective Backend for a project, incorporating
// any [tool.molt.backend] inline override from the project's config file.
// The inline override is merged field-by-field so partial overrides work.
func ProjectBackend(projectDir, lang string) (Backend, bool, error) {
	// Load the global/built-in backend first.
	base, ok, err := Find(lang)
	if err != nil {
		return Backend{}, false, err
	}

	// Read inline override from the project config file.
	var inlineRaw []byte
	for _, name := range []string{"moltproject.toml", "pyproject.toml"} {
		if d, e := os.ReadFile(filepath.Join(projectDir, name)); e == nil {
			inlineRaw = d
			break
		}
	}
	if inlineRaw != nil {
		override := extractBackendOverride(inlineRaw)
		if override != (Backend{}) {
			ok = true
			if !ok {
				base = Backend{Lang: lang}
			}
			// Merge: non-empty override fields win.
			if override.Add != "" {
				base.Add = override.Add
			}
			if override.Remove != "" {
				base.Remove = override.Remove
			}
			if override.Sync != "" {
				base.Sync = override.Sync
			}
			if override.List != "" {
				base.List = override.List
			}
			if override.Upgrade != "" {
				base.Upgrade = override.Upgrade
			}
			if override.AddWindows != "" {
				base.AddWindows = override.AddWindows
			}
			if override.SyncWindows != "" {
				base.SyncWindows = override.SyncWindows
			}
			ok = true
		}
	}

	return base, ok, nil
}

// extractBackendOverride parses [tool.molt.backend] from a TOML file.
func extractBackendOverride(data []byte) Backend {
	var b Backend
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	inSection := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[tool.molt.backend]" {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "[") {
			break
		}
		if !inSection || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
		switch key {
		case "add":
			b.Add = val
		case "remove":
			b.Remove = val
		case "sync":
			b.Sync = val
		case "list":
			b.List = val
		case "upgrade":
			b.Upgrade = val
		case "add_windows":
			b.AddWindows = val
		case "sync_windows":
			b.SyncWindows = val
		}
	}
	return b
}
