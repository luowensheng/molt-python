// Package templates manages the template system using Go text/template.
package templates

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"molt/pkg/types"
)

// Engine manages templates from all three tiers.
type Engine struct {
	ProjectDir string
	builtins   map[string]*types.TemplateMeta
}

// New creates an Engine.
func New(projectDir string) *Engine {
	e := &Engine{ProjectDir: projectDir}
	e.builtins = builtinTemplates()
	return e
}

// List returns all available templates.
func (e *Engine) List(tier, tag string) []types.TemplateMeta {
	var result []types.TemplateMeta

	// Built-in.
	if tier == "" || tier == "builtin" {
		for _, t := range e.builtins {
			if tag == "" || hasTag(t.Tags, tag) {
				result = append(result, *t)
			}
		}
	}

	// User templates.
	if tier == "" || tier == "user" {
		userTemplates := e.loadDirTemplates(e.userTemplatesDir(), types.TemplateUser)
		for _, t := range userTemplates {
			if tag == "" || hasTag(t.Tags, tag) {
				result = append(result, t)
			}
		}
	}

	// Project templates.
	if tier == "" || tier == "project" {
		projectTemplates := e.loadDirTemplates(
			filepath.Join(e.ProjectDir, ".molt", "templates"),
			types.TemplateProject,
		)
		for _, t := range projectTemplates {
			if tag == "" || hasTag(t.Tags, tag) {
				result = append(result, t)
			}
		}
	}

	return result
}

// Show displays a template's source and metadata.
func (e *Engine) Show(name string) error {
	meta, content, err := e.resolve(name)
	if err != nil {
		return err
	}

	fmt.Printf("Template: %s\n", meta.Name)
	fmt.Printf("Source:   %s\n", meta.Source)
	fmt.Printf("Tags:     %s\n", strings.Join(meta.Tags, ", "))
	if meta.Description != "" {
		fmt.Printf("Description: %s\n", meta.Description)
	}
	if len(meta.Requires) > 0 {
		fmt.Printf("Requires: %s\n", strings.Join(meta.Requires, ", "))
	}
	fmt.Println()

	if len(meta.Vars) > 0 {
		fmt.Println("Variables:")
		for _, v := range meta.Vars {
			req := ""
			if v.Required {
				req = " [required]"
			}
			def := ""
			if v.Default != "" {
				def = fmt.Sprintf(" (default: %s)", v.Default)
			}
			fmt.Printf("  %-20s %s%s%s\n", v.Name, v.Description, req, def)
		}
		fmt.Println()
	}

	if !meta.MultiFile && content != "" {
		fmt.Println("Template source:")
		fmt.Println("────────────────")
		// Strip the meta comment block.
		fmt.Println(stripMetaComment(content))
	}
	return nil
}

// Vars shows required variables for a template.
func (e *Engine) Vars(name string) error {
	meta, _, err := e.resolve(name)
	if err != nil {
		return err
	}

	fmt.Printf("Variables for template '%s':\n\n", name)
	for _, v := range meta.Vars {
		req := "optional"
		if v.Required {
			req = "REQUIRED"
		}
		fmt.Printf("  %-25s %-10s %s", v.Name, req, v.Description)
		if v.Default != "" {
			fmt.Printf(" (default: %s)", v.Default)
		}
		fmt.Println()
	}

	// Always-available vars.
	fmt.Println()
	fmt.Println("Auto-injected variables (always available):")
	fmt.Println("  project_name    from pyproject.toml")
	fmt.Println("  project_version from pyproject.toml")
	fmt.Println("  python_version  from .python-version")
	fmt.Println("  package_name    project_name with hyphens→underscores")
	fmt.Println("  author          from pyproject.toml or git")
	fmt.Println("  year            current year")
	fmt.Println("  date            current date ISO")
	return nil
}

// Preview renders a template without writing to disk.
func (e *Engine) Preview(name string, dataJSON string) error {
	meta, content, err := e.resolve(name)
	if err != nil {
		return err
	}

	data, err := e.buildData(meta, dataJSON, true)
	if err != nil {
		return err
	}

	rendered, err := renderTemplate(meta.Name, content, data)
	if err != nil {
		return err
	}

	fmt.Printf("# Preview: %s\n\n", name)
	fmt.Println(rendered)
	return nil
}

