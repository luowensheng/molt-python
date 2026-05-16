// Package tooldb manages molt's global tool registry — projects whose
// CLI has been installed system-wide via `molt tool install`. Each tool
// is a thin POSIX shim that delegates back to molt with `--project` so
// the project's current syspath (deps, native code, etc.) is always
// reflected at invocation time.
//
// Distinct from `molt build`: build produces a frozen, self-contained
// binary; tool install produces a live, dev-mode shim.
//
// Layout:
//
//	~/.molt/tools/<name>.json   # {project_dir, task, created, updated}
//	~/.molt/bin/<name>          # generated shell shim, on user's PATH
package tooldb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

// Tool is the on-disk record for one registered tool.
type Tool struct {
	Name       string    `json:"name"`
	ProjectDir string    `json:"project_dir"`      // absolute path of the source project
	Task       string    `json:"task"`             // task name to run (empty → default entry)
	Env        string    `json:"env,omitempty"`    // named env (or project query) for the shim
	Script     string    `json:"script,omitempty"` // absolute path to .py for env-only tools
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// Root returns ~/.molt/tools (json metadata files).
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "tools"), nil
}

// BinDir returns ~/.molt/bin — the directory the user adds to PATH so
// installed tool shims become executable from anywhere.
func BinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "bin"), nil
}

// metaPath returns the JSON file for the named tool.
func metaPath(name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name+".json"), nil
}

// shimPath returns the executable shim path for the named tool.
func shimPath(name string) (string, error) {
	bin, err := BinDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(bin, name+".cmd"), nil
	}
	return filepath.Join(bin, name), nil
}

// Install registers a new project-based tool. Errors if a tool by that name
// already exists unless force is true. Writes both the JSON record and the shim.
// envName is optional: when non-empty the shim will use `--env <envName>` so
// the tool runs in that named environment instead of the project's own env.
func Install(name, projectDir, task, envName string, force bool) (*Tool, error) {
	if name == "" {
		return nil, fmt.Errorf("tool name required")
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(abs, "pyproject.toml")); err != nil {
		return nil, fmt.Errorf("no pyproject.toml at %s — not a molt project", abs)
	}

	mp, err := metaPath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(mp); err == nil && !force {
		return nil, fmt.Errorf("tool %q already exists; pass --force to overwrite", name)
	}

	now := time.Now().UTC()
	t := &Tool{
		Name:       name,
		ProjectDir: abs,
		Task:       task,
		Env:        envName,
		Created:    now,
		Updated:    now,
	}
	if existing, err := load(mp); err == nil {
		t.Created = existing.Created
	}
	return save(t, mp)
}

// InstallScript registers a new env-bound script tool — no project required.
// The shim will exec `molt run --env <envName> <absScript> "$@"`.
func InstallScript(name, absScript, envName string, force bool) (*Tool, error) {
	if name == "" {
		return nil, fmt.Errorf("tool name required")
	}
	if absScript == "" || !filepath.IsAbs(absScript) {
		return nil, fmt.Errorf("script must be an absolute path")
	}
	if _, err := os.Stat(absScript); err != nil {
		return nil, fmt.Errorf("script not found: %s", absScript)
	}
	if envName == "" {
		return nil, fmt.Errorf("--env required for script-based tools")
	}

	mp, err := metaPath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(mp); err == nil && !force {
		return nil, fmt.Errorf("tool %q already exists; pass --force to overwrite", name)
	}

	now := time.Now().UTC()
	t := &Tool{
		Name:    name,
		Script:  absScript,
		Env:     envName,
		Created: now,
		Updated: now,
	}
	if existing, err := load(mp); err == nil {
		t.Created = existing.Created
	}
	return save(t, mp)
}

// SetEnv updates the named env for an existing tool and regenerates its shim.
func SetEnv(name, envName string) (*Tool, error) {
	mp, err := metaPath(name)
	if err != nil {
		return nil, err
	}
	t, err := load(mp)
	if err != nil {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	t.Env = envName
	t.Updated = time.Now().UTC()
	return save(t, mp)
}

// UnsetEnv clears the env binding from a tool and regenerates its shim.
func UnsetEnv(name string) (*Tool, error) {
	return SetEnv(name, "")
}

// save writes the tool record and its shim. On shim failure it removes the
// (potentially newly written) metadata to avoid a half-installed state.
func save(t *Tool, mp string) (*Tool, error) {
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(mp, data, 0o644); err != nil {
		return nil, err
	}
	if err := writeShim(t); err != nil {
		os.Remove(mp)
		return nil, fmt.Errorf("write shim: %w", err)
	}
	return t, nil
}

// writeShim generates the executable shim that delegates to molt.
//
// Three shim variants:
//
//  1. Env + Script  → `molt run --env <env> <script> "$@"` (env-only tool)
//  2. Project + Env → `molt --project <p> run --env <e> [task] "$@"` (project tool with env override)
//  3. Project only  → `molt --project <p> run [task] "$@"` (existing behaviour)
func writeShim(t *Tool) error {
	bin, err := BinDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	sp, err := shimPath(t.Name)
	if err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		return writeShimWindows(t, sp)
	}
	return writeShimPOSIX(t, sp)
}

