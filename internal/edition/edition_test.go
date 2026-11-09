package edition

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// add saves an item with a fixed score.
func add(t *testing.T, st *store.Store, typ item.Type, name string, score float64, mut ...func(*item.Item)) item.Item {
	t.Helper()
	it := item.Item{Type: typ, Source: "test", Title: name, URL: "https://x.example/" + strings.ReplaceAll(name, " ", "-"),
		Authors: []string{"Some Author"}, Published: time.Now(), FetchedAt: time.Now()}
	for _, m := range mut {
		m(&it)
	}
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	it.Score = score
	ctx := context.Background()
	if _, err := st.SaveItems(ctx, []item.Item{it}); err != nil {
		t.Fatal(err)
	}
	st.UpdateScores(ctx, []item.Item{it})
	return it
}

var baseOpts = Options{
	Sections: map[item.Type]int{item.TypePaper: 2, item.TypeArticle: 2, item.TypeNews: 1},
	Lookback: 7 * 24 * time.Hour,
}

func TestEnsureMakesOneEditionPerDay(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for i := range 3 {
		add(t, st, item.TypePaper, fmt.Sprintf("paper %d", i), 0.5-float64(i)/10)
		add(t, st, item.TypeArticle, fmt.Sprintf("article %d", i), 0.45-float64(i)/10)
	}
	best := add(t, st, item.TypeNews, "news best", 0.9)
	add(t, st, item.TypeNews, "news two", 0.3)

	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.Local) // a Tuesday
	made, err := Ensure(ctx, st, baseOpts, now)
	if err != nil || !made {
		t.Fatalf("first Ensure = %v, %v", made, err)
	}
	if again, err := Ensure(ctx, st, baseOpts, now.Add(time.Hour)); again || err != nil {
		t.Errorf("second Ensure the same day = %v, %v", again, err)
	}

	sum, items, err := st.EditionForDay(ctx, Day(now))
	if err != nil {
		t.Fatal(err)
	}
	v := Group(sum, items)
	if v.Lead == nil || v.Lead.ID != best.ID {
		t.Fatalf("lead should be the best item overall: %+v", v.Lead)
	}
	if sum.Title != "Tuesday, 10 November 2026" || v.Date.Day() != 10 {
		t.Errorf("title/date = %q %v", sum.Title, v.Date)
	}
	var keys []string
	for _, s := range v.Sections {
		keys = append(keys, fmt.Sprintf("%s:%d", s.Key, len(s.Items)))
	}
	// News had 2 candidates and 1 slot; the best became the lead, so the
	// section takes the next one.
	if got := strings.Join(keys, " "); got != "paper:2 article:2 news:1" {
		t.Errorf("sections = %s", got)
	}

	// The next day uses only items not yet in an edition.
	add(t, st, item.TypeArticle, "fresh article", 0.7)
	made, _ = Ensure(ctx, st, baseOpts, now.Add(24*time.Hour))
	sum, items, _ = st.EditionForDay(ctx, Day(now.Add(24*time.Hour)))
	if !made || sum.Items != 3 { // fresh article (lead), paper 2, article 2
		t.Errorf("day two: made=%v items=%d", made, sum.Items)
	}

	// Nothing left at all: no empty edition.
	st2 := openStore(t)
	if made, err := Ensure(ctx, st2, baseOpts, now); made || err != nil {
		t.Errorf("empty store made an edition: %v %v", made, err)
	}
}

