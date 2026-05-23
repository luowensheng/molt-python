// Package runhandler manages the global registry of per-extension run commands
// stored at ~/.molt/run-handlers.yaml.
//
// A "run handler" is one entry that says "to run a <ext> source file, execute
// this shell-style command after substituting these tokens." molt uses it
// during `molt run file.rb`, `molt run file.ts`, etc. to dispatch to the
// appropriate interpreter or runtime.
//
// Resolution order at run time (highest priority first):
//
//  1. ~/.molt/run-handlers.yaml entry with matching ext
//  2. Built-in default compiled into molt
//
// New languages get added by editing the global YAML or running
// `molt run-handler add`. No molt code change needed.
package runhandler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Handler is one file-extension run recipe.
type Handler struct {
	Ext     string `yaml:"ext"`               // extension without leading dot, e.g. "rb"
	Command string `yaml:"command"`            // default command (unix + fallback)
	Windows string `yaml:"windows,omitempty"` // override on GOOS==windows
	Unix    string `yaml:"unix,omitempty"`    // override on GOOS!=windows (wins over Command)
}

// Resolve picks the right command string for the given platform.
// Priority: Windows field (on windows), Unix field (on non-windows), Command.
func (h Handler) Resolve(goos string) string {
	if goos == "windows" && h.Windows != "" {
		return h.Windows
	}
	if goos != "windows" && h.Unix != "" {
		return h.Unix
	}
	return h.Command
}

// defaultHandlers are the set of recipes seeded into a fresh
// ~/.molt/run-handlers.yaml.
var defaultHandlers = []Handler{
	// Compiled languages: compile-then-run via shell one-liner.
	// {zig} resolves to the auto-installed zig binary; zig cc/c++ work as
	// drop-in replacements for clang/gcc and require no separate toolchain.
	{Ext: "c",
		Unix:    "sh -c \"{zig} cc -O2 -o {dir}/{basename} {file} && {dir}/{basename} {args}\"",
		Windows: "cmd /c \"{zig} cc -O2 -o {dir}\\{basename}.exe {file} && {dir}\\{basename}.exe {args}\"",
	},
	{Ext: "cpp",
		Unix:    "sh -c \"{zig} c++ -O2 -std=c++17 -o {dir}/{basename} {file} && {dir}/{basename} {args}\"",
		Windows: "cmd /c \"{zig} c++ -O2 -std=c++17 -o {dir}\\{basename}.exe {file} && {dir}\\{basename}.exe {args}\"",
	},
	// Zig: zig run compiles and executes a single file; -- separates zig
	// flags from program arguments.
	{Ext: "zig", Command: "{zig} run {file} -- {args}"},
	// Interpreted languages.
	{Ext: "rb", Command: "ruby {file} {args}"},
	{Ext: "js", Command: "node {file} {args}"},
	{Ext: "ts", Command: "npx ts-node {file} {args}", Windows: "npx.cmd ts-node {file} {args}"},
	{Ext: "lua", Command: "lua {file} {args}"},
	{Ext: "sh", Command: "bash {file} {args}"},
	{Ext: "bash", Command: "bash {file} {args}"},
	{Ext: "pl", Command: "perl {file} {args}"},
	{Ext: "r", Command: "Rscript {file} {args}"},
	{Ext: "php", Command: "php {file} {args}"},
	{Ext: "swift", Command: "swift {file} {args}"},
	{Ext: "go", Command: "go run {file} {args}"},
	{Ext: "java", Command: "java {file} {args}"},
	{Ext: "groovy", Command: "groovy {file} {args}"},
	{Ext: "ps1", Unix: "pwsh -File {file} {args}", Windows: "powershell.exe -File {file} {args}"},
	{Ext: "kt", Command: "kotlinc-jvm -script {file} {args}"},
	{Ext: "nim", Command: "nim r {file} {args}"},
	{Ext: "cr", Command: "crystal run {file} {args}"},
	{Ext: "ex", Command: "elixir {file} {args}"},
	{Ext: "exs", Command: "elixir {file} {args}"},
	{Ext: "jl", Command: "julia {file} {args}"},
	{Ext: "hs", Command: "runghc {file} {args}"},
	{Ext: "clj", Command: "clojure {file} {args}"},
	{Ext: "dart", Command: "dart run {file} {args}"},
	{Ext: "v", Command: "v run {file} {args}"},
	{Ext: "odin", Command: "odin run {file} {args}"},
}