// Create renders a template and writes to outputPath.
func (e *Engine) Create(name, outputPath, dataJSON string, interactive bool) error {
	meta, content, err := e.resolve(name)
	if err != nil {
		return err
	}

	var data map[string]any
	if interactive {
		data, err = e.interactiveData(meta)
	} else {
		data, err = e.buildData(meta, dataJSON, false)
	}
	if err != nil {
		return err
	}

	if meta.MultiFile {
		return e.createMultiFile(meta, data, outputPath)
	}

	rendered, err := renderTemplate(meta.Name, content, data)
	if err != nil {
		return err
	}

	// Determine output path.
	if outputPath == "" {
		outputPath = meta.Name + ".py"
	}

	// Render the output path itself (it may contain template vars).
	renderedPath, _ := renderTemplate("path", outputPath, data)
	if renderedPath != "" {
		outputPath = renderedPath
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, []byte(rendered), 0o644); err != nil {
		return err
	}

	fmt.Printf("✓ Created %s\n", outputPath)

	// Add required packages.
	if len(meta.Requires) > 0 {
		fmt.Printf("  Adding required packages: %s\n", strings.Join(meta.Requires, ", "))
		args := append([]string{"add"}, meta.Requires...)
		uvBin, _ := exec.LookPath("uv")
		if uvBin != "" {
			cmd := exec.Command(uvBin, args...)
			cmd.Dir = e.ProjectDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
	}

	return nil
}

// Add installs a template to user or project templates.
func (e *Engine) Add(sourcePath string, project bool) error {
	if strings.HasPrefix(sourcePath, "http://") || strings.HasPrefix(sourcePath, "https://") {
		return e.addFromURL(sourcePath, project)
	}

	var destDir string
	if project {
		destDir = filepath.Join(e.ProjectDir, ".molt", "templates")
	} else {
		destDir = e.userTemplatesDir()
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	// Single file template.
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		// Copy directory.
		destPath := filepath.Join(destDir, filepath.Base(sourcePath))
		return copyDir(sourcePath, destPath)
	}

	// Single .j2 or template file.
	dest := filepath.Join(destDir, filepath.Base(sourcePath))
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}

	location := "user"
	if project {
		location = "project"
	}
	fmt.Printf("✓ Template installed to %s templates: %s\n", location, filepath.Base(sourcePath))
	return nil
}

// Remove removes a template.
func (e *Engine) Remove(name string, project bool) error {
	var dirs []string
	if project {
		dirs = []string{filepath.Join(e.ProjectDir, ".molt", "templates")}
	} else {
		dirs = []string{e.userTemplatesDir()}
	}

	for _, dir := range dirs {
		// Try as directory.
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			fmt.Printf("✓ Removed template: %s\n", name)
			return nil
		}
		// Try as file.
		for _, ext := range []string{".tmpl", ".gotempl", ""} {
			p := filepath.Join(dir, name+ext)
			if _, err := os.Stat(p); err == nil {
				if err := os.Remove(p); err != nil {
					return err
				}
				fmt.Printf("✓ Removed template: %s\n", name)
				return nil
			}
		}
	}
	return fmt.Errorf("template '%s' not found", name)
}

// Export copies a built-in template to a file for customization.
func (e *Engine) Export(name, destPath string) error {
	meta, content, err := e.resolve(name)
	if err != nil {
		return err
	}
	if meta.Source != types.TemplateBuiltin {
		return fmt.Errorf("only built-in templates can be exported")
	}

	if destPath == "" {
		destPath = filepath.Join(e.userTemplatesDir(), name+".tmpl")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
		return err
	}

	fmt.Printf("✓ Exported '%s' to %s\n", name, destPath)
	fmt.Println("  Edit the file and your version will override the built-in.")
	return nil
}

// NewTemplate scaffolds a new template.
func (e *Engine) NewTemplate(name, fromFile string) error {
	destDir := e.userTemplatesDir()
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	var content string
	if fromFile != "" {
		data, err := os.ReadFile(fromFile)
		if err != nil {
			return err
		}
		content = templatize(string(data), name)
	} else {
		content = defaultTemplateContent(name)
	}

	destPath := filepath.Join(destDir, name+".tmpl")
	if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
		return err
	}

	fmt.Printf("✓ Template created: %s\n", destPath)
	fmt.Println("  Edit the file to customise your template.")
	fmt.Println("  Available in: molt create --from-template " + name)
	return nil
}

// Validate checks template syntax.
func (e *Engine) Validate(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := stripMetaComment(string(data))
	_, err = template.New("validate").Funcs(templateFuncs()).Parse(content)
	if err != nil {
		return fmt.Errorf("template syntax error: %w", err)
	}
	fmt.Printf("✓ Template syntax is valid: %s\n", path)
	return nil
}

// PrintList displays templates in a formatted table.
func (e *Engine) PrintList(tier, tag string) {
	templates := e.List(tier, tag)

	// Group by source.
	groups := map[types.TemplateSource][]types.TemplateMeta{}
	for _, t := range templates {
		groups[t.Source] = append(groups[t.Source], t)
	}

	for _, source := range []types.TemplateSource{types.TemplateBuiltin, types.TemplateUser, types.TemplateProject} {
		items, ok := groups[source]
		if !ok || len(items) == 0 {
			continue
		}

		label := map[types.TemplateSource]string{
			types.TemplateBuiltin: "BUILT-IN TEMPLATES",
			types.TemplateUser:    "USER TEMPLATES (~/.molt/templates/)",
			types.TemplateProject: "PROJECT TEMPLATES (.molt/templates/)",
		}[source]

		fmt.Printf("%s (%d)\n", label, len(items))

		// Group built-ins by tag category.
		if source == types.TemplateBuiltin {
			categories := []string{"api", "cli", "worker", "data", "pattern", "config", "test", "infra", "scaffold"}
			printed := map[string]bool{}
			for _, cat := range categories {
				var catItems []types.TemplateMeta
				for _, t := range items {
					if hasTag(t.Tags, cat) && !printed[t.Name] {
						catItems = append(catItems, t)
					}
				}
				if len(catItems) == 0 {
					continue
				}
				fmt.Printf("  %s\n", strings.ToUpper(cat))
				for _, t := range catItems {
					printed[t.Name] = true
					fmt.Printf("    %-30s %-50s [%s]\n",
						t.Name, t.Description, strings.Join(t.Tags, ", "))
				}
			}
		} else {
			for _, t := range items {
				fmt.Printf("  %-30s %s\n", t.Name, t.Description)
			}
		}
		fmt.Println()
	}
}

