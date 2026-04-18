// Command newsfetch renders one piece of bite-sized tech news each time a
// terminal opens. See newsfetch-spec.md at the repo root for the full design.
//
// M1 supports the default invocation only. The single hidden flag is
// --__refresh, used internally by the parent process to fork a detached
// child that refreshes the cache in the background.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/PietroCoppola/newsfetch/internal/cache"
	"github.com/PietroCoppola/newsfetch/internal/defaults"
	"github.com/PietroCoppola/newsfetch/internal/fetch"
	"github.com/PietroCoppola/newsfetch/internal/render"
)

// refreshFlag is the hidden internal subcommand that runs a synchronous cache
// refresh and exits. The leading underscores mark it as non-public API.
const refreshFlag = "--__refresh"

func main() {
	if len(os.Args) > 1 && os.Args[1] == refreshFlag {
		if err := runRefresh(); err != nil {
			fmt.Fprintln(os.Stderr, "newsfetch: refresh failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := runDefault(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "newsfetch:", err)
		os.Exit(1)
	}
}

// runDefault is the hot path. It reads the cache, picks one story, renders,
// and (if the cache was stale) spawns a detached refresh before returning.
func runDefault(out io.Writer) error {
	path, err := cache.Path()
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	f, readErr := cache.Read(path)
	if readErr == nil && len(f.Stories) > 0 {
		story := selectStory(f.Stories)
		fmt.Fprint(out, render.Boxed(story, now, defaults.BoxWidth))
		if !f.IsFresh(defaults.CacheTTL, now) {
			spawnRefresh()
		}
		return nil
	}

	// Cache missing, corrupt, or empty: fetch synchronously.
	ctx, cancel := context.WithTimeout(context.Background(), defaults.FetchTimeout)
	defer cancel()
	stories, err := hnFetch(ctx)
	if err != nil {
		fmt.Fprint(out, render.Fallback(defaults.FallbackMessage))
		return nil
	}
	if writeErr := writeCache(path, stories, time.Now().UTC()); writeErr != nil {
		fmt.Fprintln(os.Stderr, "newsfetch: warning: could not write cache:", writeErr)
	}
	story := selectStory(stories)
	fmt.Fprint(out, render.Boxed(story, now, defaults.BoxWidth))
	return nil
}

// runRefresh is the body of the hidden --__refresh subcommand. It fetches,
// writes the cache, and exits. Never prints to stdout.
func runRefresh() error {
	path, err := cache.Path()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaults.FetchTimeout)
	defer cancel()
	stories, err := hnFetch(ctx)
	if err != nil {
		return err
	}
	return writeCache(path, stories, time.Now().UTC())
}

func hnFetch(ctx context.Context) ([]fetch.Story, error) {
	h := &fetch.HackerNews{}
	stories, err := h.Fetch(ctx, fetch.FetchOptions{
		MinPoints: defaults.MinPoints,
		Limit:     defaults.NumStories,
	})
	if err != nil {
		return nil, err
	}
	if len(stories) == 0 {
		return nil, errors.New("hn returned no stories")
	}
	return stories, nil
}

func writeCache(path string, stories []fetch.Story, at time.Time) error {
	return cache.Write(path, &cache.File{
		Version:         cache.SchemaVersion,
		CachedByVersion: defaults.Version,
		FetchedAt:       at,
		Stories:         stories,
	})
}

// selectStory implements the M1 selection policy: newest above the points
// threshold. The fetcher returns search_by_date order (newest first) and the
// threshold was already applied server-side, so the first element is the
// right pick. Callers must pass a non-empty slice.
func selectStory(stories []fetch.Story) fetch.Story {
	return stories[0]
}

// spawnRefresh forks a detached child process running --__refresh so the
// cache refreshes after the parent has already rendered and exited. Failures
// are swallowed: the user already got a story from the stale cache, and a
// shell prompt is no place for a warning the user can't act on.
func spawnRefresh() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	cmd := exec.Command(exe, refreshFlag)
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	// Setpgid puts the child in its own process group so SIGHUP on the
	// parent's group (e.g., the shell closing its session) doesn't cascade
	// to the refresher.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		null.Close()
		return
	}
	// Release the Go-side process handle so we don't keep a zombie slot
	// open after main returns. The file descriptor leak on null is
	// bounded - main exits immediately after this.
	_ = cmd.Process.Release()
}
