package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"radar/internal/fetch"
	"radar/internal/item"
	"radar/internal/store"
)

// fetchers builds the enabled fetchers, optionally restricted to names in
// only. Feeds and book follows come from the database.
func (a *app) fetchers(ctx context.Context, only []string) ([]fetch.Fetcher, error) {
	client := fetch.NewClient(a.cfg.UserAgent)
	src := a.cfg.Sources
	all := map[string]fetch.Fetcher{}
	if src.ArXiv.Enabled {
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
	authors, err := a.store.BookFollowNames(ctx, store.FollowAuthor)
	if err != nil {
		return nil, err
	}
	subjects, err := a.store.BookFollowNames(ctx, store.FollowSubject)
	if err != nil {
		return nil, err
	}
	if b := src.Books; b.Enabled && len(authors)+len(subjects) > 0 {
		q := fetch.BookQueries{Authors: authors, Subjects: subjects}
		if b.GoogleBooks {
			key := ""
			if b.GoogleAPIKeyEnv != "" {
				key = os.Getenv(b.GoogleAPIKeyEnv)
			}
			// Google's 429s are usually daily-quota exhaustion; retrying with
			// backoff just delays the inevitable.
			gbClient := fetch.NewClient(a.cfg.UserAgent)
			gbClient.MaxRetries = 1
			all["googlebooks"] = &fetch.GoogleBooks{Client: gbClient, APIKey: key, Queries: q,
				Language: b.Language, PerQuery: b.PerQuery, Delay: 500 * time.Millisecond}
		}
		if b.OpenLibrary {
			all["openlibrary"] = &fetch.OpenLibrary{Client: client, Queries: q,
				Language: b.OpenLibraryLanguage(), PerQuery: b.PerQuery, Delay: time.Second}
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
			return nil, fmt.Errorf("source %q is unknown or disabled", n)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, errors.New("no sources enabled")
	}
	return out, nil
}

// runFetch fetches, stores and rescores; shared by `fetch` and the
// scheduler. Sources whose `every` interval hasn't elapsed are skipped
// unless force is set.
func (a *app) runFetch(ctx context.Context, only []string, force bool) error {
	fetchers, err := a.fetchers(ctx, only)
	if err != nil {
		return err
	}
	src := a.cfg.Sources
	p, err := a.pipeline(ctx)
	if err != nil {
		return err
	}
	p.Fetchers = fetchers
	p.Force = force
	p.Intervals = map[string]time.Duration{
		"arxiv": src.ArXiv.Every, "hackernews": src.HackerNews.Every, "rss": src.RSS.Every,
		"googlebooks": src.Books.Every, "openlibrary": src.Books.Every,
	}
	st, err := p.Run(ctx)
	a.log.Info("fetch done", "fetched", st.Fetched, "skipped", st.Skipped, "invalid", st.Invalid,
		"batch_dupes", st.Duplicates, "new", st.Inserted, "merged", st.Merged, "rescored", st.Rescored)
	return err
}

func (a *app) fetch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	source := fs.String("source", "", "comma-separated sources (default: all enabled): arxiv,hackernews,rss,googlebooks,openlibrary")
	force := fs.Bool("force", false, "ignore per-source every: intervals (implied by -source)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var only []string
	if *source != "" {
		only = strings.Split(*source, ",")
	}
	return a.runFetch(ctx, only, *force || len(only) > 0)
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
		fmt.Fprintln(a.out, "nothing yet - run 'radar fetch' first")
		return nil
	}

	tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	for _, it := range items {
		mark := map[item.Feedback]string{item.Liked: "+", item.Disliked: "-"}[it.Feedback]
		fmt.Fprintf(tw, "%.3f%s\t%s\t%s\t%s\t%s\n", it.Score, mark, it.ID, it.Type, it.Source, truncate(it.Title, 90))
		fmt.Fprintf(tw, "\t\t\t\t%s\n", it.URL)
		if hn := it.Meta["hn_url"]; hn != "" && hn != it.URL {
			fmt.Fprintf(tw, "\t\t\t\t%s (%s points)\n", hn, it.Meta["hn_points"])
		}
		if *explain {
			fmt.Fprintf(tw, "\t\t\t\tkw=%.2f sim=%.2f rec=%.2f  tags=%s\n",
				it.Signals.Keyword, it.Signals.Similarity, it.Signals.Recency, strings.Join(it.Tags, ","))
			if it.Summary != "" {
				fmt.Fprintf(tw, "\t\t\t\t%s\n", truncate(it.Summary, 200))
			}
		}
	}
	return tw.Flush()
}

func (a *app) feedback(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: radar feedback <id> up|down|clear")
	}
	fb, ok := map[string]item.Feedback{
		"up": item.Liked, "like": item.Liked, "+1": item.Liked,
		"down": item.Disliked, "dislike": item.Disliked, "-1": item.Disliked,
		"clear": item.Neutral, "0": item.Neutral,
	}[args[1]]
	if !ok {
		return fmt.Errorf("unknown rating %q: want up, down or clear", args[1])
	}
	it, err := a.store.SetFeedback(ctx, args[0], fb)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s %s: %s\n", map[item.Feedback]string{1: "👍", -1: "👎", 0: "cleared"}[fb], it.ID, it.Title)
	// Likes change similarity for everything, so rescore right away.
	return a.rescoreAll(ctx)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