// ── Rendering ────────────────────────────────────────────────────────────────

func renderTemplate(name, content string, data map[string]any) (string, error) {
	content = stripMetaComment(content)
	tmpl, err := template.New(name).Funcs(templateFuncs()).Parse(content)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return buf.String(), nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"upper":   strings.ToUpper,
		"lower":   strings.ToLower,
		"snake":   func(s string) string { return strings.ReplaceAll(s, "-", "_") },
		"kebab":   func(s string) string { return strings.ReplaceAll(s, "_", "-") },
		"camel": func(s string) string {
			parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
			for i, p := range parts {
				if len(p) > 0 {
					parts[i] = strings.ToUpper(p[:1]) + p[1:]
				}
			}
			return strings.Join(parts, "")
		},
		"repeat": strings.Repeat,
		"join":   strings.Join,
		"now": func() string {
			return time.Now().UTC().Format(time.RFC3339)
		},
		"year": func() string {
			return fmt.Sprintf("%d", time.Now().Year())
		},
		"indent": func(spaces int, s string) string {
			pad := strings.Repeat(" ", spaces)
			return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
		},
	}
}

// ── Data building ────────────────────────────────────────────────────────────

func (e *Engine) buildData(meta *types.TemplateMeta, dataJSON string, preview bool) (map[string]any, error) {
	data := e.projectContext()

	// Parse user-provided data.
	if strings.HasPrefix(dataJSON, "@") {
		fileData, err := os.ReadFile(strings.TrimPrefix(dataJSON, "@"))
		if err != nil {
			return nil, err
		}
		dataJSON = string(fileData)
	}

	if dataJSON != "" {
		var userVars map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &userVars); err != nil {
			return nil, fmt.Errorf("parse --data JSON: %w", err)
		}
		for k, v := range userVars {
			data[k] = v
		}
	}

	// Fill defaults and check required.
	for _, v := range meta.Vars {
		if _, ok := data[v.Name]; !ok {
			if v.Default != "" {
				data[v.Name] = v.Default
			} else if v.Required && !preview {
				return nil, fmt.Errorf("required variable '%s' not provided (use --data or --interactive)", v.Name)
			} else if preview {
				data[v.Name] = "<" + v.Name + ">"
			}
		}
	}

	return data, nil
}

func (e *Engine) interactiveData(meta *types.TemplateMeta) (map[string]any, error) {
	data := e.projectContext()
	reader := bufio.NewReader(os.Stdin)

	for _, v := range meta.Vars {
		prompt := v.Description
		if v.Default != "" {
			prompt += fmt.Sprintf(" [%s]", v.Default)
		}
		if v.Required {
			prompt += " (required)"
		}
		fmt.Printf("%s: ", prompt)

		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)

		if line == "" && v.Default != "" {
			data[v.Name] = v.Default
		} else if line != "" {
			data[v.Name] = line
		} else if v.Required {
			return nil, fmt.Errorf("variable '%s' is required", v.Name)
		}
	}
	return data, nil
}

func (e *Engine) projectContext() map[string]any {
	data := map[string]any{
		"year":            fmt.Sprintf("%d", time.Now().Year()),
		"date":            time.Now().UTC().Format("2006-01-02"),
		"project_name":    "myproject",
		"project_version": "0.1.0",
		"python_version":  "3.12",
		"package_name":    "myproject",
		"author":          "",
	}

	// Read pyproject.toml.
	if tomlData, err := os.ReadFile(filepath.Join(e.ProjectDir, "pyproject.toml")); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(tomlData))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "name = ") {
				name := strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
				data["project_name"] = name
				data["package_name"] = strings.ReplaceAll(name, "-", "_")
			}
			if strings.HasPrefix(line, "version = ") {
				data["project_version"] = strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
			}
		}
	}

	// Read .python-version.
	if pyVer, err := os.ReadFile(filepath.Join(e.ProjectDir, ".python-version")); err == nil {
		data["python_version"] = strings.TrimSpace(string(pyVer))
	}

	// Git author.
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		data["author"] = strings.TrimSpace(string(out))
	}

	return data
}

// ── Multi-file templates ──────────────────────────────────────────────────────

func (e *Engine) createMultiFile(meta *types.TemplateMeta, data map[string]any, outputDir string) error {
	if outputDir == "" {
		outputDir = "."
	}

	created := []string{}
	for _, file := range meta.Files {
		// Evaluate 'when' condition.
		if file.When != "" {
			if !evaluateCondition(file.When, data) {
				continue
			}
		}

		// Load file template content.
		content, err := e.loadFileTemplate(meta, file.Src)
		if err != nil {
			return fmt.Errorf("load file %s: %w", file.Src, err)
		}

		// Render destination path.
		dstPath, err := renderTemplate("dst", file.Dst, data)
		if err != nil {
			return err
		}
		dstPath = filepath.Join(outputDir, dstPath)

		// Render content.
		rendered, err := renderTemplate(file.Src, content, data)
		if err != nil {
			return fmt.Errorf("render %s: %w", file.Src, err)
		}

		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, []byte(rendered), 0o644); err != nil {
			return err
		}
		created = append(created, dstPath)
	}

	fmt.Printf("✓ Created %d files:\n", len(created))
	for _, p := range created {
		fmt.Printf("  %s\n", p)
	}

	// Run hooks.
	for _, hook := range meta.Hooks {
		if hook.When != "" && !evaluateCondition(hook.When, data) {
			continue
		}
		fmt.Printf("  Running: %s\n", hook.Command)
		parts := strings.Fields(hook.Command)
		if len(parts) > 0 {
			cmd := exec.Command(parts[0], parts[1:]...)
			cmd.Dir = e.ProjectDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
	}

	return nil
}

