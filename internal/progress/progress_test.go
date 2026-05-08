package progress

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{int64(2.5 * 1024 * 1024), "2.5 MB"},
		{int64(2.5 * 1024 * 1024 * 1024), "2.5 GB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderBar(t *testing.T) {
	cases := []struct {
		pct   float64
		width int
	}{
		{0.0, 10},
		{0.5, 10},
		{1.0, 10},
		{-0.5, 10}, // clamped
		{2.0, 10},  // clamped
	}
	for _, tc := range cases {
		s := renderBar(tc.pct, tc.width)
		if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
			t.Errorf("bar should be bracketed: %q", s)
		}
		// Each cell is one rune wide, so length-as-runes excluding [ ] = width.
		runes := []rune(s)
		if len(runes) != tc.width+2 {
			t.Errorf("bar width = %d, want %d (%q)", len(runes)-2, tc.width, s)
		}
	}
}

func TestDownload_BytesAccumulate(t *testing.T) {
	// We can't test the rendered output without a TTY, but we can
	// confirm the byte counter advances as the wrapped reader is drained.
	d := &Download{label: "test", total: 100, isTTY: false}
	src := bytes.NewReader(make([]byte, 100))
	wrapped := d.Wrap(src)
	if _, err := io.Copy(io.Discard, wrapped); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&d.read); got != 100 {
		t.Errorf("read counter = %d, want 100", got)
	}
}

func TestStream_DisabledFallsBackToCombinedOutput(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo hello")
	out, err := Stream(cmd, "  [test] ", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("captured output missing 'hello': %q", string(out))
	}
}

func TestStream_CapturesOutputWhileStreaming(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo line-one; echo line-two")
	out, err := Stream(cmd, "  [test] ", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "line-one") || !strings.Contains(string(out), "line-two") {
		t.Errorf("captured: %q", string(out))
	}
}

func TestStream_PropagatesError(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo nope; exit 7")
	out, err := Stream(cmd, "  [test] ", true)
	if err == nil {
		t.Fatal("expected error from exit 7")
	}
	if !strings.Contains(string(out), "nope") {
		t.Errorf("captured output missing 'nope': %q", string(out))
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("expected *exec.ExitError, got %T: %v", err, err)
	}
}
