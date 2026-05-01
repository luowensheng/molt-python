package moltcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"molt/pkg/types"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "molt.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoad_Minimal(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project:
  name: myapp
  version: 1.0.0
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project.Name != "myapp" {
		t.Errorf("name = %q", cfg.Project.Name)
	}
	if cfg.Integrity == nil || !cfg.Integrity.IntegrityEnabled() {
		t.Errorf("integrity should be enabled by default")
	}
	if cfg.Integrity.Algorithm != "sha256" {
		t.Errorf("algorithm = %q, want sha256", cfg.Integrity.Algorithm)
	}
	if cfg.Integrity.Output == "" {
		t.Errorf("output should default to {name}-v{version}.manifest.json")
	}
	if cfg.Deps.Strategy != "pyproject" {
		t.Errorf("deps default = %q, want pyproject", cfg.Deps.Strategy)
	}
}

func TestLoad_FullExample(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project:
  name: webapp
  version: 2.3.1
  python: "3.11"

deps:
  strategy: requirements
  files:
    - requirements.txt
    - requirements-prod.txt

include:
  - "src/**/*.py"
  - "templates/"

exclude:
  - "tests/"
  - "*.pyc"

assets:
  files:
    - path: models/weights.bin
      required: true
      description: model weights
    - path: config/schema.json
      required: true

commands:
  default: web
  web:
    exec: [gunicorn, "app:application"]
    description: web server
  worker:
    script: "celery -A app worker"

env:
  PYTHONUNBUFFERED: "1"

hooks:
  post_install:
    - "python manage.py migrate"

integrity:
  enabled: true
  algorithm: sha256
  verify_on_launch: true
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultCommand != "web" {
		t.Errorf("DefaultCommand = %q, want 'web'", cfg.DefaultCommand)
	}
	if len(cfg.Commands) != 2 {
		t.Errorf("len(Commands) = %d, want 2 (default should have been stripped)", len(cfg.Commands))
	}
	if _, ok := cfg.Commands["default"]; ok {
		t.Errorf("'default' should have been stripped from Commands map")
	}
	if cfg.Commands["web"].Exec[0] != "gunicorn" {
		t.Errorf("web.exec[0] = %q", cfg.Commands["web"].Exec[0])
	}
	if !cfg.Integrity.VerifyOnLaunch {
		t.Errorf("verify_on_launch should be true")
	}
	if cfg.Assets == nil || len(cfg.Assets.Files) != 2 {
		t.Errorf("assets missing or wrong count")
	}
	if cfg.Assets.MaxFileSizeMB != 100 {
		t.Errorf("default max_file_size_mb should be 100, got %d", cfg.Assets.MaxFileSizeMB)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{"no version", "project:\n  name: x\n  version: 1\n", "missing `version:`"},
		{"no name", "version: 1\nproject:\n  version: 1\n", "project.name is required"},
		{"no version-field", "version: 1\nproject:\n  name: x\n", "project.version is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeYAML(t, tc.content)
			_, err := Load(dir)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestLoad_InvalidStrategy(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
deps:
  strategy: pip-tools
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("expected strategy validation error, got %v", err)
	}
}

func TestLoad_RequirementsWithoutFiles(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
deps:
  strategy: requirements
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "deps.files is required") {
		t.Fatalf("expected deps.files error, got %v", err)
	}
}

func TestLoad_CommandExecAndScriptMutex(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
commands:
  web:
    exec: [foo]
    script: "bar"
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected exec/script mutex error, got %v", err)
	}
}

func TestLoad_CommandEmpty(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
commands:
  web:
    description: empty
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "must define either") {
		t.Fatalf("expected empty-command error, got %v", err)
	}
}

func TestLoad_DefaultPointsToMissing(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
commands:
  default: ghost
  web:
    exec: [x]
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "no such command") {
		t.Fatalf("expected missing-default error, got %v", err)
	}
}

func TestLoad_AssetAbsolutePath(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
assets:
  files:
    - path: /etc/passwd
      required: true
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestLoad_AssetTraversal(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
assets:
  files:
    - path: "../../../etc/passwd"
      required: true
`)
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

func TestLoad_UnknownKey(t *testing.T) {
	dir := writeYAML(t, `
version: 1
project: {name: x, version: 1}
projeckt:
  typo: yes
`)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected strict-mode error on unknown key")
	}
}

func TestLoad_AltFilename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "molt.yml")
	if err := os.WriteFile(p, []byte("version: 1\nproject: {name: x, version: 1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("should accept molt.yml: %v", err)
	}
	if cfg.Project.Name != "x" {
		t.Errorf("name = %q", cfg.Project.Name)
	}
}

func TestLoad_AbsentIsNotError(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("should return nil/nil when absent: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil cfg when file absent")
	}
}

func TestSynthesize_FromPyproject(t *testing.T) {
	dir := t.TempDir()
	pyproj := `
[project]
name = "myapp"
version = "2.0.0"

[tool.other]
name = "should-not-match"
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproj), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".python-version"), []byte("3.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Synthesize(dir)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if cfg.Project.Name != "myapp" {
		t.Errorf("name = %q", cfg.Project.Name)
	}
	if cfg.Project.Version != "2.0.0" {
		t.Errorf("version = %q", cfg.Project.Version)
	}
	if cfg.Project.Python != "3.11" {
		t.Errorf("python = %q", cfg.Project.Python)
	}
}

func TestSynthesize_NoFiles(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Synthesize(dir)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if cfg.Project.Name == "" || cfg.Project.Version == "" {
		t.Errorf("should have synthesized defaults, got %+v", cfg.Project)
	}
}

func mkCfg(def string, names ...string) *types.MoltConfig {
	cmds := map[string]types.MoltCommand{}
	for _, n := range names {
		cmds[n] = types.MoltCommand{Exec: []string{"stub"}}
	}
	return &types.MoltConfig{
		Project:        types.MoltProject{Name: "x", Version: "1"},
		Commands:       cmds,
		DefaultCommand: def,
	}
}

func TestResolveCommand(t *testing.T) {
	cases := []struct {
		name string
		cfg  *types.MoltConfig
		want string
	}{
		{"explicit default", mkCfg("web", "web", "worker"), "web"},
		{"fallback to run", mkCfg("", "run", "worker"), "run"},
		{"fallback to start", mkCfg("", "start", "worker"), "start"},
		{"single command", mkCfg("", "onlyone"), "onlyone"},
		{"ambiguous → empty", mkCfg("", "a", "b"), ""},
		{"no commands → empty", mkCfg(""), ""},
		{"nil cfg → empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveCommand(tc.cfg); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