// ── Resolution ────────────────────────────────────────────────────────────────

// resolve finds a template by name and returns its metadata and content.
func (e *Engine) resolve(name string) (*types.TemplateMeta, string, error) {
	// Project templates override user which override built-in.
	for _, dir := range []string{
		filepath.Join(e.ProjectDir, ".molt", "templates"),
		e.userTemplatesDir(),
	} {
		meta, content, err := e.loadFromDir(dir, name)
		if err == nil {
			return meta, content, nil
		}
	}

	// Built-in.
	if meta, ok := e.builtins[name]; ok {
		content := builtinContent(name)
		return meta, content, nil
	}

	return nil, "", fmt.Errorf("template '%s' not found — use 'molt template list' to see available templates", name)
}

func (e *Engine) loadFromDir(dir, name string) (*types.TemplateMeta, string, error) {
	// Try single-file template.
	for _, ext := range []string{".tmpl", ".gotempl", ""} {
		p := filepath.Join(dir, name+ext)
		data, err := os.ReadFile(p)
		if err == nil {
			meta := parseSingleFileMeta(name, string(data))
			meta.Source = types.TemplateUser
			return meta, string(data), nil
		}
	}
	// Try directory template with template.toml.
	p := filepath.Join(dir, name, "template.toml")
	if _, err := os.Stat(p); err == nil {
		meta, err := loadTemplateTOML(p, name)
		if err != nil {
			return nil, "", err
		}
		meta.Source = types.TemplateUser
		return meta, "", nil
	}
	return nil, "", fmt.Errorf("not found")
}

func (e *Engine) loadDirTemplates(dir string, source types.TemplateSource) []types.TemplateMeta {
	var result []types.TemplateMeta
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			p := filepath.Join(dir, name, "template.toml")
			if _, err := os.Stat(p); err == nil {
				meta, err := loadTemplateTOML(p, name)
				if err == nil {
					meta.Source = source
					result = append(result, *meta)
				}
			}
		} else {
			for _, ext := range []string{".tmpl", ".gotempl"} {
				if strings.HasSuffix(name, ext) {
					templateName := strings.TrimSuffix(name, ext)
					data, err := os.ReadFile(filepath.Join(dir, name))
					if err == nil {
						meta := parseSingleFileMeta(templateName, string(data))
						meta.Source = source
						result = append(result, *meta)
					}
				}
			}
		}
	}
	return result
}

func (e *Engine) loadFileTemplate(meta *types.TemplateMeta, src string) (string, error) {
	p := filepath.Join(meta.Path, src)
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (e *Engine) addFromURL(url string, project bool) error {
	fmt.Printf("Downloading template from %s...\n", url)
	tmp, err := os.CreateTemp("", "molt-template-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.Close()

	if err := downloadFile(url, tmp.Name()); err != nil {
		return err
	}

	return e.Add(tmp.Name(), project)
}

func downloadFile(url, dest string) error {
	for _, tool := range []string{"curl", "wget"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		var args []string
		if tool == "curl" {
			args = []string{"-L", "-o", dest, url}
		} else {
			args = []string{"-O", dest, url}
		}
		return exec.Command(tool, args...).Run()
	}
	return fmt.Errorf("curl or wget required")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (e *Engine) userTemplatesDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "templates")
}

func parseSingleFileMeta(name, content string) *types.TemplateMeta {
	meta := &types.TemplateMeta{
		Name:     name,
		MultiFile: false,
	}

	// Parse {{/* template.meta ... */}} comment block.
	startMarker := "{{/*"
	endMarker := "*/}}"
	start := strings.Index(content, startMarker)
	end := strings.Index(content, endMarker)
	if start < 0 || end < 0 {
		return meta
	}

	block := content[start+len(startMarker) : end]
	scanner := bufio.NewScanner(strings.NewReader(block))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "name:") {
			meta.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			meta.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "tags:") {
			tagStr := strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
			tagStr = strings.Trim(tagStr, "[]")
			for _, t := range strings.Split(tagStr, ",") {
				meta.Tags = append(meta.Tags, strings.TrimSpace(t))
			}
		} else if strings.HasPrefix(line, "requires:") {
			reqStr := strings.TrimSpace(strings.TrimPrefix(line, "requires:"))
			reqStr = strings.Trim(reqStr, "[]")
			for _, r := range strings.Split(reqStr, ",") {
				meta.Requires = append(meta.Requires, strings.TrimSpace(r))
			}
		} else if strings.HasPrefix(line, "- name:") {
			varName := strings.TrimSpace(strings.TrimPrefix(line, "- name:"))
			meta.Vars = append(meta.Vars, types.TemplateVar{Name: varName})
		}
	}
	return meta
}

func loadTemplateTOML(path, name string) (*types.TemplateMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta := &types.TemplateMeta{
		Name: name,
		Path: filepath.Dir(path),
	}
	// Simple TOML parser for our schema.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "description = ") {
			meta.Description = strings.Trim(strings.TrimPrefix(line, "description = "), `"`)
		} else if strings.HasPrefix(line, "multi_file = ") {
			meta.MultiFile = strings.Contains(line, "true")
		} else if strings.HasPrefix(line, "requires = ") {
			reqStr := strings.Trim(strings.TrimPrefix(line, "requires = "), "[]")
			for _, r := range strings.Split(reqStr, ",") {
				r = strings.Trim(r, `" `)
				if r != "" {
					meta.Requires = append(meta.Requires, r)
				}
			}
		}
	}
	return meta, nil
}