func TestWeeklyBooks(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	add(t, st, item.TypeArticle, "an article", 0.5)
	pick := add(t, st, item.TypeBook, "Designing Data-Intensive Applications", 0.6, func(it *item.Item) {
		it.FetchedAt = time.Now().AddDate(-1, 0, 0) // books stay eligible however old
	})
	release := add(t, st, item.TypeBook, "Exhalation", 0.2, func(it *item.Item) {
		it.Published = time.Now().AddDate(0, -1, 0)
		it.Meta = map[string]string{MetaFollowedAuthor: "Ted Chiang", item.MetaDatePrecision: "day"}
	})
	opts := baseOpts
	opts.Books = BookOptions{Weekly: true, Weekday: time.Sunday, PickOfWeek: true, NewReleases: 3, NewReleaseWindow: 180 * 24 * time.Hour}

	// The very first edition has books whatever the day, so a new reader
	// sees them; after that they wait for Sunday.
	friday := time.Date(2026, 11, 13, 8, 0, 0, 0, time.Local)
	Ensure(ctx, st, opts, friday)
	sum, items, _ := st.EditionForDay(ctx, Day(friday))
	if v := Group(sum, items); len(v.Sections) != 2 || v.Sections[0].Key != SectionBookPick {
		t.Fatalf("first edition should include books: %+v", v.Sections)
	}
	// Fresh books for the next block.
	pick = add(t, st, item.TypeBook, "Another Pick", 0.6, func(it *item.Item) { it.FetchedAt = time.Now().AddDate(-1, 0, 0) })
	release = add(t, st, item.TypeBook, "Another Release", 0.2, func(it *item.Item) {
		it.Published = time.Now().AddDate(0, -1, 0)
		it.Meta = map[string]string{MetaFollowedAuthor: "Ted Chiang", item.MetaDatePrecision: "day"}
	})

	add(t, st, item.TypeArticle, "saturday article", 0.5)
	saturday := friday.Add(24 * time.Hour)
	Ensure(ctx, st, opts, saturday)
	sum, items, _ = st.EditionForDay(ctx, Day(saturday))
	if v := Group(sum, items); len(v.Sections) != 0 || v.Lead == nil {
		t.Errorf("no books on Saturday: %+v", v.Sections)
	}

	add(t, st, item.TypeArticle, "sunday article", 0.5)
	sunday := saturday.Add(24 * time.Hour)
	Ensure(ctx, st, opts, sunday)
	sum, items, _ = st.EditionForDay(ctx, Day(sunday))
	v := Group(sum, items)
	if len(v.Sections) != 2 || v.Sections[0].Key != SectionBookPick || v.Sections[0].Items[0].ID != pick.ID ||
		v.Sections[1].Key != SectionBookNew || v.Sections[1].Items[0].ID != release.ID {
		t.Fatalf("Sunday books = %+v", v.Sections)
	}
	if SectionTitle(SectionBookNew) != "New from authors you follow" {
		t.Error("section title")
	}

	// Missed the next Sunday: books come on the first edition after a week.
	add(t, st, item.TypeBook, "Another Book", 0.5, func(it *item.Item) { it.FetchedAt = time.Now().AddDate(-1, 0, 0) })
	add(t, st, item.TypeArticle, "monday article", 0.5)
	monday := sunday.Add(8 * 24 * time.Hour)
	Ensure(ctx, st, opts, monday)
	sum, items, _ = st.EditionForDay(ctx, Day(monday))
	if v := Group(sum, items); len(v.Sections) != 1 || v.Sections[0].Key != SectionBookPick {
		t.Errorf("overdue books = %+v", v.Sections)
	}
}

func TestNewEntry(t *testing.T) {
	e := NewEntry(item.Item{Type: item.TypeBook, Authors: []string{"A", "B", "C", "D"}, Summary: strings.Repeat("word ", 100),
		Published: time.Date(2019, 5, 7, 0, 0, 0, 0, time.UTC),
		Meta:      map[string]string{item.MetaDatePrecision: "day", "pages": "368", "cover_url": "https://c.example/x.jpg", "ol_readers": "500"},
		Signals:   item.Signals{Similarity: 0.4, Learned: 0.9, LearnedWhy: []string{"cs.DC"}}},
		func(item.Item) []string { return []string{"consensus"} })
	if e.Byline != "A, B, C et al. · 2019 · 368 pages" || e.Cover == "" || !strings.HasSuffix(e.Snippet, "…") {
		t.Errorf("entry = %+v", e)
	}
	if e.Why != "matches consensus · similar to items you liked · you tend to like cs.DC · popular on Open Library" {
		t.Errorf("why = %q", e.Why)
	}
	undated := NewEntry(item.Item{Type: item.TypeBook, Authors: []string{"X"}, Published: time.Now()}, nil)
	if undated.Byline != "X" {
		t.Errorf("undated book shows a year: %q", undated.Byline)
	}
	a := NewEntry(item.Item{Type: item.TypeArticle, Authors: []string{"Julia Evans"}, Meta: map[string]string{"feed": "Julia Evans' blog", "domain": "jvns.ca"}}, nil)
	if a.Byline != "Julia Evans · Julia Evans' blog" {
		t.Errorf("article byline = %q", a.Byline)
	}
}
