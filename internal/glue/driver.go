// Package glue implements the Transport Glue build pipeline.
//
// Transport Glue lets Python call Go, Rust, or any compiled language's
// functions without writing C/FFI boilerplate. Declare the functions you need
// in [[tool.molt.glue]] stanzas in moltproject.toml; `molt sync` auto-generates:
//
//  1. A server binary that speaks JSON-RPC over the chosen transport
//     (stdio, unix_socket, or tcp).
//  2. A Python module that starts the server lazily and provides typed
//     wrapper functions that call through to the compiled code.
//
// The driver registry (~/.molt/glue-drivers.yaml) mirrors pkg-backends and
// run-handlers: built-in drivers for Go and Rust are compiled into molt; users
// can add drivers for Nim, Zig, Crystal, or any binary-producing language.
package glue

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// GlueDriver describes how to compile and serve a glue module for a specific
// language. Parallel to pkgbackend.Backend and kernelbuilder.Builder.
type GlueDriver struct {
	// Lang is the language identifier, e.g. "go", "rust", "nim".
	Lang string `yaml:"lang"`

	// BuildCmd is the shell command to compile the generated server source into
	// a binary. Tokens: {server_dir} {server_file} {cargo_toml} {release_bin}
	// {output} {import_path} {module}.
	BuildCmd string `yaml:"build_cmd"`

	// LibSetupCmd is an optional command run before BuildCmd when the glue
	// module's src is a library import path (not a local file). E.g. for Go
	// third-party modules this runs `go get <import_path>`.
	LibSetupCmd string `yaml:"lib_setup_cmd,omitempty"`

	// ServerTemplate is the path to a text/template file for the server source.
	// Empty string means use the built-in template for this language.
	ServerTemplate string `yaml:"server_template,omitempty"`

	// ClientTemplate is the path to a text/template file for the Python client.
	// Empty string means use the built-in Python client template (recommended).
	ClientTemplate string `yaml:"client_template,omitempty"`
}

// defaultDrivers are the built-in drivers seeded into a fresh
// ~/.molt/glue-drivers.yaml on first use.
var defaultDrivers = []GlueDriver{
	{
		Lang:        "go",
		BuildCmd:    "go build -o {output} {server_dir}",
		LibSetupCmd: "go get {import_path}",
	},
	{
		Lang:     "rust",
		BuildCmd: "cargo build --release --manifest-path {cargo_toml} && cp {release_bin} {output}",
	},
}

// DefaultDrivers returns a copy of the built-in set.
func DefaultDrivers() []GlueDriver {
	out := make([]GlueDriver, len(defaultDrivers))
	copy(out, defaultDrivers)
	return out
}

// GlobalPath returns the path to ~/.molt/glue-drivers.yaml.
func GlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "glue-drivers.yaml"), nil
}

// Load reads ~/.molt/glue-drivers.yaml. If the file is missing it is seeded
// with the built-in defaults and saved.
func Load() ([]GlueDriver, error) {
	path, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := Save(DefaultDrivers()); err != nil {
			return nil, err
		}
		return DefaultDrivers(), nil
	}
	if err != nil {
		return nil, err
	}
	var drivers []GlueDriver
	if err := yaml.Unmarshal(data, &drivers); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return drivers, nil
}

// Save writes the driver list to ~/.molt/glue-drivers.yaml.
func Save(drivers []GlueDriver) error {
	path, err := GlobalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(drivers)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Find returns the GlueDriver for lang. Searches the user file first, then
// falls back to DefaultDrivers. Returns (GlueDriver{}, false, nil) when not
// found.
func Find(lang string) (GlueDriver, bool, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	drivers, err := Load()
	if err != nil {
		return GlueDriver{}, false, err
	}
	for _, d := range drivers {
		if strings.ToLower(d.Lang) == lang {
			return d, true, nil
		}
	}
	for _, d := range defaultDrivers {
		if strings.ToLower(d.Lang) == lang {
			return d, true, nil
		}
	}
	return GlueDriver{}, false, nil
}

// Add inserts or replaces a driver by lang. Idempotent.
func Add(d GlueDriver) error {
	d.Lang = strings.ToLower(strings.TrimSpace(d.Lang))
	drivers, err := Load()
	if err != nil {
		return err
	}
	for i, existing := range drivers {
		if strings.ToLower(existing.Lang) == d.Lang {
			drivers[i] = d
			return Save(drivers)
		}
	}
	return Save(append(drivers, d))
}

// Remove deletes the driver for lang from the user file.
// Built-in drivers (go, rust) cannot be removed; use Add to override them.
func Remove(lang string) error {
	lang = strings.ToLower(strings.TrimSpace(lang))
	// Refuse to remove built-in drivers.
	for _, d := range defaultDrivers {
		if strings.ToLower(d.Lang) == lang {
			return fmt.Errorf("built-in driver for lang %q cannot be removed\n"+
				"(use 'molt glue-driver add %s ...' to override it)", lang, lang)
		}
	}
	drivers, err := Load()
	if err != nil {
		return err
	}
	out := drivers[:0]
	found := false
	for _, d := range drivers {
		if strings.ToLower(d.Lang) == lang {
			found = true
			continue
		}
		out = append(out, d)
	}
	if !found {
		return fmt.Errorf("driver for lang %q not found in user config", lang)
	}
	return Save(out)
}

// Reset overwrites the global YAML with the built-in defaults.
func Reset() error {
	return Save(DefaultDrivers())
}

// ExpandTokens substitutes {tokens} in a command template.
// Recognised tokens: {server_dir} {server_file} {cargo_toml} {release_bin}
// {output} {import_path} {module}.
func ExpandTokens(cmd string, tokens map[string]string) string {
	out := cmd
	for k, v := range tokens {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}
