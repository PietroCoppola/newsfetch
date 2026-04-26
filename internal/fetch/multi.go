package fetch

import (
	"context"
	"sync"
)

// FetchAll runs the given sources concurrently and returns their merged
// stories along with per-source errors. The returned errs map ONLY contains
// entries for sources that failed; a successful source with zero stories is
// silently absent. Three states the caller can distinguish:
//
//	len(errs) == 0                  → every source succeeded (stories may be empty)
//	len(errs) == len(sources)       → every source failed
//	otherwise                       → partial failure
//
// Each source is invoked with the same ctx; one timeout governs the whole
// batch. A failure from one source does not cancel the others — the contract
// is graceful degradation, not fail-fast. Stories are appended in
// completion order, not source-list order; callers that need stable order
// should sort downstream (rank.Select already re-sorts by score).
func FetchAll(ctx context.Context, sources []Source, opts FetchOptions) (stories []Story, errs map[string]error) {
	if len(sources) == 0 {
		return nil, nil
	}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	errs = make(map[string]error)
	wg.Add(len(sources))
	for _, src := range sources {
		src := src
		go func() {
			defer wg.Done()
			got, err := src.Fetch(ctx, opts)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[src.Name()] = err
				return
			}
			stories = append(stories, got...)
		}()
	}
	wg.Wait()
	if len(errs) == 0 {
		errs = nil
	}
	return stories, errs
}