func stripMetaComment(content string) string {
	start := strings.Index(content, "{{/*")
	end := strings.Index(content, "*/}}")
	if start >= 0 && end >= 0 {
		return strings.TrimSpace(content[:start] + content[end+4:])
	}
	return content
}

func evaluateCondition(cond string, data map[string]any) bool {
	// Simple evaluation: check if a variable is truthy.
	if v, ok := data[cond]; ok {
		switch val := v.(type) {
		case bool:
			return val
		case string:
			return val != "" && val != "false"
		}
	}
	if strings.HasPrefix(cond, "not ") {
		return !evaluateCondition(strings.TrimPrefix(cond, "not "), data)
	}
	return false
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func templatize(content, name string) string {
	// Replace common patterns with template variables.
	replacements := []struct{ from, to string }{
		{"myapp", "{{.package_name}}"},
		{"my_app", "{{.package_name}}"},
	}
	for _, r := range replacements {
		content = strings.ReplaceAll(content, r.from, r.to)
	}
	return fmt.Sprintf("{{/*\nname: %s\ndescription: \ntags: []\n*/}}\n%s", name, content)
}

func defaultTemplateContent(name string) string {
	return fmt.Sprintf(`{{/*
name: %s
description: Add a description here
tags: []
requires: []
vars:
  - name: class_name
    description: Name of the main class
    required: true
*/}}
# {{.project_name}} — generated by molt
# Template: %s

class {{.class_name | camel}}:
    """{{.class_name | camel}} implementation."""

    def __init__(self) -> None:
        pass
`, name, name)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		destPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destPath, data, 0o644)
	})
}

// ── Built-in templates registry ───────────────────────────────────────────────

