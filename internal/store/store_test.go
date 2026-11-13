package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prashna.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func mustNormalize(t *testing.T, it item.Item) item.Item {
	t.Helper()
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestRoundTripAndMerge(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)

	paper := mustNormalize(t, item.Item{
		Type: item.TypePaper, Source: "arxiv", Title: "Leaderless Consensus for Key-Value Stores",
		URL: "https://arxiv.org/abs/2611.01234", Summary: "abstract", Authors: []string{"A"},
		Tags: []string{"cs.DC"}, Meta: map[string]string{"arxiv_id": "2611.01234"}, Published: at, FetchedAt: at,
	})
	st, err := s.SaveItems(ctx, []item.Item{paper})
	if err != nil || st.Inserted != 1 {
		t.Fatalf("save: %+v %v", st, err)
	}
	if _, err := s.SetFeedback(ctx, paper.ID[:8], item.Liked); err != nil {
		t.Fatal(err)
	}

	// Same title, different URL (e.g. a blog mirror) merges by title key.
	mirror := mustNormalize(t, item.Item{
		Type: item.TypeNews, Source: "hackernews", Title: "Leaderless consensus for key-value stores",
		URL: "https://blog.example.com/leaderless", Tags: []string{"hn"}, Meta: map[string]string{"hn_points": "99"},
		Published: at.Add(time.Hour), FetchedAt: at.Add(time.Hour),
	})
	st, err = s.SaveItems(ctx, []item.Item{mirror})
	if err != nil || st.Merged != 1 || st.Inserted != 0 {
		t.Fatalf("merge: %+v %v", st, err)
	}

	// Reopen to exercise migrations being idempotent.
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get(ctx, paper.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != item.TypePaper || got.URL != paper.URL || got.Feedback != item.Liked {
		t.Errorf("identity/feedback lost: %+v", got)
	}
	if len(got.Tags) != 2 || got.Meta["hn_points"] != "99" || got.Meta["arxiv_id"] != "2611.01234" {
		t.Errorf("tags/meta not merged: %v %v", got.Tags, got.Meta)
	}
	if !got.Published.Equal(at) || got.Summary != "abstract" || len(got.Authors) != 1 {
		t.Errorf("fields wrong: %+v", got)
	}
}

func TestTopAndScores(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var items []item.Item
	for i, u := range []string{"https://a.com/1", "https://a.com/2", "https://a.com/3"} {
		typ := item.TypeArticle
		if i == 2 {
			typ = item.TypeNews
		}
		items = append(items, mustNormalize(t, item.Item{Type: typ, Source: "rss", Title: u, URL: u, Published: now, FetchedAt: now}))
	}
	if _, err := s.SaveItems(ctx, items); err != nil {
		t.Fatal(err)
	}
	for i := range items {
		items[i].Score = float64(i)
		items[i].Signals.Recency = 0.5
	}
	if err := s.UpdateScores(ctx, items); err != nil {
		t.Fatal(err)
	}

	top, err := s.Top(ctx, TopQuery{Limit: 10, Since: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].ID != items[2].ID || top[0].Signals.Recency != 0.5 {
		t.Errorf("top order wrong: %+v", top)
	}
	articles, _ := s.Top(ctx, TopQuery{Limit: 10, Since: now.Add(-time.Hour), Types: []item.Type{item.TypeArticle}})
	if len(articles) != 2 {
		t.Errorf("type filter: got %d", len(articles))
	}
	if _, err := s.SetFeedback(ctx, items[2].ID, item.Disliked); err != nil {
		t.Fatal(err)
	}
	unrated, _ := s.Top(ctx, TopQuery{Limit: 10, Since: now.Add(-time.Hour)})
	if len(unrated) != 2 {
		t.Errorf("rated item not excluded: got %d", len(unrated))
	}
}

func TestGetValidation(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	for _, bad := range []string{"", "abc", "zzzz", "%%%%", "abcd%"} {
		if _, err := s.Get(ctx, bad); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) should be rejected as invalid, got %v", bad, err)
		}
	}
	if _, err := s.Get(ctx, "abcd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(abcd) = %v, want ErrNotFound", err)
	}
}
