package native

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifest_StringForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "math.molt.toml")
	src := `module = "math"

[[fn]]
name = "add"
description = "Sum two integers."
args = ["i32", "i32"]
returns = "i32"

[[fn]]
name = "neg"
args = ["f32"]
returns = "f32"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Module != "math" {
		t.Errorf("Module = %q, want math", m.Module)
	}
	if len(m.Functions) != 2 {
		t.Fatalf("Functions = %d, want 2", len(m.Functions))
	}
	add := m.Functions[0]
	if add.Name != "add" || add.Description != "Sum two integers." {
		t.Errorf("add fn fields wrong: %+v", add)
	}
	if len(add.Args) != 2 || add.Args[0].Type != "i32" || add.Args[1].Type != "i32" {
		t.Errorf("add args = %+v", add.Args)
	}
	if add.Returns.Type != "i32" {
		t.Errorf("add returns = %+v", add.Returns)
	}
}

func TestParseManifest_InlineTableForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "math.molt.toml")
	src := `[[fn]]
name = "add"
args = [
    { name = "a", type = "i32", description = "First" },
    { name = "b", type = "i32", description = "Second" },
]
returns = { type = "i32", description = "Sum" }
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	fn := m.Functions[0]
	if fn.Args[0].Name != "a" || fn.Args[0].Type != "i32" || fn.Args[0].Description != "First" {
		t.Errorf("arg[0] = %+v", fn.Args[0])
	}
	if fn.Returns.Type != "i32" || fn.Returns.Description != "Sum" {
		t.Errorf("returns = %+v", fn.Returns)
	}
}

func TestEmitGlueC_HasPyInit(t *testing.T) {
	m := &Manifest{
		Module: "crypto",
		Functions: []ManifestFn{
			{
				Name: "add",
				Args: []ManifestArg{{Type: "i32"}, {Type: "i32"}},
				Returns: ManifestReturn{Type: "i32"},
			},
		},
	}
	out := emitGlueC(m)
	for _, want := range []string{
		"PyInit_crypto",
		"PyModuleDef_HEAD_INIT",
		"\"add\"",
		"PyArg_ParseTuple",
		"extern int32_t add(",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("glue.c missing %q\n%s", want, out)
		}
	}
}

func TestEmitGlueC_VoidReturn(t *testing.T) {
	m := &Manifest{
		Module: "x",
		Functions: []ManifestFn{
			{Name: "doit", Args: nil, Returns: ManifestReturn{Type: "void"}},
		},
	}
	out := emitGlueC(m)
	if !strings.Contains(out, "Py_RETURN_NONE") {
		t.Errorf("expected Py_RETURN_NONE in glue:\n%s", out)
	}
	if strings.Contains(out, "_ret =") {
		t.Errorf("void fn should not assign _ret:\n%s", out)
	}
}

func TestEmitPyiStub_TypedSignatures(t *testing.T) {
	m := &Manifest{
		Module: "math",
		Functions: []ManifestFn{
			{
				Name: "add",
				Description: "Sum two ints.",
				Args: []ManifestArg{
					{Name: "a", Type: "i32", Description: "First."},
					{Name: "b", Type: "i32"},
				},
				Returns: ManifestReturn{Type: "i32"},
			},
			{
				Name: "scale",
				Args: []ManifestArg{{Name: "x", Type: "f32"}},
				Returns: ManifestReturn{Type: "f32"},
			},
		},
	}
	out := emitPyiStub(m, "math.zig")
	for _, want := range []string{
		"def add(a: int, b: int) -> int:",
		"\"\"\"Sum two ints.",
		"a: First.",
		"def scale(x: float) -> float: ...",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pyi missing %q\n%s", want, out)
		}
	}
}

func TestResolveFunctions_DropsUnsupported(t *testing.T) {
	m := &Manifest{
		Functions: []ManifestFn{
			{Name: "ok", Args: []ManifestArg{{Type: "i32"}}, Returns: ManifestReturn{Type: "i32"}},
			{Name: "bad", Args: []ManifestArg{{Type: "Color"}}, Returns: ManifestReturn{Type: "i32"}},
		},
	}
	var buf bytes.Buffer
	old := stderrSink
	stderrSink = &buf
	defer func() { stderrSink = old }()

	resolved := resolveFunctions(m)
	if len(resolved) != 1 {
		t.Fatalf("got %d resolved fns, want 1", len(resolved))
	}
	if resolved[0].Name != "ok" {
		t.Errorf("kept the wrong fn: %q", resolved[0].Name)
	}
	if !strings.Contains(buf.String(), "skipping `bad`") {
		t.Errorf("expected warning, got: %q", buf.String())
	}
}

func TestResolveSource(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "math.molt.toml")
	zigSrc := filepath.Join(dir, "math.zig")
	if err := os.WriteFile(manifest, []byte("[[fn]]\nname='add'\nargs=['i32']\nreturns='i32'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zigSrc, []byte("export fn add(a: i32) i32 { return a; }"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := ParseManifest(manifest)

	srcPath, lang, ok := ResolveSource(manifest, m, []string{".zig", ".c"})
	if !ok {
		t.Fatal("ResolveSource didn't find sibling .zig")
	}
	if srcPath != zigSrc {
		t.Errorf("ResolveSource = %q, want %q", srcPath, zigSrc)
	}
	if lang != "zig" {
		t.Errorf("lang = %q, want zig", lang)
	}
}

func TestDiscoverKernels(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(dir, "math.molt.toml"),
		filepath.Join(dir, "src", "crypto.molt.toml"),
		filepath.Join(dir, "src", "crypto.zig"),
		filepath.Join(dir, "ignored.txt"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := KernelConfig{
		Paths:            []string{".", "src"},
		ManifestSuffixes: []string{".molt.toml"},
	}
	out, err := DiscoverKernels(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("found %d kernels, want 2: %+v", len(out), out)
	}
	mods := map[string]bool{}
	for _, s := range out {
		if s.Lang != "kernel" {
			t.Errorf("Lang = %q, want kernel", s.Lang)
		}
		mods[s.Module] = true
	}
	for _, m := range []string{"math", "crypto"} {
		if !mods[m] {
			t.Errorf("missing module %q in discovery: %+v", m, mods)
		}
	}
}
