// Package templates implements the molt project-init template engine.
//
// A template is a directory tree with optional metadata at template.toml.
// Templates can ship built-in (embedded into the molt binary via go:embed)
// or live in the user store at ~/.molt/templates/<name>/.
//
// Substitution rules — minimal, no DSL:
//
//   - Path components equal to "__pkg__" are renamed to the project's
//     normalised package name (e.g. "tag-control" → "tag_control").
//   - File contents pass through string substitution for {{name}} (the
//     project name) and {{pkg}} (the package name).
//   - The metadata file template.toml is consumed by the engine and is
//     never copied into the project.
//
// Built-in templates: bare, flat, src, app, lib.
package templates

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:builtin
var builtinFS embed.FS

const (
	metaFile     = "template.toml"
	pkgPlaceholder = "__pkg__"
)

// Meta is parsed from template.toml. Every field is optional.
type Meta struct {
	Description string   // human description for `molt template list`
	UseUvLib    bool     // pass --lib to uv init
	KeepHello   bool     // don't delete uv's hello.py placeholder
	Add         []string // packages to add post-init
	AddDev      []string // dev packages to add post-init
	Tasks       string   // raw TOML appended under [tool.molt.tasks]
}

// Template is a resolved template ready to apply.
type Template struct {
	Name   string
	Source string // "builtin" or "user"
	Root   fs.FS  // root of the template tree (template.toml + payload)
	Meta   Meta
}

// Vars holds the substitution variables passed to Apply.
type Vars struct {
	Name string // project name as the user typed it (may contain dashes)
	Pkg  string // normalised Python package name (lowercase, underscores)
}

// List returns every available template, built-ins first, then user.
func List() ([]Template, error) {
	var out []Template

	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := loadBuiltin(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}

	udir, err := UserDir()
	if err == nil {
		us, _ := os.ReadDir(udir)
		for _, e := range us {
			if !e.IsDir() {
				continue
			}
			t, err := loadUser(filepath.Join(udir, e.Name()))
			if err != nil {
				continue // skip malformed user templates rather than aborting list
			}
			out = append(out, t)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source == "builtin" // builtins first
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Lookup finds a template by name, preferring user templates so users can
// override built-ins. Returns os.ErrNotExist when missing.
func Lookup(name string) (Template, error) {
	udir, err := UserDir()
	if err == nil {
		path := filepath.Join(udir, name)
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			return loadUser(path)
		}
	}
	if t, err := loadBuiltin(name); err == nil {
		return t, nil
	}
	return Template{}, fmt.Errorf("template %q not found: %w", name, os.ErrNotExist)
}

// Apply copies the template into projectDir, performing path/content
// substitution. Existing files are not overwritten. Returns the resolved
// Meta so callers can act on it (uv init flags, post-add deps, tasks).
func (t Template) Apply(projectDir string, vars Vars) error {
	return fs.WalkDir(t.Root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || p == metaFile {
			return nil
		}
		dest := filepath.Join(projectDir, substPath(p, vars))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		// Don't clobber user files — re-applying a template is safe.
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		f, err := t.Root.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		raw, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, []byte(substContent(string(raw), vars)), 0o644)
	})
}

// AddTo registers a directory tree as a user template at ~/.molt/templates/<name>/.
// Copies the entire tree verbatim, validates that template.toml parses if present.
func AddTo(name, srcDir string) error {
	if name == "" {
		return errors.New("template name required")
	}
	udir, err := UserDir()
	if err != nil {
		return err
	}
	dest := filepath.Join(udir, name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("template %q already exists at %s — remove it first", name, dest)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// Validate template.toml if present.
	metaPath := filepath.Join(srcDir, metaFile)
	if data, err := os.ReadFile(metaPath); err == nil {
		if _, perr := parseMeta(data); perr != nil {
			return fmt.Errorf("invalid template.toml in %s: %w", srcDir, perr)
		}
	}
	return copyTree(srcDir, dest)
}

// Remove deletes a user template. Built-ins cannot be removed.
func Remove(name string) error {
	udir, err := UserDir()
	if err != nil {
		return err
	}
	path := filepath.Join(udir, name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("user template %q not found", name)
	}
	return os.RemoveAll(path)
}

// UserDir returns ~/.molt/templates/. Created on first use by callers.
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "templates"), nil
}

// ── internal ─────────────────────────────────────────────────────────────────

func loadBuiltin(name string) (Template, error) {
	root, err := fs.Sub(builtinFS, "builtin/"+name)
	if err != nil {
		return Template{}, fmt.Errorf("builtin template %q: %w", name, err)
	}
	if _, err := fs.Stat(root, "."); err != nil {
		return Template{}, fmt.Errorf("builtin template %q: %w", name, os.ErrNotExist)
	}
	t := Template{Name: name, Source: "builtin", Root: root}
	if data, err := fs.ReadFile(root, metaFile); err == nil {
		m, err := parseMeta(data)
		if err != nil {
			return Template{}, fmt.Errorf("builtin %q template.toml: %w", name, err)
		}
		t.Meta = m
	}
	return t, nil
}

func loadUser(dir string) (Template, error) {
	t := Template{Name: filepath.Base(dir), Source: "user", Root: os.DirFS(dir)}
	if data, err := os.ReadFile(filepath.Join(dir, metaFile)); err == nil {
		m, err := parseMeta(data)
		if err != nil {
			return Template{}, fmt.Errorf("user template %q: %w", t.Name, err)
		}
		t.Meta = m
	}
	return t, nil
}

func substPath(p string, v Vars) string {
	parts := strings.Split(p, "/")
	for i, x := range parts {
		if x == pkgPlaceholder {
			parts[i] = v.Pkg
		}
	}
	return filepath.Join(parts...)
}

func substContent(s string, v Vars) string {
	r := strings.NewReplacer(
		"{{name}}", v.Name,
		"{{pkg}}", v.Pkg,
	)
	return r.Replace(s)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
