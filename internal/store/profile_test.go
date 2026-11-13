package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

func TestInterests(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	for _, in := range []Interest{{Term: "rust", Weight: 0.5}, {Term: "Distributed Systems", Weight: 1}, {Term: "RUST", Weight: 2}} {
		if err := s.SaveInterest(ctx, in.Term, in.Weight); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Interests(ctx)
	if err != nil || len(got) != 2 || got[0].Term != "rust" || got[0].Weight != 2 {
		t.Fatalf("interests = %+v, %v (case-insensitive upsert, strongest first)", got, err)
	}
	if err := s.SetInterestWeight(ctx, got[1].ID, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInterest(ctx, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInterest(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
	got, _ = s.Interests(ctx)
	if len(got) != 1 || got[0].Weight != -1 {
		t.Errorf("after edits = %+v", got)
	}
	if err := s.SaveInterest(ctx, "  ", 1); err == nil {
		t.Error("empty interest accepted")
	}
}

func TestFeedsAndFollows(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	id, err := s.AddFeed(ctx, Feed{URL: "https://b.example/feed", Tags: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddFeed(ctx, Feed{URL: "https://b.example/feed"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate feed = %v", err)
	}
	s.AddFeed(ctx, Feed{URL: "https://a.example/rss", Name: "Alpha", Type: item.TypeNews})
	s.SaveFeedState(ctx, FeedState{URL: "https://b.example/feed", Title: "Beta Blog", LastOKAt: time.Unix(1762700000, 0), LastItems: 4})

	feeds, err := s.Feeds(ctx)
	if err != nil || len(feeds) != 2 {
		t.Fatalf("feeds = %+v, %v", feeds, err)
	}
	if feeds[0].Name != "Alpha" || feeds[0].Type != item.TypeNews || !feeds[0].Enabled {
		t.Errorf("first feed = %+v", feeds[0])
	}
	if b := feeds[1]; b.State.Title != "Beta Blog" || b.State.LastItems != 4 || b.Type != item.TypeArticle || len(b.Tags) != 1 {
		t.Errorf("second feed = %+v", b)
	}
	if err := s.SetFeedEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFeed(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.FeedState(ctx, "https://b.example/feed"); !st.LastOKAt.IsZero() {
		t.Error("feed state not deleted with feed")
	}
	if err := s.DeleteFeed(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete = %v", err)
	}

	for _, n := range []string{"Ted Chiang", "ted chiang", "Ursula K. Le Guin"} {
		if err := s.AddBookFollow(ctx, FollowAuthor, n); err != nil {
			t.Fatal(err)
		}
	}
	s.AddBookFollow(ctx, FollowSubject, "science fiction")
	if err := s.AddBookFollow(ctx, "genre", "x"); err == nil {
		t.Error("unknown kind accepted")
	}
	authors, _ := s.BookFollowNames(ctx, FollowAuthor)
	if len(authors) != 2 || authors[0] != "Ted Chiang" {
		t.Errorf("authors = %v", authors)
	}
}

func TestSeedProfileOnce(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	p := Profile{
		Interests: []Interest{{Term: "consensus", Weight: 1}},
		Feeds:     []Feed{{URL: "https://x.example/feed", Tags: []string{"t"}}},
		Authors:   []string{"Ted Chiang"},
		Subjects:  []string{"science fiction"},
	}
	if ok, err := s.SeedProfile(ctx, p); !ok || err != nil {
		t.Fatalf("first seed = %v, %v", ok, err)
	}
	ins, _ := s.Interests(ctx)
	s.DeleteInterest(ctx, ins[0].ID) // user edits in the UI...
	if ok, err := s.SeedProfile(ctx, p); ok || err != nil {
		t.Fatalf("second seed = %v, %v", ok, err)
	}
	if ins, _ := s.Interests(ctx); len(ins) != 0 {
		t.Errorf("re-seed overwrote UI edits: %+v", ins)
	}
	feeds, _ := s.Feeds(ctx)
	subjects, _ := s.BookFollowNames(ctx, FollowSubject)
	if len(feeds) != 1 || len(subjects) != 1 {
		t.Errorf("feeds=%v subjects=%v", feeds, subjects)
	}
}

func TestReadingListAndHistory(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var items []item.Item
	for _, u := range []string{"https://r.example/1", "https://r.example/2", "https://r.example/3"} {
		items = append(items, mustNormalize(t, item.Item{Type: item.TypeArticle, Source: "rss", Title: u, URL: u, Published: now, FetchedAt: now}))
	}
	s.SaveItems(ctx, items)

	if it, err := s.SetSaved(ctx, items[0].ID, true); err != nil || it.SavedAt.IsZero() {
		t.Fatalf("save: %+v %v", it, err)
	}
	s.SetSaved(ctx, items[1].ID, true)
	if it, _ := s.SetRead(ctx, items[0].ID, true); it.ReadAt.IsZero() {
		t.Error("read not set")
	}
	saved, _ := s.SavedItems(ctx)
	if len(saved) != 2 || saved[0].ID != items[1].ID {
		t.Errorf("unread should come first: %+v", saved)
	}
	if n, _ := s.CountUnreadSaved(ctx); n != 1 {
		t.Errorf("unread count = %d", n)
	}
	if it, _ := s.SetSaved(ctx, items[0].ID, false); !it.SavedAt.IsZero() || !it.ReadAt.IsZero() {
		t.Errorf("unsave should clear saved and read: %+v", it)
	}

	s.SetFeedback(ctx, items[2].ID, item.Liked)
	liked, _ := s.RatedItems(ctx, item.Liked, 10, 0)
	if len(liked) != 1 || liked[0].ID != items[2].ID {
		t.Errorf("liked = %+v", liked)
	}

	page, _ := s.Top(ctx, TopQuery{Limit: 1, Offset: 1, Since: now.Add(-time.Hour), IncludeRated: true})
	if len(page) != 1 {
		t.Errorf("offset page = %d items", len(page))
	}
}
