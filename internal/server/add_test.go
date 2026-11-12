package server

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/Sahas001/prashna/internal/edition"
	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/reader"
	"github.com/Sahas001/prashna/internal/store"
)

func TestAddLink(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	ctx := context.Background()
	e.s.ReadText = func(_ context.Context, u string) (reader.Article, error) {
		switch {
		case strings.Contains(u, "paper.pdf"):
			return reader.Article{}, reader.ErrNotArticle
		case strings.Contains(u, "192.168."):
			return reader.Article{}, reader.ErrBlocked
		}
		return reader.Article{HTML: "<p>Found elsewhere.</p>", Title: "A Piece Found Elsewhere Today", Excerpt: "What it says.",
			Byline: "Some Writer", Site: "Elsewhere", Words: 700}, nil
	}
	c := client()

	// The bookmarklet opens the page with the address filled in; nothing
	// is saved until the reader says so.
	_, body := do(t, c, "GET", e.srv.URL+"/add?url="+url.QueryEscape("https://elsewhere.example/a?x=1"), nil, nil)
	host := strings.TrimPrefix(e.srv.URL, "http://")
	for _, want := range []string{`value="https://elsewhere.example/a?x=1"`, `href="javascript:void%28open%28%27http://` + host + `/add?url=%27&#43;encodeURIComponent%28location.href%29%29%29"`} {
		if !strings.Contains(body, want) {
			t.Errorf("add page missing %q", want)
		}
	}
	if _, err := e.st.Get(ctx, item.IDFor("https://elsewhere.example/a?x=1")); !errors.Is(err, store.ErrNotFound) {
		t.Error("GET /add saved something")
	}

	resp, _ := do(t, c, "POST", e.srv.URL+"/add", url.Values{"url": {"www.elsewhere.example/a/#top"}}, nil)
	id := item.IDFor("https://elsewhere.example/a")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/library" {
		t.Fatalf("add: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	it, err := e.st.Get(ctx, id)
	if err != nil || it.Title != "A Piece Found Elsewhere Today" || it.Source != item.SourceSaved || it.SavedAt.IsZero() ||
		it.Summary != "What it says." || len(it.Authors) != 1 || it.Type != item.TypeArticle {
		t.Errorf("stored item: %+v, %v", it, err)
	}
	if art, err := e.st.Article(ctx, id); err != nil || art.Words != 700 {
		t.Errorf("stored text: %+v, %v", art, err)
	}
	if _, body = do(t, c, "GET", e.srv.URL+"/library", nil, nil); !strings.Contains(body, "A Piece Found Elsewhere Today") {
		t.Error("added piece is not on the reading list")
	}
	_, body = do(t, c, "GET", e.srv.URL+"/read/"+id, nil, nil)
	if !strings.Contains(body, "<p>Found elsewhere.</p>") {
		t.Error("reader doesn't show the added piece")
	}
	// Adding it again is not an error and makes no second copy.
	if resp, _ = do(t, c, "POST", e.srv.URL+"/add", url.Values{"url": {"https://elsewhere.example/a"}}, nil); resp.Header.Get("Location") != "/library" {
		t.Errorf("add twice: %s", resp.Header.Get("Location"))
	}

	// Something Prashna already found: saved, not duplicated.
	resp, _ = do(t, c, "POST", e.srv.URL+"/add", url.Values{"url": {e.items[1].URL}}, nil)
	if resp.Header.Get("Location") != "/library" {
		t.Errorf("known item: %s", resp.Header.Get("Location"))
	}
	if it, _ := e.st.Get(ctx, e.items[1].ID); it.SavedAt.IsZero() || it.Source != "test" {
		t.Errorf("known item not saved: %+v", it)
	}

	// No text (a PDF): kept as a link, titled by its address.
	resp, _ = do(t, c, "POST", e.srv.URL+"/add", url.Values{"url": {"https://files.example/paper.pdf"}}, nil)
	pdf, err := e.st.Get(ctx, item.IDFor("https://files.example/paper.pdf"))
	if resp.StatusCode != 303 || err != nil || pdf.Title != "files.example/paper.pdf" || pdf.SavedAt.IsZero() {
		t.Errorf("pdf: %d %+v %v", resp.StatusCode, pdf, err)
	}

	for _, bad := range []string{"", "not a url", "ftp://files.example/x", "javascript:alert(1)", "http://localhost/x", "https://192.168.1.1/admin"} {
		resp, body := do(t, c, "POST", e.srv.URL+"/add", url.Values{"url": {bad}}, nil)
		if resp.StatusCode != 200 || !strings.Contains(body, `class="add-error"`) {
			t.Errorf("%q: %d, want the form back with an error", bad, resp.StatusCode)
		}
	}

	// Links the reader added don't come back as news in an edition.
	if _, err := e.s.EnsureEdition(ctx); err != nil {
		t.Fatal(err)
	}
	_, items, err := e.st.EditionForDay(ctx, edition.Day(pdf.FetchedAt.Local()))
	if err != nil {
		t.Fatal(err)
	}
	for _, ei := range items {
		if ei.Item.Source == item.SourceSaved {
			t.Errorf("edition includes an added link: %s", ei.Item.Title)
		}
	}
}
