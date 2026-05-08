// Package globalenv manages the global env-var registry stored at
// ~/.molt/env.yaml. Variables defined there are exported into the
// environment of every process molt spawns (Python interpreters,
// task runners, builds, etc.) — across every project on the machine.
//
// Layered into syspath.Spec.BuildEnv with this priority order
// (highest wins):
//
//   1. project's [tool.molt.runtime.env] in pyproject.toml
//   2. ~/.molt/env.yaml
//   3. parent process environment (whatever was already set)
//
// Critical molt-managed variables (PYTHONPATH, VIRTUAL_ENV, PYTHONHOME)
// are NOT overrideable — molt always controls those to keep the syspath
// hermetic. Anything else is fair game.
//
// Use cases: `HTTPS_PROXY` for corporate networks, `PIP_INDEX_URL` for
// internal mirrors, language locale, `EDITOR`/`PAGER`, debug flags.
// Per-project overrides in pyproject.toml handle project-specific
// `DATABASE_URL`, `LOG_LEVEL`, etc.
package globalenv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// GlobalPath returns the path to ~/.molt/env.yaml.
func GlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "env.yaml"), nil
}

// Reserved variable names that molt always manages. Setting these in
// either the global file or a project's [tool.molt.runtime.env] is a
// no-op — molt keeps control to prevent accidental venv leakage.
var reserved = map[string]bool{
	"PYTHONPATH":  true,
	"VIRTUAL_ENV": true,
	"PYTHONHOME":  true,
}

// IsReserved reports whether a variable name is molt-managed and
// therefore can't be overridden by env config.
func IsReserved(name string) bool { return reserved[name] }

// Load reads ~/.molt/env.yaml and returns the parsed map. Missing file
// returns an empty map (no error). Reserved names are silently dropped.
func Load() (map[string]string, error) {
	path, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for k := range out {
		if reserved[k] {
			delete(out, k)
		}
	}
	return out, nil
}

// Save writes vars to ~/.molt/env.yaml with deterministic ordering and
// 0600 permissions (the file may contain secrets — tokens, credentials).
func Save(vars map[string]string) error {
	path, err := GlobalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// Sort for deterministic output (yaml.v3 doesn't sort map keys).
	keys := make([]string, 0, len(vars))
	for k := range vars {
		if !reserved[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// Build a yaml.Node so output is alphabetised + cleanly formatted.
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Value: vars[k]},
		)
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}

	// Write to a temp file then rename — atomic + preserves perms.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Set inserts or updates a single variable. Reserved names error out
// rather than failing silently — users get a clear "you can't do that".
func Set(name, value string) error {
	if reserved[name] {
		return fmt.Errorf("%s is molt-managed and can't be set in env config", name)
	}
	vars, err := Load()
	if err != nil {
		return err
	}
	vars[name] = value
	return Save(vars)
}

// Unset removes a variable. Returns an error if it wasn't set.
func Unset(name string) error {
	vars, err := Load()
	if err != nil {
		return err
	}
	if _, ok := vars[name]; !ok {
		return fmt.Errorf("%s not set in global env", name)
	}
	delete(vars, name)
	return Save(vars)
}

// Get returns one variable's value (and whether it was set).
func Get(name string) (string, bool, error) {
	vars, err := Load()
	if err != nil {
		return "", false, err
	}
	v, ok := vars[name]
	return v, ok, nil
}
