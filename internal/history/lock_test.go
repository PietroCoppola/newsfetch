package history

import (
	"path/filepath"
	"testing"
	"time"
)

// TestAcquireLock_GivesUpWhenHeld pins the bounded-wait contract: a lock
// held by someone else (a stopped process, hung disk I/O) must produce an
// error after the timeout, never an indefinite block — the render path
// runs on every terminal open and a hung shell is worse than one lost
// history entry.
func TestAcquireLock_GivesUpWhenHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.lock")
	held, err := acquireLock(path, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer held.Close()
	if second, err := acquireLock(path, 30*time.Millisecond); err == nil {
		second.Close()
		t.Fatal("expected timeout error while lock is held elsewhere")
	}
}

// TestAcquireLock_ReacquiresAfterRelease pins the release-by-close
// contract the callers rely on.
func TestAcquireLock_ReacquiresAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.lock")
	held, err := acquireLock(path, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	held.Close()
	again, err := acquireLock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("re-acquire after close: %v", err)
	}
	again.Close()
}
