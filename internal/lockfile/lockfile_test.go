package lockfile_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/lockfile"
)

// TestAcquire_GivesUpWhenHeld pins the bounded-wait contract: a lock
// held by someone else (a stopped process, hung disk I/O) must produce an
// error after the timeout, never an indefinite block — the render path
// runs on every terminal open and a hung shell is worse than one lost
// history entry.
func TestAcquire_GivesUpWhenHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.lock")
	held, err := lockfile.Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer held.Close()
	if second, err := lockfile.Acquire(path, 30*time.Millisecond); err == nil {
		second.Close()
		t.Fatal("expected timeout error while lock is held elsewhere")
	}
}

// TestAcquire_ReacquiresAfterRelease pins the release-by-close
// contract the callers rely on.
func TestAcquire_ReacquiresAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.lock")
	held, err := lockfile.Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	held.Close()
	again, err := lockfile.Acquire(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("re-acquire after close: %v", err)
	}
	again.Close()
}
