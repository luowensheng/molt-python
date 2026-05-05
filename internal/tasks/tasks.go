// Package tasks implements the molt task runner.
package tasks

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"molt/internal/syspath"
	"molt/pkg/types"
)

// ErrTaskNotFound is returned by Run when name does not match any task in
// pyproject.toml. Callers (e.g. `molt run`) use this to fall through to
// generic command exec.
var ErrTaskNotFound = errors.New("task not found")

// Runner runs named tasks defined in pyproject.toml.
type Runner struct {
	ProjectDir string
}

// New creates a Runner.
func New(projectDir string) *Runner {
	return &Runner{ProjectDir: projectDir}
}

// List returns all tasks defined in pyproject.toml.
func (r *Runner) List() ([]types.Task, error) {
	return r.loadTasks()
}

// Run executes a named task.
func (r *Runner) Run(name string, watch bool, extraArgs []string) error {
	tasks, err := r.loadTasks()
	if err != nil {
		return err
	}

	var task *types.Task
	for i := range tasks {
		if tasks[i].Name == name {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, name)
	}

	if watch {
		return r.runWatch(task, extraArgs)
	}
	return r.runOnce(task, extraArgs)
}

// Add adds a task to pyproject.toml.
func (r *Runner) Add(name, command, description string) error {
	tomlPath := filepath.Join(r.ProjectDir, "pyproject.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}

	// Reject duplicate.
	if existing, _ := r.List(); existing != nil {
		for _, t := range existing {
			if t.Name == name {
				return fmt.Errorf("task %q already exists; remove it first or pick a different name", name)
			}
		}
	}

	content := string(data)
	entry := fmt.Sprintf("%s = %s\n", name, tomlString(command))

	if strings.Contains(content, "[tool.molt.tasks]") {
		// Insert after the section header.
		content = strings.Replace(content, "[tool.molt.tasks]\n",
			"[tool.molt.tasks]\n"+entry, 1)
	} else {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n[tool.molt.tasks]\n" + entry
	}

	if err := os.WriteFile(tomlPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("✓ Task '%s' added\n", name)
	return nil
}

// unescapeBasicTOMLString reverses the escapes applied by tomlString for
// basic (double-quoted) strings. Sufficient for the subset we ever emit.
func unescapeBasicTOMLString(s string) string {
	r := strings.NewReplacer(
		`\\`, `\`,
		`\"`, `"`,
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
	)
	return r.Replace(s)
}

// tomlString quotes s as a valid TOML string literal. It prefers single-
// quoted literal strings (no escape processing — perfect for shell commands
// containing double quotes); falls back to a basic string with escapes if
// the command itself contains an apostrophe.
func tomlString(s string) string {
	if !strings.ContainsAny(s, "'\n\r") {
		return "'" + s + "'"
	}
	// Basic string: escape backslash, double-quote, and control chars.
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Remove removes a task from pyproject.toml. Returns an error if the task
// does not exist so callers (and CI) can fail loudly on typos.
func (r *Runner) Remove(name string) error {
	tomlPath := filepath.Join(r.ProjectDir, "pyproject.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}

	// Confirm it actually exists before pretending to remove it.
	existing, _ := r.List()
	found := false
	for _, t := range existing {
		if t.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("task %q not found", name)
	}

	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		// Match `<name> = ...` only when name is not a substring prefix of a
		// longer task name. e.g. removing "test" must not delete "test-cov".
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, name) {
			rest := strings.TrimPrefix(trimmed, name)
			rest = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(rest, "=") {
				continue
			}
		}
		lines = append(lines, line)
	}

	out := strings.Join(lines, "\n")
	if len(data) > 0 && data[len(data)-1] == '\n' {
		out += "\n"
	}
	if err := os.WriteFile(tomlPath, []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Printf("✓ Task '%s' removed\n", name)
	return nil
}

// PrintList prints all tasks in a formatted table.
func (r *Runner) PrintList() error {
	tasks, err := r.loadTasks()
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Println("No tasks defined. Add tasks to [tool.molt.tasks] in pyproject.toml.")
		return nil
	}

	fmt.Println("Available tasks:")
	fmt.Println()
	for _, t := range tasks {
		fmt.Printf("  %-20s %s\n", t.Name, t.Command)
		if t.Description != "" {
			fmt.Printf("  %-20s %s\n", "", t.Description)
		}
	}
	fmt.Println()
	fmt.Println("Run with: molt run <task>")
	return nil
}

// ── Internal ──────────────────────────────────────────────────────────────────

func (r *Runner) runOnce(task *types.Task, extraArgs []string) error {
	// Structured task forms (module/script) bypass the shell entirely and
	// exec the project's Python interpreter directly. No PATH lookup, no
	// shim required — works even immediately after `molt sync` on a system
	// with no `python` on PATH.
	if task.Module != "" || task.Script != "" {
		return r.runPython(task, extraArgs)
	}

	cmdStr := task.Command
	if len(extraArgs) > 0 {
		cmdStr += " " + strings.Join(extraArgs, " ")
	}

	fmt.Printf("$ %s\n", cmdStr)

	shell := "/bin/sh"
	shellFlag := "-c"
	if isWindows() {
		shell = "cmd"
		shellFlag = "/C"
	}

	cmd := exec.Command(shell, shellFlag, cmdStr)
	cmd.Dir = r.workDir(task)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	env, err := r.buildTaskEnv(task.Env)
	if err != nil {
		return err
	}
	cmd.Env = env

	return cmd.Run()
}

// runPython execs the project's Python interpreter directly with -m / a
// script path plus task.Args + extraArgs.
func (r *Runner) runPython(task *types.Task, extraArgs []string) error {
	spec, err := syspath.Load(r.ProjectDir)
	if err != nil {
		return fmt.Errorf("project not synced; run 'molt sync' (%w)", err)
	}

	var args []string
	var display string
	switch {
	case task.Module != "":
		args = []string{"-m", task.Module}
		display = "python -m " + task.Module
	case task.Script != "":
		path := task.Script
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.workDir(task), path)
		}
		args = []string{path}
		display = "python " + task.Script
	}
	args = append(args, task.Args...)
	args = append(args, extraArgs...)
	if len(task.Args)+len(extraArgs) > 0 {
		display += " " + strings.Join(append(append([]string{}, task.Args...), extraArgs...), " ")
	}

	fmt.Printf("$ %s\n", display)

	cmd := spec.PythonCommand(args...)
	cmd.Dir = r.workDir(task)
	// Layer .env + task.Env on top of spec.BuildEnv.
	cmd.Env = r.layerEnv(cmd.Env, task.Env)
	return cmd.Run()
}

func (r *Runner) runWatch(task *types.Task, extraArgs []string) error {
	fmt.Printf("Watching for changes (Ctrl+C to stop)...\n\n")

	// Initial run.
	r.runOnce(task, extraArgs)

	// Watch using inotifywait if available, otherwise poll.
	if _, err := exec.LookPath("inotifywait"); err == nil {
		return r.watchInotify(task, extraArgs)
	}
	return r.watchPoll(task, extraArgs)
}

func (r *Runner) watchInotify(task *types.Task, extraArgs []string) error {
	for {
		cmd := exec.Command("inotifywait", "-r", "-e", "modify,create,delete",
			"--include", "\\.py$", r.ProjectDir)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		fmt.Println("\nFile changed, re-running...")
		r.runOnce(task, extraArgs)
	}
}

func (r *Runner) watchPoll(task *types.Task, extraArgs []string) error {
	fmt.Println("(inotifywait not found — polling every 2s)")
	var lastMtime int64
	for {
		mtime := r.latestMtime()
		if mtime != lastMtime && lastMtime != 0 {
			fmt.Println("\nFile changed, re-running...")
			r.runOnce(task, extraArgs)
		}
		lastMtime = mtime
		// Sleep 2 seconds via a blocking channel.
		done := make(chan struct{})
		go func() {
			exec.Command("sleep", "2").Run()
			close(done)
		}()
		<-done
	}
}

func (r *Runner) latestMtime() int64 {
	var latest int64
	filepath.WalkDir(r.ProjectDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.Contains(path, ".venv") || strings.Contains(path, "__pycache__") {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().UnixNano() > latest {
			latest = info.ModTime().UnixNano()
		}
		return nil
	})
	return latest
}

func (r *Runner) loadTasks() ([]types.Task, error) {
	tomlPath := filepath.Join(r.ProjectDir, "pyproject.toml")
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return nil, fmt.Errorf("pyproject.toml not found in %s", r.ProjectDir)
	}

	var tasks []types.Task
	inTasks := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "[tool.molt.tasks]" {
			inTasks = true
			continue
		}
		// Stop at next section.
		if inTasks && strings.HasPrefix(line, "[") {
			break
		}
		if !inTasks || line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse: name = "command"  or  name = { command = "...", description = "..." }
		eqIdx := strings.Index(line, " = ")
		if eqIdx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:eqIdx])
		rest := strings.TrimSpace(line[eqIdx+3:])

		task := types.Task{Name: name}
		if strings.HasPrefix(rest, `"`) {
			// Basic string form — last quote terminates.
			if end := strings.LastIndex(rest, `"`); end > 0 {
				task.Command = unescapeBasicTOMLString(rest[1:end])
			}
		} else if strings.HasPrefix(rest, `'`) {
			// Literal string form — last apostrophe terminates, no escapes.
			if end := strings.LastIndex(rest, `'`); end > 0 {
				task.Command = rest[1:end]
			}
		} else if strings.HasPrefix(rest, "{") {
			// Inline table form — extract command/module/script/args.
			task.Command = extractInlineKey(rest, "command")
			task.Module = extractInlineKey(rest, "module")
			task.Script = extractInlineKey(rest, "script")
			task.Args = extractInlineArrayKey(rest, "args")
			task.Description = extractInlineKey(rest, "description")
		}
		if !taskValid(&task) {
			// Reject ambiguous declarations rather than silently dropping.
			if conflictedTask(&task) {
				return nil, fmt.Errorf("task %q: only one of command/module/script may be set", name)
			}
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// extractInlineKey reads `key = "value"` (basic or literal string) from an
// inline-table fragment such as `{ module = "tagctl", args = [...] }`.
// Returns "" if the key is absent. Handles both bare and quoted key forms.
func extractInlineKey(rest, key string) string {
	patterns := []string{
		`"` + key + `"`,
		key,
	}
	for _, pat := range patterns {
		idx := strings.Index(rest, pat)
		if idx < 0 {
			continue
		}
		// Confirm it's a key, not a substring of a longer name. The char
		// before should be `{`, `,`, or whitespace; the next non-space char
		// after should be `=`.
		if idx > 0 {
			c := rest[idx-1]
			if c != '{' && c != ',' && c != ' ' && c != '\t' {
				continue
			}
		}
		after := strings.TrimSpace(rest[idx+len(pat):])
		if !strings.HasPrefix(after, "=") {
			continue
		}
		after = strings.TrimSpace(after[1:])
		if strings.HasPrefix(after, `"`) {
			if end := strings.Index(after[1:], `"`); end >= 0 {
				return unescapeBasicTOMLString(after[1 : 1+end])
			}
		} else if strings.HasPrefix(after, `'`) {
			if end := strings.Index(after[1:], `'`); end >= 0 {
				return after[1 : 1+end]
			}
		}
	}
	return ""
}

// extractInlineArrayKey reads `key = ["a", "b"]` from an inline-table fragment.
// Returns nil if the key is absent. Strings only — no nested arrays.
func extractInlineArrayKey(rest, key string) []string {
	patterns := []string{`"` + key + `"`, key}
	for _, pat := range patterns {
		idx := strings.Index(rest, pat)
		if idx < 0 {
			continue
		}
		if idx > 0 {
			c := rest[idx-1]
			if c != '{' && c != ',' && c != ' ' && c != '\t' {
				continue
			}
		}
		after := strings.TrimSpace(rest[idx+len(pat):])
		if !strings.HasPrefix(after, "=") {
			continue
		}
		after = strings.TrimSpace(after[1:])
		if !strings.HasPrefix(after, "[") {
			continue
		}
		end := strings.Index(after, "]")
		if end < 0 {
			continue
		}
		body := after[1:end]
		var out []string
		// Split on commas at depth 0; we only support strings inside.
		for _, raw := range strings.Split(body, ",") {
			s := strings.TrimSpace(raw)
			if s == "" {
				continue
			}
			if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
				out = append(out, unescapeBasicTOMLString(s[1:len(s)-1]))
			} else if strings.HasPrefix(s, `'`) && strings.HasSuffix(s, `'`) && len(s) >= 2 {
				out = append(out, s[1:len(s)-1])
			}
		}
		return out
	}
	return nil
}

// taskValid reports whether a parsed task has exactly one execution form set.
func taskValid(t *types.Task) bool {
	n := 0
	if t.Command != "" {
		n++
	}
	if t.Module != "" {
		n++
	}
	if t.Script != "" {
		n++
	}
	return n == 1
}

// conflictedTask reports whether a task has more than one execution form set.
func conflictedTask(t *types.Task) bool {
	n := 0
	if t.Command != "" {
		n++
	}
	if t.Module != "" {
		n++
	}
	if t.Script != "" {
		n++
	}
	return n > 1
}

// buildTaskEnv constructs the env for a shell-form task (Command). It
// requires .molt/syspath.json to exist (so the .molt/bin/ shim PATH is
// known); callers should auto-sync before running tasks. The legacy .venv
// fallback was removed because it produced a misleading PATH that made
// "python: command not found" failures look like a packaging bug.
func (r *Runner) buildTaskEnv(taskEnv []string) ([]string, error) {
	spec, err := syspath.Load(r.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("project not synced; run 'molt sync' (%w)", err)
	}
	return r.layerEnv(spec.BuildEnv(os.Environ()), taskEnv), nil
}

// layerEnv applies .env file entries and per-task env overrides on top of
// a base environment.
func (r *Runner) layerEnv(base []string, taskEnv []string) []string {
	out := append([]string{}, base...)
	if envData, err := os.ReadFile(filepath.Join(r.ProjectDir, ".env")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(envData))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
	}
	out = append(out, taskEnv...)
	return out
}

func (r *Runner) workDir(task *types.Task) string {
	if task.Dir != "" {
		if filepath.IsAbs(task.Dir) {
			return task.Dir
		}
		return filepath.Join(r.ProjectDir, task.Dir)
	}
	return r.ProjectDir
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}
