// Package progress renders progress for long-running molt operations:
// HTTP downloads (zig install today, anything else later) and subprocess
// builds (cargo, cython+cc, zig build-obj, kernel link, …).
//
// All output is TTY-aware: on a real terminal we use \r to update a
// single line in place; on a pipe / CI log / redirect we degrade to
// once-per-N-bytes plain text so log readers stay sane.
//
// No external dependencies — uses only the stdlib (os.ModeCharDevice
// for TTY detection).
package progress

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// IsTerminal reports whether the given file descriptor is a terminal.
// Uses os.ModeCharDevice — pure stdlib, no x/term dependency.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// stdoutMu serialises stdout writes from progress-related goroutines so
// concurrent builds don't garble each other's lines. Public so other
// molt packages can lock around custom prints if they need to.
var stdoutMu sync.Mutex

// Lock returns the global stdout lock; callers can use it to bracket
// their own prints with progress output.
func Lock()   { stdoutMu.Lock() }
func Unlock() { stdoutMu.Unlock() }

// ── Download progress ───────────────────────────────────────────────────────

// Download wraps an io.Reader to render download progress. Construct
// with NewDownload(label, totalBytes) where totalBytes is the
// Content-Length (0 if unknown — we degrade to "X MB downloaded").
//
// On TTY: a single \r-updated line at most every 100ms.
// On non-TTY: a one-shot "starting/done" pair so log files don't fill
// with spinner noise.
type Download struct {
	label  string
	total  int64
	read   int64
	start  time.Time
	last   atomic.Int64 // unix-nano of last render
	isTTY  bool
	closed atomic.Bool
}

// NewDownload constructs a Download progress renderer.
func NewDownload(label string, total int64) *Download {
	d := &Download{
		label: label,
		total: total,
		start: time.Now(),
		isTTY: IsTerminal(os.Stderr),
	}
	if d.isTTY {
		d.render(false)
	} else {
		size := "unknown size"
		if total > 0 {
			size = humanBytes(total)
		}
		fmt.Fprintf(os.Stderr, "  %s (%s) …\n", d.label, size)
	}
	return d
}

// Wrap returns a Reader that updates the progress bar as it's drained.
func (d *Download) Wrap(r io.Reader) io.Reader { return &dlReader{d: d, r: r} }

// Finish prints the final state and flushes the line.
func (d *Download) Finish() {
	if d.closed.Swap(true) {
		return
	}
	elapsed := time.Since(d.start).Round(100 * time.Millisecond)
	if d.isTTY {
		fmt.Fprintf(os.Stderr, "\r\033[K  ✓ %s (%s in %s)\n",
			d.label, humanBytes(d.read), elapsed)
	} else {
		fmt.Fprintf(os.Stderr, "  ✓ %s (%s in %s)\n",
			d.label, humanBytes(d.read), elapsed)
	}
}

func (d *Download) render(force bool) {
	if !d.isTTY {
		return
	}
	now := time.Now().UnixNano()
	if !force {
		// Throttle to ~10 fps.
		last := d.last.Load()
		if now-last < int64(100*time.Millisecond) {
			return
		}
		if !d.last.CompareAndSwap(last, now) {
			return
		}
	}
	read := atomic.LoadInt64(&d.read)
	elapsed := time.Since(d.start).Seconds()
	rate := float64(read) / elapsed

	stdoutMu.Lock()
	defer stdoutMu.Unlock()

	if d.total > 0 {
		pct := float64(read) / float64(d.total)
		bar := renderBar(pct, 24)
		eta := ""
		if rate > 0 {
			remain := float64(d.total-read) / rate
			eta = fmt.Sprintf("  ETA %ds", int(remain))
		}
		fmt.Fprintf(os.Stderr,
			"\r\033[K  %s  %s  %s / %s  %.0f%%  %s/s%s",
			d.label, bar,
			humanBytes(read), humanBytes(d.total),
			pct*100, humanBytes(int64(rate)), eta)
	} else {
		fmt.Fprintf(os.Stderr,
			"\r\033[K  %s  %s  %s/s",
			d.label, humanBytes(read), humanBytes(int64(rate)))
	}
}

type dlReader struct {
	d *Download
	r io.Reader
}

func (dr *dlReader) Read(p []byte) (int, error) {
	n, err := dr.r.Read(p)
	if n > 0 {
		atomic.AddInt64(&dr.d.read, int64(n))
		dr.d.render(false)
	}
	return n, err
}

// renderBar draws a unicode block-element bar. width = total cells.
func renderBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	} else if pct > 1 {
		pct = 1
	}
	full := int(pct * float64(width))
	var b []byte
	b = append(b, '[')
	for i := 0; i < full; i++ {
		b = append(b, []byte("▰")...)
	}
	for i := full; i < width; i++ {
		b = append(b, []byte("░")...)
	}
	b = append(b, ']')
	return string(b)
}

func humanBytes(n int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ── Subprocess streaming ────────────────────────────────────────────────────

// Stream runs `cmd` with stdout+stderr piped through a line-prefixing
// writer that prints to stdout in real time AND captures the entire
// output to a returned []byte for use in error messages.
//
// `prefix` is added to every line — typically "    [<module>] " so the
// user can attribute interleaved output from parallel builds.
//
// `stream` controls whether to print at all (when false, we just
// capture and return — equivalent to CombinedOutput).
//
// Returns (capturedOutput, err). err is nil only if the command exited
// successfully; capturedOutput is populated either way so the caller
// can include it in a wrapping error message.
func Stream(cmd *exec.Cmd, prefix string, stream bool) ([]byte, error) {
	if !stream {
		return cmd.CombinedOutput()
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(pr)
		// Some build tools emit very long lines (cargo's vendored crate
		// list can exceed 64 KB); raise the limit so we don't drop them.
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')
			stdoutMu.Lock()
			fmt.Println(prefix + line)
			stdoutMu.Unlock()
		}
	}()

	startErr := cmd.Start()
	if startErr != nil {
		_ = pw.Close()
		<-done
		return buf.Bytes(), startErr
	}
	waitErr := cmd.Wait()
	_ = pw.Close()
	<-done
	return buf.Bytes(), waitErr
}