func writeShimPOSIX(t *Tool, sp string) error {
	const hdr = "#!/bin/sh\n# Auto-generated by `molt tool install`. Edits will be overwritten.\n"
	var body string

	switch {
	case t.Script != "" && t.Env != "":
		// Env-bound standalone script.
		body = hdr + fmt.Sprintf("exec molt run --env '%s' '%s' \"$@\"\n",
			shellEscape(t.Env), shellEscape(t.Script))

	case t.ProjectDir != "" && t.Env != "" && t.Task != "":
		// Project tool with env override + named task.
		body = hdr + fmt.Sprintf("exec molt --project '%s' run --env '%s' '%s' \"$@\"\n",
			shellEscape(t.ProjectDir), shellEscape(t.Env), shellEscape(t.Task))

	case t.ProjectDir != "" && t.Env != "":
		// Project tool with env override, default entry.
		body = hdr + fmt.Sprintf("exec molt --project '%s' run --env '%s' \"$@\"\n",
			shellEscape(t.ProjectDir), shellEscape(t.Env))

	case t.Task != "":
		// Project tool with named task (original behaviour).
		body = hdr + fmt.Sprintf("exec molt --project '%s' run '%s' \"$@\"\n",
			shellEscape(t.ProjectDir), shellEscape(t.Task))

	default:
		// Project tool, default entry (original behaviour).
		body = hdr + fmt.Sprintf("exec molt --project '%s' run \"$@\"\n",
			shellEscape(t.ProjectDir))
	}
	return os.WriteFile(sp, []byte(body), 0o755)
}

func writeShimWindows(t *Tool, sp string) error {
	const hdr = "@echo off\r\nrem Auto-generated by `molt tool install`. Edits will be overwritten.\r\n"
	var body string
	switch {
	case t.Script != "" && t.Env != "":
		body = hdr + fmt.Sprintf("molt run --env \"%s\" \"%s\" %%*\r\n", t.Env, t.Script)
	case t.ProjectDir != "" && t.Env != "" && t.Task != "":
		body = hdr + fmt.Sprintf("molt --project \"%s\" run --env \"%s\" %s %%*\r\n", t.ProjectDir, t.Env, t.Task)
	case t.ProjectDir != "" && t.Env != "":
		body = hdr + fmt.Sprintf("molt --project \"%s\" run --env \"%s\" %%*\r\n", t.ProjectDir, t.Env)
	case t.Task != "":
		body = hdr + fmt.Sprintf("molt --project \"%s\" run %s %%*\r\n", t.ProjectDir, t.Task)
	default:
		body = hdr + fmt.Sprintf("molt --project \"%s\" run %%*\r\n", t.ProjectDir)
	}
	return os.WriteFile(sp, []byte(body), 0o755)
}

func shellEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, []byte(`'\''`)...)
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// Get returns the named tool, or os.ErrNotExist if missing.
func Get(name string) (*Tool, error) {
	mp, err := metaPath(name)
	if err != nil {
		return nil, err
	}
	return load(mp)
}

func load(path string) (*Tool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Tool
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// List returns every registered tool. Sorted by name. Returns an empty
// slice (not an error) when the tools dir doesn't exist yet.
func List() ([]*Tool, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]*Tool, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		t, err := load(filepath.Join(root, e.Name()))
		if err != nil {
			continue // skip malformed entries
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Uninstall removes both the metadata and the shim. Returns nil if the
// tool didn't exist (idempotent).
func Uninstall(name string) error {
	mp, err := metaPath(name)
	if err != nil {
		return err
	}
	sp, err := shimPath(name)
	if err != nil {
		return err
	}
	_ = os.Remove(sp)
	if err := os.Remove(mp); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ProjectAlive reports whether the tool's backing source is still present.
// For env-bound script tools it checks the script file; for project tools
// it checks for pyproject.toml in the project dir.
func (t *Tool) ProjectAlive() bool {
	if t.Script != "" {
		_, err := os.Stat(t.Script)
		return err == nil
	}
	_, err := os.Stat(filepath.Join(t.ProjectDir, "pyproject.toml"))
	return err == nil
}

// IsEnvTool reports whether this tool is an env-bound standalone-script tool
// (as opposed to a project-based tool).
func (t *Tool) IsEnvTool() bool {
	return t.Script != "" && t.Env != ""
}

// ShimPath returns the on-disk path of the tool's shim.
func (t *Tool) ShimPath() string {
	p, _ := shimPath(t.Name)
	return p
}
