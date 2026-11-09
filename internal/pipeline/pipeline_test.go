package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/fetch"
	"radar/internal/item"
	"radar/internal/score"
	"radar/internal/store"
)

type fakeFetcher struct {
	name  string
	items []item.Item
	err   error
}

func (f fakeFetcher) Name() string                               { return f.name }
func (f fakeFetcher) Fetch(context.Context) ([]item.Item, error) { return f.items, f.err }

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func newPipeline(t *testing.T, fetchers ...fetch.Fetcher) *Pipeline {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Pipeline{
		Fetchers: fetchers,
		Store:    st,
		Scorer: score.New(score.Config{
			Weights:   score.Weights{Keyword: 0.5, Similarity: 0.3, Recency: 0.2},
			Interests: []score.Interest{{Term: "consensus", Weight: 1}},
		}),
		Window: 30 * 24 * time.Hour,
		Now:    func() time.Time { return now },
	}
}

func TestRunMergesCrossSourceDuplicates(t *testing.T) {
	arxiv := fakeFetcher{name: "arxiv", items: []item.Item{
		{Type: item.TypePaper, Source: "arxiv", Title: "Leaderless Consensus Protocols", URL: "http://arxiv.org/abs/2611.01234v2",
			Summary: "An abstract about consensus.", Tags: []string{"cs.DC"}, Published: now.Add(-time.Hour)},
		{Type: item.TypePaper, Source: "arxiv", Title: "Unrelated Vision Paper", URL: "https://arxiv.org/abs/2611.09999",
			Published: now.Add(-time.Hour)},
	}}
	hn := fakeFetcher{name: "hackernews", items: []item.Item{
		{Type: item.TypeNews, Source: "hackernews", Title: "Leaderless consensus protocols", URL: "https://arxiv.org/pdf/2611.01234v1.pdf",
			Tags: []string{"hn"}, Meta: map[string]string{"hn_points": "300"}, Published: now.Add(-30 * time.Minute)},
		{Type: item.TypeNews, Source: "hackernews", Title: "", URL: "https://example.com"}, // invalid
	}}
	p := newPipeline(t, arxiv, hn)
	ctx := context.Background()

	st, err := p.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Fetched != 4 || st.Invalid != 1 || st.Duplicates != 1 || st.Inserted != 2 || st.Rescored != 2 {
		t.Errorf("stats = %+v", st)
	}

	top, err := p.Store.Top(ctx, store.TopQuery{Limit: 10, Since: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("top has %d items", len(top))
	}
	m := top[0]
	if m.URL != "https://arxiv.org/abs/2611.01234" || m.Type != item.TypePaper || m.Meta["hn_points"] != "300" {
		t.Errorf("merged item wrong: %+v", m)
	}
	if m.Signals.Keyword == 0 || m.Score <= top[1].Score {
		t.Errorf("interest match should rank first: %+v vs %+v", m, top[1])
	}

	// Second run: same items again merge into existing rows, nothing new.
	st, err = p.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Inserted != 0 || st.Merged != 2 {
		t.Errorf("rerun stats = %+v", st)
	}
}

func TestRunReportsFailedSourceButSavesOthers(t *testing.T) {
	ok := fakeFetcher{name: "arxiv", items: []item.Item{
		{Type: item.TypePaper, Source: "arxiv", Title: "A paper", URL: "https://arxiv.org/abs/2611.00001", Published: now},
	}}
	boom := errors.New("boom")
	p := newPipeline(t, ok, fakeFetcher{name: "hackernews", err: boom})
	st, err := p.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if st.Inserted != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestLikesFeedSimilarity(t *testing.T) {
	p := newPipeline(t, fakeFetcher{name: "arxiv", items: []item.Item{
		{Type: item.TypePaper, Source: "arxiv", Title: "Raft log replication under partitions", URL: "https://arxiv.org/abs/1", Published: now},
		{Type: item.TypePaper, Source: "arxiv", Title: "Batching log replication in Raft clusters", URL: "https://arxiv.org/abs/2", Published: now},
		{Type: item.TypePaper, Source: "arxiv", Title: "Diffusion models for audio", URL: "https://arxiv.org/abs/3", Published: now},
	}})
	ctx := context.Background()
	if _, err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	liked, err := p.Store.SetFeedback(ctx, item.IDFor("https://arxiv.org/abs/1")[:6], item.Liked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rescore(ctx); err != nil {
		t.Fatal(err)
	}
	top, err := p.Store.Top(ctx, store.TopQuery{Limit: 10, Since: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 {
		t.Fatalf("liked item should be excluded from top, got %d items", len(top))
	}
	if top[0].URL != "https://arxiv.org/abs/2" || top[0].Signals.Similarity == 0 {
		t.Errorf("similar item should rank first after liking %s: %+v", liked.ID, top)
	}
}

func TestDedupe(t *testing.T) {
	a := item.Item{ID: "1", Title: "Same long title for dedupe", Tags: []string{"x"}}
	b := item.Item{ID: "2", Title: "Same long title for dedupe!", Tags: []string{"y"}}
	c := item.Item{ID: "1", Title: "Short", Tags: []string{"z"}}
	d := item.Item{ID: "3", Title: "Short"}
	out := Dedupe([]item.Item{a, b, c, d})
	if len(out) != 2 || len(out[0].Tags) != 3 || out[1].ID != "3" {
		t.Errorf("Dedupe = %+v", out)
	}
}

func TestPartialResultsAreSaved(t *testing.T) {
	boom := errors.New("one feed broke")
	partial := fakeFetcher{name: "rss", err: boom, items: []item.Item{
		{Type: item.TypeArticle, Source: "rss", Title: "From the working feed", URL: "https://blog.example.com/p", Published: now},
	}}
	p := newPipeline(t, partial)
	st, err := p.Run(context.Background())
	if !errors.Is(err, boom) || st.Inserted != 1 {
		t.Fatalf("stats=%+v err=%v", st, err)
	}
	// A partial failure must not reset the interval.
	if last, _ := p.Store.TimeSetting(context.Background(), lastOKKey("rss")); !last.IsZero() {
		t.Errorf("interval reset after partial failure: %v", last)
	}
}

func TestIntervals(t *testing.T) {
	books := &countingFetcher{name: "googlebooks"}
	feeds := &countingFetcher{name: "rss"}
	p := newPipeline(t, books, feeds)
	p.Intervals = map[string]time.Duration{"googlebooks": 24 * time.Hour}
	ctx := context.Background()

	clock := now
	p.Now = func() time.Time { return clock }
	run := func() Stats {
		t.Helper()
		st, err := p.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	run()
	clock = clock.Add(6 * time.Hour)
	if st := run(); len(st.Skipped) != 1 || st.Skipped[0] != "googlebooks" {
		t.Errorf("skipped = %v", st.Skipped)
	}
	p.Force = true
	run()
	p.Force = false
	clock = clock.Add(25 * time.Hour)
	run()
	if books.calls.Load() != 3 || feeds.calls.Load() != 4 {
		t.Errorf("calls = %d/%d, want googlebooks 3 (first, forced, after 24h), rss 4", books.calls.Load(), feeds.calls.Load())
	}
}

// countingFetcher counts calls; fetchers run concurrently, hence atomic.
type countingFetcher struct {
	name  string
	calls atomic.Int32
}

func (c *countingFetcher) Name() string { return c.name }
func (c *countingFetcher) Fetch(context.Context) ([]item.Item, error) {
	c.calls.Add(1)
	return nil, nil
}
