package native

import (
	"strings"
	"testing"
)

func TestUserReferencesPyO3(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"clean", "#[pyfunction]\nfn x() {}", false},
		{"with-use", "use pyo3::prelude::*;\nfn x() {}", true},
		{"qualified", "fn x() -> pyo3::PyResult<()> { Ok(()) }", true},
		{"line-comment", "// use pyo3::prelude::*;\nfn x() {}", false},
		{"block-comment", "/* mentions pyo3 here */\nfn x() {}", false},
		{"string-literal", "fn x() { let s = \"pyo3\"; }", false},
		{"char-literal", "fn x() { let c = 'p'; }", false},
		{"after-comment", "// hi\nuse pyo3::prelude::*;\n", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		got := userReferencesPyO3([]byte(tc.src))
		if got != tc.want {
			t.Errorf("%s: got %v, want %v\nsrc=%q", tc.name, got, tc.want, tc.src)
		}
	}
}

func TestParseTopLevelFnName(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"fn xor_bytes(...) {}", "xor_bytes", true},
		{"pub fn mul(a: f32) -> f32 { a }", "mul", true},
		{"pub(crate) fn helper() {}", "helper", true},
		{"async fn run() {}", "run", true},
		{"unsafe fn raw() {}", "raw", true},
		{"pub async unsafe fn many() {}", "many", true},
		{`extern "C" fn cb() {}`, "cb", true},
		{"    fn nested() {}", "", false},          // indented = nested
		{"\tfn nested() {}", "", false},            // tab indent
		{"fnord(); // not a fn", "", false},
		{"// fn not_a_decl()", "", false},
		{"let x = 1;", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := parseTopLevelFnName(tc.line)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got (%q,%v), want (%q,%v)", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

func TestInjectPyO3Attrs_MinimalSource(t *testing.T) {
	src := `fn xor_bytes(data: Vec<u8>, key: u8) -> Vec<u8> {
    data.iter().map(|b| b ^ key).collect()
}

fn mul(a: f32, b: f32) -> f32 {
    a * b
}

fn crypto(m: &Bound<'_, PyModule>) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(xor_bytes, m)?)?;
    m.add_function(wrap_pyfunction!(mul, m)?)?;
    Ok(())
}
`
	got := string(injectPyO3Attrs([]byte(src), "crypto"))

	// xor_bytes and mul must get #[pyfunction]; crypto must get #[pymodule].
	if !strings.Contains(got, "#[pyfunction]\nfn xor_bytes") {
		t.Errorf("missing #[pyfunction] on xor_bytes:\n%s", got)
	}
	if !strings.Contains(got, "#[pyfunction]\nfn mul") {
		t.Errorf("missing #[pyfunction] on mul:\n%s", got)
	}
	if !strings.Contains(got, "#[pymodule]\nfn crypto") {
		t.Errorf("missing #[pymodule] on crypto:\n%s", got)
	}
	// Must NOT add #[pyfunction] on the module fn.
	if strings.Contains(got, "#[pyfunction]\nfn crypto") {
		t.Errorf("incorrectly tagged crypto with #[pyfunction]:\n%s", got)
	}
}

func TestInjectPyO3Attrs_RespectsExistingAttrs(t *testing.T) {
	src := `#[pyfunction]
fn already_decorated() {}

#[allow(dead_code)]
fn user_helper() {}

fn auto_added() {}
`
	got := string(injectPyO3Attrs([]byte(src), "mod"))

	// already_decorated keeps its single #[pyfunction], no duplication.
	if c := strings.Count(got, "#[pyfunction]"); c != 2 {
		t.Errorf("want exactly 2 #[pyfunction] (one preserved + one auto), got %d:\n%s", c, got)
	}
	// user_helper has #[allow(dead_code)], so molt MUST NOT add #[pyfunction].
	idx := strings.Index(got, "fn user_helper")
	if idx < 0 {
		t.Fatalf("user_helper missing")
	}
	upTo := got[:idx]
	if strings.HasSuffix(strings.TrimSpace(upTo), "#[pyfunction]") {
		t.Errorf("incorrectly added #[pyfunction] to user_helper:\n%s", got)
	}
	// auto_added gets #[pyfunction] auto-injected.
	if !strings.Contains(got, "#[pyfunction]\nfn auto_added") {
		t.Errorf("missing #[pyfunction] on auto_added:\n%s", got)
	}
}

func TestInjectPyO3Attrs_NestedFnSkipped(t *testing.T) {
	src := `fn outer() {
    fn nested() {}
}
`
	got := string(injectPyO3Attrs([]byte(src), "mymod"))
	// outer gets #[pyfunction]; nested must not.
	if !strings.Contains(got, "#[pyfunction]\nfn outer") {
		t.Errorf("missing on outer:\n%s", got)
	}
	if strings.Contains(got, "#[pyfunction]\n    fn nested") ||
		strings.Contains(got, "#[pyfunction]\nfn nested") {
		t.Errorf("incorrectly tagged nested fn:\n%s", got)
	}
}
