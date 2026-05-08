package kernelbuilder

import (
	"reflect"
	"testing"
)

func TestResolve_KnownTokens(t *testing.T) {
	got := Resolve(
		"{cc} -c -O2 -fPIC -o {output} {source}",
		map[string]string{
			"cc":     "/usr/bin/cc",
			"source": "/abs/foo.c",
			"output": "/abs/foo.o",
		},
	)
	want := "/usr/bin/cc -c -O2 -fPIC -o /abs/foo.o /abs/foo.c"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestResolve_UnknownTokenPassesThrough(t *testing.T) {
	got := Resolve("{cc} {mystery} {source}", map[string]string{
		"cc": "cc", "source": "x.c",
	})
	if got != "cc {mystery} x.c" {
		t.Errorf("unexpected: %q", got)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"cc -c x.c", []string{"cc", "-c", "x.c"}},
		{"  cc   -c   x.c  ", []string{"cc", "-c", "x.c"}},
		{`cc "long path/x.c" -o out`, []string{"cc", "long path/x.c", "-o", "out"}},
		{"", nil},
	}
	for _, tc := range cases {
		got := SplitCommand(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDefaultBuildersIsACopy(t *testing.T) {
	a := DefaultBuilders()
	a[0].Command = "tampered"
	b := DefaultBuilders()
	if b[0].Command == "tampered" {
		t.Errorf("DefaultBuilders should return a fresh copy")
	}
}

func TestSuggestedTemplatesCoversCommonLanguages(t *testing.T) {
	for _, lang := range []string{"zig", "c", "cpp", "odin", "nim", "rs", "f90"} {
		if _, ok := SuggestedTemplates[lang]; !ok {
			t.Errorf("missing suggested template for %q", lang)
		}
	}
}
