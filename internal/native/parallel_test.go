package native

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunInParallel_PreservesOrder(t *testing.T) {
	inputs := []int{1, 2, 3, 4, 5}
	got, err := runInParallel(inputs, 4, func(i int) (string, error) {
		// Random-ish work duration to encourage out-of-order completion.
		time.Sleep(time.Duration(5-i) * 5 * time.Millisecond)
		return fmt.Sprintf("v%d", i), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"v1", "v2", "v3", "v4", "v5"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

func TestRunInParallel_RunsConcurrently(t *testing.T) {
	const items = 8
	const workers = 4
	const sleep = 50 * time.Millisecond

	start := time.Now()
	_, err := runInParallel(make([]struct{}, items), workers, func(_ struct{}) (int, error) {
		time.Sleep(sleep)
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// Sequential time = items * sleep = 400ms.
	// Parallel with 4 workers = (items/workers) * sleep = 100ms (+ overhead).
	// Anything under 250ms confirms real parallelism.
	if elapsed > 250*time.Millisecond {
		t.Errorf("expected real parallelism: %d items, %d workers, took %v "+
			"(sequential would be %v)",
			items, workers, elapsed, items*int(sleep))
	}
}

func TestRunInParallel_BoundedByJobs(t *testing.T) {
	const items = 10
	const workers = 3
	var inFlight, peak int32
	_, err := runInParallel(make([]struct{}, items), workers, func(_ struct{}) (int, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if peak > workers {
		t.Errorf("peak in-flight = %d, exceeds workers=%d", peak, workers)
	}
}

func TestRunInParallel_FirstErrorReturned(t *testing.T) {
	myErr := errors.New("boom")
	_, err := runInParallel([]int{1, 2, 3, 4}, 2, func(i int) (int, error) {
		if i == 3 {
			return 0, myErr
		}
		return i, nil
	})
	if !errors.Is(err, myErr) {
		t.Errorf("got %v, want %v", err, myErr)
	}
}

func TestRunInParallel_EmptyInput(t *testing.T) {
	got, err := runInParallel([]int{}, 4, func(int) (int, error) { return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestRunInParallel_SingleItemNoGoroutines(t *testing.T) {
	// Single-item path is short-circuited; verify it still returns the
	// right thing.
	got, err := runInParallel([]int{42}, 4, func(i int) (int, error) {
		return i * 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != 84 {
		t.Errorf("got %v, want [84]", got)
	}
}

func TestJobsCount_RespectsEnv(t *testing.T) {
	t.Setenv("MOLT_JOBS", "3")
	if got := jobsCount(); got != 3 {
		t.Errorf("MOLT_JOBS=3, jobsCount() = %d", got)
	}
	t.Setenv("MOLT_JOBS", "")
	// Default path: between 1 and 8 inclusive.
	got := jobsCount()
	if got < 1 || got > 8 {
		t.Errorf("default jobsCount() out of range: %d", got)
	}
}
