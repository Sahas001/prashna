package digest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"radar/internal/item"
	"radar/internal/links"
	"radar/internal/store"
)

type fakeSource map[item.Type][]item.Item

func (f fakeSource) DigestCandidates(_ context.Context, q store.DigestQuery) ([]item.Item, error) {
	items := f[q.Type]
	return items[:min(len(items), q.Limit)], nil
}

var now = time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)

func sample() fakeSource {
	return fakeSource{
		item.TypePaper: {
			{ID: "aaaaaaaaaaaaaaaa", Type: item.TypePaper, Title: "Leaderless Consensus <Fast>", URL: "https://arxiv.org/abs/2611.01234",
				Authors: []string{"A", "B", "C", "D"}, Tags: []string{"cs.DC"}, Summary: strings.Repeat("word ", 100),
				Meta:    map[string]string{"hn_url": "https://news.ycombinator.com/item?id=1", "hn_points": "312", "hn_comments": "87"},
				Signals: item.Signals{Similarity: 0.4}},
			{ID: "bbbbbbbbbbbbbbbb", Type: item.TypePaper, Title: "Second paper", URL: "https://arxiv.org/abs/2611.00002"},
		},
		item.TypeNews: {
			{ID: "cccccccccccccccc", Type: item.TypeNews, Title: "Ask HN: Reading?", URL: "https://news.ycombinator.com/item?id=2",
				Meta: map[string]string{"hn_url": "https://news.ycombinator.com/item?id=2", "hn_points": "50", "hn_comments": "9", "domain": "news.ycombinator.com"}},
		},
	}
}

