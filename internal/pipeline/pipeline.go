// Package pipeline wires fetchers, dedupe, storage and scoring:
//
//	fetchers -> normalize -> dedupe -> SQLite -> rescore
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"radar/internal/fetch"
	"radar/internal/item"
	"radar/internal/score"
	"radar/internal/store"
)

type Pipeline struct {
	Fetchers []fetch.Fetcher
	Store    *store.Store
	Scorer   *score.Scorer
	Window   time.Duration // rescore items fetched within this window
	// Intervals sets a minimum time between successful fetches per source
	// name (e.g. books daily while feeds run every few hours). Force ignores it.
	Intervals map[string]time.Duration
	Force     bool
	Log       *slog.Logger
	Now       func() time.Time
}

type Stats struct {
	Fetched    int
	Skipped    []string // sources not due yet
	Invalid    int      // dropped by Normalize
	Duplicates int      // collapsed within this batch
	store.SaveStats
	Rescored int
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pipeline) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func lastOKKey(source string) string { return "fetch_last_ok:" + source }

// due reports whether source's interval has elapsed since its last
// successful fetch.
func (p *Pipeline) due(ctx context.Context, source string, now time.Time) (bool, error) {
	every := p.Intervals[source]
	if p.Force || every <= 0 {
		return true, nil
	}
	last, err := p.Store.TimeSetting(ctx, lastOKKey(source))
	if err != nil {
		return false, err
	}
	return now.Sub(last) >= every, nil
}

// Run fetches every due source concurrently, stores the results and
// rescores. A failing source does not stop the others: its error is
// included in the returned (joined) error, and any partial results it
// returned are still saved.
func (p *Pipeline) Run(ctx context.Context) (Stats, error) {
	var st Stats
	now := p.now()

	var fetchers []fetch.Fetcher
	for _, f := range p.Fetchers {
		ok, err := p.due(ctx, f.Name(), now)
		if err != nil {
			return st, err
		}
		if !ok {
			st.Skipped = append(st.Skipped, f.Name())
			p.log().Debug("skipping source, fetched recently", "source", f.Name(), "every", p.Intervals[f.Name()])
			continue
		}
		fetchers = append(fetchers, f)
	}

	type result struct {
		name  string
		items []item.Item
		err   error
		took  time.Duration
	}
	results := make([]result, len(fetchers))
	var wg sync.WaitGroup
	for i, f := range fetchers {
		wg.Go(func() {
			start := time.Now()
			items, err := f.Fetch(ctx)
			results[i] = result{f.Name(), items, err, time.Since(start)}
		})
	}
	wg.Wait()

	var errs []error
	var succeeded []string
	var batch []item.Item
	for _, r := range results {
		if r.err != nil {
			p.log().Error("fetch failed", "source", r.name, "err", r.err, "partial_items", len(r.items))
			errs = append(errs, r.err)
		} else {
			succeeded = append(succeeded, r.name)
			p.log().Info("fetched", "source", r.name, "items", len(r.items), "took", r.took.Round(time.Millisecond))
		}
		st.Fetched += len(r.items)
		for _, it := range r.items {
			it.FetchedAt = now
			if err := it.Normalize(); err != nil {
				st.Invalid++
				p.log().Debug("dropping invalid item", "source", r.name, "err", err)
				continue
			}
			batch = append(batch, it)
		}
	}

	deduped := Dedupe(batch)
	st.Duplicates = len(batch) - len(deduped)
	saved, err := p.Store.SaveItems(ctx, deduped)
	st.SaveStats = saved
	if err != nil {
		return st, errors.Join(append(errs, fmt.Errorf("save: %w", err))...)
	}
	// Only a fully successful, saved fetch resets the interval; partial
	// failures retry on the next run.
	for _, name := range succeeded {
		if err := p.Store.SetTimeSetting(ctx, lastOKKey(name), now); err != nil {
			errs = append(errs, err)
		}
	}

	n, err := p.Rescore(ctx)
	st.Rescored = n
	if err != nil {
		errs = append(errs, fmt.Errorf("rescore: %w", err))
	}
	return st, errors.Join(errs...)
}

// Rescore recomputes scores for every item in the window (plus unsent
// books, see store.ItemsForScoring). Scores depend on the whole liked set
// (similarity) and on the clock (recency), so they are recomputed
// wholesale rather than incrementally.
func (p *Pipeline) Rescore(ctx context.Context) (int, error) {
	now := p.now()
	cands, err := p.Store.ItemsForScoring(ctx, now.Add(-p.Window))
	if err != nil {
		return 0, err
	}
	liked, err := p.Store.ItemsWithFeedback(ctx, item.Liked)
	if err != nil {
		return 0, err
	}
	p.Scorer.Score(cands, liked, now)
	if err := p.Store.UpdateScores(ctx, cands); err != nil {
		return 0, err
	}
	return len(cands), nil
}

// Dedupe collapses items within one batch that share an ID (same canonical
// URL) or a dedupe title key, merging them with item.Merge. Order of first
// appearance is preserved.
func Dedupe(items []item.Item) []item.Item {
	out := make([]item.Item, 0, len(items))
	byID := make(map[string]int, len(items))
	byTitle := make(map[string]int, len(items))
	for _, it := range items {
		key := item.DedupeKey(it)
		idx, ok := byID[it.ID]
		if !ok && key != "" {
			idx, ok = byTitle[key]
		}
		if ok {
			out[idx] = item.Merge(out[idx], it)
			if k := item.DedupeKey(out[idx]); k != "" {
				byTitle[k] = idx
			}
			continue
		}
		byID[it.ID] = len(out)
		if key != "" {
			byTitle[key] = len(out)
		}
		out = append(out, it)
	}
	return out
}