func builtinTemplates() map[string]*types.TemplateMeta {
	defs := []struct {
		name, desc string
		tags       []string
		requires   []string
		vars       []types.TemplateVar
	}{
		// API
		{"fastapi-server", "Production-ready FastAPI server with lifespan, logging, config",
			[]string{"api", "http", "fastapi", "async"},
			[]string{"fastapi", "uvicorn"},
			[]types.TemplateVar{
				{Name: "app_name", Description: "Application name", Required: true},
				{Name: "prefix", Description: "API route prefix", Default: "/api/v1"},
				{Name: "with_auth", Description: "Include auth scaffold", Default: "false"},
			}},
		{"fastapi-router", "FastAPI router with CRUD endpoints",
			[]string{"api", "http", "fastapi"},
			[]string{"fastapi"},
			[]types.TemplateVar{
				{Name: "router_name", Description: "Router name (e.g. users)", Required: true},
				{Name: "prefix", Description: "Route prefix", Default: ""},
			}},
		{"http-client", "Typed httpx client wrapper",
			[]string{"http", "client", "async"},
			[]string{"httpx"},
			[]types.TemplateVar{
				{Name: "class_name", Description: "Client class name", Required: true},
				{Name: "base_url_env", Description: "Env var for base URL", Default: "SERVICE_BASE_URL"},
			}},
		{"http-client-retry", "httpx client with retry/backoff",
			[]string{"http", "client", "async"},
			[]string{"httpx", "tenacity"},
			[]types.TemplateVar{
				{Name: "class_name", Description: "Client class name", Required: true},
			}},
		// CLI
		{"cli-argparse", "Full argparse CLI with subcommands",
			[]string{"cli"},
			nil,
			[]types.TemplateVar{
				{Name: "cmd_name", Description: "CLI command name", Required: true},
			}},
		{"cli-typer", "Typer-based CLI",
			[]string{"cli", "typer"},
			[]string{"typer"},
			[]types.TemplateVar{
				{Name: "cmd_name", Description: "CLI command name", Required: true},
			}},
		// Workers
		{"worker-asyncio", "asyncio worker with graceful shutdown",
			[]string{"worker", "async"},
			nil,
			[]types.TemplateVar{
				{Name: "worker_name", Description: "Worker class name", Required: true},
			}},
		{"worker-celery", "Celery task with retry logic",
			[]string{"worker", "celery"},
			[]string{"celery"},
			[]types.TemplateVar{
				{Name: "task_name", Description: "Task function name", Required: true},
			}},
		// Data
		{"sqlalchemy-model", "SQLAlchemy ORM model",
			[]string{"data", "sqlalchemy", "orm"},
			[]string{"sqlalchemy"},
			[]types.TemplateVar{
				{Name: "model_name", Description: "Model class name", Required: true},
				{Name: "table_name", Description: "Database table name", Required: true},
			}},
		{"pydantic-model", "Pydantic v2 model with validators",
			[]string{"data", "pydantic"},
			[]string{"pydantic"},
			[]types.TemplateVar{
				{Name: "model_name", Description: "Model class name", Required: true},
			}},
		{"dataclass-model", "Frozen dataclass with validation",
			[]string{"data"},
			nil,
			[]types.TemplateVar{
				{Name: "model_name", Description: "Class name", Required: true},
				{Name: "fields", Description: "Fields as 'name:type' comma separated", Default: "name:str"},
			}},
		// Patterns
		{"service-class", "Service layer class with dependency injection",
			[]string{"pattern", "service"},
			nil,
			[]types.TemplateVar{
				{Name: "service_name", Description: "Service class name", Required: true},
			}},
		{"repository", "Repository pattern over data store",
			[]string{"pattern", "repository"},
			nil,
			[]types.TemplateVar{
				{Name: "entity_name", Description: "Entity class name", Required: true},
			}},
		{"retry-decorator", "Retry decorator with exponential backoff",
			[]string{"pattern", "util"},
			nil,
			nil},
		{"context-manager", "Class-based context manager",
			[]string{"pattern", "util"},
			nil,
			[]types.TemplateVar{
				{Name: "class_name", Description: "Context manager class name", Required: true},
			}},
		// Config
		{"config-dataclass", "Config dataclass with .env support",
			[]string{"config"},
			[]string{"python-dotenv"},
			nil},
		{"config-pydantic", "Pydantic settings with env layers",
			[]string{"config", "pydantic"},
			[]string{"pydantic-settings"},
			nil},
		{"logging-structured", "Structured JSON logging setup",
			[]string{"config", "logging"},
			nil,
			nil},
		// Testing
		{"test-unit", "Unit test with fixtures",
			[]string{"test"},
			[]string{"pytest"},
			[]types.TemplateVar{
				{Name: "module_name", Description: "Module being tested", Required: true},
			}},
		{"test-async", "Async pytest test",
			[]string{"test", "async"},
			[]string{"pytest", "pytest-asyncio"},
			[]types.TemplateVar{
				{Name: "module_name", Description: "Module being tested", Required: true},
			}},
		{"conftest", "Shared pytest fixtures",
			[]string{"test"},
			[]string{"pytest"},
			nil},
		// Infra
		{"dockerfile", "Multi-stage Python Dockerfile",
			[]string{"infra", "docker"},
			nil,
			[]types.TemplateVar{
				{Name: "python_version", Description: "Python version", Default: "3.12"},
			}},
		{"github-actions-ci", "GitHub Actions CI workflow",
			[]string{"infra", "ci"},
			nil,
			[]types.TemplateVar{
				{Name: "python_version", Description: "Python version", Default: "3.12"},
			}},
		{"github-actions-release", "GitHub Actions release workflow",
			[]string{"infra", "ci"},
			nil,
			nil},
		// Scaffold
		{"exceptions", "Standard exception hierarchy",
			[]string{"scaffold"},
			nil,
			[]types.TemplateVar{
				{Name: "app_name", Description: "Application name", Required: true},
			}},
		{"plugin-system", "Plugin discovery system",
			[]string{"scaffold", "plugin"},
			nil,
			[]types.TemplateVar{
				{Name: "app_name", Description: "Application name", Required: true},
			}},
		{"script-etl", "ETL script with logging and error handling",
			[]string{"scaffold", "script"},
			nil,
			[]types.TemplateVar{
				{Name: "script_name", Description: "Script name", Required: true},
			}},
		{"makefile", "Makefile wrapping molt tasks",
			[]string{"infra"},
			nil,
			nil},
	}

	result := map[string]*types.TemplateMeta{}
	for _, d := range defs {
		d2 := d
		result[d.name] = &types.TemplateMeta{
			Name:        d2.name,
			Description: d2.desc,
			Tags:        d2.tags,
			Requires:    d2.requires,
			Vars:        d2.vars,
			Source:      types.TemplateBuiltin,
			MultiFile:   false,
		}
	}
	return result
}

