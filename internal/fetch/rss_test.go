package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/store"
)

func TestParseRSS2(t *testing.T) {
	f, err := ParseFeed(serveFile(t, "testdata/rss2.xml"), "https://blog.example.com/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Example & Co Blog" {
		t.Errorf("title = %q", f.Title)
	}
	if len(f.Items) != 3 { // linkless item dropped
		t.Fatalf("got %d items", len(f.Items))
	}
	a := f.Items[0]
	if a.Title != "Notes on Raft snapshots" && a.Title != "Notes on Raft snapshots" {
		t.Errorf("title = %q", a.Title)
	}
	if a.URL != "https://blog.example.com/posts/raft-snapshots/" {
		t.Errorf("relative link not resolved: %q", a.URL)
	}
	if a.Summary != "How Raft compacts its log & why it matters." {
		t.Errorf("summary = %q", a.Summary)
	}
	if !slices.Equal(a.Authors, []string{"Jane Doe"}) || !slices.Equal(a.Tags, []string{"distributed systems", "raft"}) {
		t.Errorf("authors=%v tags=%v", a.Authors, a.Tags)
	}
	if want := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC); !a.Published.Equal(want) {
		t.Errorf("published = %v", a.Published)
	}
	if a.Meta["domain"] != "blog.example.com" || a.Type != item.TypeArticle || a.Source != "rss" {
		t.Errorf("meta/type = %v %s %s", a.Meta, a.Type, a.Source)
	}

	b := f.Items[1]
	if b.URL != "https://blog.example.com/posts/content-only" || b.Summary != "Escaped HTML body." {
		t.Errorf("guid permalink / content fallback: %q %q", b.URL, b.Summary)
	}
	if !slices.Equal(b.Authors, []string{"Jane Doe"}) {
		t.Errorf("rss author = %v", b.Authors)
	}
}

func TestParseAtom(t *testing.T) {
	f, err := ParseFeed(serveFile(t, "testdata/atom.xml"), "https://atom.example.org/atom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Atom Example" || len(f.Items) != 2 {
		t.Fatalf("title=%q items=%d", f.Title, len(f.Items))
	}
	a := f.Items[0]
	if a.Title != "Consensus without leaders" || a.URL != "https://atom.example.org/2026/11/leaderless" {
		t.Errorf("title/url = %q %q", a.Title, a.URL)
	}
	if a.Summary != "A short summary." {
		t.Errorf("xhtml summary = %q", a.Summary)
	}
	if want := time.Date(2026, 11, 7, 6, 15, 0, 0, time.UTC); !a.Published.Equal(want) {
		t.Errorf("published = %v, want %v", a.Published, want)
	}
	if !slices.Equal(a.Tags, []string{"Consensus", "paxos"}) || !slices.Equal(a.Authors, []string{"Lee Author"}) {
		t.Errorf("tags=%v authors=%v", a.Tags, a.Authors)
	}
	b := f.Items[1]
	if b.URL != "https://atom.example.org/2026/10/older" || b.Summary != "Plain text content." || b.Published.Day() != 30 {
		t.Errorf("second entry = %+v", b)
	}
}

func TestParseRDFAndCharset(t *testing.T) {
	f, err := ParseFeed(serveFile(t, "testdata/rdf.xml"), "https://rdf.example.net/rss")
	if err != nil || len(f.Items) != 1 || f.Items[0].Published.Day() != 1 || f.Title != "RDF Site" {
		t.Fatalf("rdf: %+v %v", f, err)
	}
	f, err = ParseFeed(serveFile(t, "testdata/latin1.xml"), "https://latin.example.com/feed")
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Café Notes" || len(f.Items) != 1 || f.Items[0].Title != "Crème brûlée and B-trees" {
		t.Errorf("latin1: %+v", f)
	}
	if _, err := ParseFeed([]byte("<html><body>not a feed</body></html>"), "https://x/"); err == nil {
		t.Error("html page accepted as feed")
	}
}

