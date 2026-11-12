package item

import (
	"testing"
	"time"
)

func TestCanonicalURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://www.Example.com/post/?utm_source=hn&b=2&a=1#comments", "https://example.com/post?a=1&b=2"},
		{"https://example.com/", "https://example.com"},
		{"https://example.com:8080/x?fbclid=abc", "https://example.com:8080/x"},
		{"https://example.com:443/x", "https://example.com/x"},
		{"http://arxiv.org/abs/2401.12345v3", "https://arxiv.org/abs/2401.12345"},
		{"https://arxiv.org/pdf/2401.12345v1.pdf", "https://arxiv.org/abs/2401.12345"},
		{"https://export.arxiv.org/abs/2401.1234", "https://arxiv.org/abs/2401.1234"},
		{"https://arxiv.org/abs/hep-th/9901001v2", "https://arxiv.org/abs/hep-th/9901001"},
		{"https://arxiv.org/abs/math.GT/0309136", "https://arxiv.org/abs/math.GT/0309136"},
		{"https://news.ycombinator.com/item?id=42", "https://news.ycombinator.com/item?id=42"},
	}
	for _, c := range cases {
		got, err := CanonicalURL(c.in)
		if err != nil {
			t.Errorf("CanonicalURL(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "ftp://example.com/x", "/relative/path", "mailto:a@b.c"} {
		if _, err := CanonicalURL(bad); err == nil {
			t.Errorf("CanonicalURL(%q): expected error", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	it := Item{
		Type:   TypePaper,
		Source: "arxiv",
		Title:  "  Attention\n   Is All\tYou Need ",
		URL:    "http://arxiv.org/abs/1706.03762v7",
		Tags:   []string{"cs.CL", "cs.cl", " ", "cs.LG"},
	}
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	if it.Title != "Attention Is All You Need" {
		t.Errorf("title = %q", it.Title)
	}
	if it.URL != "https://arxiv.org/abs/1706.03762" || it.ID != IDFor(it.URL) || len(it.ID) != 16 {
		t.Errorf("url/id = %q/%q", it.URL, it.ID)
	}
	if len(it.Tags) != 2 {
		t.Errorf("tags = %v", it.Tags)
	}
	if it.Published.IsZero() {
		t.Error("published not defaulted")
	}

	bad := Item{Type: "podcast", Title: "x", URL: "https://example.com"}
	if err := bad.Normalize(); err == nil {
		t.Error("expected error for invalid type")
	}
}

func TestDedupeKey(t *testing.T) {
	if k := DedupeKey(Item{Title: "Show HN: Prashna!"}); k != "" {
		t.Errorf("short title should not be dedupe-eligible, got %q", k)
	}
	a := DedupeKey(Item{Title: "Scaling Laws for Neural Language Models"})
	b := DedupeKey(Item{Title: "Scaling laws for neural language models."})
	if a == "" || a != b {
		t.Errorf("keys differ: %q vs %q", a, b)
	}

	ol := Item{Type: TypeBook, Title: "Exhalation", Authors: []string{"Ted Chiang"}}
	gb := Item{Type: TypeBook, Title: "Exhalation: Stories", Authors: []string{"TED CHIANG", "Someone Else"}}
	if k := DedupeKey(ol); k != "book:exhalation|chiang" || k != DedupeKey(gb) {
		t.Errorf("book keys: %q vs %q", k, DedupeKey(gb))
	}
	if k := DedupeKey(Item{Type: TypeBook, Title: "Exhalation"}); k != "" {
		t.Errorf("book without author should not dedupe: %q", k)
	}
}

func TestMergeDatePrecision(t *testing.T) {
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ol := Item{Type: TypeBook, Published: jan, Meta: map[string]string{MetaDatePrecision: "year", "ol_key": "x"}}
	gb := Item{Type: TypeBook, Published: oct, Meta: map[string]string{MetaDatePrecision: "day"}}
	for _, m := range []Item{Merge(ol, gb), Merge(gb, ol)} {
		if !m.Published.Equal(oct) || m.Meta[MetaDatePrecision] != "day" {
			t.Errorf("precise same-year date should win: %v %v", m.Published, m.Meta)
		}
	}
	reissue := Item{Type: TypeBook, Published: oct, Meta: map[string]string{MetaDatePrecision: "day"}}
	original := Item{Type: TypeBook, Published: time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC), Meta: map[string]string{MetaDatePrecision: "year"}}
	if m := Merge(reissue, original); m.Published.Year() != 2019 || m.Meta[MetaDatePrecision] != "year" {
		t.Errorf("original year should beat a reissue: %v %v", m.Published, m.Meta)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("alpha beta gamma delta", 13); got != "alpha beta…" {
		t.Errorf("got %q", got)
	}
}

func TestMerge(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	existing := Item{
		ID: "aaa", Type: TypeNews, Source: "hackernews", Title: "HN title", URL: "https://arxiv.org/abs/1",
		Tags: []string{"hn"}, Meta: map[string]string{"hn_points": "10"},
		Published: early.Add(time.Hour), Feedback: Liked, Score: 0.9,
	}
	incoming := Item{
		ID: "aaa", Type: TypePaper, Source: "arxiv", Title: "Paper title", Summary: "An abstract.",
		Tags: []string{"cs.LG", "HN"}, Meta: map[string]string{"arxiv_id": "1"}, Published: early,
	}
	m := Merge(existing, incoming)
	if m.Type != TypePaper || m.Title != "Paper title" || m.Source != "arxiv" {
		t.Errorf("richer type should win: %+v", m)
	}
	if m.Summary != "An abstract." || m.Feedback != Liked || m.Score != 0.9 {
		t.Errorf("summary/user state wrong: %+v", m)
	}
	if len(m.Tags) != 2 || m.Meta["hn_points"] != "10" || m.Meta["arxiv_id"] != "1" {
		t.Errorf("tags/meta not unioned: %v %v", m.Tags, m.Meta)
	}
	if !m.Published.Equal(early) {
		t.Errorf("published = %v, want earliest", m.Published)
	}
	if existing.Meta["arxiv_id"] != "" {
		t.Error("Merge mutated existing.Meta")
	}
}

func TestPlainText(t *testing.T) {
	got := PlainText("<p>I built <i>a thing</i> &amp; it&#x27;s neat.<p>Second &lt;para&gt;")
	if got != "I built a thing & it's neat. Second <para>" {
		t.Errorf("PlainText = %q", got)
	}
}