func TestBuild(t *testing.T) {
	d, err := Build(context.Background(), sample(), Options{
		Sections: map[item.Type]int{item.TypePaper: 1, item.TypeArticle: 5, item.TypeNews: 5},
		Lookback: 24 * time.Hour,
		Explain:  func(item.Item) []string { return []string{"consensus"} },
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sections) != 2 || d.Sections[0].Type != item.TypePaper || d.Sections[1].Type != item.TypeNews {
		t.Fatalf("sections = %+v", d.Sections)
	}
	if d.Count() != 2 || strings.Join(d.IDs(), ",") != "aaaaaaaaaaaaaaaa,cccccccccccccccc" {
		t.Errorf("count=%d ids=%v", d.Count(), d.IDs())
	}
	if got := d.Subject(); got != "Radar · Mon 9 Nov · 1 paper, 1 discussion" {
		t.Errorf("subject = %q", got)
	}

	e := d.Sections[0].Entries[0]
	if e.Byline != "A, B, C et al. · cs.DC · 312 points · 87 comments" {
		t.Errorf("byline = %q", e.Byline)
	}
	if e.Discussion == "" || !strings.HasSuffix(e.Snippet, "…") || len([]rune(e.Snippet)) > snippetRunes+1 || e.Snippet == "" {
		t.Errorf("discussion=%q snippet=%q", e.Discussion, e.Snippet)
	}
	if e.Why != "matches consensus · similar to items you liked · popular on HN" {
		t.Errorf("why = %q", e.Why)
	}
	if hn := d.Sections[1].Entries[0]; hn.Discussion != "" {
		t.Errorf("discussion link duplicated for text post: %q", hn.Discussion)
	}
}

func TestRender(t *testing.T) {
	d, err := Build(context.Background(), sample(), Options{
		Sections: map[item.Type]int{item.TypePaper: 5, item.TypeNews: 5}, Lookback: time.Hour,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	signer := links.NewSigner("https://radar.example.com", []byte("k"))
	r, err := Render(d, signer)
	if err != nil {
		t.Fatal(err)
	}
	up := signer.URL("aaaaaaaaaaaaaaaa", links.Up)
	for name, body := range map[string]string{"html": r.HTML, "text": r.Text} {
		if !strings.Contains(body, "Monday, 9 November") {
			t.Errorf("%s: missing date", name)
		}
		if !strings.Contains(body, "Second paper") || !strings.Contains(body, "Discussions") {
			t.Errorf("%s: missing entries/sections", name)
		}
	}
	if !strings.Contains(r.Text, up) {
		t.Errorf("text missing up link %s", up)
	}
	if !strings.Contains(r.HTML, strings.ReplaceAll(up, "&", "&amp;")) {
		t.Errorf("html missing up link")
	}
	if strings.Contains(r.HTML, "<Fast>") || !strings.Contains(r.HTML, "&lt;Fast&gt;") {
		t.Error("html title not escaped")
	}
	if r.Subject != "Radar · Mon 9 Nov · 2 papers, 1 discussion" {
		t.Errorf("subject = %q", r.Subject)
	}
	for _, line := range strings.Split(r.Text, "\n") {
		if !strings.Contains(line, "http") && len([]rune(line)) > 76 {
			t.Errorf("text line not wrapped (%d): %q", len(line), line)
		}
	}
}

func TestBooksBlock(t *testing.T) {
	src := fakeBookSource{
		releases: []item.Item{{ID: "r1", Type: item.TypeBook, Title: "Exhalation", Authors: []string{"Ted Chiang"},
			Published: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Meta: map[string]string{"publisher": "Knopf", "pages": "368", "cover_url": "https://covers.example/x.jpg", MetaFollowedAuthor: "Ted Chiang", item.MetaDatePrecision: "day"}}},
		// Best-scoring book is also the new release; the pick must skip it.
		best: []item.Item{
			{ID: "r1", Type: item.TypeBook, Title: "Exhalation"},
			{ID: "p1", Type: item.TypeBook, Title: "Designing Data-Intensive Applications", Authors: []string{"Martin Kleppmann"}, Published: time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC), Meta: map[string]string{item.MetaDatePrecision: "year"}},
		},
	}
	d, err := Build(context.Background(), src, Options{
		Sections: map[item.Type]int{item.TypePaper: 1},
		Books:    BookOptions{Include: true, PickOfWeek: true, NewReleases: 5, NewReleaseWindow: 180 * 24 * time.Hour},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasBooks() || len(d.Sections) != 2 {
		t.Fatalf("sections = %+v", d.Sections)
	}
	pick, rel := d.Sections[0], d.Sections[1]
	if pick.Title != "Book of the week" || len(pick.Entries) != 1 || pick.Entries[0].Item.ID != "p1" {
		t.Errorf("pick = %+v", pick)
	}
	if pick.Entries[0].Byline != "Martin Kleppmann · 2017" {
		t.Errorf("pick byline = %q", pick.Entries[0].Byline)
	}
	e := rel.Entries[0]
	if rel.Title != "New from authors you follow" || e.Byline != "Ted Chiang · 2026 · Knopf · 368 pages" || e.Cover == "" {
		t.Errorf("release = %+v", e)
	}
	if got := d.Subject(); got != "Radar · Mon 9 Nov · 1 book pick, 1 new release" {
		t.Errorf("subject = %q", got)
	}
	r, err := Render(d, links.NewSigner("https://r.example", []byte("k")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.HTML, `src="https://covers.example/x.jpg"`) || !strings.Contains(r.Text, "New from authors you follow") {
		t.Error("books not rendered")
	}

	undated := NewEntry(item.Item{Type: item.TypeBook, Authors: []string{"X"}, Published: now}, nil)
	if undated.Byline != "X" {
		t.Errorf("undated book shows a year: %q", undated.Byline)
	}

	// Without Include the books block is absent.
	d, _ = Build(context.Background(), src, Options{Books: BookOptions{PickOfWeek: true, NewReleases: 5}}, now)
	if d.HasBooks() {
		t.Error("books included without Include")
	}
}

type fakeBookSource struct{ releases, best []item.Item }

func (f fakeBookSource) DigestCandidates(_ context.Context, q store.DigestQuery) ([]item.Item, error) {
	if q.Type != item.TypeBook {
		return nil, nil
	}
	if slices.Contains(q.MetaKeys, MetaFollowedAuthor) {
		if !slices.Contains(q.MetaKeys, item.MetaDatePrecision) {
			return nil, fmt.Errorf("new releases must require a known date")
		}
		if q.PublishedSince.IsZero() {
			return nil, nil
		}
		return f.releases, nil
	}
	return f.best[:min(len(f.best), q.Limit)], nil
}

func TestArticleBylinePrefersFeedName(t *testing.T) {
	e := NewEntry(item.Item{Type: item.TypeArticle, Authors: []string{"Julia Evans"}, Meta: map[string]string{"feed": "Julia Evans' blog", "domain": "jvns.ca"}}, nil)
	if e.Byline != "Julia Evans · Julia Evans' blog" {
		t.Errorf("byline = %q", e.Byline)
	}
}
