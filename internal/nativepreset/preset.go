// Package nativepreset manages the global registry of native-build presets
// stored at ~/.molt/native-presets.yaml. Presets define default build
// commands and output templates for languages like Rust, C, and C++.
// Projects reference them via preset = "rust" in [[tool.molt.native]].
package nativepreset

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Preset is one language build recipe.
type Preset struct {
	// Name is the identifier used in [[tool.molt.native]] preset = "<name>".
	Name string `yaml:"name"`

	// SrcPatterns are glob patterns (relative to the module's src dir) used
	// to hash source files for change detection.
	SrcPatterns []string `yaml:"src_patterns"`

	// Build is a shell command template. Tokens: {module}, {ext},
	// {python_include}, {src_files}, {output}.
	Build string `yaml:"build"`

	// Output is a path template for the produced shared library, relative
	// to the module's src dir. Same tokens as Build.
	Output string `yaml:"output"`
}

// GlobalPresetsPath returns the path to ~/.molt/native-presets.yaml.
func GlobalPresetsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "native-presets.yaml"), nil
}

var defaultPresets = []Preset{
	{
		Name:        "rust",
		SrcPatterns: []string{"**/*.rs", "Cargo.toml", "Cargo.lock"},
		Build:       "cargo build --release",
		Output:      "target/release/lib{module}.{ext}",
	},
	{
		Name:        "c",
		SrcPatterns: []string{"**/*.c", "**/*.h"},
		Build:       "cc -O2 -shared -fPIC -I {python_include} -o {output} {src_files}",
		Output:      "{module}.{ext}",
	},
	{
		Name:        "cpp",
		SrcPatterns: []string{"**/*.cpp", "**/*.hpp", "**/*.h"},
		Build:       "c++ -O2 -shared -fPIC -I {python_include} -o {output} {src_files}",
		Output:      "{module}.{ext}",
	},
}

// Load reads ~/.molt/native-presets.yaml. If the file is missing it is seeded
// with the built-in rust/c/cpp presets and saved.
func Load() ([]Preset, error) {
	path, err := GlobalPresetsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := Save(defaultPresets); err != nil {
			return nil, err
		}
		return defaultPresets, nil
	}
	if err != nil {
		return nil, err
	}
	var presets []Preset
	if err := yaml.Unmarshal(data, &presets); err != nil {
		return nil, fmt.Errorf("parse native-presets.yaml: %w", err)
	}
	return presets, nil
}

// Save writes presets to ~/.molt/native-presets.yaml.
func Save(presets []Preset) error {
	path, err := GlobalPresetsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(presets)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Find returns the preset with the given name, or an error if not found.
func Find(name string) (*Preset, error) {
	presets, err := Load()
	if err != nil {
		return nil, err
	}
	for _, p := range presets {
		if p.Name == name {
			cp := p
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("preset %q not found — run `molt native-preset list` to see available presets", name)
}

// Add inserts or replaces a preset by name.
func Add(p Preset) error {
	presets, err := Load()
	if err != nil {
		return err
	}
	for i, existing := range presets {
		if existing.Name == p.Name {
			presets[i] = p
			return Save(presets)
		}
	}
	return Save(append(presets, p))
}

// Remove deletes the preset with the given name.
func Remove(name string) error {
	presets, err := Load()
	if err != nil {
		return err
	}
	out := presets[:0]
	found := false
	for _, p := range presets {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("preset %q not found", name)
	}
	return Save(out)
}
