package onboard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/lockfile"
)

// Removable names one file --uninstall may delete. Path is a function
// rather than a resolved string because every owner of these paths
// (config, cache, history, session, feedstate) resolves XDG itself and
// can fail doing it; deferring the call keeps that failure inside
// UninstallFlow's error path instead of forcing main to handle six
// resolution errors while building a list.
//
// Lock resolves the flock sidecar guarding Path, and is nil for files
// nothing locks. It is a field on the guarded entry rather than a roster
// entry of its own because a lock file is never itself removed — it exists
// to be held while Path is unlinked. See removeEntry for both halves of
// that: why unlinking a guarded file without its lock is not enough, and
// why unlinking the lock is worse.
type Removable struct {
	Label string
	Path  func() (string, error)
	Lock  func() (string, error)
}

// uninstallLockTimeout bounds the wait for a state file's sidecar. The
// holders (history.Append, session.Save, feedstate.Update) take their lock
// only for a small read-modify-write, so a quarter second outlasts an
// honest writer while keeping a jammed one from stalling an uninstall the
// user is watching.
const uninstallLockTimeout = 250 * time.Millisecond

// UninstallDeps wires UninstallFlow to its dependencies. Same pattern as
// InitDeps: production fills in real functions, tests inject stubs.
//
// The removable files arrive in three groups because they carry three
// different costs to lose. Config is a minute of retyping, caches rebuild
// themselves on the next fetch, and state is irreplaceable: seen.json is
// the user's dedup memory and feeds.json is up to four weeks of cadence
// observation that has to be re-earned in real time. One question per
// group lets a user keep the expensive one and drop the cheap ones.
type UninstallDeps struct {
	Shell func() (Shell, error)
	Out   io.Writer
	// Confirm asks the user a yes/no question, once per group that has
	// at least one file on disk. nil (or one that always returns false)
	// preserves the legacy "leave files in place" behaviour — important
	// for non-TTY invocations where prompting would hang.
	Confirm func(prompt string) bool
	Config  Removable
	Caches  []Removable
	// State holds the irreplaceable files. Callers that cannot show a
	// human the prompt leave this empty, which skips the group entirely
	// — see runUninstall in cmd/newsfetch.
	State []Removable
}

// UninstallFlow removes the newsfetch block from the user's rc file, if
// present, then walks the config / caches / state groups offering to
// delete what it finds. Files the user declines to remove have their
// paths printed so they can `rm` them later. Safe to re-run: missing rc
// file, absent block, or already-clean state all succeed with an
// explanatory line.
func UninstallFlow(d UninstallDeps) error {
	sh, err := d.Shell()
	if err != nil {
		return err
	}
	// Resolve every path before touching anything: a broken XDG
	// environment should abort the whole run, not delete half a roster.
	config, err := resolveGroup([]Removable{d.Config})
	if err != nil {
		return err
	}
	caches, err := resolveGroup(d.Caches)
	if err != nil {
		return err
	}
	state, err := resolveGroup(d.State)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(sh.RCPath)
	switch {
	case err == nil:
		updated, changed := Remove(string(data))
		if !changed {
			fmt.Fprintf(d.Out, "newsfetch: no block found in %s (nothing to remove)\n", sh.RCPath)
		} else {
			if err := atomicWrite(sh.RCPath, []byte(updated), 0o644); err != nil {
				return fmt.Errorf("write rc: %w", err)
			}
			fmt.Fprintf(d.Out, "newsfetch: removed block from %s\n", sh.RCPath)
		}
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(d.Out, "newsfetch: no rc file at %s (nothing to remove)\n", sh.RCPath)
	default:
		return fmt.Errorf("read rc: %w", err)
	}

	maybeRemoveGroup(d, "config", config)
	maybeRemoveGroup(d, "caches", caches)
	maybeRemoveGroup(d, "state", state)
	return nil
}

// resolved is one roster entry with its path — and its sidecar, when it
// has one — already resolved. lock is "" for an unguarded file.
type resolved struct {
	label string
	path  string
	lock  string
}

// resolveGroup turns a roster group into resolved entries. An entry with
// a nil Path is skipped rather than dereferenced: a zero-value Removable
// means "this caller has nothing here", and panicking on it would break
// the no-panics-in-library-code rule for a caller mistake we can absorb.
// A nil Lock is the ordinary case for config and caches.
func resolveGroup(items []Removable) ([]resolved, error) {
	out := make([]resolved, 0, len(items))
	for _, it := range items {
		if it.Path == nil {
			continue
		}
		p, err := it.Path()
		if err != nil {
			return nil, fmt.Errorf("resolve %s path: %w", it.Label, err)
		}
		r := resolved{label: it.Label, path: p}
		if it.Lock != nil {
			l, err := it.Lock()
			if err != nil {
				return nil, fmt.Errorf("resolve %s lock path: %w", it.Label, err)
			}
			r.lock = l
		}
		out = append(out, r)
	}
	return out, nil
}

