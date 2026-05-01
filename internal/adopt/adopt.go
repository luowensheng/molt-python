// internal/adopt/adopt.go
//
// Package adopt scaffolds a molt.yaml for an EXISTING project.
//
// Philosophy: we don't guess. We detect what's present (requirements.txt,
// pyproject.toml, Pipfile, manage.py, etc.) and show it as options, but
// every field the user ends up with was one they confirmed. This avoids
// the classic "adoption tool wrote the wrong thing and now I have to debug
// its output" failure mode.
//
// The output is always reviewable YAML — users are expected to open the
// file after `molt adopt` and refine it. The tool's job is to give them
// a good starting point, not a final answer.

package adopt

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"molt/internal/moltcfg"
	"molt/pkg/types"
)

// Options controls the adoption run.
type Options struct {
	ProjectDir     string
	Force          bool     // Overwrite an existing molt.yaml
	In             *os.File // Input for prompts; defaults to os.Stdin
	Out            *os.File // Output for prompts; defaults to os.Stdout
	NonInteractive bool     // Take detected defaults; suitable for CI
}

// Run performs adoption. When non-interactive, uses detected defaults.
func Run(opts Options) error {
	if opts.ProjectDir == "" {
		opts.ProjectDir = "."
	}
	absDir, err := filepath.Abs(opts.ProjectDir)
	if err != nil {
		return err
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}

	dest := filepath.Join(absDir, moltcfg.Filename)
	if _, err := os.Stat(dest); err == nil && !opts.Force {
		return fmt.Errorf(
			"%s already exists — pass --force to overwrite, or edit the existing file",
			moltcfg.Filename,
		)
	}

	det := detect(absDir)
	fmt.Fprintf(opts.Out, "molt adopt — detected existing project layout\n\n")
	det.Print(opts.Out)

	cfg := &types.MoltConfig{
		Version: types.MoltConfigSchemaVersion,
		Project: types.MoltProject{
			Name:    det.SuggestedName,
			Version: "0.1.0",
		},
	}

	reader := bufio.NewReader(opts.In)

	if !opts.NonInteractive {
		cfg.Project.Name = prompt(opts.Out, reader,
			"Project name", det.SuggestedName,
		)
		cfg.Project.Version = prompt(opts.Out, reader,
			"Version", "0.1.0",
		)
		if det.PythonVersion != "" {
			cfg.Project.Python = prompt(opts.Out, reader,
				"Python version", det.PythonVersion,
			)
		}
	}

	// Deps strategy.
	cfg.Deps = chooseDeps(opts, det, reader)

	// Include/exclude suggestions.
	cfg.Include, cfg.Exclude = chooseIncludes(opts, det, reader)

	// Commands.
	cfg.Commands = chooseCommands(opts, det, reader)

	// Serialize. We emit a header comment so users are oriented when they
	// open the file — a fresh YAML file with no context is confusing.
	if err := writeYAML(cfg, dest); err != nil {
		return fmt.Errorf("write %s: %w", moltcfg.Filename, err)
	}

	// Re-load to validate what we just wrote. If validation fails, the
	// adoption script has a bug — surface it loudly rather than letting
	// the user discover it at build time.
	if _, err := moltcfg.Load(absDir); err != nil {
		return fmt.Errorf("generated %s failed validation: %w", moltcfg.Filename, err)
	}

	fmt.Fprintf(opts.Out, "\n✓ Wrote %s\n", dest)
	fmt.Fprintln(opts.Out, "  Review the file, refine as needed, then run: molt build")
	return nil
}

// ── Detection ────────────────────────────────────────────────────────────────

// Detected summarises what signals adopt found in the project tree.
type Detected struct {
	SuggestedName     string
	PythonVersion     string
	HasPyProject      bool
	HasRequirements   bool
	RequirementsFiles []string
	HasPipfile        bool
	HasPoetryLock     bool
	HasManagePy       bool // Django
	HasSetupPy        bool
	HasSrcLayout      bool // src/ directory present
	TopLevelPackages  []string
	EntryHints        []EntryHint
}

// EntryHint is a likely entry command we detected. User picks which (if any)
// to promote into `commands:`.
type EntryHint struct {
	Name        string   // suggested command name ("web", "migrate", ...)
	Exec        []string // argv
	Description string
	Reason      string // why we suggested it — surfaced in the prompt
}

