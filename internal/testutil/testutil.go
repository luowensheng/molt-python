// Package testutil provides minimal assertion helpers to avoid external deps.
package testutil

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
)

func caller() string {
	_, file, line, ok := runtime.Caller(2)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

func NoError(t *testing.T, err error, msg ...string) {
	t.Helper()
	if err != nil {
		label := ""
		if len(msg) > 0 {
			label = msg[0] + ": "
		}
		t.Fatalf("%s%sunexpected error: %v", caller(), label, err)
	}
}

func Error(t *testing.T, err error, msg ...string) {
	t.Helper()
	if err == nil {
		label := ""
		if len(msg) > 0 {
			label = msg[0] + ": "
		}
		t.Fatalf("%s%sexpected an error but got nil", caller(), label)
	}
}

func Equal(t *testing.T, expected, actual interface{}, msg ...string) {
	t.Helper()
	if fmt.Sprintf("%v", expected) != fmt.Sprintf("%v", actual) {
		label := ""
		if len(msg) > 0 {
			label = msg[0] + ": "
		}
		t.Fatalf("%s%sexpected %v, got %v", caller(), label, expected, actual)
	}
}

func NotEmpty(t *testing.T, v string, msg ...string) {
	t.Helper()
	if v == "" {
		label := ""
		if len(msg) > 0 {
			label = msg[0] + ": "
		}
		t.Fatalf("%s%sexpected non-empty string", caller(), label)
	}
}

func True(t *testing.T, cond bool, msg ...string) {
	t.Helper()
	if !cond {
		label := "condition is false"
		if len(msg) > 0 {
			label = msg[0]
		}
		t.Fatalf("%s: %s", caller(), label)
	}
}

func False(t *testing.T, cond bool, msg ...string) {
	t.Helper()
	if cond {
		label := "condition is true"
		if len(msg) > 0 {
			label = msg[0]
		}
		t.Fatalf("%s: %s", caller(), label)
	}
}

func Contains(t *testing.T, s, sub string, msg ...string) {
	t.Helper()
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return
		}
	}
	label := ""
	if len(msg) > 0 {
		label = msg[0] + ": "
	}
	t.Fatalf("%s%s%q not found in %q", caller(), label, sub, s)
}

func Greater(t *testing.T, a, b int64, msg ...string) {
	t.Helper()
	if a <= b {
		label := ""
		if len(msg) > 0 {
			label = msg[0] + ": "
		}
		t.Fatalf("%s%sexpected %d > %d", caller(), label, a, b)
	}
}

func Len(t *testing.T, n int, actual int, msg ...string) {
	t.Helper()
	if actual != n {
		label := ""
		if len(msg) > 0 {
			label = msg[0] + ": "
		}
		t.Fatalf("%s%sexpected length %d, got %d", caller(), label, n, actual)
	}
}