// SuggestedTemplates is the curated set used by `run-handler templates`.
// Same as defaultHandlers but exported as a map for easy lookup.
var SuggestedTemplates = func() map[string]string {
	m := make(map[string]string, len(defaultHandlers))
	for _, h := range defaultHandlers {
		cmd := h.Command
		if cmd == "" {
			cmd = h.Unix
		}
		m[h.Ext] = cmd
	}
	return m
}()

// DefaultHandlers returns a copy of the seeded set. Used by reset and merge.
func DefaultHandlers() []Handler {
	out := make([]Handler, len(defaultHandlers))
	copy(out, defaultHandlers)
	return out
}

// GlobalPath returns the path to ~/.molt/run-handlers.yaml.
func GlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "run-handlers.yaml"), nil
}

// Load reads ~/.molt/run-handlers.yaml. If the file is missing it is
// seeded with the built-in defaults and saved.
func Load() ([]Handler, error) {
	path, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := Save(DefaultHandlers()); err != nil {
			return nil, err
		}
		return DefaultHandlers(), nil
	}
	if err != nil {
		return nil, err
	}
	var handlers []Handler
	if err := yaml.Unmarshal(data, &handlers); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return handlers, nil
}

// Save writes the handler list to ~/.molt/run-handlers.yaml.
func Save(handlers []Handler) error {
	path, err := GlobalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(handlers)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Find returns the Handler for ext (without the leading dot), or
// (Handler{}, false) if not found. Searches user file first, then falls back
// to DefaultHandlers.
func Find(ext string) (Handler, bool, error) {
	ext = strings.TrimPrefix(ext, ".")
	handlers, err := Load()
	if err != nil {
		return Handler{}, false, err
	}
	for _, h := range handlers {
		if h.Ext == ext {
			return h, true, nil
		}
	}
	// Fall back to built-in defaults (may not be in user file if it was
	// seeded with an older version of molt).
	for _, h := range defaultHandlers {
		if h.Ext == ext {
			return h, true, nil
		}
	}
	return Handler{}, false, nil
}

// Add inserts or replaces a handler by ext. Idempotent.
func Add(h Handler) error {
	h.Ext = strings.TrimPrefix(h.Ext, ".")
	handlers, err := Load()
	if err != nil {
		return err
	}
	for i, existing := range handlers {
		if existing.Ext == h.Ext {
			handlers[i] = h
			return Save(handlers)
		}
	}
	return Save(append(handlers, h))
}

// Remove deletes the handler for ext. Returns an error if not found.
func Remove(ext string) error {
	ext = strings.TrimPrefix(ext, ".")
	handlers, err := Load()
	if err != nil {
		return err
	}
	out := handlers[:0]
	found := false
	for _, h := range handlers {
		if h.Ext == ext {
			found = true
			continue
		}
		out = append(out, h)
	}
	if !found {
		return fmt.Errorf("handler for .%s not found in user config\n"+
			"(built-in handlers cannot be removed; use 'add' to override them)", ext)
	}
	return Save(out)
}

// Reset overwrites the global YAML with the seeded defaults.
func Reset() error {
	return Save(DefaultHandlers())
}

// Resolve substitutes tokens in command and returns the final shell
// invocation. {file}, {dir}, and {basename} are derived from filePath;
// everything else comes from the tokens map. Unknown tokens pass through
// unchanged.
func Resolve(command, filePath string, tokens map[string]string) string {
	abs := filePath
	if !filepath.IsAbs(abs) {
		if cwd, err := os.Getwd(); err == nil {
			abs = filepath.Join(cwd, abs)
		}
	}

	out := command
	// File-derived tokens.
	out = strings.ReplaceAll(out, "{file}", abs)
	out = strings.ReplaceAll(out, "{dir}", filepath.Dir(abs))
	base := filepath.Base(abs)
	ext := filepath.Ext(base)
	basename := strings.TrimSuffix(base, ext)
	out = strings.ReplaceAll(out, "{basename}", basename)

	// Caller-supplied tokens.
	for k, v := range tokens {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// SplitCommand splits a resolved command string into argv tokens,
// honouring "double-quoted" segments. Sufficient for run-handler commands;
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
