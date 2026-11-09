package store

import (
	"bytes"
	"context"
	"testing"
	"time"

	"radar/internal/item"
)

func TestDigestCandidatesAndRecord(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var items []item.Item
	for i, u := range []string{"https://a.com/1", "https://a.com/2", "https://a.com/3", "https://a.com/old"} {
		fetched := now
		if i == 3 {
			fetched = now.Add(-30 * 24 * time.Hour)
		}
		it := mustNormalize(t, item.Item{Type: item.TypeArticle, Source: "rss", Title: u, URL: u, Published: fetched, FetchedAt: fetched})
		it.Score = 0.1 * float64(i+1)
		items = append(items, it)
	}
	if _, err := s.SaveItems(ctx, items); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateScores(ctx, items); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFeedback(ctx, items[1].ID, item.Liked); err != nil {
		t.Fatal(err)
	}

	q := DigestQuery{Type: item.TypeArticle, Since: now.Add(-7 * 24 * time.Hour), MinScore: 0.15, Limit: 10}
	got, err := s.DigestCandidates(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	// items[0] below min score, items[1] rated, items[3] too old.
	if len(got) != 1 || got[0].ID != items[2].ID {
		t.Fatalf("candidates = %+v", got)
	}

	if _, err := s.RecordDigest(ctx, "subject", []string{got[0].ID}, now); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.DigestCandidates(ctx, q); len(again) != 0 {
		t.Errorf("digested item offered again: %+v", again)
	}
	if none, _ := s.DigestCandidates(ctx, DigestQuery{Type: item.TypeNews, Since: q.Since, Limit: 10}); len(none) != 0 {
		t.Errorf("type filter broken: %+v", none)
	}
}

func TestFeedbackKeyIsStable(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	a, err := s.FeedbackKey(ctx)
	if err != nil || len(a) != 32 {
		t.Fatalf("key=%x err=%v", a, err)
	}
	b, err := s.FeedbackKey(ctx)
	if err != nil || !bytes.Equal(a, b) {
		t.Errorf("key changed: %x vs %x (%v)", a, b, err)
	}
}

func TestBookQueries(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	mk := func(u string, published, fetched time.Time, meta map[string]string) item.Item {
		return mustNormalize(t, item.Item{Type: item.TypeBook, Source: "googlebooks", Title: u, URL: u,
			Authors: []string{"A " + u}, Meta: meta, Published: published, FetchedAt: fetched})
	}
	newRelease := mk("https://b.com/new", now.Add(-10*24*time.Hour), old, map[string]string{"followed_author": "X", "date_precision": "day"})
	oldByAuthor := mk("https://b.com/old", old.AddDate(-5, 0, 0), old, map[string]string{"followed_author": "X", "date_precision": "year"})
	// No known date: Normalize defaulted Published to the fetch time, which must not count as "new".
	undated := mk("https://b.com/undated", time.Time{}, now, map[string]string{"followed_author": "X"})
	classic := mk("https://b.com/classic", old.AddDate(-40, 0, 0), old, nil)
	if _, err := s.SaveItems(ctx, []item.Item{newRelease, oldByAuthor, classic, undated}); err != nil {
		t.Fatal(err)
	}

	// Books fetched long ago are still scored while unsent.
	scored, err := s.ItemsForScoring(ctx, now.Add(-24*time.Hour))
	if err != nil || len(scored) != 4 {
		t.Fatalf("ItemsForScoring = %d items, %v", len(scored), err)
	}

	got, err := s.DigestCandidates(ctx, DigestQuery{Type: item.TypeBook, PublishedSince: now.AddDate(0, -6, 0), MetaKeys: []string{"followed_author", item.MetaDatePrecision}, Limit: 5})
	if err != nil || len(got) != 1 || got[0].ID != newRelease.ID {
		t.Fatalf("new releases = %+v, %v", got, err)
	}
	all, _ := s.DigestCandidates(ctx, DigestQuery{Type: item.TypeBook, Limit: 5})
	if len(all) != 4 {
		t.Errorf("zero Since should not filter: %d", len(all))
	}
}

func TestSettingsAndFeedState(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if v, err := s.Setting(ctx, "nope"); v != "" || err != nil {
		t.Errorf("unset setting = %q, %v", v, err)
	}
	at := time.Unix(1762700000, 0).UTC()
	if err := s.SetTimeSetting(ctx, "k", at); err != nil {
		t.Fatal(err)
	}
	s.SetTimeSetting(ctx, "k", at.Add(time.Hour)) // upsert
	if got, _ := s.TimeSetting(ctx, "k"); !got.Equal(at.Add(time.Hour)) {
		t.Errorf("TimeSetting = %v", got)
	}

	fs, err := s.FeedState(ctx, "https://x/feed")
	if err != nil || fs.URL != "https://x/feed" || !fs.LastOKAt.IsZero() {
		t.Fatalf("unseen feed = %+v, %v", fs, err)
	}
	fs.ETag, fs.LastOKAt, fs.LastItems = `"abc"`, at, 7
	if err := s.SaveFeedState(ctx, fs); err != nil {
		t.Fatal(err)
	}
	fs.LastError = "boom"
	s.SaveFeedState(ctx, fs)
	got, _ := s.FeedState(ctx, "https://x/feed")
	if got.ETag != `"abc"` || !got.LastOKAt.Equal(at) || got.LastItems != 7 || got.LastError != "boom" || !got.LastFetchedAt.IsZero() {
		t.Errorf("feed state = %+v", got)
	}
}
