package server

import (
	"context"
	"errors"
	"io"
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

	"radar/internal/item"
	"radar/internal/learn"
	"radar/internal/links"
	"radar/internal/score"
	"radar/internal/store"
)

func init() { loginFailWait = time.Millisecond }

type uiEnv struct {
	srv     *httptest.Server
	st      *store.Store
	s       *Server
	items   []item.Item
	fetches atomic.Int32
}

func newUI(t *testing.T, auth Auth) *uiEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	var items []item.Item
	for i, u := range []string{"https://arxiv.org/abs/2611.00001", "https://blog.example.com/post", "https://books.google.com/books?id=x"} {
		typ := []item.Type{item.TypePaper, item.TypeArticle, item.TypeBook}[i]
		it := item.Item{Type: typ, Source: "test", Title: "Item <" + string(typ) + ">", URL: u,
			Summary: "about consensus", Authors: []string{"A Author"}, Published: now, FetchedAt: now}
		if typ == item.TypeBook {
			it.FetchedAt = now.AddDate(-1, 0, 0) // old fetch: only on the Books tab
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

	e := &uiEnv{st: st, items: items}
	e.s = &Server{
		Store:  st,
		Signer: links.NewSigner("http://x", []byte("k")),
		Scorer: func(ctx context.Context) (*score.Scorer, error) {
			ins, err := st.Interests(ctx)
			var cfg score.Config
			for _, in := range ins {
				cfg.Interests = append(cfg.Interests, score.Interest{Term: in.Term, Weight: in.Weight})
			}
			return score.New(cfg), err
		},
		ProbeFeed: func(_ context.Context, u string) (string, string, error) {
			if strings.Contains(u, "broken") {
				return "", "", errors.New("GET " + u + ": unexpected status 404 Not Found")
			}
			if strings.HasSuffix(u, "/blog/") {
				return "Discovered Blog", u + "feed.xml", nil // a page that links to its feed
			}
			return "A Feed", u, nil
		},
		FetchFeeds: func(context.Context) error { e.fetches.Add(1); return nil },
		Auth:       auth,
		SessionKey: []byte("session-key"),
	}
	e.srv = httptest.NewServer(e.s.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

// client returns a cookie-keeping client that does not follow redirects.
func (e *uiEnv) client(t *testing.T) *http.Client {
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

var htmx = map[string]string{"HX-Request": "true"}

func TestAuthFlow(t *testing.T) {
	e := newUI(t, Auth{Password: "correct horse"})
	c := e.client(t)

	resp, _ := do(t, c, "GET", e.srv.URL+"/saved?x=1", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=%2Fsaved%3Fx%3D1" {
		t.Fatalf("unauthenticated page: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = do(t, c, "POST", e.srv.URL+"/items/"+e.items[0].ID+"/save", url.Values{}, htmx)
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(resp.Header.Get("HX-Redirect"), "/login") {
		t.Fatalf("unauthenticated htmx: %d %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}

	resp, body := do(t, c, "POST", e.srv.URL+"/login", url.Values{"password": {"wrong"}, "next": {"/saved"}}, nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Wrong password") {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	resp, _ = do(t, c, "POST", e.srv.URL+"/login", url.Values{"password": {"correct horse"}, "next": {"//evil.example/x"}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("login with off-site next: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("session cookie flags: %q", cookie)
	}
	if resp, _ := do(t, c, "GET", e.srv.URL+"/saved", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("after login: %d", resp.StatusCode)
	}

	// Changing the password invalidates existing sessions.
	e.s.Auth.Password = "new password"
	if resp, _ := do(t, c, "GET", e.srv.URL+"/saved", nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("old session survived password change: %d", resp.StatusCode)
	}
	e.s.Auth.Password = "correct horse"

	// A forged cookie is rejected.
	forged := e.client(t)
	u, _ := url.Parse(e.srv.URL)
	forged.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "9999999999.AAAA"}})
	if resp, _ := do(t, forged, "GET", e.srv.URL+"/", nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("forged cookie accepted: %d", resp.StatusCode)
	}

	resp, _ = do(t, c, "POST", e.srv.URL+"/logout", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := do(t, c, "GET", e.srv.URL+"/", nil, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("still signed in after logout: %d", resp.StatusCode)
	}

	// Feedback links and PWA assets stay public.
	for _, p := range []string{"/manifest.webmanifest", "/sw.js", "/offline", "/static/app.css", "/healthz"} {
		if resp, _ := do(t, e.client(t), "GET", e.srv.URL+p, nil, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d without login", p, resp.StatusCode)
		}
	}
}

func TestLockedWithoutPassword(t *testing.T) {
	e := newUI(t, Auth{})
	resp, body := do(t, e.client(t), "GET", e.srv.URL+"/", nil, nil)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "RADAR_UI_PASSWORD") {
		t.Errorf("no password: %d", resp.StatusCode)
	}
}

func TestCrossOriginPostsRejected(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	resp, _ := do(t, e.client(t), "POST", e.srv.URL+"/interests", url.Values{"term": {"x"}, "weight": {"1"}},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST = %d", resp.StatusCode)
	}
	if ins, _ := e.st.Interests(context.Background()); len(ins) != 1 {
		t.Errorf("cross-site POST changed interests: %+v", ins)
	}
}

func TestPages(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	c := e.client(t)
	ctx := context.Background()
	e.st.SetSaved(ctx, e.items[1].ID, true)
	e.st.SetFeedback(ctx, e.items[0].ID, item.Liked)
	digestID, _ := e.st.RecordDigest(ctx, "Radar · digest", []string{e.items[0].ID, e.items[1].ID}, time.Now())

	cases := []struct {
		path      string
		want, not []string
	}{
		{"/", []string{"Item &lt;article&gt;", `id="unread-badge" class="count">1<`, "matches consensus"}, []string{"Item &lt;paper&gt;", "Item &lt;book&gt;"}},
		{"/?type=book", []string{"Item &lt;book&gt;", `src="https://covers.example/c.jpg"`}, []string{"Item &lt;article&gt;"}},
		{"/saved", []string{"Item &lt;article&gt;", "Mark read"}, nil},
		{"/history", []string{"Radar · digest", "2 items · 1 liked"}, nil},
		{"/history?tab=liked", []string{"Item &lt;paper&gt;", "Liked"}, nil},
		{"/history/" + itoa(digestID), []string{"Item &lt;paper&gt;", "Item &lt;article&gt;"}, nil},
		{"/interests", []string{"consensus", "Followed authors", "Book subjects"}, nil},
		{"/feeds", []string{"No feeds yet"}, nil},
	}
	for _, tc := range cases {
		resp, body := do(t, c, "GET", e.srv.URL+tc.path, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", tc.path, resp.StatusCode)
			continue
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("GET %s CSP = %q", tc.path, csp)
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
	if resp, _ := do(t, c, "GET", e.srv.URL+"/history/999", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing digest = %d", resp.StatusCode)
	}
}

func TestMorePagination(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	ctx := context.Background()
	var many []item.Item
	for i := range pageSize + 5 {
		it := item.Item{Type: item.TypeArticle, Source: "rss", Title: "Bulk " + itoa(int64(i)), URL: "https://bulk.example/" + itoa(int64(i)), Published: time.Now()}
		it.Normalize()
		many = append(many, it)
	}
	e.st.SaveItems(ctx, many)
	c := e.client(t)
	_, body := do(t, c, "GET", e.srv.URL+"/", nil, nil)
	if strings.Count(body, `<article class="card`) != pageSize || !strings.Contains(body, "offset=20") {
		t.Fatalf("first page: %d cards", strings.Count(body, `<article class="card`))
	}
	_, body = do(t, c, "GET", e.srv.URL+"/more?offset=20", nil, htmx)
	if n := strings.Count(body, `<article class="card`); n != 7 || strings.Contains(body, "Load more") || strings.Contains(body, "<html") {
		t.Errorf("second page: %d cards, body %.200s", n, body)
	}
}

func TestItemActions(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	c := e.client(t)
	id := e.items[1].ID

	_, body := do(t, c, "POST", e.srv.URL+"/items/"+id+"/save", url.Values{}, htmx)
	if !strings.Contains(body, `id="item-`+id+`"`) || !strings.Contains(body, "/unsave") || !strings.Contains(body, `hx-swap-oob="true">1<`) {
		t.Errorf("save response: %s", body)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/items/"+id+"/read", url.Values{}, htmx)
	if !strings.Contains(body, "card read") || !strings.Contains(body, `hx-swap-oob="true"><`) {
		t.Errorf("read response should dim the card and clear the badge: %s", body)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/items/"+id+"/rate/up", url.Values{}, htmx)
	if !strings.Contains(body, "card liked") || !strings.Contains(body, "/rate/clear") {
		t.Errorf("rate response: %s", body)
	}
	if it, _ := e.st.Get(context.Background(), id); it.Feedback != item.Liked || it.SavedAt.IsZero() || it.ReadAt.IsZero() {
		t.Errorf("stored state: %+v", it)
	}
	for _, p := range []string{"/items/" + id + "/explode", "/items/" + id + "/rate/love", "/items/zzzz/save"} {
		if resp, _ := do(t, c, "POST", e.srv.URL+p, url.Values{}, htmx); resp.StatusCode < 400 {
			t.Errorf("POST %s = %d", p, resp.StatusCode)
		}
	}
	if resp, _ := do(t, c, "POST", e.srv.URL+"/items/0123456789abcdef/save", url.Values{}, htmx); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown item = %d", resp.StatusCode)
	}
}

func TestInterestsAndFollows(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	c := e.client(t)
	ctx := context.Background()

	_, body := do(t, c, "POST", e.srv.URL+"/interests", url.Values{"term": {"Rust"}, "weight": {"9"}}, htmx)
	if !strings.Contains(body, `id="interests"`) || !strings.Contains(body, "Rust") || strings.Contains(body, "<html") {
		t.Errorf("add interest response: %.300s", body)
	}
	ins, _ := e.st.Interests(ctx)
	if len(ins) != 2 || ins[0].Term != "Rust" || ins[0].Weight != maxWeight {
		t.Fatalf("interests = %+v (weight should clamp to %d)", ins, maxWeight)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/interests", url.Values{"term": {"  "}, "weight": {"1"}}, htmx)
	if !strings.Contains(body, "form-error") {
		t.Error("empty term not reported")
	}
	_, body = do(t, c, "POST", e.srv.URL+"/interests", url.Values{"term": {"go"}, "weight": {"lots"}}, htmx)
	if !strings.Contains(body, "weight must be a number") || !strings.Contains(body, `value="go"`) {
		t.Error("bad weight not reported with the term kept")
	}
	do(t, c, "POST", e.srv.URL+"/interests/"+itoa(ins[0].ID)+"/weight", url.Values{"weight": {"-1"}}, htmx)
	do(t, c, "POST", e.srv.URL+"/interests/"+itoa(ins[1].ID)+"/delete", url.Values{}, htmx)
	if ins, _ = e.st.Interests(ctx); len(ins) != 1 || ins[0].Weight != -1 {
		t.Errorf("after edit/delete: %+v", ins)
	}

	_, body = do(t, c, "POST", e.srv.URL+"/follows", url.Values{"kind": {"author"}, "name": {"Ted Chiang"}}, htmx)
	if !strings.Contains(body, `id="follows-author"`) || !strings.Contains(body, "Ted Chiang") {
		t.Errorf("add follow response: %.300s", body)
	}
	fs, _ := e.st.BookFollows(ctx, store.FollowAuthor)
	do(t, c, "POST", e.srv.URL+"/follows/"+itoa(fs[0].ID)+"/delete", url.Values{"kind": {"author"}}, htmx)
	if fs, _ = e.st.BookFollows(ctx, store.FollowAuthor); len(fs) != 0 {
		t.Errorf("follow not deleted: %+v", fs)
	}
	if resp, _ := do(t, c, "POST", e.srv.URL+"/follows", url.Values{"kind": {"genre"}, "name": {"x"}}, htmx); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad kind = %d", resp.StatusCode)
	}
}

func TestFeedManagement(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	c := e.client(t)
	ctx := context.Background()

	_, body := do(t, c, "POST", e.srv.URL+"/feeds", url.Values{"url": {"https://site.example/blog/"}, "tags": {"go, , systems"}, "type": {"news"}}, htmx)
	if !strings.Contains(body, "Discovered Blog") || !strings.Contains(body, "https://site.example/blog/feed.xml") || !strings.Contains(body, "Added.") {
		t.Errorf("add feed response: %.500s", body)
	}
	feeds, _ := e.st.Feeds(ctx)
	if len(feeds) != 1 || feeds[0].URL != "https://site.example/blog/feed.xml" || feeds[0].Type != item.TypeNews || len(feeds[0].Tags) != 2 {
		t.Fatalf("feeds = %+v", feeds)
	}

	for form, want := range map[string]string{
		"https://site.example/blog/feed.xml": "already follow",
		"ftp://nope":                         "full URL",
		"https://broken.example/rss":         "Couldn&#39;t read a feed at that URL: unexpected status 404 Not Found.",
	} {
		_, body := do(t, c, "POST", e.srv.URL+"/feeds", url.Values{"url": {form}, "name": {"Kept"}}, htmx)
		if !strings.Contains(body, want) || !strings.Contains(body, `value="Kept"`) {
			t.Errorf("add %s: want %q in %.400s", form, want, body)
		}
	}

	id := itoa(feeds[0].ID)
	_, body = do(t, c, "POST", e.srv.URL+"/feeds/"+id+"/toggle", url.Values{"enabled": {"false"}}, htmx)
	if !strings.Contains(body, "Paused") || !strings.Contains(body, "Resume") {
		t.Errorf("pause response: %.300s", body)
	}
	_, body = do(t, c, "POST", e.srv.URL+"/feeds/fetch", url.Values{}, htmx)
	if !strings.Contains(body, "Fetching") || !strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Errorf("fetch-now response should poll: %.300s", body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for e.s.fetching.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.fetches.Load() != 1 {
		t.Errorf("FetchFeeds calls = %d", e.fetches.Load())
	}
	_, body = do(t, c, "GET", e.srv.URL+"/feeds?partial=1", nil, htmx)
	if strings.Contains(body, "every 3s") || strings.Contains(body, "<html") {
		t.Errorf("polling should stop after the fetch: %.300s", body)
	}
	do(t, c, "POST", e.srv.URL+"/feeds/"+id+"/delete", url.Values{}, htmx)
	if feeds, _ = e.st.Feeds(ctx); len(feeds) != 0 {
		t.Errorf("feed not deleted")
	}
}

func TestPWAAssets(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	c := e.client(t)
	resp, body := do(t, c, "GET", e.srv.URL+"/sw.js", nil, nil)
	if !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || !strings.Contains(body, "radar-"+assetVersion) ||
		!strings.Contains(body, `"/static/app.css?v=`+assetVersion+`"`) || strings.Contains(body, "__") {
		t.Errorf("sw.js: %s %.300s", resp.Header.Get("Content-Type"), body)
	}
	resp, body = do(t, c, "GET", e.srv.URL+"/manifest.webmanifest", nil, nil)
	if resp.Header.Get("Content-Type") != "application/manifest+json" || !strings.Contains(body, "maskable") {
		t.Errorf("manifest: %s", resp.Header.Get("Content-Type"))
	}
	resp, _ = do(t, c, "GET", e.srv.URL+"/static/icons/icon-512.png?v="+assetVersion, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("versioned asset: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	resp, _ = do(t, c, "GET", e.srv.URL+"/static/app.css", nil, nil)
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("unversioned asset cache: %q", resp.Header.Get("Cache-Control"))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestModelPanel(t *testing.T) {
	e := newUI(t, Auth{Disabled: true})
	c := e.client(t)
	ctx := context.Background()

	_, body := do(t, c, "GET", e.srv.URL+"/interests", nil, nil)
	if strings.Contains(body, "Learned ranking") {
		t.Error("panel shown before any report exists")
	}

	e.st.SetJSONSetting(ctx, learn.ReportKey, learn.Report{Ratings: 5, Status: "Rate 15 more items to start learning (5 of 20)."})
	_, body = do(t, c, "GET", e.srv.URL+"/interests", nil, nil)
	if !strings.Contains(body, "Rate 15 more") || !strings.Contains(body, `<progress class="meter" value="25"`) {
		t.Errorf("collecting state: %s", body[strings.Index(body, "Learned"):][:600])
	}

	e.st.SetJSONSetting(ctx, learn.ReportKey, learn.Report{Ratings: 40, Liked: 25, Disliked: 15, ModelAUC: 0.83, BaselineAUC: 0.64,
		Active: true, Weight: 0.53, Status: "Active: 53% of the content score",
		TopLiked: []learn.Weight{{Label: "tag: cs.DC", Weight: 1.2}}, TopDisliked: []learn.Weight{{Label: "“benchmark”", Weight: -0.5}}})
	_, body = do(t, c, "GET", e.srv.URL+"/interests", nil, nil)
	for _, want := range []string{"model-status on", "0.83", "0.64", "tag: cs.DC", "“benchmark”", "You tend to skip"} {
		if !strings.Contains(body, want) {
			t.Errorf("active state missing %q", want)
		}
	}
	if strings.Contains(body, "<progress") {
		t.Error("progress bar shown for an evaluated model")
	}
}