// builtinContent returns the Go template source for a built-in template.
func builtinContent(name string) string {
	contents := map[string]string{
		"fastapi-server": `{{/*
name: fastapi-server
description: Production-ready FastAPI server
tags: [api, http, fastapi, async]
*/}}
from contextlib import asynccontextmanager
from fastapi import FastAPI
from {{.package_name}}.config import config
from {{.package_name}}.logging import get_logger
import uvicorn

logger = get_logger(__name__)

@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("starting {{.app_name}}")
    yield
    logger.info("shutting down {{.app_name}}")

app = FastAPI(
    title="{{.app_name | camel}}",
    version="{{.project_version}}",
    lifespan=lifespan,
)

@app.get("/health")
async def health():
    return {"status": "ok", "version": "{{.project_version}}"}

if __name__ == "__main__":
    uvicorn.run("{{.package_name}}.main:app", host="0.0.0.0", port=8000, reload=config.debug)
`,
		"fastapi-router": `{{/*
name: fastapi-router
description: FastAPI router with CRUD endpoints
tags: [api, http, fastapi]
*/}}
from fastapi import APIRouter, HTTPException
from {{.package_name}}.logging import get_logger

logger = get_logger(__name__)
router = APIRouter(prefix="{{if .prefix}}{{.prefix}}{{else}}/{{.router_name}}{{end}}", tags=["{{.router_name}}"])

@router.get("/")
async def list_{{.router_name | snake}}():
    return []

@router.get("/{item_id}")
async def get_{{.router_name | snake}}(item_id: int):
    raise HTTPException(status_code=404, detail="not found")

@router.post("/")
async def create_{{.router_name | snake}}(body: dict):
    return body

@router.put("/{item_id}")
async def update_{{.router_name | snake}}(item_id: int, body: dict):
    return body

@router.delete("/{item_id}")
async def delete_{{.router_name | snake}}(item_id: int):
    return {"deleted": item_id}
`,
		"http-client": `{{/*
name: http-client
description: Typed httpx client wrapper
tags: [http, client, async]
*/}}
import os
from typing import Any
import httpx
from {{.package_name}}.logging import get_logger

logger = get_logger(__name__)

class {{.class_name | camel}}:
    """HTTP client for {{.class_name}} service."""

    def __init__(self) -> None:
        self._base_url = os.environ["{{.base_url_env}}"]
        self._client = httpx.AsyncClient(
            base_url=self._base_url,
            timeout=30.0,
            headers={"Content-Type": "application/json"},
        )

    async def get(self, path: str, **kwargs: Any) -> dict:
        async with self._client as client:
            response = await client.get(path, **kwargs)
            response.raise_for_status()
            return response.json()

    async def post(self, path: str, body: dict, **kwargs: Any) -> dict:
        async with self._client as client:
            response = await client.post(path, json=body, **kwargs)
            response.raise_for_status()
            return response.json()
`,
		"cli-argparse": `{{/*
name: cli-argparse
description: Full argparse CLI with subcommands
tags: [cli]
*/}}
import argparse
import sys
from {{.package_name}}.config import config
from {{.package_name}}.logging import get_logger

logger = get_logger(__name__)

def main() -> None:
    parser = argparse.ArgumentParser(
        prog="{{.cmd_name | kebab}}",
        description="{{.project_name}} CLI",
    )
    parser.add_argument("--verbose", "-v", action="store_true", help="verbose output")
    parser.add_argument("--version", action="version", version="{{.project_version}}")

    subparsers = parser.add_subparsers(dest="command", required=True)

    # Add subcommands here.
    run_p = subparsers.add_parser("run", help="Run the application")
    run_p.add_argument("target", help="target to run")

    args = parser.parse_args()

    if args.command == "run":
        _cmd_run(args)

def _cmd_run(args: argparse.Namespace) -> None:
    logger.info("running %s", args.target)

if __name__ == "__main__":
    main()
`,
		"config-dataclass": `{{/*
name: config-dataclass
description: Config dataclass with .env support
tags: [config]
*/}}
from __future__ import annotations
from dataclasses import dataclass
from pathlib import Path
import os
from dotenv import load_dotenv

load_dotenv(Path(__file__).parent.parent / ".env", override=False)

@dataclass(frozen=True)
class Config:
    env:       str  = os.getenv("ENV",       "development")
    debug:     bool = os.getenv("DEBUG",     "false").lower() == "true"
    log_level: str  = os.getenv("LOG_LEVEL", "INFO")

    def is_production(self) -> bool:
        return self.env == "production"

config = Config()
`,
		"logging-structured": `{{/*
name: logging-structured
description: Structured logging setup
tags: [config, logging]
*/}}
from __future__ import annotations
import logging
import sys
from {{.package_name}}.config import config

def get_logger(name: str) -> logging.Logger:
    logger = logging.getLogger(name)
    if not logger.handlers:
        handler = logging.StreamHandler(sys.stdout)
        fmt = "%(asctime)s %(levelname)-8s %(name)s %(message)s"
        if config.env == "production":
            # JSON in production.
            fmt = '{"time":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","msg":"%(message)s"}'
        handler.setFormatter(logging.Formatter(fmt))
        logger.addHandler(handler)
        logger.propagate = False
    logger.setLevel(config.log_level)
    return logger
`,
		"service-class": `{{/*
name: service-class
description: Service layer class with dependency injection
tags: [pattern, service]
*/}}
from __future__ import annotations
from {{.package_name}}.logging import get_logger
from {{.package_name}}.exceptions import NotFoundError

logger = get_logger(__name__)

class {{.service_name | camel}}Service:
    """{{.service_name | camel}} domain service."""

    def __init__(self) -> None:
        # Inject dependencies here.
        pass

    def get(self, id: int) -> dict:
        logger.debug("get %s id=%d", "{{.service_name}}", id)
        raise NotFoundError(f"{{.service_name}} {id} not found")

    def create(self, data: dict) -> dict:
        logger.info("create %s", "{{.service_name}}")
        return data

    def update(self, id: int, data: dict) -> dict:
        logger.info("update %s id=%d", "{{.service_name}}", id)
        return data

    def delete(self, id: int) -> None:
        logger.info("delete %s id=%d", "{{.service_name}}", id)
`,
		"exceptions": `{{/*
name: exceptions
description: Standard exception hierarchy
tags: [scaffold]
*/}}
class {{.app_name | camel}}Error(Exception):
    """Base exception for {{.app_name}}."""

class ConfigError({{.app_name | camel}}Error):
    """Configuration is invalid or missing."""

class NotFoundError({{.app_name | camel}}Error):
    """Requested resource does not exist."""

class ValidationError({{.app_name | camel}}Error):
    """Input data failed validation."""

class AuthError({{.app_name | camel}}Error):
    """Authentication or authorisation failure."""

class ServiceError({{.app_name | camel}}Error):
    """Downstream service call failed."""
`,
		"worker-asyncio": `{{/*
name: worker-asyncio
description: asyncio worker with graceful shutdown
tags: [worker, async]
*/}}
from __future__ import annotations
import asyncio
import signal
from {{.package_name}}.logging import get_logger

logger = get_logger(__name__)

class {{.worker_name | camel}}Worker:
    """Async worker with graceful shutdown."""

    def __init__(self) -> None:
        self._running = False

    async def start(self) -> None:
        self._running = True
        logger.info("worker starting")
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, self.stop)
        await self._run()

    def stop(self) -> None:
        logger.info("worker stopping")
        self._running = False

    async def _run(self) -> None:
        while self._running:
            try:
                await self._process()
            except Exception as e:
                logger.error("worker error: %s", e)
            await asyncio.sleep(1)

    async def _process(self) -> None:
        # Implement your work here.
        pass

async def main() -> None:
    worker = {{.worker_name | camel}}Worker()
    await worker.start()

if __name__ == "__main__":
    asyncio.run(main())
`,
		"dockerfile": `{{/*
name: dockerfile
description: Multi-stage Python Dockerfile
tags: [infra, docker]
*/}}
# syntax=docker/dockerfile:1
FROM python:{{.python_version}}-slim AS builder
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN pip install uv && uv sync --frozen --no-dev

FROM python:{{.python_version}}-slim AS runtime
WORKDIR /app
COPY --from=builder /app/.venv /app/.venv
COPY {{.package_name}} ./{{.package_name}}
ENV PATH="/app/.venv/bin:$PATH" \
    PYTHONNOUSERSITE=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    ENV=production
ENTRYPOINT ["python", "-m", "{{.package_name}}"]
`,
		"github-actions-ci": `{{/*
name: github-actions-ci
description: GitHub Actions CI workflow
tags: [infra, ci]
*/}}
name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "{{.python_version}}"
      - name: Install molt
        run: curl -sSf https://molt.dev/install.sh | sh
      - name: Check
        run: molt check
  build:
    needs: check
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{"{{"}} matrix.os {{"}}"}}
    steps:
      - uses: actions/checkout@v4
      - name: Build
        run: molt build
      - uses: actions/upload-artifact@v4
        with:
          name: binary-${{"{{"}} matrix.os {{"}}"}}
          path: "{{.package_name}}-v*"
`,
		"test-unit": `{{/*
name: test-unit
description: Unit test with fixtures
tags: [test]
*/}}
import pytest
from {{.package_name}}.{{.module_name | snake}} import *

@pytest.fixture
def subject():
    # Set up the object under test.
    return None

class Test{{.module_name | camel}}:
    def test_basic(self, subject):
        assert subject is not None

    def test_raises_on_invalid_input(self, subject):
        with pytest.raises(ValueError):
            pass  # TODO
`,
		"makefile": `{{/*
name: makefile
description: Makefile wrapping molt tasks
tags: [infra]
*/}}
.PHONY: install dev test lint format check build clean

install:
	molt sync

dev:
	molt run dev

test:
	molt run test

lint:
	molt run lint

format:
	molt run format

check:
	molt check

build:
	molt build

clean:
	rm -rf dist/ build/ __pycache__ .pyc
	find . -name "*.pyc" -delete
`,
		"plugin-system": `{{/*
name: plugin-system
description: Plugin discovery and loading system
tags: [scaffold, plugin]
*/}}
from __future__ import annotations
import importlib
import pkgutil
from pathlib import Path
from typing import Protocol, runtime_checkable

@runtime_checkable
class Plugin(Protocol):
    """Protocol that all plugins must implement."""
    name: str
    version: str

    def setup(self) -> None: ...
    def teardown(self) -> None: ...

class PluginRegistry:
    """Central plugin registry for {{.app_name}}."""

    def __init__(self) -> None:
        self._plugins: dict[str, Plugin] = {}

    def register(self, plugin: Plugin) -> None:
        self._plugins[plugin.name] = plugin

    def get(self, name: str) -> Plugin:
        if name not in self._plugins:
            raise KeyError(f"plugin {name!r} not registered")
        return self._plugins[name]

    def all(self) -> list[Plugin]:
        return list(self._plugins.values())

    def load_directory(self, directory: str = "plugins") -> None:
        path = Path(directory)
        if not path.exists():
            return
        for finder, name, _ in pkgutil.iter_modules([str(path)]):
            module = importlib.import_module(f"{directory}.{name}")
            if hasattr(module, "plugin") and isinstance(module.plugin, Plugin):
                self.register(module.plugin)

registry = PluginRegistry()
`,
		"dataclass-model": `{{/*
name: dataclass-model
description: Frozen dataclass with validation
tags: [data]
*/}}
from __future__ import annotations
from dataclasses import dataclass, field
from datetime import datetime
from typing import Optional

@dataclass(frozen=True)
class {{.model_name | camel}}:
    """{{.model_name | camel}} value object."""

    {{range $f := .fields | split ","}}{{$parts := $f | split ":"}}    {{index $parts 0 | trim | snake}}: {{if gt (len $parts) 1}}{{index $parts 1 | trim}}{{else}}str{{end}}
    {{end}}
    created_at: datetime = field(default_factory=datetime.utcnow)
    id: Optional[int] = None

    def __post_init__(self) -> None:
        # Add validation here.
        pass
`,
	}

	if content, ok := contents[name]; ok {
		return content
	}

	// Generic fallback template.
	return fmt.Sprintf(`{{/*
name: %s
description: %s template
tags: []
*/}}
# {{.project_name}} — %s
# Generated by molt on {{.date}}
`, name, name, name)
}

// We need this for the dataclass template split function.
func init() {
	// The templateFuncs function is defined above and includes all needed functions.
	// The "split" function for dataclass-model needs to be handled differently
	// since it needs a closure over the separator.
	_ = runtime.GOOS // ensure runtime is used
}
