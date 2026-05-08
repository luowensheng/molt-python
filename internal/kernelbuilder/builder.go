// Package kernelbuilder manages the global registry of per-extension build
// commands for kernel modules, stored at ~/.molt/kernel-builders.yaml.
//
// A "kernel builder" is one entry that says "to compile a <ext> source
// into a position-independent .o, run this shell-style command after
// substituting these tokens." molt uses it during BuildKernelModule:
// finds the right builder for the source's extension, substitutes, runs.
//
// Resolution order at build time (highest priority first):
//
//   1. [tool.molt.native_kernel.build.<ext>] in the project's pyproject.toml
//   2. ~/.molt/kernel-builders.yaml entry with matching ext
//   3. Built-in default compiled into molt (zig / c / cpp only)
//
// New languages — Odin, Nim, Fortran, Rust-without-PyO3, anything with a
// C ABI — get added by editing the global YAML or running
// `molt kernel-builder add`. No molt code change needed.
package kernelbuilder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Builder is one per-extension build recipe.
type Builder struct {
	// Ext is the source file extension *without* the leading dot.
	// E.g. "zig", "odin", "c", "cpp".
	Ext string `yaml:"ext"`

	// Command is a shell command template. Tokens get substituted at
	// build time. Recognised tokens:
	//
	//   {source}      absolute path to the user's source file
	//   {output}      absolute path molt expects the .o at
	//   {zig}         absolute path to the auto-installed zig binary
	//   {cc}, {cxx}   resolved system C / C++ compilers
	//   {include_dir} Python include directory
	//
	// Unknown tokens are left untouched (the shell will see them and
	// usually fail loudly, which is the right error message).
	Command string `yaml:"command"`
}

// GlobalPath returns the path to ~/.molt/kernel-builders.yaml.
func GlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "kernel-builders.yaml"), nil
}

// defaultBuilders are the set of recipes seeded into a fresh
// ~/.molt/kernel-builders.yaml. Match the hardcoded behaviour we shipped
// in the kernel MVP, so an upgrade is behaviour-preserving.
var defaultBuilders = []Builder{
	{
		Ext:     "zig",
		Command: "{zig} build-obj {source} -O ReleaseFast -fPIC -femit-bin={output}",
	},
	{
		Ext:     "c",
		Command: "{cc} -c -O2 -fPIC -o {output} {source}",
	},
	{
		Ext:     "cpp",
		Command: "{cxx} -c -O2 -fPIC -o {output} {source}",
	},
}

// SuggestedTemplates is the curated set used by `kernel-builder add
// --from-template`. Languages with widely-agreed-on flags get a starting
// point so users don't have to look them up. Layered above the defaults
// (which are also exposed here) for completeness.
var SuggestedTemplates = map[string]string{
	"zig":  "{zig} build-obj {source} -O ReleaseFast -fPIC -femit-bin={output}",
	"c":    "{cc} -c -O2 -fPIC -o {output} {source}",
	"cpp":  "{cxx} -c -O2 -fPIC -o {output} {source}",
	"odin": "odin build {source} -file -build-mode:obj -reloc-mode:pic -o:speed -out:{output}",
	"nim":  "nim c --app:staticlib --noMain --noLinking --out:{output} {source}",
	"rs":   "rustc --crate-type=staticlib --edition=2021 -O --emit=obj -o {output} {source}",
	"f90":  "gfortran -c -fPIC -O2 -o {output} {source}",
	"f95":  "gfortran -c -fPIC -O2 -o {output} {source}",
}

// DefaultBuilders returns a copy of the seeded set. Used by `reset`.
func DefaultBuilders() []Builder {
	out := make([]Builder, len(defaultBuilders))
	copy(out, defaultBuilders)
	return out
}

// Load reads ~/.molt/kernel-builders.yaml. If the file is missing it is
// seeded with the built-in defaults and saved.
func Load() ([]Builder, error) {
	path, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := Save(DefaultBuilders()); err != nil {
			return nil, err
		}
		return DefaultBuilders(), nil
	}
	if err != nil {
		return nil, err
	}
	var builders []Builder
	if err := yaml.Unmarshal(data, &builders); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return builders, nil
}

// Save writes the builder list to ~/.molt/kernel-builders.yaml.
func Save(builders []Builder) error {
	path, err := GlobalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(builders)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Find returns the global Builder for ext (without the leading dot), or
// (Builder{}, false) if not found. Doesn't consult per-project overrides
// or built-in defaults — caller layers those.
func Find(ext string) (Builder, bool, error) {
	ext = strings.TrimPrefix(ext, ".")
	builders, err := Load()
	if err != nil {
		return Builder{}, false, err
	}
	for _, b := range builders {
		if b.Ext == ext {
			return b, true, nil
		}
	}
	return Builder{}, false, nil
}

// Add inserts or replaces a builder by ext. Idempotent.
func Add(b Builder) error {
	b.Ext = strings.TrimPrefix(b.Ext, ".")
	builders, err := Load()
	if err != nil {
		return err
	}
	for i, existing := range builders {
		if existing.Ext == b.Ext {
			builders[i] = b
			return Save(builders)
		}
	}
	return Save(append(builders, b))
}

// Remove deletes the builder for ext. Returns an error if not found.
func Remove(ext string) error {
	ext = strings.TrimPrefix(ext, ".")
	builders, err := Load()
	if err != nil {
		return err
	}
	out := builders[:0]
	found := false
	for _, b := range builders {
		if b.Ext == ext {
			found = true
			continue
		}
		out = append(out, b)
	}
	if !found {
		return fmt.Errorf("builder for .%s not found", ext)
	}
	return Save(out)
}

// Reset overwrites the global YAML with the seeded defaults.
func Reset() error {
	return Save(DefaultBuilders())
}

// Resolve substitutes tokens in command and returns the final shell
// invocation. Unknown tokens pass through unchanged.
func Resolve(command string, tokens map[string]string) string {
	out := command
	for k, v := range tokens {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// SplitCommand splits a resolved command string into argv tokens,
// honouring "double-quoted" segments. Sufficient for our build commands;
// not a full POSIX shell parser (no $vars, no backticks, no escapes).
func SplitCommand(cmd string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (c == ' ' || c == '\t' || c == '\n'):
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}