// maybeRemoveGroup asks once for the whole group and removes every entry in
// it on a yes. Entries with nothing on disk are filtered out first, so a
// group with no files asks nothing and prints nothing — a fresh machine's
// uninstall stays quiet. Presence is the data file alone: no uninstall
// path removes a lock file (removeEntry says why), so a sidecar left
// behind by a crash with no data file beside it would buy only a prompt
// about a file that is already gone, answered yes, that removes nothing.
// Without a Confirm the files are left in place and their paths printed,
// matching the original non-interactive behaviour.
func maybeRemoveGroup(d UninstallDeps, group string, items []resolved) {
	present := make([]resolved, 0, len(items))
	for _, it := range items {
		if exists(it.path) {
			present = append(present, it)
		}
	}
	if len(present) == 0 {
		return
	}
	if d.Confirm == nil || !d.Confirm(groupPrompt(group, present)) {
		for _, it := range present {
			fmt.Fprintf(d.Out, "newsfetch: %s left in place at %s (rm to remove)\n", it.label, it.path)
		}
		return
	}
	for _, it := range present {
		removeEntry(d, it)
	}
}

// exists reports whether path names something on disk. The empty path is
// never anything, so a resolver that hands back "" cannot make a group
// look occupied.
func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// removeEntry deletes one roster entry's data file. An unguarded file is
// unlinked directly. A guarded one is not, because os.Remove unlinks a
// name and does nothing to a process that already holds the file open
// under an flock: that process keeps writing to the now-orphaned inode,
// and the next writer creates and locks a brand-new file at the same path.
// The user sees the state they just deleted reappear. The window is real —
// the statusline takes sessions.lock on every terminal prompt — so the
// sidecar is acquired first and held across the unlink, released only by
// the deferred Close.
//
// The sidecar itself is never unlinked, and an earlier version of this
// function that did so was wrong in a way no ordering could fix. A lock
// file's path IS its identity: os.OpenFile(path, O_CREATE, ...) on a name
// that no longer exists creates a fresh inode, so the instant the path is
// gone another process can create and lock a new file there while this one
// still holds the original and believes it has exclusive access. Removing
// the data file first only moves that window; it does not close it. The
// sidecars are zero bytes, and leaving an empty file behind is a trivial
// cost against silently breaking the mutual exclusion every writer of the
// guarded file depends on. Callers tell the user what was left: see
// printKeptLocks in cmd/newsfetch.
//
// Contention is not failure: lockfile.ErrHeld means another newsfetch is
// mid-write, and deleting under it is exactly the race this exists to
// avoid. The entry is reported by name and left intact so a rerun finishes
// the job. Every other Acquire error is a real fault (unopenable lock,
// filesystem without flock) and warns, per the same degrade-don't-abort
// rule the removals use: uninstall has already reverted the rc block by
// the time we get here.
func removeEntry(d UninstallDeps, it resolved) {
	if it.lock == "" {
		removeOne(d, it.label, it.path)
		return
	}
	lock, err := lockfile.Acquire(it.lock, uninstallLockTimeout)
	switch {
	case errors.Is(err, lockfile.ErrHeld):
		fmt.Fprintf(d.Out, "newsfetch: %s at %s is in use by another newsfetch process; left in place (rerun --uninstall to remove it)\n", it.label, it.path)
		return
	case err != nil:
		fmt.Fprintf(d.Out, "newsfetch: warning: could not lock %s at %s: %v\n", it.label, it.lock, err)
		return
	}
	defer lock.Close()
	removeOne(d, it.label, it.path)
}

// removeOne unlinks path and reports what happened. An already-absent file
// is silent: another newsfetch may have removed it between the presence
// check and here, and "could not remove seen.json" would be a lie there.
func removeOne(d UninstallDeps, label, path string) {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		fmt.Fprintf(d.Out, "newsfetch: warning: could not remove %s at %s: %v\n", label, path, err)
		return
	}
	fmt.Fprintf(d.Out, "newsfetch: removed %s at %s\n", label, path)
}

// groupPrompt names the files the answer covers. A bare "Remove state?"
// would ask the user to consent to a list they cannot see, which for the
// state group means consenting to lose their dedup history without being
// told that is what state means.
func groupPrompt(group string, present []resolved) string {
	labels := make([]string, len(present))
	for i, it := range present {
		labels[i] = it.label
	}
	return fmt.Sprintf("Remove %s (%s)?", group, strings.Join(labels, ", "))
}
