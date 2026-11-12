package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/Sahas001/prashna/internal/config"
	"github.com/Sahas001/prashna/internal/edition"
	"github.com/Sahas001/prashna/internal/fetch"
	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/store"
)

// fetchers builds the enabled fetchers from the current settings,
// optionally restricted to names in only. Feeds and book follows come
// from the database.
func (a *app) fetchers(ctx context.Context, cfg config.Config, only []string) ([]fetch.Fetcher, error) {
	client := fetch.NewClient(cfg.UserAgent)
	src := cfg.Sources
	all := map[string]fetch.Fetcher{}
	if src.ArXiv.Enabled && len(src.ArXiv.Categories) > 0 {
		all["arxiv"] = &fetch.ArXiv{Client: client, Categories: src.ArXiv.Categories, MaxResults: src.ArXiv.MaxResults}
	}
	if src.HackerNews.Enabled {
		all["hackernews"] = &fetch.HackerNews{
			Client: client, MinPoints: src.HackerNews.MinPoints,
			Lookback: src.HackerNews.Lookback, MaxResults: src.HackerNews.MaxResults,
		}
	}
	if src.RSS.Enabled {
		stored, err := a.store.Feeds(ctx)
		if err != nil {
			return nil, err
		}
		var feeds []fetch.Feed
		for _, f := range stored {
			if f.Enabled {
				feeds = append(feeds, fetch.Feed{URL: f.URL, Name: f.Name, Tags: f.Tags, Type: f.Type})
			}
		}
		if len(feeds) > 0 {
			all["rss"] = &fetch.RSS{Client: client, Feeds: feeds, State: a.store, MaxAge: src.RSS.MaxAge}
		}
	}
	if b := src.Books; b.Enabled {
		authors, err := a.store.BookFollowNames(ctx, store.FollowAuthor)
		if err != nil {
			return nil, err
		}
		subjects, err := a.store.BookFollowNames(ctx, store.FollowSubject)
		if err != nil {
			return nil, err
		}
		if len(authors)+len(subjects) > 0 {
			all["openlibrary"] = &fetch.OpenLibrary{
				Client: client, Queries: fetch.BookQueries{Authors: authors, Subjects: subjects},
				Language: b.OpenLibraryLanguage(), PerQuery: b.PerQuery, Delay: time.Second,
				NeedsSummary: func(ctx context.Context, it item.Item) bool {
					c := it
					return c.Normalize() == nil && !a.store.HasSummary(ctx, c.ID)
				},
			}
		}
		if len(authors) > 0 {
			all["wikidata"] = &fetch.Wikidata{Client: client, Authors: authors}
		}
	}
	names := only
	if len(names) == 0 {
		for n := range all {
			names = append(names, n)
		}
		slices.Sort(names)
	}
	var out []fetch.Fetcher
	for _, n := range names {
		f, ok := all[n]
		if !ok {
			if len(only) > 0 {
				return nil, fmt.Errorf("source %q is unknown, turned off or has nothing to fetch", n)
			}
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// runFetch fetches, stores and rescores. Sources whose `every` interval
// hasn't elapsed are skipped unless force is set.
func (a *app) runFetch(ctx context.Context, only []string, force bool) error {
	cfg := a.current(ctx)
	fetchers, err := a.fetchers(ctx, cfg, only)
	if err != nil {
		return err
	}
	if len(fetchers) == 0 {
		a.log.Info("nothing to fetch yet: add interests, feeds or authors in the app")
		return nil
	}
	src := cfg.Sources
	p, err := a.pipeline(ctx)
	if err != nil {
		return err
	}
	p.Fetchers = fetchers
	p.Force = force
	p.Intervals = map[string]time.Duration{
		"arxiv": src.ArXiv.Every, "hackernews": src.HackerNews.Every, "rss": src.RSS.Every,
		"openlibrary": src.Books.Every, "wikidata": src.Books.Every,
	}
	st, err := p.Run(ctx)
	a.log.Info("fetch done", "fetched", st.Fetched, "skipped", st.Skipped, "new", st.Inserted, "merged", st.Merged)
	return err
}

// fetchState lets the scheduler, the UI and the CLI share one fetch at a time.
type fetchState struct {
	running atomic.Bool
	mu      sync.Mutex
	last    time.Time
	lastErr string
}

var errFetchRunning = errors.New("a fetch is already running")

// fetchAll fetches every source and then makes today's edition if needed.
func (a *app) fetchAll(ctx context.Context, force bool) error {
	if !a.fetches.running.CompareAndSwap(false, true) {
		return errFetchRunning
	}
	defer a.fetches.running.Store(false)
	return a.fetchHeld(ctx, force)
}

// fetchHeld does the work of fetchAll; the caller holds fetches.running.
func (a *app) fetchHeld(ctx context.Context, force bool) error {
	err := a.runFetch(ctx, nil, force)
	a.fetches.mu.Lock()
	a.fetches.last = time.Now()
	a.fetches.lastErr = ""
	if err != nil {
		a.fetches.lastErr = err.Error()
	}
	a.fetches.mu.Unlock()
	if _, eerr := a.ensureEdition(ctx); eerr != nil {
		err = errors.Join(err, eerr)
	}
	return err
}

// startFetch runs a fetch in the background unless one is running. The
// running flag is taken before returning, so a page loaded right after
// sees the fetch in progress.
func (a *app) startFetch(force bool) {
	if !a.fetches.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.fetches.running.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := a.fetchHeld(ctx, force); err != nil {
			a.log.Warn("fetch finished with errors", "err", err)
		}
	}()
}

func (a *app) fetchStatus() (running bool, last time.Time, lastErr string) {
	a.fetches.mu.Lock()
	defer a.fetches.mu.Unlock()
	return a.fetches.running.Load(), a.fetches.last, a.fetches.lastErr
}

func (a *app) editionOptions(cfg config.Config) edition.Options {
	sections := map[item.Type]int{}
	for k, v := range cfg.Edition.Sections {
		sections[item.Type(k)] = v
	}
	b := cfg.Edition.Books
	day, weekly, _ := b.ParseWeekday() // validated
	return edition.Options{
		Sections: sections,
		MinScore: cfg.Edition.MinScore,
		Lookback: cfg.Edition.Lookback,
		Books: edition.BookOptions{
			Weekly: weekly, Weekday: day, PickOfWeek: b.PickOfWeek,
			NewReleases: b.NewReleases, NewReleaseWindow: b.NewReleaseWindow,
		},
	}
}

// ensureEdition rescores (so recency is current) and makes today's edition
// if there isn't one yet. Nothing is made before the reader has told Prashna
// what they like: an edition of whatever happened to arrive first would
// take the day's slot.
func (a *app) ensureEdition(ctx context.Context) (bool, error) {
	if ready, err := a.hasProfile(ctx); err != nil || !ready {
		return false, err
	}
	if err := a.rescoreAll(ctx); err != nil {
		return false, err
	}
	made, err := edition.Ensure(ctx, a.store, a.editionOptions(a.current(ctx)), time.Now())
	if made {
		a.log.Info("made today's edition")
	}
	return made, err
}

// hasProfile reports whether there is anything to choose an edition by.
func (a *app) hasProfile(ctx context.Context) (bool, error) {
	ins, err := a.store.Interests(ctx)
	if err != nil || len(ins) > 0 {
		return len(ins) > 0, err
	}
	feeds, err := a.store.Feeds(ctx)
	if err != nil || len(feeds) > 0 {
		return len(feeds) > 0, err
	}
	authors, err := a.store.BookFollows(ctx, store.FollowAuthor)
	if err != nil || len(authors) > 0 {
		return len(authors) > 0, err
	}
	subjects, err := a.store.BookFollows(ctx, store.FollowSubject)
	return len(subjects) > 0, err
}

func (a *app) fetch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	source := fs.String("source", "", "comma-separated sources (default: all): arxiv,hackernews,rss,openlibrary,wikidata")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *source != "" {
		return a.runFetch(ctx, strings.Split(*source, ","), true)
	}
	return a.fetchAll(ctx, true)
}

func (a *app) rescore(ctx context.Context) error {
	p, err := a.pipeline(ctx)
	if err != nil {
		return err
	}
	n, err := p.Rescore(ctx)
	if err != nil {
		return err
	}
	a.log.Info("rescored", "items", n)
	return nil
}

func (a *app) top(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("top", flag.ContinueOnError)
	n := fs.Int("n", 20, "number of items")
	days := fs.Int("days", 7, "only items fetched in the last N days")
	types := fs.String("type", "", "comma-separated types: paper,article,news,book")
	all := fs.Bool("all", false, "include items you already rated")
	explain := fs.Bool("explain", false, "show score breakdown and summaries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	q := store.TopQuery{Limit: *n, Since: time.Now().AddDate(0, 0, -*days), IncludeRated: *all}
	if *types != "" {
		for t := range strings.SplitSeq(*types, ",") {
			typ := item.Type(strings.TrimSpace(t))
			if !typ.Valid() {
				return fmt.Errorf("unknown type %q", t)
			}
			q.Types = append(q.Types, typ)
		}
	}
	items, err := a.store.Top(ctx, q)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintln(a.out, "Nothing yet. Run 'prashna fetch' first.")
		return nil
	}
	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	for _, it := range items {
		mark := map[item.Feedback]string{item.Liked: "+", item.Disliked: "-"}[it.Feedback]
		fmt.Fprintf(tw, "%.3f%s\t%s\t%s\t%s\n", it.Score, mark, it.ID, it.Type, truncate(it.Title, 90))
		fmt.Fprintf(tw, "\t\t\t%s\n", it.URL)
		if *explain {
			fmt.Fprintf(tw, "\t\t\tkw=%.2f sim=%.2f rec=%.2f learned=%.2f\n",
				it.Signals.Keyword, it.Signals.Similarity, it.Signals.Recency, it.Signals.Learned)
		}
	}
	return tw.Flush()
}

func (a *app) feedback(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: prashna feedback <id> up|down|clear")
	}
	fb, ok := map[string]item.Feedback{"up": item.Liked, "down": item.Disliked, "clear": item.Neutral}[args[1]]
	if !ok {
		return fmt.Errorf("unknown rating %q: want up, down or clear", args[1])
	}
	it, err := a.store.SetFeedback(ctx, args[0], fb)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s %s: %s\n", args[1], it.ID, it.Title)
	return a.rescoreAll(ctx)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
