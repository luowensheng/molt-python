//go:build windows

package store

import (
	"os"
	"time"
)

// acquireLock implements a spin-based exclusive lock for Windows, where
// syscall.Flock is not available. It repeatedly tries to create a
// <path>.lck file with O_EXCL until it succeeds or times out (30 s).
// The returned release function removes the lock file.
//
// This is intentionally simple: Windows is a supported build target but
// not the primary development platform. For production-grade locking
// consider golang.org/x/sys/windows.LockFileEx.
func acquireLock(path string) (release func(), err error) {
	lockFile := path + ".lck"
	deadline := time.Now().Add(30 * time.Second)
	for {
		f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lockFile) }, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
