// Package moltcfg loads, validates, and applies defaults to molt.yaml.
//
// Loading philosophy:
//   - If molt.yaml exists → it's the source of truth. pyproject.toml is
//     consulted only for Python dep resolution when deps.strategy=pyproject.
//   - If molt.yaml does NOT exist → Synthesize() builds a best-effort config
//     from pyproject.toml, preserving backward-compat with old molt behaviour.
//
// Validation is strict: unknown keys are rejected, mutually-exclusive fields
// are enforced, required-but-empty fields produce clear errors. Silent
// acceptance of typos would defeat the "explicit over magical" goal.
package moltcfg

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"molt/pkg/types"
)

const (
	ConfigFilename    = "molt.yaml"
	ConfigFilenameAlt = "molt.yml"
	SchemaVersion     = 1

	// Filename is an alias for ConfigFilename used by callers (adopt, etc.).
	Filename = ConfigFilename
)

var validStrategies = map[string]bool{
	"requirements": true,
	"pyproject":    true,
	"poetry":       true,
	"pipenv":       true,
	"none":         true,
}

var validAlgorithms = map[string]bool{
	"sha256": true,
	"sha512": true,
}

// Find returns the path to molt.yaml in projectDir, or "" if not present.
// Both .yaml and .yml are accepted.
func Find(projectDir string) string {
	for _, name := range []string{ConfigFilename, ConfigFilenameAlt} {
		p := filepath.Join(projectDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Exists reports whether a molt.yaml is present in projectDir.
func Exists(projectDir string) bool { return Find(projectDir) != "" }

// Load reads molt.yaml from projectDir, parses it, validates it, and fills
// in defaults. Returns (nil, nil) if no molt.yaml exists — callers should
// then decide whether to synthesize from pyproject.toml.
func Load(projectDir string) (*types.MoltConfig, error) {
	path := Find(projectDir)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	cfg.SourcePath = path

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	applyDefaults(cfg)
	return cfg, nil
}

// parse deserializes YAML with strict unknown-key checking. That catches
// typos like `projects:` vs `project:` which would otherwise silently
// disappear into a nil struct field.
func parse(data []byte) (*types.MoltConfig, error) {
	// First pass: pull out `commands.default:` if the user wrote it as a
	// shorthand string (e.g. `default: web`). We can't let the strict
	// decoder see that, because it would try to unmarshal a string into a
	// MoltCommand struct and fail.
	defaultName, err := extractDefaultShorthand(data)
	if err != nil {
		return nil, err
	}
	if defaultName != "" {
		data = stripDefaultKey(data)
	}

	var cfg types.MoltConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	cfg.DefaultCommand = defaultName
	return &cfg, nil
}

// extractDefaultShorthand returns the value of `commands.default` if it's a
// scalar string. Returns "" if commands.default is absent or is a mapping
// (i.e. a full command definition, which validate() will reject).
func extractDefaultShorthand(data []byte) (string, error) {
	var tree map[string]any
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return "", err
	}
	cmds, ok := tree["commands"].(map[string]any)
	if !ok {
		return "", nil
	}
	def, ok := cmds["default"]
	if !ok {
		return "", nil
	}
	if s, ok := def.(string); ok {
		return strings.TrimSpace(s), nil
	}
	return "", nil
}

// stripDefaultKey removes `  default: <value>` scalar lines from commands
// so the strict decoder doesn't reject them. Operates line-wise because we
// need to re-emit otherwise-identical YAML. Only touches a line if it's a
// scalar value (no `:` continuation) inside a commands: block.
func stripDefaultKey(data []byte) []byte {
	var out bytes.Buffer
	inCommands := false
	commandsIndent := -1

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)

		// Detect entering/leaving the commands: block.
		if !inCommands {
			if strings.HasPrefix(trimmed, "commands:") {
				inCommands = true
				commandsIndent = indent
			}
		} else {
			// We leave the commands block when we see a line at or below the
			// commands: indent that isn't a comment/blank.
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && indent <= commandsIndent {
				inCommands = false
			}
		}

		// Strip only an exact `default: <scalar>` line inside commands.
		if inCommands && strings.HasPrefix(trimmed, "default:") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "default:"))
			// Scalar value with no trailing colon/mapping — skip this line.
			if rest != "" && !strings.HasSuffix(rest, ":") {
				continue
			}
		}

		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// validate enforces schema rules beyond what yaml unmarshalling catches.
