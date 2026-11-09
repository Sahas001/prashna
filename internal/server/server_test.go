package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/item"
	"radar/internal/links"
	"radar/internal/store"
)

func setup(t *testing.T) (*httptest.Server, *store.Store, *links.Signer, item.Item, *int) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	it := item.Item{Type: item.TypePaper, Source: "arxiv", Title: "A <b>paper</b>", URL: "https://arxiv.org/abs/2611.00001", Published: time.Now()}
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveItems(ctx, []item.Item{it}); err != nil {
		t.Fatal(err)
	}
	signer := links.NewSigner("http://unused", []byte("key"))
	rescores := 0
	srv := httptest.NewServer((&Server{Store: st, Signer: signer, Rescore: func(context.Context) error {
		rescores++
		return nil
	}}).Handler())
	t.Cleanup(srv.Close)
	return srv, st, signer, it, &rescores
}

// client doesn't follow redirects; it keeps the client from following anything, so each request is observed.
var client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGetDoesNotRecord(t *testing.T) {
	srv, st, signer, it, rescores := setup(t)
	resp, err := client.Get(srv.URL + signer.Path(it.ID, links.Up))
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(html, `method="post"`) || !strings.Contains(html, "A &lt;b&gt;paper&lt;/b&gt;") {
		t.Fatalf("status %d, body %s", resp.StatusCode, html)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(html, `nonce="`) {
		t.Errorf("csp/nonce missing: %q", csp)
	}
	got, _ := st.Get(context.Background(), it.ID)
	if got.Feedback != item.Neutral || *rescores != 0 {
		t.Errorf("GET recorded feedback: %v, rescores %d", got.Feedback, *rescores)
	}
}

func TestPostRecords(t *testing.T) {
	srv, st, signer, it, rescores := setup(t)
	resp, err := client.Post(srv.URL+signer.Path(it.ID, links.Down), "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	html := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(html, "Recorded 👎") {
		t.Fatalf("status %d body %s", resp.StatusCode, html)
	}
	// The result page offers the other votes, each with its own valid token.
	for _, v := range []links.Vote{links.Up, links.Clear} {
		if !strings.Contains(html, strings.ReplaceAll(signer.Path(it.ID, v), "&", "&amp;")) {
			t.Errorf("missing %s action", v)
		}
	}
	got, _ := st.Get(context.Background(), it.ID)
	if got.Feedback != item.Disliked || *rescores != 1 {
		t.Errorf("feedback=%v rescores=%d", got.Feedback, *rescores)
	}

	resp, _ = client.Post(srv.URL+signer.Path(it.ID, links.Clear), "", nil)
	body(t, resp)
	if got, _ := st.Get(context.Background(), it.ID); got.Feedback != item.Neutral {
		t.Errorf("clear failed: %v", got.Feedback)
	}
}

func TestRejectsBadLinks(t *testing.T) {
	srv, st, signer, it, _ := setup(t)
	good := signer.Path(it.ID, links.Up)
	otherKey := links.NewSigner("", []byte("other")).Path(it.ID, links.Up)
	cases := map[string]int{
		"/f/" + it.ID + "/up?t=wrong": http.StatusForbidden,
		"/f/" + it.ID + "/up":         http.StatusForbidden,
		otherKey:                      http.StatusForbidden,
		strings.Replace(good, "/up?", "/down?", 1): http.StatusForbidden, // token for another vote
		"/f/" + it.ID + "/love?t=x":                http.StatusNotFound,
		"/f/" + it.ID[:8] + "/up?t=" + url.QueryEscape(signer.Token(it.ID[:8], links.Up)): http.StatusNotFound, // prefixes not allowed
		links.NewSigner("", []byte("key")).Path("0123456789abcdef", links.Up):             http.StatusNotFound, // valid token, unknown item
	}
	for path, want := range cases {
		resp, err := client.Post(srv.URL+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		body(t, resp)
		if resp.StatusCode != want {
			t.Errorf("POST %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
	if got, _ := st.Get(context.Background(), it.ID); got.Feedback != item.Neutral {
		t.Errorf("bad link changed feedback: %v", got.Feedback)
	}
}
