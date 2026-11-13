package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/config"
	"github.com/Sahas001/prashna/internal/edition"
	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/learn"
	"github.com/Sahas001/prashna/internal/score"
	"github.com/Sahas001/prashna/internal/store"
)

func init() { loginFailWait = time.Millisecond }

type env struct {
	srv      *httptest.Server
	st       *store.Store
	s        *Server
	items    []item.Item
	fetches  atomic.Int32
	settings config.Settings
}

func newEnv(t *testing.T, auth Auth) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "prashna.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	var items []item.Item
	for i, spec := range []struct {
		typ item.Type
		url string
	}{
		{item.TypePaper, "https://arxiv.org/abs/2611.00001"},
		{item.TypeArticle, "https://blog.example.com/post"},
		{item.TypeNews, "https://news.example.com/story"},
		{item.TypeBook, "https://openlibrary.org/works/OL1W"},
	} {
		it := item.Item{Type: spec.typ, Source: "test", Title: "Item <" + string(spec.typ) + ">", URL: spec.url,
			Summary: "about consensus", Authors: []string{"A Author"}, Published: now, FetchedAt: now}
		if spec.typ == item.TypeBook {
			it.FetchedAt = now.AddDate(-1, 0, 0)
			it.Meta = map[string]string{"cover_url": "https://covers.example/c.jpg"}
		}
		if err := it.Normalize(); err != nil {
			t.Fatal(err)
		}
		it.Score = 0.9 - float64(i)*0.1
		items = append(items, it)
	}
	if _, err := st.SaveItems(ctx, items); err != nil {
		t.Fatal(err)
	}
	st.UpdateScores(ctx, items)
	st.SaveInterest(ctx, "consensus", 1)

	e := &env{st: st, items: items, settings: config.Default().Settings()}
	e.s = &Server{
		Store: st,
		Scorer: func(ctx context.Context) (*score.Scorer, error) {
			ins, err := st.Interests(ctx)
			var cfg score.Config
			for _, in := range ins {
				cfg.Interests = append(cfg.Interests, score.Interest{Term: in.Term, Weight: in.Weight})
			}
			return score.New(cfg), err
		},
		ProbeFeed: func(_ context.Context, u string) (string, string, error) {
			switch {
			case strings.Contains(u, "broken"):
				return "", "", errors.New("GET " + u + ": unexpected status 404 Not Found")
			case strings.HasSuffix(u, "/blog"):
				return "Discovered Blog", u + "/feed.xml", nil // a page that links to its feed
			}
			return "A Feed", u, nil
		},
		StartFetch:  func() { e.fetches.Add(1) },
		FetchStatus: func() (bool, time.Time, string) { return false, time.Time{}, "" },
		EnsureEdition: func(ctx context.Context) (bool, error) {
			return edition.Ensure(ctx, st, edition.Options{
				Sections: map[item.Type]int{item.TypePaper: 2, item.TypeArticle: 2, item.TypeNews: 2},
				Lookback: 7 * 24 * time.Hour,
				Books:    edition.BookOptions{PickOfWeek: true},
			}, time.Now())
		},
		Settings:     func(context.Context) config.Settings { return e.settings },
		SaveSettings: func(_ context.Context, s config.Settings) error { e.settings = s; return nil },
		Auth:         auth,
		SessionKey:   []byte("session-key"),
		About:        About{Version: "test", DataDir: "/data/prashna", Addr: "127.0.0.1:7878"},
	}
	st.SetSetting(ctx, onboardedKey, "yes")
	e.srv = httptest.NewServer(e.s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

// client keeps cookies and does not follow redirects.
func client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func do(t *testing.T, c *http.Client, method, u string, form url.Values, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, u, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

var hx = map[string]string{"HX-Request": "true"}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestHealthz(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	_, body := do(t, client(), "GET", e.srv.URL+"/healthz", nil, nil)
	if body != "prashna ok\n" {
		t.Errorf("healthz = %q (the CLI looks for this to detect a running Prashna)", body)
	}
}

func TestTodayMakesAndShowsEdition(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	resp, body := do(t, c, "GET", e.srv.URL+"/", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"Edition 1", "Item &lt;paper&gt;", `class="lead`, "Essays &amp; articles", "Book of the week",
		"That’s today’s edition", `data-open="` + e.items[3].ID + `"`, `href="https://openlibrary.org/works/OL1W"`,
		`<a href="/read/` + e.items[0].ID + `">`,
		`<a href="/read/` + e.items[1].ID + `">`, "matches consensus"} {
		if !strings.Contains(body, want) {
			t.Errorf("today missing %q", want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("CSP = %q", csp)
	}
	// The edition is fixed for the day: a new, better item doesn't change it.
	extra := item.Item{Type: item.TypeArticle, Source: "test", Title: "Late arrival", URL: "https://late.example/x", Published: time.Now()}
	extra.Normalize()
	extra.Score = 5
	e.st.SaveItems(context.Background(), []item.Item{extra})
	e.st.UpdateScores(context.Background(), []item.Item{extra})
	if _, body := do(t, c, "GET", e.srv.URL+"/", nil, nil); strings.Contains(body, "Late arrival") {
		t.Error("edition changed during the day")
	}
}

func TestTodayWhenNothingToShow(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	e.s.EnsureEdition = func(context.Context) (bool, error) { return false, nil }
	_, body := do(t, client(), "GET", e.srv.URL+"/", nil, nil)
	if !strings.Contains(body, "Nothing new yet") || !strings.Contains(body, `hx-post="/fetch"`) {
		t.Errorf("empty state: %.400s", body)
	}
	e.s.FetchStatus = func() (bool, time.Time, string) { return true, time.Time{}, "" }
	_, body = do(t, client(), "GET", e.srv.URL+"/", nil, nil)
	if !strings.Contains(body, "Putting together today’s edition") || !strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Errorf("preparing state should poll: %.400s", body)
	}
}

func TestWelcomeFlow(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "prashna.db"))
	defer st.Close()
	var fetched atomic.Int32
	settings := config.Default().Settings()
	s := &Server{Store: st, Auth: Auth{Disabled: true}, StartFetch: func() { fetched.Add(1) },
		Settings:     func(context.Context) config.Settings { return settings },
		SaveSettings: func(_ context.Context, v config.Settings) error { settings = v; return nil }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := client()

	resp, _ := do(t, c, "GET", srv.URL+"/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/welcome" {
		t.Fatalf("fresh install should go to welcome: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, body := do(t, c, "GET", srv.URL+"/welcome", nil, nil)
	if !strings.Contains(body, "What do you like to read?") || strings.Count(body, `name="topic"`) != len(topics) || strings.Contains(body, `class="masthead"`) {
		t.Errorf("welcome page: %.300s", body)
	}
	_, body = do(t, c, "POST", srv.URL+"/welcome", url.Values{}, nil)
	if !strings.Contains(body, "Pick at least one topic") {
		t.Error("empty submission not rejected")
	}

	// Multipart, as browsers send it, with an OPML file.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("topic", "systems")
	mw.WriteField("topic", "fiction")
	mw.WriteField("authors", "Ted Chiang\nUrsula K. Le Guin, Ted Chiang")
	fw, _ := mw.CreateFormFile("opml", "feeds.opml")
	fw.Write([]byte(`<?xml version="1.0"?><opml version="2.0"><body><outline text="Folder"><outline text="Mine" xmlUrl="https://mine.example/feed"/></outline></body></opml>`))
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/welcome", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" || fetched.Load() != 1 {
		t.Fatalf("submit: %d %q fetches=%d", resp.StatusCode, resp.Header.Get("Location"), fetched.Load())
	}
	authors, _ := st.BookFollowNames(ctx, store.FollowAuthor)
	if len(authors) != 2 || authors[0] != "Ted Chiang" || authors[1] != "Ursula K. Le Guin" {
		t.Errorf("authors = %v", authors)
	}
	subjects, _ := st.BookFollowNames(ctx, store.FollowSubject)
	feeds, _ := st.Feeds(ctx)
	ins, _ := st.Interests(ctx)
	if len(subjects) != 2 || len(feeds) != 4+3+1 || len(ins) < 6 {
		t.Errorf("subjects=%v feeds=%d interests=%d", subjects, len(feeds), len(ins))
	}
	if !settings.HNEnabled || !settings.ArxivEnabled || len(settings.ArxivCategories) != 2 {
		t.Errorf("a tech topic should turn on HN and its arXiv categories: %+v", settings)
	}
	if resp, _ := do(t, c, "GET", srv.URL+"/", nil, nil); resp.StatusCode != 200 {
		t.Errorf("after welcome, Today = %d", resp.StatusCode)
	}
}

func TestWelcomeNonTechKeepsHNOff(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "prashna.db"))
	defer st.Close()
	settings := config.Default().Settings()
	s := &Server{Store: st, Auth: Auth{Disabled: true},
		Settings:     func(context.Context) config.Settings { return settings },
		SaveSettings: func(_ context.Context, v config.Settings) error { settings = v; return nil }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	do(t, client(), "POST", srv.URL+"/welcome", url.Values{"topic": {"history", "sff"}}, nil)
	if settings.HNEnabled || settings.ArxivEnabled {
		t.Errorf("history and SF readers shouldn't get HN or arXiv: %+v", settings)
	}
	resp, _ := do(t, client(), "POST", srv.URL+"/welcome", url.Values{"skip": {"1"}}, nil)
	if resp.Header.Get("Location") != "/interests" {
		t.Errorf("skip -> %q", resp.Header.Get("Location"))
	}
}

func TestItemActionsAndOpens(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	id := e.items[1].ID

	_, body := do(t, c, "POST", e.srv.URL+"/items/"+id+"/save", url.Values{"v": {"lead"}}, hx)
	if !strings.Contains(body, `class="lead`) || !strings.Contains(body, "/unsave") || !strings.Contains(body, `hx-swap-oob="innerHTML:.library-count">1<`) {
		t.Errorf("save response keeps the variant and updates the badge: %s", body)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/items/"+id+"/rate/up", url.Values{"v": {"book"}}, hx)
	if !strings.Contains(body, `class="book is-liked"`) || !strings.Contains(body, "/rate/clear") || !strings.Contains(body, "Favorite") {
		t.Errorf("rate response: %s", body)
	}
	resp, _ := do(t, c, "POST", e.srv.URL+"/items/"+id+"/opened", url.Values{}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("opened = %d", resp.StatusCode)
	}
	do(t, c, "POST", e.srv.URL+"/items/"+id+"/opened", url.Values{}, nil)
	it, _ := e.st.Get(context.Background(), id)
	if it.OpenCount != 2 || it.ReadAt.IsZero() || it.Feedback != item.Liked || it.SavedAt.IsZero() {
		t.Errorf("stored state = %+v", it)
	}
	_, body = do(t, c, "GET", e.srv.URL+"/library?shelf=favorites", nil, nil)
	if !strings.Contains(body, "Item &lt;article&gt;") || !strings.Contains(body, "Opened 2 times") {
		t.Errorf("favorites shelf: %.600s", body)
	}
	_, body = do(t, c, "GET", e.srv.URL+"/library", nil, nil)
	if strings.Contains(body, "Item &lt;article&gt;") {
		t.Error("read items should leave the To read shelf")
	}
	for _, p := range []string{"/items/" + id + "/explode", "/items/" + id + "/rate/love", "/items/zzzz/save", "/items/zzzz/opened"} {
		if resp, _ := do(t, c, "POST", e.srv.URL+p, url.Values{}, hx); resp.StatusCode < 400 {
			t.Errorf("POST %s = %d", p, resp.StatusCode)
		}
	}
	if resp, _ := do(t, c, "POST", e.srv.URL+"/items/0123456789abcdef/save", url.Values{}, hx); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown item = %d", resp.StatusCode)
	}
}

func TestPages(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	ctx := context.Background()
	do(t, c, "GET", e.srv.URL+"/", nil, nil) // makes the edition
	e.st.SetFeedback(ctx, e.items[2].ID, item.Disliked)
	e.st.SetJSONSetting(ctx, learn.ReportKey, learn.Report{Ratings: 5, Status: "Rate 15 more items to start learning."})

	cases := []struct {
		path      string
		want, not []string
	}{
		{"/explore", []string{"Everything", "Item &lt;article&gt;"}, []string{"Item &lt;book&gt;"}},
		{"/explore?type=book", []string{"Item &lt;book&gt;", `src="https://covers.example/c.jpg"`}, nil},
		{"/library?shelf=read", []string{"Everything you open"}, nil},
		{"/archive", []string{"No. 1", "pieces"}, nil},
		{"/archive/1", []string{"Edition 1", "Item &lt;paper&gt;"}, nil},
		{"/archive?tab=skipped", []string{"Item &lt;news&gt;"}, nil},
		{"/search?q=consensus", []string{"results", "Item &lt;paper&gt;"}, nil},
		{"/search?q=zzzz", []string{"Nothing matches"}, nil},
		{"/interests", []string{"consensus", "Authors you follow", "Book subjects", "Rate 15 more", `<progress`}, nil},
		{"/feeds", []string{"You don’t follow any feeds yet", "OPML"}, nil},
		{"/settings", []string{"Each day’s edition", "On Sundays", "/data/prashna", "Hacker News"}, nil},
		{"/more", []string{"Archive", "Settings"}, nil},
	}
	for _, tc := range cases {
		resp, body := do(t, c, "GET", e.srv.URL+tc.path, nil, nil)
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d", tc.path, resp.StatusCode)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s: missing %q", tc.path, w)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(body, n) {
				t.Errorf("GET %s: unexpected %q", tc.path, n)
			}
		}
	}
	if resp, _ := do(t, c, "GET", e.srv.URL+"/archive/999", nil, nil); resp.StatusCode != 404 {
		t.Errorf("missing edition = %d", resp.StatusCode)
	}
	// Live search answers with just the results fragment.
	_, body := do(t, c, "GET", e.srv.URL+"/search?q=consensus", nil, hx)
	if strings.Contains(body, "<html") || !strings.Contains(body, `id="results"`) {
		t.Errorf("search fragment: %.200s", body)
	}
}

func TestExplorePaging(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	var many []item.Item
	for i := range pageSize + 5 {
		it := item.Item{Type: item.TypeArticle, Source: "rss", Title: "Bulk " + strconv.Itoa(i), URL: "https://bulk.example/" + strconv.Itoa(i), Published: time.Now()}
		it.Normalize()
		many = append(many, it)
	}
	e.st.SaveItems(context.Background(), many)
	c := client()
	_, body := do(t, c, "GET", e.srv.URL+"/explore", nil, nil)
	if n := strings.Count(body, `<article class="story`); n != pageSize || !strings.Contains(body, "offset=20") {
		t.Fatalf("first page: %d stories", n)
	}
	_, body = do(t, c, "GET", e.srv.URL+"/explore/more?offset=20", nil, hx)
	if n := strings.Count(body, `<article class="story`); n != 8 || strings.Contains(body, "Show more") {
		t.Errorf("second page: %d stories", n)
	}
}

func TestInterestsAndFollows(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	ctx := context.Background()
	_, body := do(t, c, "POST", e.srv.URL+"/interests", url.Values{"term": {"Typography"}, "weight": {"9"}}, hx)
	if !strings.Contains(body, `id="interests"`) || !strings.Contains(body, "Typography") || strings.Contains(body, "<html") {
		t.Errorf("add interest: %.300s", body)
	}
	ins, _ := e.st.Interests(ctx)
	if len(ins) != 2 || ins[0].Term != "Typography" || ins[0].Weight != maxWeight {
		t.Fatalf("interests = %+v", ins)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/interests", url.Values{"term": {"go"}, "weight": {"lots"}}, hx)
	if !strings.Contains(body, "weight must be a number") || !strings.Contains(body, `value="go"`) {
		t.Error("bad weight not reported with the term kept")
	}
	do(t, c, "POST", e.srv.URL+"/interests/"+itoa(ins[0].ID)+"/weight", url.Values{"weight": {"-1"}}, hx)
	_, body = do(t, c, "POST", e.srv.URL+"/interests/"+itoa(ins[1].ID)+"/delete", url.Values{}, hx)
	if !strings.Contains(body, "Less of") {
		t.Errorf("negative interest should read as 'Less of': %.400s", body)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/follows", url.Values{"kind": {"author"}, "name": {"Ted Chiang"}}, hx)
	if !strings.Contains(body, `id="follows-author"`) || !strings.Contains(body, "Ted Chiang") {
		t.Errorf("add follow: %.300s", body)
	}
	fs, _ := e.st.BookFollows(ctx, store.FollowAuthor)
	do(t, c, "POST", e.srv.URL+"/follows/"+itoa(fs[0].ID)+"/delete", url.Values{"kind": {"author"}}, hx)
	if fs, _ = e.st.BookFollows(ctx, store.FollowAuthor); len(fs) != 0 {
		t.Errorf("follow not deleted")
	}
}

func TestFeedsAndOPML(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	ctx := context.Background()

	_, body := do(t, c, "POST", e.srv.URL+"/feeds", url.Values{"url": {"site.example/blog"}, "tags": {"go, , systems"}, "type": {"news"}}, hx)
	if !strings.Contains(body, "Added Discovered Blog") || !strings.Contains(body, "https://site.example/blog/feed.xml") {
		t.Errorf("add feed (scheme added, feed discovered): %.500s", body)
	}
	for in, want := range map[string]string{
		"https://site.example/blog/feed.xml": "already follow",
		"https://broken.example/rss":         "Couldn’t find a feed there: unexpected status 404 Not Found.",
		"ftp://nope":                         "Paste the address",
	} {
		_, body := do(t, c, "POST", e.srv.URL+"/feeds", url.Values{"url": {in}, "name": {"Kept"}}, hx)
		if !strings.Contains(body, want) || !strings.Contains(body, `value="Kept"`) {
			t.Errorf("add %s: want %q", in, want)
		}
	}

	// OPML import with folders, duplicates and junk.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("opml", "subs.opml")
	fw.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<opml version="1.0"><head><title>subs</title></head><body>
  <outline text="Design" title="Design">
    <outline type="rss" text="Butterick" xmlUrl="https://practicaltypography.com/feed.xml"/>
    <outline type="rss" text="Dup" xmlUrl="https://site.example/blog/feed.xml"/>
  </outline>
  <outline type="rss" text="Plain" title="Plain Blog" xmlUrl="https://plain.example/rss"/>
  <outline type="rss" text="Bad" xmlUrl="javascript:alert(1)"/>
</body></opml>`))
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/feeds/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Imported 2 new feeds from 4 in the file") {
		t.Errorf("import: %.400s", b)
	}
	feeds, _ := e.st.Feeds(ctx)
	if len(feeds) != 3 {
		t.Fatalf("feeds = %d", len(feeds))
	}
	var design store.Feed
	for _, f := range feeds {
		if strings.Contains(f.URL, "practicaltypography") {
			design = f
		}
	}
	if design.Name != "Butterick" || len(design.Tags) != 1 || design.Tags[0] != "Design" {
		t.Errorf("folder should become a tag: %+v", design)
	}

	resp, body = do(t, c, "GET", e.srv.URL+"/feeds/export", nil, nil)
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "prashna-feeds.opml") || strings.Count(body, "xmlUrl=") != 3 {
		t.Errorf("export: %s %.300s", resp.Header.Get("Content-Disposition"), body)
	}
	parsed, err := parseOPML(strings.NewReader(body))
	if err != nil || len(parsed) != 3 {
		t.Errorf("export doesn't round-trip: %d %v", len(parsed), err)
	}

	id := itoa(feeds[0].ID)
	_, body = do(t, c, "POST", e.srv.URL+"/feeds/"+id+"/toggle", url.Values{"enabled": {"false"}}, hx)
	if !strings.Contains(body, "Paused") {
		t.Errorf("pause: %.300s", body)
	}
	do(t, c, "POST", e.srv.URL+"/feeds/"+id+"/delete", url.Values{}, hx)
	if feeds, _ = e.st.Feeds(ctx); len(feeds) != 2 {
		t.Errorf("delete: %d feeds", len(feeds))
	}
}

func TestSettingsAndFetch(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	form := url.Values{
		"papers": {"5"}, "articles": {"2"}, "discussions": {"0"}, "books_weekday": {"every"},
		"hn_enabled": {"on"}, "hn_min_points": {"250"}, "arxiv_enabled": {"on"}, "arxiv_categories": {"cs.LG, q-bio.NC"},
		"books_enabled": {"on"}, "fetch_every_hours": {"8"},
	}
	_, body := do(t, c, "POST", e.srv.URL+"/settings", form, hx)
	if !strings.Contains(body, "Saved.") || strings.Contains(body, "<html") {
		t.Errorf("save: %.300s", body)
	}
	s := e.settings
	if s.Papers != 5 || s.Discussions != 0 || s.BooksWeekday != "" || s.HNMinPoints != 250 || len(s.ArxivCategories) != 2 || s.FetchEveryHours != 8 || s.LearningEnabled {
		t.Errorf("saved = %+v", s)
	}
	form.Set("arxiv_categories", "not a category!")
	_, body = do(t, c, "POST", e.srv.URL+"/settings", form, hx)
	if !strings.Contains(body, "doesn&#39;t look like an arXiv category") || !strings.Contains(body, `value="not a category!"`) {
		t.Errorf("invalid settings: %.500s", body)
	}

	_, body = do(t, c, "POST", e.srv.URL+"/fetch", url.Values{}, hx)
	if e.fetches.Load() != 1 || !strings.Contains(body, "Checking your sources") || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Errorf("fetch now: fetches=%d %s", e.fetches.Load(), body)
	}
	_, body = do(t, c, "GET", e.srv.URL+"/status", nil, hx)
	if strings.Contains(body, "every 2s") {
		t.Errorf("status should stop polling when idle: %s", body)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t, Auth{Password: "correct horse"})
	c := client()
	resp, _ := do(t, c, "GET", e.srv.URL+"/library?shelf=read", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=%2Flibrary%3Fshelf%3Dread" {
		t.Fatalf("unauthenticated: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = do(t, c, "POST", e.srv.URL+"/items/"+e.items[0].ID+"/save", url.Values{}, hx)
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("HX-Redirect"), "/login") {
		t.Fatalf("unauthenticated htmx: %d", resp.StatusCode)
	}
	resp, body := do(t, c, "POST", e.srv.URL+"/login", url.Values{"password": {"wrong"}}, nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Wrong password") {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	resp, _ = do(t, c, "POST", e.srv.URL+"/login", url.Values{"password": {"correct horse"}, "next": {"//evil.example/x"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("login: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := do(t, c, "GET", e.srv.URL+"/library", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("after login: %d", resp.StatusCode)
	}
	e.s.Auth.Password = "changed"
	if resp, _ := do(t, c, "GET", e.srv.URL+"/library", nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Error("session survived a password change")
	}
	for _, p := range []string{"/manifest.webmanifest", "/sw.js", "/offline", "/static/app.css", "/static/fonts/literata-opsz-normal.woff2", "/healthz"} {
		if resp, _ := do(t, client(), "GET", e.srv.URL+p, nil, nil); resp.StatusCode != 200 {
			t.Errorf("GET %s = %d without login", p, resp.StatusCode)
		}
	}
	locked := newEnv(t, Auth{})
	if resp, body := do(t, client(), "GET", locked.srv.URL+"/", nil, nil); resp.StatusCode != 503 || !strings.Contains(body, "PRASHNA_PASSWORD") {
		t.Errorf("no password: %d", resp.StatusCode)
	}
}

func TestCrossOriginPostsRejected(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	resp, _ := do(t, client(), "POST", e.srv.URL+"/interests", url.Values{"term": {"x"}, "weight": {"1"}},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST = %d", resp.StatusCode)
	}
}

func TestPWA(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	resp, body := do(t, c, "GET", e.srv.URL+"/sw.js", nil, nil)
	if !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || !strings.Contains(body, `"/library"`) ||
		!strings.Contains(body, `"/static/app.css?v=`+assetVersion+`"`) || !strings.Contains(body, "fonts/literata") {
		t.Errorf("sw.js: %.400s", body)
	}
	_, body = do(t, c, "GET", e.srv.URL+"/manifest.webmanifest", nil, nil)
	if !strings.Contains(body, "maskable") || !strings.Contains(body, `"start_url": "/"`) {
		t.Errorf("manifest: %s", body)
	}
	resp, _ = do(t, c, "GET", e.srv.URL+"/static/fonts/literata-opsz-normal.woff2?v="+assetVersion, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("font: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
}

func TestTheme(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	c := client()
	_, body := do(t, c, "GET", e.srv.URL+"/settings", nil, nil)
	if strings.Contains(body, "data-theme=") || !strings.Contains(body, `value="system" checked`) || !strings.Contains(body, `media="(prefers-color-scheme: dark)"`) {
		t.Errorf("default should follow the system: %.400s", body)
	}

	resp, _ := do(t, c, "POST", e.srv.URL+"/settings/theme", url.Values{"theme": {"dark"}}, hx)
	if resp.StatusCode != http.StatusNoContent || e.settings.Theme != "dark" {
		t.Fatalf("save theme: %d %q", resp.StatusCode, e.settings.Theme)
	}
	for _, p := range []string{"/", "/settings", "/library"} {
		_, body := do(t, c, "GET", e.srv.URL+p, nil, nil)
		if !strings.Contains(body, `<html lang="en" data-theme="dark">`) || !strings.Contains(body, `<meta name="theme-color" content="#16140f">`) {
			t.Errorf("GET %s not rendered dark", p)
		}
	}
	_, body = do(t, c, "GET", e.srv.URL+"/settings", nil, nil)
	if !strings.Contains(body, `value="dark" checked`) {
		t.Error("picker doesn't show the saved theme")
	}

	// Saving the main settings form must not reset the theme.
	do(t, c, "POST", e.srv.URL+"/settings", url.Values{"papers": {"3"}, "articles": {"3"}, "discussions": {"3"},
		"books_weekday": {"sunday"}, "hn_min_points": {"100"}, "fetch_every_hours": {"4"}}, hx)
	if e.settings.Theme != "dark" {
		t.Errorf("settings form reset the theme to %q", e.settings.Theme)
	}

	if resp, _ := do(t, c, "POST", e.srv.URL+"/settings/theme", url.Values{"theme": {"neon"}}, hx); resp.StatusCode != http.StatusBadRequest || e.settings.Theme != "dark" {
		t.Errorf("unknown theme accepted: %d", resp.StatusCode)
	}
	do(t, c, "POST", e.srv.URL+"/settings/theme", url.Values{"theme": {"system"}}, hx)
	if _, body := do(t, c, "GET", e.srv.URL+"/", nil, nil); strings.Contains(body, "data-theme=") {
		t.Error("system theme should leave data-theme off")
	}
}