// All problems are collected before returning so users see every error at
// once instead of fixing-then-rediscovering in a loop.
func validate(cfg *types.MoltConfig) error {
	var problems []string

	if cfg.Version == 0 {
		problems = append(problems, "missing `version:` (expected: 1)")
	} else if cfg.Version != SchemaVersion {
		problems = append(problems, fmt.Sprintf(
			"unsupported version %d (this molt supports version %d)",
			cfg.Version, SchemaVersion))
	}

	if strings.TrimSpace(cfg.Project.Name) == "" {
		problems = append(problems, "project.name is required")
	} else if !isValidProjectName(cfg.Project.Name) {
		problems = append(problems, fmt.Sprintf(
			"project.name %q contains invalid characters (use letters, digits, -, _, .)",
			cfg.Project.Name))
	}
	if strings.TrimSpace(cfg.Project.Version) == "" {
		problems = append(problems, "project.version is required")
	}

	if cfg.Deps != nil {
		if cfg.Deps.Strategy == "" {
			problems = append(problems, "deps.strategy is required when deps block is present")
		} else if !validStrategies[string(cfg.Deps.Strategy)] {
			problems = append(problems, fmt.Sprintf(
				"deps.strategy %q is invalid (expected one of: requirements, pyproject, poetry, pipenv, none)",
				cfg.Deps.Strategy))
		}
		if cfg.Deps.Strategy == types.DepsStrategyRequirements && len(cfg.Deps.Files) == 0 {
			problems = append(problems,
				"deps.files is required when deps.strategy is 'requirements'")
		}
	}

	for name, cmd := range cfg.Commands {
		if !isValidCommandName(name) {
			problems = append(problems, fmt.Sprintf(
				"commands.%s: invalid command name (use letters, digits, -, _)", name))
			continue
		}
		if len(cmd.Exec) > 0 && cmd.Script != "" {
			problems = append(problems, fmt.Sprintf(
				"commands.%s: `exec` and `script` are mutually exclusive", name))
		}
		if len(cmd.Exec) == 0 && cmd.Script == "" {
			problems = append(problems, fmt.Sprintf(
				"commands.%s: must define either `exec` or `script`", name))
		}
	}

	if cfg.DefaultCommand != "" {
		if _, ok := cfg.Commands[cfg.DefaultCommand]; !ok {
			problems = append(problems, fmt.Sprintf(
				"commands.default refers to %q but no such command is defined",
				cfg.DefaultCommand))
		}
	}

	if cfg.Integrity != nil && cfg.Integrity.Algorithm != "" &&
		!validAlgorithms[cfg.Integrity.Algorithm] {
		problems = append(problems, fmt.Sprintf(
			"integrity.algorithm %q is invalid (expected: sha256, sha512)",
			cfg.Integrity.Algorithm))
	}

	if cfg.Assets != nil {
		for i, a := range cfg.Assets.Files {
			if strings.TrimSpace(a.Path) == "" {
				problems = append(problems, fmt.Sprintf(
					"assets.files[%d].path is required", i))
				continue
			}
			if filepath.IsAbs(a.Path) {
				problems = append(problems, fmt.Sprintf(
					"assets.files[%d].path %q: absolute paths not allowed (must be project-relative)",
					i, a.Path))
			}
			if containsTraversal(a.Path) {
				problems = append(problems, fmt.Sprintf(
					"assets.files[%d].path %q: .. not allowed (must stay within project)",
					i, a.Path))
			}
		}
	}

	for i, g := range cfg.Include {
		if filepath.IsAbs(g) {
			problems = append(problems, fmt.Sprintf("include[%d] %q: absolute paths not allowed", i, g))
		}
		if containsTraversal(g) {
			problems = append(problems, fmt.Sprintf("include[%d] %q: .. not allowed", i, g))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// applyDefaults fills in sensible defaults after validation has passed.
// Defaults are chosen to make the safest behaviour the default:
//   - Integrity enabled with install-time check (free authentication)
//   - Asset size cap at 100MB (catch accidental gigabyte embeds)
//   - Deps default to pyproject for back-compat
func applyDefaults(cfg *types.MoltConfig) {
	trueBool := true
	if cfg.Integrity == nil {
		cfg.Integrity = &types.MoltIntegrity{
			Enabled:         &trueBool,
			Algorithm:       "sha256",
			VerifyOnInstall: true,
			VerifyOnLaunch:  false,
		}
	} else {
		if cfg.Integrity.Enabled == nil {
			cfg.Integrity.Enabled = &trueBool
		}
		if cfg.Integrity.Algorithm == "" {
			cfg.Integrity.Algorithm = "sha256"
		}
	}
	if cfg.Integrity.IntegrityEnabled() && cfg.Integrity.Output == "" {
		cfg.Integrity.Output = fmt.Sprintf("%s-v%s.manifest.json",
			cfg.Project.Name, cfg.Project.Version)
	}

	if cfg.Assets != nil && cfg.Assets.MaxFileSizeMB == 0 {
		cfg.Assets.MaxFileSizeMB = 100
	}

	if cfg.Deps == nil {
		cfg.Deps = &types.MoltDeps{Strategy: types.DepsStrategyPyProject}
	}
}

// ResolveOutputName returns the filename template resolved with app name
// and version substituted. Honors user-set `integrity.output` if present.
func ResolveOutputName(cfg *types.MoltConfig) string {
	if cfg == nil || cfg.Integrity == nil || cfg.Integrity.Output == "" {
		name := "app"
		ver := "0.0.0"
		if cfg != nil {
			if cfg.Project.Name != "" {
				name = cfg.Project.Name
			}
			if cfg.Project.Version != "" {
				ver = cfg.Project.Version
			}
		}
		return fmt.Sprintf("%s-v%s.manifest.json", name, ver)
	}
	out := cfg.Integrity.Output
	out = strings.ReplaceAll(out, "{name}", cfg.Project.Name)
	out = strings.ReplaceAll(out, "{version}", cfg.Project.Version)
	return out
}

// Synthesize builds a MoltConfig from pyproject.toml when molt.yaml is
// absent. Parses only what we need — name, version, python — deliberately
// minimal, mirroring the existing codebase's TOML-scanning style.
func Synthesize(projectDir string) (*types.MoltConfig, error) {
	name, version := readProjectMeta(projectDir)
	if name == "" {
		name = filepath.Base(projectDir)
	}
	if version == "" {
		version = "0.1.0"
	}

	python := ""
	if data, err := os.ReadFile(filepath.Join(projectDir, ".python-version")); err == nil {
		python = strings.TrimSpace(string(data))
	}

	cfg := &types.MoltConfig{
		Version: SchemaVersion,
		Project: types.MoltProject{
			Name:    name,
			Version: version,
			Python:  python,
		},
		Deps: &types.MoltDeps{Strategy: types.DepsStrategyPyProject},
	}
	applyDefaults(cfg)
	return cfg, nil
}

// LoadOrSynthesize is the one-stop entry point for callers that don't care
// whether molt.yaml exists.
func LoadOrSynthesize(projectDir string) (*types.MoltConfig, error) {
	cfg, err := Load(projectDir)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	return Synthesize(projectDir)
}

// ResolveCommand picks the command a bare `molt run` should execute.
// Precedence:
//   1. cfg.DefaultCommand (explicit `commands.default: <n>`)
//   2. command literally named "run"
//   3. command literally named "start"
//   4. if exactly one command is defined, that one
//   5. "" — caller falls back to legacy `-m <pkg>.main` behaviour
func ResolveCommand(cfg *types.MoltConfig) string {
	if cfg == nil || len(cfg.Commands) == 0 {
		return ""
	}
	if cfg.DefaultCommand != "" {
		if _, ok := cfg.Commands[cfg.DefaultCommand]; ok {
			return cfg.DefaultCommand
		}
	}
	for _, try := range []string{"run", "start"} {
		if _, ok := cfg.Commands[try]; ok {
			return try
		}
	}
	if len(cfg.Commands) == 1 {
		for name := range cfg.Commands {
			return name
		}
	}
	return ""
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func isValidProjectName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}

func isValidCommandName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// containsTraversal checks for `..` components in a path. Glob-safe — we
// split on both `/` and `\` and look for the exact segment "..", not just
// substring presence, so paths like `../foo` trigger but `foo..bar` doesn't.
func containsTraversal(p string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

// readProjectMeta reads name and version from [project] in pyproject.toml.
// Deliberately minimal — scans under a [project] table, mirroring the
// existing codebase's hand-rolled TOML style.
func readProjectMeta(projectDir string) (name, version string) {
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return "", ""
	}
	inProject := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inProject = line == "[project]"
			continue
		}
		if !inProject {
			continue
		}
		if name == "" && strings.HasPrefix(line, "name") {
			name = extractTOMLString(line)
		}
		if version == "" && strings.HasPrefix(line, "version") {
			version = extractTOMLString(line)
		}
	}
	return name, version
}

func extractTOMLString(line string) string {
	idx := strings.Index(line, "=")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[idx+1:])
	rest = strings.TrimSpace(strings.SplitN(rest, "#", 2)[0])
	return strings.Trim(rest, `"'`)
}
