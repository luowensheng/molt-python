package native

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
)

// progressMu serialises one-line progress markers (`↻ rust foo`, `✓ cython
// bar (cached)`, etc.) so they never interleave when multiple build
// workers run in parallel. Build commands themselves capture stdout/stderr
// via CombinedOutput so their output is always atomic per-build.
var progressMu sync.Mutex

// progressLine prints one progress marker under the mutex.
func progressLine(format string, args ...any) {
	progressMu.Lock()
	defer progressMu.Unlock()
	fmt.Printf(format, args...)
}

// jobsCount returns the maximum number of native builds to run in
// parallel. Honoured: $MOLT_JOBS (positive integer). Otherwise:
// min(GOMAXPROCS, 8). The cap exists because cargo builds internally
// parallelise too — running 16 of those at once on a 4-core machine
// thrashes the scheduler.
func jobsCount() int {
	if v := os.Getenv("MOLT_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// runInParallel applies `build` to every input concurrently with at most
// `jobs` workers in flight, then returns results in the original input
// order. The first error encountered is returned after all queued
// builds finish — we don't cancel pending builds because each one is
// content-keyed and its result is useful even if an unrelated source
// later fails.
//
// Single-input case is short-circuited (no goroutines spawned) so small
// projects pay zero parallelism overhead.
func runInParallel[T any, R any](inputs []T, jobs int, build func(T) (R, error)) ([]R, error) {
	results := make([]R, len(inputs))
	if len(inputs) == 0 {
		return results, nil
	}
	if len(inputs) == 1 || jobs <= 1 {
		for i, in := range inputs {
			r, err := build(in)
			if err != nil {
				return nil, err
			}
			results[i] = r
		}
		return results, nil
	}
	if jobs > len(inputs) {
		jobs = len(inputs)
	}

	type job struct {
		idx int
		in  T
	}
	type res struct {
		idx int
		r   R
		err error
	}

	jobsCh := make(chan job, len(inputs))
	resCh := make(chan res, len(inputs))

	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobsCh {
				r, err := build(j.in)
				resCh <- res{idx: j.idx, r: r, err: err}
			}
		}()
	}

	for i, in := range inputs {
		jobsCh <- job{idx: i, in: in}
	}
	close(jobsCh)

	go func() { wg.Wait(); close(resCh) }()

	var firstErr error
	for r := range resCh {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		results[r.idx] = r.r
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}