// detect runs filesystem checks and returns what adopt knows about the project.
// No side effects, safe to call before the user has approved anything.
func detect(dir string) Detected {
	d := Detected{
		SuggestedName: sanitizeProjectName(filepath.Base(dir)),
	}

	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		d.HasPyProject = true
	}
	if _, err := os.Stat(filepath.Join(dir, "setup.py")); err == nil {
		d.HasSetupPy = true
	}
	if _, err := os.Stat(filepath.Join(dir, "Pipfile")); err == nil {
		d.HasPipfile = true
	}
	if _, err := os.Stat(filepath.Join(dir, "poetry.lock")); err == nil {
		d.HasPoetryLock = true
	}
	if _, err := os.Stat(filepath.Join(dir, "manage.py")); err == nil {
		d.HasManagePy = true
	}
	if fi, err := os.Stat(filepath.Join(dir, "src")); err == nil && fi.IsDir() {
		d.HasSrcLayout = true
	}

	// Pick up every requirements*.txt we can find at the root — users
	// commonly split into requirements.txt, requirements-dev.txt, etc.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt") {
			d.HasRequirements = true
			d.RequirementsFiles = append(d.RequirementsFiles, name)
		}
	}
	sort.Strings(d.RequirementsFiles)

	// .python-version → suggested python.
	if data, err := os.ReadFile(filepath.Join(dir, ".python-version")); err == nil {
		d.PythonVersion = strings.TrimSpace(string(data))
	}

	// Top-level packages: directories containing an __init__.py.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "." || e.Name() == ".." || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "__init__.py")); err == nil {
			d.TopLevelPackages = append(d.TopLevelPackages, e.Name())
		}
	}
	// Also check src/* if present.
	if d.HasSrcLayout {
		srcEntries, _ := os.ReadDir(filepath.Join(dir, "src"))
		for _, e := range srcEntries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, "src", e.Name(), "__init__.py")); err == nil {
				d.TopLevelPackages = append(d.TopLevelPackages, "src/"+e.Name())
			}
		}
	}

	// Entry hints.
	if d.HasManagePy {
		d.EntryHints = append(d.EntryHints,
			EntryHint{
				Name: "web", Description: "Django dev server",
				Exec:   []string{"python", "manage.py", "runserver", "0.0.0.0:8000"},
				Reason: "manage.py present (Django)",
			},
			EntryHint{
				Name: "migrate", Description: "Apply Django migrations",
				Exec:   []string{"python", "manage.py", "migrate"},
				Reason: "manage.py present (Django)",
			},
			EntryHint{
				Name: "shell", Description: "Django shell",
				Exec:   []string{"python", "manage.py", "shell"},
				Reason: "manage.py present (Django)",
			},
		)
	}
	// Generic fallback: `python -m <first_pkg>`.
	if len(d.TopLevelPackages) > 0 {
		first := d.TopLevelPackages[0]
		name := strings.TrimPrefix(first, "src/")
		d.EntryHints = append(d.EntryHints, EntryHint{
			Name: "main", Description: "Default module entry point",
			Exec:   []string{"python", "-m", name},
			Reason: fmt.Sprintf("detected package %q with __init__.py", first),
		})
	}

	return d
}

// Print writes a human-readable summary of what was detected. Used both at
// the start of `molt adopt` and standalone via `molt adopt --dry-run`.
func (d *Detected) Print(w *os.File) {
	fmt.Fprintf(w, "  Project directory name : %s\n", d.SuggestedName)
	if d.PythonVersion != "" {
		fmt.Fprintf(w, "  .python-version        : %s\n", d.PythonVersion)
	}
	if d.HasPyProject {
		fmt.Fprintln(w, "  ✓ pyproject.toml")
	}
	if d.HasRequirements {
		fmt.Fprintf(w, "  ✓ requirements files  : %s\n", strings.Join(d.RequirementsFiles, ", "))
	}
	if d.HasPipfile {
		fmt.Fprintln(w, "  ✓ Pipfile (pipenv)")
	}
	if d.HasPoetryLock {
		fmt.Fprintln(w, "  ✓ poetry.lock")
	}
	if d.HasSetupPy {
		fmt.Fprintln(w, "  ✓ setup.py")
	}
	if d.HasManagePy {
		fmt.Fprintln(w, "  ✓ manage.py (Django)")
	}
	if d.HasSrcLayout {
		fmt.Fprintln(w, "  ✓ src/ layout")
	}
	if len(d.TopLevelPackages) > 0 {
		fmt.Fprintf(w, "  Packages detected      : %s\n", strings.Join(d.TopLevelPackages, ", "))
	}
	fmt.Fprintln(w)
}

// ── Choice helpers ───────────────────────────────────────────────────────────

