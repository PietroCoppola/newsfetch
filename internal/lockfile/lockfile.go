// Package lockfile provides a bounded-wait exclusive advisory lock backed
// by flock, shared by any package that needs to serialize a
// read-modify-write against a state file (internal/history, internal/session).
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Acquire takes an exclusive advisory lock on path, waiting at most
// timeout for a holder to release it. Two flock realities shape the loop:
// a blocking LOCK_EX can return EINTR when a signal lands (Go's
// async-preemption SIGURG makes that routine, which is why cmd/go's
// filelock retries it), so the non-blocking form is polled instead; and
// the kernel drops the lock when the file closes — crashed holders
// included — so callers release by closing the returned file and no
// stale-lock recovery exists. The deadline bounds EINTR retries just as
// it bounds contention retries: a signal storm cannot spin past timeout.
// The first attempt is unconditional, so a zero timeout still tries once.
func Acquire(path string, timeout time.Duration) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return lock, nil
		case errors.Is(err, syscall.EINTR), errors.Is(err, syscall.EWOULDBLOCK):
			if time.Now().After(deadline) {
				lock.Close()
				return nil, fmt.Errorf("lock %s: held elsewhere for over %s", path, timeout)
			}
			if errors.Is(err, syscall.EWOULDBLOCK) {
				// Held by someone else; back off before re-polling.
				// EINTR resolved nothing, so it retries immediately.
				time.Sleep(2 * time.Millisecond)
			}
		default:
			lock.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
	}
}
