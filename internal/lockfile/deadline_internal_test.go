package lockfile

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDeadlineError covers the errno-to-error mapping at the deadline.
// Only genuine contention may wrap ErrHeld: a caller whose answer to
// ErrHeld is "someone else is doing this work, skip" must not skip because
// a signal happened to interrupt the one attempt a zero timeout allows.
func TestDeadlineError(t *testing.T) {
	const path = "/tmp/newsfetch/refresh.lock"
	cases := []struct {
		name string
		last error
		held bool
		// wantWrapped, when set, must be reachable through errors.Is: a
		// non-contention failure has to carry its cause, since ErrHeld is
		// no longer there to explain it.
		wantWrapped error
	}{
		{
			name: "EWOULDBLOCK at deadline is contention",
			last: syscall.EWOULDBLOCK,
			held: true,
		},
		{
			name:        "EINTR at deadline is a real failure",
			last:        syscall.EINTR,
			held:        false,
			wantWrapped: syscall.EINTR,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := deadlineError(path, 30*time.Millisecond, tc.last)
			if err == nil {
				t.Fatal("deadlineError returned nil")
			}
			if got := errors.Is(err, ErrHeld); got != tc.held {
				t.Errorf("errors.Is(err, ErrHeld) = %t, want %t (err: %v)", got, tc.held, err)
			}
			if tc.wantWrapped != nil && !errors.Is(err, tc.wantWrapped) {
				t.Errorf("error %v does not wrap %v", err, tc.wantWrapped)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %v does not name the lock path %q", err, path)
			}
		})
	}
}
