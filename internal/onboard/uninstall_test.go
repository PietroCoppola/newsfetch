package onboard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/lockfile"
)

// fixedRemovable returns a Removable whose Path resolves to a constant, so
// no uninstall test ever resolves a real XDG directory.
func fixedRemovable(label, path string) Removable {
	return Removable{Label: label, Path: func() (string, error) { return path, nil }}
}

// lockedRemovable is fixedRemovable plus the flock sidecar guarding path —
// the shape every state entry has in production.
func lockedRemovable(label, path, lock string) Removable {
	r := fixedRemovable(label, path)
	r.Lock = func() (string, error) { return lock, nil }
	return r
}

// uninstallTree is one temp directory holding a file for every entry in
// the production roster, plus an rc file carrying the newsfetch block.
// Grouping the paths the way UninstallDeps groups them keeps the
// per-group assertions honest. state and stateLocks are parallel slices:
// stateLocks[i] is the sidecar attached to state[i], never a roster entry
// of its own, so it is removed under its own lock and never named in
// output.
type uninstallTree struct {
	rcPath     string
	config     string
	caches     []string
	state      []string
	stateLocks []string
}

// all returns every path the flow reports on by label, config first —
// the sidecars are deliberately absent.
func (u uninstallTree) all() []string {
	paths := []string{u.config}
	paths = append(paths, u.caches...)
	paths = append(paths, u.state...)
	return paths
}

// everyFile is all() plus the sidecars: what must exist before, and be
// gone after, a fully confirmed uninstall.
func (u uninstallTree) everyFile() []string {
	return append(u.all(), u.stateLocks...)
}

