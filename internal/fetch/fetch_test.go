package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func testClient() *Client {
	c := NewClient("prashna-test")
	c.RetryDelay = time.Millisecond
	return c
}

func serveFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestArXivFetch(t *testing.T) {
	fixture := serveFile(t, "testdata/arxiv.xml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("search_query"); got != "cat:cs.LG OR cat:cs.DC" {
			t.Errorf("search_query = %q", got)
		}
		if q.Get("sortBy") != "submittedDate" || q.Get("max_results") != "50" {
			t.Errorf("unexpected query %v", q)
		}
		if r.UserAgent() != "prashna-test" {
			t.Errorf("user agent = %q", r.UserAgent())
		}
		w.Write(fixture)
	}))
	defer srv.Close()

	a := &ArXiv{Client: testClient(), BaseURL: srv.URL, Categories: []string{"cs.LG", "cs.DC"}, MaxResults: 50}
	items, err := a.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	it := items[0]
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	if it.Title != "Leaderless Consensus for Geo-Distributed Key-Value Stores" {
		t.Errorf("title = %q", it.Title)
	}
	if it.URL != "https://arxiv.org/abs/2611.01234" {
		t.Errorf("url = %q", it.URL)
	}
	if it.Summary == "" || it.Summary[0] == ' ' {
		t.Errorf("summary = %q", it.Summary)
	}
	if !slices.Equal(it.Tags, []string{"cs.DC", "cs.DB"}) {
		t.Errorf("tags = %v", it.Tags)
	}
	if !slices.Equal(it.Authors, []string{"Ada Lovelace", "Leslie Lamport"}) {
		t.Errorf("authors = %v", it.Authors)
	}
	if it.Meta["arxiv_id"] != "2611.01234" || it.Meta["pdf_url"] == "" || it.Meta["arxiv_comment"] != "12 pages, 5 figures" {
		t.Errorf("meta = %v", it.Meta)
	}
	if want := time.Date(2026, 11, 6, 17, 30, 0, 0, time.UTC); !it.Published.Equal(want) {
		t.Errorf("published = %v", it.Published)
	}
	if items[1].Meta["doi"] != "10.1234/example.5678" {
		t.Errorf("doi meta = %v", items[1].Meta)
	}
}

func TestArXivAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry>
			<id>http://arxiv.org/api/errors#incorrect_id_format_for_x</id>
			<title>Error</title><summary>incorrect id format for x</summary></entry></feed>`))
	}))
	defer srv.Close()
	a := &ArXiv{Client: testClient(), BaseURL: srv.URL, Categories: []string{"cs.LG"}, MaxResults: 1}
	if _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("expected API error")
	}
}

func TestHackerNewsFetch(t *testing.T) {
	pages := [][]byte{serveFile(t, "testdata/hn_page0.json"), serveFile(t, "testdata/hn_page1.json")}
	now := time.Unix(1762700000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search_by_date" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("tags") != "story" {
			t.Errorf("tags = %q", q.Get("tags"))
		}
		if want := "created_at_i>1762613600,points>=50"; q.Get("numericFilters") != want {
			t.Errorf("numericFilters = %q, want %q", q.Get("numericFilters"), want)
		}
		switch q.Get("page") {
		case "0":
			w.Write(pages[0])
		case "1":
			w.Write(pages[1])
		default:
			t.Errorf("unexpected page %q", q.Get("page"))
		}
	}))
	defer srv.Close()

	h := &HackerNews{
		Client: testClient(), BaseURL: srv.URL, MinPoints: 50, Lookback: 24 * time.Hour,
		MaxResults: 10, Now: func() time.Time { return now },
	}
	items, err := h.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 { // the untitled hit is dropped
		t.Fatalf("got %d items, want 3", len(items))
	}

	paper := items[0]
	if err := paper.Normalize(); err != nil {
		t.Fatal(err)
	}
	if paper.URL != "https://arxiv.org/abs/2611.01234" {
		t.Errorf("arxiv link not canonicalized: %q", paper.URL)
	}
	if paper.Meta["hn_points"] != "312" || paper.Meta["hn_url"] != "https://news.ycombinator.com/item?id=45000001" {
		t.Errorf("meta = %v", paper.Meta)
	}

	ask := items[1]
	if ask.URL != "https://news.ycombinator.com/item?id=45000002" {
		t.Errorf("ask hn url = %q", ask.URL)
	}
	if ask.Summary != "I'm drowning in arXiv & HN. What works for you?" {
		t.Errorf("ask hn summary = %q", ask.Summary)
	}
	if !slices.Contains(ask.Tags, "ask_hn") {
		t.Errorf("ask hn tags = %v", ask.Tags)
	}
	if items[2].Meta["domain"] != "example.com" {
		t.Errorf("domain = %q", items[2].Meta["domain"])
	}
}

func TestHackerNewsMaxResultsStopsPaging(t *testing.T) {
	page0 := serveFile(t, "testdata/hn_page0.json")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write(page0)
	}))
	defer srv.Close()
	h := &HackerNews{Client: testClient(), BaseURL: srv.URL, Lookback: time.Hour, MaxResults: 1}
	items, err := h.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || calls.Load() != 1 {
		t.Errorf("items=%d calls=%d, want 1/1", len(items), calls.Load())
	}
}

func TestClientRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	body, err := testClient().Get(context.Background(), srv.URL)
	if err != nil || string(body) != "ok" || calls.Load() != 3 {
		t.Fatalf("body=%q err=%v calls=%d", body, err, calls.Load())
	}
}

func TestClientDoesNotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, err := testClient().Get(context.Background(), srv.URL); err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