func TestParseDate(t *testing.T) {
	want := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)
	for _, s := range []string{
		"Fri, 06 Nov 2026 09:30:00 +0000",
		"Fri, 6 Nov 2026 09:30:00 GMT",
		"Thu, 06 Nov 2026 09:30:00 +0000", // wrong weekday
		"06 Nov 2026 09:30:00 +0000",
		"2026-11-06T09:30:00Z",
		"2026-11-06T15:15:00+05:45",
		"  2026-11-06T09:30:00.000Z ",
		"2026-11-06 09:30:00",
	} {
		if got, ok := parseDate(s); !ok || !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v %v", s, got, ok)
		}
	}
	if _, ok := parseDate("last tuesday"); ok {
		t.Error("garbage parsed")
	}
}

type memState struct {
	mu sync.Mutex
	m  map[string]store.FeedState
}

func (s *memState) FeedState(_ context.Context, u string) (store.FeedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fs, ok := s.m[u]; ok {
		return fs, nil
	}
	return store.FeedState{URL: u}, nil
}

func (s *memState) SaveFeedState(_ context.Context, fs store.FeedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[fs.URL] = fs
	return nil
}

func TestRSSFetchConditionalAndPartial(t *testing.T) {
	rss2 := serveFile(t, "testdata/rss2.xml")
	var conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			if r.Header.Get("If-None-Match") == `"v1"` {
				conditional++
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Write(rss2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	state := &memState{m: map[string]store.FeedState{}}
	r := &RSS{
		Client: testClient(),
		Feeds: []Feed{
			{URL: srv.URL + "/feed.xml", Tags: []string{"blogs"}, Type: item.TypeNews},
			{URL: srv.URL + "/missing.xml", Name: "Broken"},
		},
		State:  state,
		MaxAge: 365 * 24 * time.Hour,
		Now:    func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) },
	}
	items, err := r.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing.xml") {
		t.Errorf("err = %v, want failure naming the broken feed", err)
	}
	if len(items) != 2 { // ancient post dropped by MaxAge
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Type != item.TypeNews || !slices.Contains(it.Tags, "blogs") || it.Meta["feed"] != "Example & Co Blog" {
		t.Errorf("feed overrides not applied: %+v", it)
	}
	good := state.m[srv.URL+"/feed.xml"]
	if good.ETag != `"v1"` || good.LastItems != 2 || good.LastError != "" || good.Title != "Example & Co Blog" {
		t.Errorf("good feed state = %+v", good)
	}
	if bad := state.m[srv.URL+"/missing.xml"]; bad.LastError == "" || !bad.LastOKAt.IsZero() {
		t.Errorf("bad feed state = %+v", bad)
	}

	r.Feeds = r.Feeds[:1]
	items, err = r.Fetch(context.Background())
	if err != nil || len(items) != 0 || conditional != 1 {
		t.Errorf("second fetch: items=%d err=%v conditional=%d", len(items), err, conditional)
	}
	if st := state.m[srv.URL+"/feed.xml"]; st.ETag != `"v1"` || st.LastOKAt.IsZero() || st.LastItems != 2 {
		t.Errorf("304 lost validators: %+v", st)
	}
}

func TestProbeFeedDiscovery(t *testing.T) {
	atom := serveFile(t, "testdata/atom.xml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`<!doctype html><html><head>
				<link rel="stylesheet" href="/s.css">
				<link rel='alternate' type='application/atom+xml' title='Posts' href='/atom.xml'>
				</head><body>Blog</body></html>`))
		case "/atom.xml":
			w.Write(atom)
		case "/plain":
			w.Write([]byte(`<html><head><title>No feed</title></head></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient()

	title, u, err := ProbeFeed(context.Background(), c, srv.URL+"/atom.xml")
	if err != nil || title != "Atom Example" || u != srv.URL+"/atom.xml" {
		t.Errorf("direct: %q %q %v", title, u, err)
	}
	title, u, err = ProbeFeed(context.Background(), c, srv.URL+"/")
	if err != nil || title != "Atom Example" || u != srv.URL+"/atom.xml" {
		t.Errorf("discovered: %q %q %v", title, u, err)
	}
	if _, _, err := ProbeFeed(context.Background(), c, srv.URL+"/plain"); !errors.Is(err, ErrNotAFeed) {
		t.Errorf("plain page: %v", err)
	}
}