// newUninstallTree lays out the tree and returns deps already wired to it.
// withFiles=false creates the rc file only, which is how the "absent files
// ask nothing" case is set up.
func newUninstallTree(t *testing.T, withFiles bool) (uninstallTree, *bytes.Buffer, UninstallDeps) {
	t.Helper()
	dir := t.TempDir()
	tree := uninstallTree{
		rcPath: filepath.Join(dir, ".bashrc"),
		config: filepath.Join(dir, "config.toml"),
		caches: []string{
			filepath.Join(dir, "feed.json"),
			filepath.Join(dir, "following.json"),
			filepath.Join(dir, "refresh.log"),
			filepath.Join(dir, "refresh.lock"),
		},
		state: []string{
			filepath.Join(dir, "seen.json"),
			filepath.Join(dir, "sessions.json"),
			filepath.Join(dir, "feeds.json"),
		},
		stateLocks: []string{
			filepath.Join(dir, "seen.lock"),
			filepath.Join(dir, "sessions.lock"),
			filepath.Join(dir, "feeds.lock"),
		},
	}
	if err := os.WriteFile(tree.rcPath, []byte(BeginMarker+"\nnewsfetch\n"+EndMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withFiles {
		for _, p := range tree.everyFile() {
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	caches := make([]Removable, len(tree.caches))
	for i, p := range tree.caches {
		caches[i] = fixedRemovable(filepath.Base(p), p)
	}
	state := make([]Removable, len(tree.state))
	for i, p := range tree.state {
		state[i] = lockedRemovable(filepath.Base(p), p, tree.stateLocks[i])
	}
	out := &bytes.Buffer{}
	deps := UninstallDeps{
		Shell:  func() (Shell, error) { return Shell{Name: "bash", RCPath: tree.rcPath}, nil },
		Out:    out,
		Config: fixedRemovable("config.toml", tree.config),
		Caches: caches,
		State:  state,
	}
	return tree, out, deps
}

func TestUninstallFlow_StripsBlock(t *testing.T) {
	tree, _, deps := newUninstallTree(t, false)
	rcOrig := "# rc\nalias ll='ls -l'\n"
	if err := os.WriteFile(tree.rcPath, []byte(rcOrig+"\n"+BeginMarker+"\nnewsfetch\n"+EndMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	got, _ := os.ReadFile(tree.rcPath)
	if strings.Contains(string(got), BeginMarker) {
		t.Errorf("block still present:\n%s", got)
	}
	if !strings.Contains(string(got), "alias ll='ls -l'") {
		t.Errorf("rc content lost:\n%s", got)
	}
}

func TestUninstallFlow_NoBlockIsNoOp(t *testing.T) {
	tree, out, deps := newUninstallTree(t, false)
	original := "# rc\nalias ll='ls -l'\n"
	if err := os.WriteFile(tree.rcPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	got, _ := os.ReadFile(tree.rcPath)
	if string(got) != original {
		t.Errorf("rc modified despite no block:\ngot:\n%s", got)
	}
	if !strings.Contains(out.String(), "nothing") && !strings.Contains(out.String(), "no block") {
		t.Errorf("output should explain no-op; got:\n%s", out.String())
	}
}

func TestUninstallFlow_MissingRCIsNoOp(t *testing.T) {
	tree, _, deps := newUninstallTree(t, false)
	if err := os.Remove(tree.rcPath); err != nil {
		t.Fatal(err)
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow should succeed when rc missing: %v", err)
	}
	if _, err := os.Stat(tree.rcPath); !os.IsNotExist(err) {
		t.Errorf("uninstall created rc file; want it left absent")
	}
}

// TestUninstallFlow_ConfirmYesRemovesEveryGroup replaces the old
// TestUninstallFlow_ConfirmYesRemovesConfigAndCache, which asserted two
// Confirm calls. The roster is now three groups (config / caches /
// state), so a tree with a present file in each must ask exactly three
// questions and delete everything it asked about.
func TestUninstallFlow_ConfirmYesRemovesEveryGroup(t *testing.T) {
	tree, _, deps := newUninstallTree(t, true)
	prompts := 0
	deps.Confirm = func(prompt string) bool {
		prompts++
		return true
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	if prompts != 3 {
		t.Errorf("Confirm calls = %d, want 3 (config + caches + state)", prompts)
	}
	// everyFile, not all: the state sidecars are removed under their own
	// lock rather than as roster entries, and must still be gone.
	for _, p := range tree.everyFile() {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", p)
		}
	}
}

func TestUninstallFlow_ConfirmNoLeavesFilesInPlace(t *testing.T) {
	tree, out, deps := newUninstallTree(t, true)
	deps.Confirm = func(prompt string) bool { return false }
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	for _, p := range tree.all() {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should remain after declined prompt: %v", p, err)
		}
		if !strings.Contains(out.String(), p) {
			t.Errorf("declined path %s not reported:\n%s", p, out.String())
		}
	}
}

// TestUninstallFlow_AbsentFilesAskNothing pins the stat-then-skip guard:
// a group with no files on disk must not prompt and must not print. This
// is what keeps a fresh machine's uninstall quiet.
func TestUninstallFlow_AbsentFilesAskNothing(t *testing.T) {
	tree, out, deps := newUninstallTree(t, false)
	prompts := 0
	deps.Confirm = func(prompt string) bool {
		prompts++
		return true
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	if prompts != 0 {
		t.Errorf("Confirm calls = %d, want 0 (no roster file exists)", prompts)
	}
	for _, p := range tree.all() {
		if strings.Contains(out.String(), p) {
			t.Errorf("absent path %s should not be mentioned:\n%s", p, out.String())
		}
	}
}

// TestUninstallFlow_NilStateSkipsStatePrompt is the flow-level half of the
// piped rule: main withholds the state group when stdin is not a TTY, so
// an empty State roster must ask two questions and leave every state file
// untouched even though Confirm says yes to everything.
func TestUninstallFlow_NilStateSkipsStatePrompt(t *testing.T) {
	tree, _, deps := newUninstallTree(t, true)
	deps.State = nil
	prompts := 0
	deps.Confirm = func(prompt string) bool {
		prompts++
		return true
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	if prompts != 2 {
		t.Errorf("Confirm calls = %d, want 2 (config + caches)", prompts)
	}
	if _, err := os.Stat(tree.config); !os.IsNotExist(err) {
		t.Errorf("config should have been removed")
	}
	for _, p := range tree.caches {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("cache %s should have been removed", p)
		}
	}
	for _, p := range append(append([]string{}, tree.state...), tree.stateLocks...) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("state %s must survive an empty State roster: %v", p, err)
		}
	}
}

func TestUninstallFlow_ReportsRemainingPaths(t *testing.T) {
	tree, out, deps := newUninstallTree(t, true)
	// deps.Confirm stays nil: the legacy "leave files in place" behaviour.
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	for _, p := range tree.all() {
		if !strings.Contains(out.String(), p) {
			t.Errorf("output should mention %s:\n%s", p, out.String())
		}
	}
	for _, p := range tree.everyFile() {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s deleted despite nil Confirm: %v", p, err)
		}
	}
}

// TestUninstallFlow_HeldLockKeepsProtectedFile pins the POSIX unlink race
// that motivates Removable.Lock. Unlinking a state file another process
// holds an flock on does not stop that process: it keeps writing to the
// orphaned inode and the next writer recreates the path, so state the user
// asked to delete comes back minutes later. The statusline takes
// sessions.lock on every terminal prompt, so this is a window a real
// uninstall can land in. The flow must take the sidecar first and, when it
// cannot, leave both files alone and name the file it skipped.
//
// flock is per open file description, not per process, so a second Acquire
// from inside this test blocks exactly as another process would — the same
// technique internal/lockfile's own tests use.
func TestUninstallFlow_HeldLockKeepsProtectedFile(t *testing.T) {
	tree, out, deps := newUninstallTree(t, true)
	const sessions = 1 // index of sessions.json in tree.state / tree.stateLocks
	held, err := lockfile.Acquire(tree.stateLocks[sessions], time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { held.Close() })

	deps.Confirm = func(string) bool { return true }
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow should not fail on contention: %v", err)
	}

	for _, p := range []string{tree.state[sessions], tree.stateLocks[sessions]} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must survive while its lock is held: %v", p, err)
		}
	}
	got := out.String()
	if !strings.Contains(got, "sessions.json") || !strings.Contains(got, "in use") {
		t.Errorf("output must name sessions.json as in use so the user can rerun:\n%s", got)
	}
	// Contention on one entry must not stop the rest of the uninstall.
	for i, p := range tree.state {
		if i == sessions {
			continue
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should still have been removed", p)
		}
		if _, err := os.Stat(tree.stateLocks[i]); !os.IsNotExist(err) {
			t.Errorf("%s should still have been removed", tree.stateLocks[i])
		}
	}
	if _, err := os.Stat(tree.config); !os.IsNotExist(err) {
		t.Errorf("config should have been removed")
	}
}

// TestUninstallFlow_PathErrorPropagates checks a roster entry whose XDG
// resolution fails aborts the flow with a wrapped, labelled error rather
// than deleting a zero-length path.
func TestUninstallFlow_PathErrorPropagates(t *testing.T) {
	tree, _, deps := newUninstallTree(t, true)
	boom := errors.New("no home dir")
	deps.Caches = append(deps.Caches, Removable{
		Label: "following.json",
		Path:  func() (string, error) { return "", boom },
	})
	err := UninstallFlow(deps)
	if err == nil {
		t.Fatal("UninstallFlow should fail when a roster path cannot resolve")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error should wrap the resolver failure; got %v", err)
	}
	if !strings.Contains(err.Error(), "following.json") {
		t.Errorf("error should name the failing entry; got %v", err)
	}
	if _, err := os.Stat(tree.config); err != nil {
		t.Errorf("config must not be touched when resolution fails: %v", err)
	}
}

// TestUninstallFlow_DataRemovedBeforeLock pins the ordering inside
// removeEntry that TestUninstallFlow_HeldLockKeepsProtectedFile cannot:
// that test proves the sidecar is taken before either unlink happens, but
// says nothing about which of the two unlinks comes first — both orders
// leave the same end state (both files gone), so nothing in the suite
// above would notice them swapped.
//
// The order matters because of what happens the instant the lock *path*
// is unlinked: os.OpenFile(path, O_CREATE, ...) on a name that no longer
// exists creates a fresh inode, so a racing lockfile.Acquire on that same
// path succeeds immediately — it cannot see the still-open, still-flocked
// original. That window exists no matter which file is removed first; what
// changes is what a writer who slips through it does. With the lock
// removed last (data file gone first), a slipped-through writer merely
// recreates the data file — the known, accepted "state reappears" residual
// from a legitimate concurrent write. Reversed, the window opens before
// the data file is gone: a writer can slip through, write a fresh file,
// and this function's own subsequent removal of the data file silently
// deletes that write. That is loss of a legitimate concurrent write, not
// mere staleness, and the statusline takes this exact lock on every
// terminal prompt, so the window is not hypothetical.
//
// To observe the order without adding a recording hook to production code,
// the test puts the data file and the lock file in separate, otherwise
// untouched directories. Unlinking a file changes its parent directory's
// own mtime (the directory's entry list changed), so each directory's
// mtime marks exactly when its one file was removed — a side effect of
// the real os.Remove calls UninstallFlow already makes, not anything added
// to watch it. Two unlinks issued back to back from the same goroutine
// reliably produce distinguishable mtimes on the filesystems this project
// runs its tests on (measured tens of microseconds apart on darwin/APFS);
// this is the same style of tie the flow's own timeout constant assumes
// away, not a new assumption.
func TestUninstallFlow_DataRemovedBeforeLock(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	lockDir := filepath.Join(dir, "lock")
	for _, d := range []string{dataDir, lockDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(dataDir, "sessions.json")
	lockPath := filepath.Join(lockDir, "sessions.lock")
	for _, p := range []string{dataPath, lockPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rcPath := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(rcPath, []byte(BeginMarker+"\nnewsfetch\n"+EndMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := UninstallDeps{
		Shell:   func() (Shell, error) { return Shell{Name: "bash", RCPath: rcPath}, nil },
		Out:     &bytes.Buffer{},
		Confirm: func(string) bool { return true },
		// Config resolves to a path with nothing on disk, so that group
		// asks nothing and only the state group's removal runs.
		Config: fixedRemovable("config.toml", filepath.Join(dir, "config.toml")),
		State:  []Removable{lockedRemovable("sessions.json", dataPath, lockPath)},
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}

	dataDirInfo, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	lockDirInfo, err := os.Stat(lockDir)
	if err != nil {
		t.Fatalf("stat lock dir: %v", err)
	}
	if !dataDirInfo.ModTime().Before(lockDirInfo.ModTime()) {
		t.Errorf("data file's directory mtime (%v) is not strictly before the lock file's directory mtime (%v): sessions.json must be unlinked before sessions.lock, or a concurrent writer that slips through the reopened lock has its write silently deleted by this function's own trailing removal",
			dataDirInfo.ModTime(), lockDirInfo.ModTime())
	}
}

// TestUninstallFlow_OrphanedLockCountsAsPresent pins the widening
// described on maybeRemoveGroup: an entry counts as present when either
// its data file or its lock sidecar exists on disk, not only its data
// file. Without it, a seen.lock left behind by a crash — with no
// seen.json anywhere near it, since the crash happened before the write
// that would have created one — would never be swept up: the group would
// never see it as present, never prompt, and the orphan would survive
// every future --uninstall.
func TestUninstallFlow_OrphanedLockCountsAsPresent(t *testing.T) {
	tree, _, deps := newUninstallTree(t, false)
	const sessions = 1 // index of sessions.json in tree.state / tree.stateLocks
	if err := os.WriteFile(tree.stateLocks[sessions], []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompts := 0
	deps.Confirm = func(prompt string) bool {
		prompts++
		return true
	}
	if err := UninstallFlow(deps); err != nil {
		t.Fatalf("UninstallFlow: %v", err)
	}
	if prompts != 1 {
		t.Errorf("Confirm calls = %d, want 1 (state group has an orphaned lock and nothing else on disk)", prompts)
	}
	if _, err := os.Stat(tree.state[sessions]); !os.IsNotExist(err) {
		t.Errorf("sessions.json should still be absent — it was never created")
	}
	if _, err := os.Stat(tree.stateLocks[sessions]); !os.IsNotExist(err) {
		t.Errorf("orphaned lock %s should have been removed", tree.stateLocks[sessions])
	}
}
