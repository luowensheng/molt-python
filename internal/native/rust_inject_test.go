package native

import "testing"

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
