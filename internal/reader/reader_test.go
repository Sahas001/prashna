package reader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const para = `Consensus protocols let a group of machines agree on a value even when some of them fail, and the details of how they do it are subtle enough to have filled decades of papers. `

func page(body string) string {
	return `<!doctype html><html><head><title>A piece</title><meta property="og:site_name" content="Example Blog">
<style>p{color:red}</style><script>alert(1)</script></head><body>
<nav><a href="/">Home</a><a href="/about">About</a></nav>
<article><h1>A piece about consensus</h1>` + body + `</article>
<footer>Copyright</footer></body></html>`
}

func TestFromHTML(t *testing.T) {
	base, _ := url.Parse("http://blog.example.com/posts/1")
	body := strings.Repeat("<p>"+para+"</p>", 6) +
		`<p onclick="steal()">See <a href="/other" onmouseover="x()">another post</a> and <a href="javascript:alert(1)">this</a>.</p>` +
		`<p><img src="/img/a.png" alt="A &quot;diagram&quot;" onerror="x()"><script>evil()</script></p>` +
		`<iframe src="https://ads.example"></iframe><svg><script>bad()</script></svg>` +
		strings.Repeat("<p>"+para+"</p>", 3)
	art, err := FromHTML([]byte(page(body)), "text/html; charset=utf-8", base)
	if err != nil {
		t.Fatal(err)
	}
	if art.Site != "Example Blog" {
		t.Errorf("site = %q", art.Site)
	}
	if art.Words < 200 {
		t.Errorf("words = %d", art.Words)
	}
	for _, bad := range []string{"<script", "alert(", "evil()", "bad()", "onclick", "onerror", "onmouseover", "javascript:", "<iframe", "<svg", "<style", "Copyright"} {
		if strings.Contains(art.HTML, bad) {
			t.Errorf("output contains %q", bad)
		}
	}
	for _, want := range []string{
		`<a href="http://blog.example.com/other" target="_blank" rel="noopener noreferrer">another post</a>`,
		`<img src="https://blog.example.com/img/a.png" alt="A &#34;diagram&#34;" loading="lazy" referrerpolicy="no-referrer">`,
		"this", // the javascript: link's text stays, unlinked
	} {
		if !strings.Contains(art.HTML, want) {
			t.Errorf("output lacks %q\n%s", want, art.HTML)
		}
	}
}

func TestSanitizeAttributes(t *testing.T) {
	in := `<div class="x" style="position:fixed"><h1 id="t">Title</h1><ol start="3" onload="x()"><li>one</li></ol>` +
		`<table><tr><td colspan="2" style="x">a</td><td colspan="9999">b</td></tr></table>` +
		`<span data-x="1">kept <b>text</b></span><math><mi>x</mi></math><a href="data:text/html,x">d</a></div>`
	doc, err := html.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	got, words := Sanitize(doc, nil)
	want := `<div><h2>Title</h2><ol start="3"><li>one</li></ol>` +
		`<table><tbody><tr><td colspan="2">a</td><td>b</td></tr></tbody></table>` +
		`kept <b>text</b>d</div>`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if words != 7 {
		t.Errorf("words = %d, want 7", words)
	}
}

func TestExtract(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "radar-test" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		w.Write([]byte(page(strings.Repeat("<p>"+para+"</p>", 8) + "<p>caf\xe9</p>")))
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/post", http.StatusFound) })
	mux.HandleFunc("/pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.7"))
	})
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><div id="app"></div><script src="app.js"></script><p>Please enable JavaScript.</p></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := &Extractor{HTTP: srv.Client(), UserAgent: "radar-test"}
	ctx := context.Background()

	art, err := e.Extract(ctx, srv.URL+"/moved")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(art.HTML, "café") {
		t.Errorf("latin-1 page not decoded: %s", art.HTML[len(art.HTML)-80:])
	}
	if _, err := e.Extract(ctx, srv.URL+"/pdf"); !errors.Is(err, ErrNotArticle) {
		t.Errorf("pdf: err = %v", err)
	}
	if _, err := e.Extract(ctx, srv.URL+"/empty"); !errors.Is(err, ErrNoText) {
		t.Errorf("empty: err = %v", err)
	}
	if _, err := e.Extract(ctx, srv.URL+"/missing"); err == nil {
		t.Error("404: no error")
	}
	if _, err := e.Extract(ctx, "file:///etc/passwd"); !errors.Is(err, ErrNotArticle) {
		t.Errorf("file: err = %v", err)
	}

	// The default extractor won't read from this computer.
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, "")
	}
	if _, err := New("radar-test").Extract(ctx, srv.URL+"/post"); !errors.Is(err, ErrBlocked) {
		t.Errorf("local address: err = %v", err)
	}
}
