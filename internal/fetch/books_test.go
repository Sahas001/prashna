package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

func TestOpenLibrary(t *testing.T) {
	subject, search, work := serveFile(t, "testdata/openlibrary_subject.json"), serveFile(t, "testdata/openlibrary_search.json"), serveFile(t, "testdata/openlibrary_work.json")
	var described []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/works/") {
			described = append(described, r.URL.Path)
			if r.URL.Path == "/works/OL99W.json" {
				w.Write(work)
			} else {
				w.Write([]byte(`{"description": "A book about data."}`))
			}
			return
		}
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
		// The DDIA description is already stored: only Exhalation needs one.
		NeedsSummary: func(_ context.Context, it item.Item) bool { return it.Meta["ol_key"] != "/works/OL17800000W" },
	}
	items, err := o.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 { // the other-author result and the authorless work are dropped
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	ex, ddia := items[0], items[1]
	if ex.Meta[MetaFollowedAuthor] != "Ted Chiang" || ex.Meta["pages"] != "350" {
		t.Errorf("author result meta = %v", ex.Meta)
	}
	if ex.Summary != "Nine stunningly original stories, from Ted Chiang." {
		t.Errorf("description not cleaned: %q", ex.Summary)
	}
	if !slices.Equal(described, []string{"/works/OL99W.json"}) {
		t.Errorf("described = %v", described)
	}
	if ddia.URL != "https://openlibrary.org/works/OL17800000W" || ddia.Published.Year() != 2017 || ddia.Meta[item.MetaDatePrecision] != "year" {
		t.Errorf("ddia = %+v", ddia)
	}
	if !slices.Equal(ddia.Tags, []string{"Distributed databases", "Database management", "Distributed Systems"}) {
		t.Errorf("tags not cleaned: %v", ddia.Tags)
	}
	if ddia.Meta["cover_url"] != "https://covers.openlibrary.org/b/id/8434231-M.jpg" || ddia.Meta[MetaOLReaders] != "412" || ddia.Summary != "" {
		t.Errorf("ddia meta = %v summary %q", ddia.Meta, ddia.Summary)
	}
}

func TestWikidata(t *testing.T) {
	fixture := serveFile(t, "testdata/wikidata.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if r.Header.Get("Accept") != "application/sparql-results+json" || r.UserAgent() != "prashna-test" {
			t.Errorf("headers: %v", r.Header)
		}
		// Both the typed and the title-cased name are queried.
		if !strings.Contains(q, `"ursula k. le guin"@en`) || !strings.Contains(q, `"Ursula K. Le Guin"@en`) || !strings.Contains(q, "LIMIT 50") {
			t.Errorf("query = %s", q)
		}
		w.Write(fixture)
	}))
	defer srv.Close()

	wd := &Wikidata{Client: testClient(), Endpoint: srv.URL, Authors: []string{"Ted Chiang", "ursula k. le guin"}}
	items, err := wd.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 { // the unlabelled work (label "Q999") is dropped
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	ex := items[0]
	if err := ex.Normalize(); err != nil {
		t.Fatal(err)
	}
	if ex.URL != "https://openlibrary.org/works/OL20149336W" || ex.Meta["ol_key"] != "/works/OL20149336W" {
		t.Errorf("Open Library link: %q %v", ex.URL, ex.Meta)
	}
	if !ex.Published.Equal(time.Date(2019, 5, 7, 0, 0, 0, 0, time.UTC)) || ex.Meta[item.MetaDatePrecision] != "day" || ex.Meta[MetaFollowedAuthor] != "Ted Chiang" {
		t.Errorf("date/meta: %v %v", ex.Published, ex.Meta)
	}
	lg := items[1]
	if lg.URL != "https://www.wikidata.org/wiki/Q123" || lg.Meta[item.MetaDatePrecision] != "year" || lg.Authors[0] != "ursula k. le guin" {
		t.Errorf("second = %+v", lg)
	}

	// Merging with the Open Library record keeps one item with the exact date.
	ol := item.Item{Type: item.TypeBook, Source: "openlibrary", Title: "Exhalation", URL: "https://openlibrary.org/works/OL20149336W",
		Authors: []string{"Ted Chiang"}, Summary: "Nine stories.", Published: time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC),
		Meta: map[string]string{item.MetaDatePrecision: "year"}}
	ol.Normalize()
	if ol.ID != ex.ID {
		t.Fatalf("Open Library and Wikidata IDs differ: %s vs %s", ol.ID, ex.ID)
	}
	m := item.Merge(ol, ex)
	if m.Published.Month() != time.May || m.Summary != "2019 short story collection by Ted Chiang" && m.Summary != "Nine stories." {
		t.Errorf("merged = %+v", m)
	}
}

func TestWikidataNoAuthors(t *testing.T) {
	items, err := (&Wikidata{Client: testClient(), Endpoint: "http://127.0.0.1:1"}).Fetch(context.Background())
	if err != nil || items != nil {
		t.Errorf("no authors should be a no-op: %v %v", items, err)
	}
}
