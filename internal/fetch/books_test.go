package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"radar/internal/item"
)

func TestGoogleBooks(t *testing.T) {
	author := serveFile(t, "testdata/googlebooks_author.json")
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, q.Get("q")+"|"+q.Get("orderBy")+"|"+q.Get("langRestrict")+"|"+q.Get("key"))
		switch {
		case strings.HasPrefix(q.Get("q"), "inauthor:"):
			w.Write(author)
		case q.Get("q") == `subject:"broken"`:
			http.Error(w, "nope", http.StatusBadRequest)
		default:
			w.Write([]byte(`{"totalItems":0}`))
		}
	}))
	defer srv.Close()

	g := &GoogleBooks{
		Client: testClient(), BaseURL: srv.URL, APIKey: "k", Language: "en",
		Queries: BookQueries{Authors: []string{"Ted Chiang"}, Subjects: []string{"science fiction", "broken"}},
	}
	items, err := g.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("err = %v, want partial failure for subject 'broken'", err)
	}
	want := []string{`inauthor:"Ted Chiang"|newest|en|k`, `subject:"science fiction"|relevance|en|k`, `subject:"broken"|relevance|en|k`}
	if !slices.Equal(queries, want) {
		t.Errorf("queries = %q", queries)
	}
	if len(items) != 2 { // summary and other-author volumes dropped
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	b := items[0]
	if err := b.Normalize(); err != nil {
		t.Fatal(err)
	}
	if b.Title != "Exhalation: Stories" || b.URL != "https://books.google.com/books?id=gb-new-1" || b.Type != item.TypeBook {
		t.Errorf("book = %+v", b)
	}
	if b.Summary != "Nine stunningly original stories." {
		t.Errorf("summary = %q", b.Summary)
	}
	if !b.Published.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || b.Meta[item.MetaDatePrecision] != "day" {
		t.Errorf("published = %v %v", b.Published, b.Meta)
	}
	if b.Meta[MetaFollowedAuthor] != "Ted Chiang" || b.Meta["isbn"] != "9781101947883" || b.Meta["pages"] != "368" {
		t.Errorf("meta = %v", b.Meta)
	}
	if c := b.Meta["cover_url"]; !strings.HasPrefix(c, "https://") || strings.Contains(c, "edge=curl") {
		t.Errorf("cover = %q", c)
	}
	if !slices.Equal(b.Tags, []string{"Fiction", "Science Fiction", "Short Stories"}) {
		t.Errorf("tags = %v", b.Tags)
	}
	if old := items[1]; old.Published.Year() != 2002 || old.Meta[item.MetaDatePrecision] != "year" {
		t.Errorf("year-only date: %v %v", old.Published, old.Meta)
	}
}

func TestOpenLibrary(t *testing.T) {
	subject, search := serveFile(t, "testdata/openlibrary_subject.json"), serveFile(t, "testdata/openlibrary_search.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/search.json" || q.Get("language") != "eng" || !strings.Contains(q.Get("fields"), "want_to_read_count") {
			t.Errorf("unexpected request %s", r.URL)
		}
		switch {
		case q.Get("subject") == "Distributed Systems" && q.Get("sort") == "readinglog":
			w.Write(subject)
		case q.Get("author") == "Ted Chiang" && q.Get("sort") == "new":
			w.Write(search)
		default:
			t.Errorf("unexpected query %v", q)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := &OpenLibrary{
		Client: testClient(), BaseURL: srv.URL, Language: "eng",
		Queries: BookQueries{Authors: []string{"Ted Chiang"}, Subjects: []string{"Distributed Systems"}},
	}
	items, err := o.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	ddia := items[0]
	if ddia.URL != srv.URL+"/works/OL17800000W" || ddia.Published.Year() != 2017 || ddia.Meta[item.MetaDatePrecision] != "year" {
		t.Errorf("ddia = %+v", ddia)
	}
	if !slices.Equal(ddia.Tags, []string{"Distributed databases", "Database management", "Distributed Systems"}) {
		t.Errorf("tags not cleaned: %v", ddia.Tags)
	}
	if ddia.Meta["cover_url"] != "https://covers.openlibrary.org/b/id/8434231-M.jpg" || ddia.Meta[MetaFollowedAuthor] != "" || ddia.Meta[MetaOLReaders] != "412" {
		t.Errorf("meta = %v", ddia.Meta)
	}
	ex := items[1]
	if ex.Meta[MetaFollowedAuthor] != "Ted Chiang" || ex.Meta["pages"] != "350" {
		t.Errorf("author result meta = %v", ex.Meta)
	}

	// The same book from both APIs collapses to one dedupe key.
	gb := item.Item{Type: item.TypeBook, Title: "Exhalation: Stories", Authors: []string{"Ted Chiang"}}
	if item.DedupeKey(ex) != item.DedupeKey(gb) {
		t.Errorf("cross-source keys differ: %q vs %q", item.DedupeKey(ex), item.DedupeKey(gb))
	}
}

func TestParseBookDate(t *testing.T) {
	for in, want := range map[string]string{"2026-10-01": "day", "2026-10-01T00:00:00Z": "day", "2026-10": "month", "2026": "year", "": "", "c. 1990": ""} {
		if _, p := parseBookDate(in); p != want {
			t.Errorf("parseBookDate(%q) precision = %q, want %q", in, p, want)
		}
	}
}

func TestGoogleBooksStopsOnQuota(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":{"code":429}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := testClient()
	c.MaxRetries = 0
	g := &GoogleBooks{Client: c, BaseURL: srv.URL, Queries: BookQueries{Authors: []string{"A", "B"}, Subjects: []string{"C"}}}
	_, err := g.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API key") || calls != 1 {
		t.Errorf("err=%v calls=%d, want one call and an API key hint", err, calls)
	}
}