func chooseDeps(opts Options, d Detected, r *bufio.Reader) *types.MoltDeps {
	// Preference order when auto-picking:
	//   poetry.lock → poetry
	//   Pipfile → pipenv
	//   requirements*.txt → requirements (with files)
	//   pyproject.toml → pyproject
	//   nothing detected → none
	auto := types.MoltDeps{}
	switch {
	case d.HasPoetryLock:
		auto.Strategy = types.DepsStrategyPoetry
	case d.HasPipfile:
		auto.Strategy = types.DepsStrategyPipenv
	case d.HasRequirements:
		auto.Strategy = types.DepsStrategyRequirements
		auto.Files = append([]string(nil), d.RequirementsFiles...)
	case d.HasPyProject:
		auto.Strategy = types.DepsStrategyPyProject
	default:
		auto.Strategy = types.DepsStrategyNone
	}

	if opts.NonInteractive {
		return &auto
	}

	fmt.Fprintf(opts.Out, "Dependency install strategy [%s]: ", auto.Strategy)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return &auto
	}
	cfg := &types.MoltDeps{Strategy: types.MoltDepsStrategy(line)}
	if cfg.Strategy == types.DepsStrategyRequirements && len(d.RequirementsFiles) > 0 {
		fmt.Fprintf(opts.Out, "  Requirements files [%s]: ", strings.Join(d.RequirementsFiles, ","))
		filesLine, _ := r.ReadString('\n')
		filesLine = strings.TrimSpace(filesLine)
		if filesLine == "" {
			cfg.Files = append([]string(nil), d.RequirementsFiles...)
		} else {
			for _, f := range strings.Split(filesLine, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					cfg.Files = append(cfg.Files, f)
				}
			}
		}
	}
	return cfg
}

func chooseIncludes(opts Options, d Detected, r *bufio.Reader) (include, exclude []string) {
	// Conservative defaults: include the detected top-level packages (so
	// they ship), exclude tests/docs/caches (rarely wanted). These are
	// starting points, not final decisions.
	for _, pkg := range d.TopLevelPackages {
		include = append(include, pkg+"/**/*.py")
	}
	if d.HasSrcLayout {
		include = append(include, "src/**/*.py")
	}
	if d.HasManagePy {
		// Django-specific: manage.py and likely templates/static assets.
		include = append(include, "manage.py", "**/templates/**/*", "**/static/**/*")
	}
	exclude = []string{
		"**/__pycache__/**",
		"**/*.pyc",
		"tests/**",
		"**/test_*.py",
		"docs/**",
	}
	return
}

func chooseCommands(opts Options, d Detected, r *bufio.Reader) map[string]types.MoltCommand {
	cmds := map[string]types.MoltCommand{}
	if opts.NonInteractive {
		for _, h := range d.EntryHints {
			cmds[h.Name] = types.MoltCommand{Exec: h.Exec, Description: h.Description}
		}
		return cmds
	}

	if len(d.EntryHints) == 0 {
		fmt.Fprintln(opts.Out, "\nNo entry points detected. You'll need to add `commands:` by hand.")
		return cmds
	}

	fmt.Fprintln(opts.Out, "\nSuggested run commands:")
	for i, h := range d.EntryHints {
		fmt.Fprintf(opts.Out, "  [%d] %-8s %s\n", i+1, h.Name, strings.Join(h.Exec, " "))
		fmt.Fprintf(opts.Out, "      (%s)\n", h.Reason)
	}
	fmt.Fprint(opts.Out, "Accept all suggested commands? [Y/n]: ")
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" || line == "y" || line == "yes" {
		for _, h := range d.EntryHints {
			cmds[h.Name] = types.MoltCommand{
				Exec:        h.Exec,
				Description: h.Description,
			}
		}
	}
	return cmds
}

// ── IO helpers ────────────────────────────────────────────────────────────────

func prompt(w *os.File, r *bufio.Reader, label, def string) string {
	fmt.Fprintf(w, "%s [%s]: ", label, def)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// writeYAML serialises cfg as well-formatted YAML with a leading header.
// Headers matter: a bare machine-generated file is confusing to open in
// two weeks when you've forgotten what molt.yaml even does.
func writeYAML(cfg *types.MoltConfig, path string) error {
	var buf strings.Builder
	buf.WriteString("# molt.yaml — deployment config (non-Python concerns live here).\n")
	buf.WriteString("# Schema reference: https://molt.dev/docs/molt-yaml\n")
	buf.WriteString("# Safe to edit; `molt build` re-reads this file.\n\n")

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

// sanitizeProjectName applies the same charset restriction as moltcfg.
// Used to suggest a name from the directory — "my app" becomes "my_app".
func sanitizeProjectName(raw string) string {
	if raw == "" || raw == "." || raw == "/" {
		return "app"
	}
	var b strings.Builder
	for _, r := range raw {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.'
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	s := b.String()
	for len(s) > 0 && (s[0] == '.' || s[0] == '-') {
		s = s[1:]
	}
	if s == "" {
		return "app"
	}
	return s
}
