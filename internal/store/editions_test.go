package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sahas001/radar/internal/item"
)

func seedItems(t *testing.T, s *Store, n int) []item.Item {
	t.Helper()
	now := time.Now().UTC()
	var items []item.Item
	for i := range n {
		u := "https://e.example/" + string(rune('a'+i))
		it := mustNormalize(t, item.Item{Type: item.TypeArticle, Source: "rss", Title: "Article " + string(rune('A'+i)),
			URL: u, Summary: "notes on raft", Authors: []string{"Jane Doe"}, Tags: []string{"distributed systems"}, Published: now, FetchedAt: now})
		it.Score = 1 - float64(i)/10
		items = append(items, it)
	}
	if _, err := s.SaveItems(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	s.UpdateScores(context.Background(), items)
	return items
}

func TestEditions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	items := seedItems(t, s, 4)
	now := time.Now()

	cands, err := s.Candidates(ctx, CandidateQuery{Type: item.TypeArticle, Since: now.Add(-time.Hour), Limit: 10})
	if err != nil || len(cands) != 4 || cands[0].ID != items[0].ID {
		t.Fatalf("candidates = %d %v", len(cands), err)
	}
	id, err := s.SaveEdition(ctx, "2026-11-09", "Monday, 9 November", now, []EditionEntry{
		{Section: "lead", ItemID: items[0].ID}, {Section: "article", ItemID: items[2].ID}, {Section: "article", ItemID: items[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveEdition(ctx, "2026-11-09", "again", now, nil); !errors.Is(err, ErrDuplicate) {
		t.Errorf("second edition same day = %v", err)
	}
	if left, _ := s.Candidates(ctx, CandidateQuery{Type: item.TypeArticle, Limit: 10}); len(left) != 1 || left[0].ID != items[3].ID {
		t.Errorf("used items offered again: %+v", left)
	}

	s.SetFeedback(ctx, items[2].ID, item.Liked)
	s.RecordOpen(ctx, items[0].ID)
	sum, its, err := s.EditionForDay(ctx, "2026-11-09")
	if err != nil {
		t.Fatal(err)
	}
	if sum.ID != id || sum.Number != 1 || sum.Items != 3 || sum.Liked != 1 || sum.Opened != 1 || sum.Title != "Monday, 9 November" {
		t.Errorf("summary = %+v", sum)
	}
	if len(its) != 3 || its[0].Section != "lead" || its[1].Item.ID != items[2].ID || its[2].Section != "article" {
		t.Errorf("items in order = %+v", its)
	}
	if _, _, err := s.EditionForDay(ctx, "2026-11-10"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing day = %v", err)
	}
	s.SaveEdition(ctx, "2026-11-10", "Tuesday", now.Add(24*time.Hour), []EditionEntry{{Section: "article", ItemID: items[3].ID}})
	list, _ := s.Editions(ctx, 10, 0)
	if len(list) != 2 || list[0].Day != "2026-11-10" || list[0].Number != 2 || list[1].Number != 1 {
		t.Errorf("editions = %+v", list)
	}
	if e, _, err := s.Edition(ctx, id); err != nil || e.Day != "2026-11-09" {
		t.Errorf("by id = %+v %v", e, err)
	}
}

func TestLibrarySearchAndOpens(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	items := seedItems(t, s, 4)

	s.SetSaved(ctx, items[0].ID, true)
	s.SetSaved(ctx, items[1].ID, true)
	it, err := s.RecordOpen(ctx, items[1].ID)
	if err != nil || it.OpenCount != 1 || it.OpenedAt.IsZero() || it.ReadAt.IsZero() {
		t.Fatalf("open = %+v %v", it, err)
	}
	firstRead := it.ReadAt
	it, _ = s.RecordOpen(ctx, items[1].ID)
	if it.OpenCount != 2 || !it.ReadAt.Equal(firstRead) {
		t.Errorf("second open should count but keep first read time: %+v", it)
	}
	s.SetFeedback(ctx, items[2].ID, item.Liked)

	check := func(shelf Shelf, want ...string) {
		t.Helper()
		got, err := s.Library(ctx, shelf, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, g := range got {
			ids = append(ids, g.ID)
		}
		if len(ids) != len(want) {
			t.Errorf("%s = %v, want %v", shelf, ids, want)
			return
		}
		for i := range want {
			if ids[i] != want[i] {
				t.Errorf("%s = %v, want %v", shelf, ids, want)
			}
		}
	}
	check(ShelfToRead, items[0].ID)
	check(ShelfFavorites, items[2].ID)
	check(ShelfRead, items[1].ID)
	counts, _ := s.ShelfCounts(ctx)
	if counts[ShelfToRead] != 1 || counts[ShelfFavorites] != 1 || counts[ShelfRead] != 1 {
		t.Errorf("counts = %v", counts)
	}
	if _, err := s.Library(ctx, "nope", 10, 0); err == nil {
		t.Error("unknown shelf accepted")
	}

	// Revisit: favorites and saved items not opened recently.
	rev, _ := s.Revisit(ctx, time.Now().Add(time.Hour), 10)
	if len(rev) != 3 {
		t.Errorf("revisit = %d items", len(rev))
	}
	if rev, _ := s.Revisit(ctx, time.Now().Add(-time.Hour), 10); len(rev) != 0 {
		t.Errorf("recently touched items offered for revisit: %d", len(rev))
	}

	for q, want := range map[string]int{"RAFT jane": 4, "notes on": 4, "distributed": 4, "nothing here": 0, "  ": 0, "100%": 0, "a_b": 0} {
		got, err := s.Search(ctx, q, 20)
		if err != nil || len(got) != want {
			t.Errorf("Search(%q) = %d items, %v; want %d", q, len(got), err, want)
		}
	}

	k1, _ := s.SecretKey(ctx, "session")
	k2, _ := s.SecretKey(ctx, "session")
	if len(k1) != 32 || string(k1) != string(k2) {
		t.Error("secret key not stable")
	}
	if ok, _ := s.FetchedAny(ctx); !ok {
		t.Error("FetchedAny = false")
	}
	if !s.HasSummary(ctx, items[0].ID) || s.HasSummary(ctx, "0000000000000000") {
		t.Error("HasSummary wrong")
	}
}
