package server

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/reader"
)

func TestReader(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	var fetched atomic.Int32
	e.s.ReadText = func(_ context.Context, u string) (reader.Article, error) {
		fetched.Add(1)
		if strings.Contains(u, "news.example.com") {
			return reader.Article{}, reader.ErrNoText
		}
		return reader.Article{HTML: "<p>The <em>text</em> of the piece.</p>", Site: "Example Blog", Words: 460}, nil
	}
	c := client()
	paper, essay, news := e.items[0], e.items[1], e.items[2]

	// The page loads at once and fetches the text itself.
	resp, body := do(t, c, "GET", e.srv.URL+"/read/"+essay.ID, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"Item &lt;article&gt;", `hx-get="/read/` + essay.ID + `/text"`, "Read on blog.example.com",
		`href="https://blog.example.com/post"`, `id="item-` + essay.ID + `"`, "Select any passage"} {
		if !strings.Contains(body, want) {
			t.Errorf("reader page missing %q", want)
		}
	}
	if fetched.Load() != 0 {
		t.Error("page load waited on the site")
	}
	if it, _ := e.st.Get(context.Background(), essay.ID); it.OpenCount != 1 || it.ReadAt.IsZero() {
		t.Errorf("opening the reader should count as reading: %+v", it)
	}

	_, body = do(t, c, "GET", e.srv.URL+"/read/"+essay.ID+"/text", nil, map[string]string{"HX-Request": "true"})
	for _, want := range []string{"<p>The <em>text</em> of the piece.</p>", "2 min read", `data-item="` + essay.ID + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("text missing %q in %s", want, body)
		}
	}
	// Stored: the next visit has the text in the page and asks no one.
	_, body = do(t, c, "GET", e.srv.URL+"/read/"+essay.ID, nil, nil)
	if !strings.Contains(body, "<p>The <em>text</em> of the piece.</p>") || strings.Contains(body, "Getting the text") {
		t.Error("stored text not shown on second visit")
	}
	if fetched.Load() != 1 {
		t.Errorf("fetched %d times, want 1", fetched.Load())
	}

	// A page with no text explains itself and offers the original.
	_, body = do(t, c, "GET", e.srv.URL+"/read/"+news.ID+"/text", nil, nil)
	if !strings.Contains(body, "couldn’t find the text") || !strings.Contains(body, "Try again") || !strings.Contains(body, `href="https://news.example.com/story"`) {
		t.Errorf("problem notice: %s", body)
	}

	// Papers and books go to their own pages.
	resp, _ = do(t, c, "GET", e.srv.URL+"/read/"+paper.ID, nil, nil)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != paper.URL {
		t.Errorf("paper: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ = do(t, c, "GET", e.srv.URL+"/read/0000000000000000", nil, nil); resp.StatusCode != 404 {
		t.Errorf("unknown item: %d", resp.StatusCode)
	}

	// Actions on the reader page re-render its own actions bar.
	_, body = do(t, c, "POST", e.srv.URL+"/items/"+essay.ID+"/save", url.Values{"v": {"reader"}}, nil)
	if !strings.Contains(body, `class="reader-actions" id="item-`+essay.ID+`"`) || strings.Contains(body, "story-title") {
		t.Errorf("reader actions: %s", body)
	}
}

func TestSavingKeepsText(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	e.s.ReadText = func(context.Context, string) (reader.Article, error) {
		return reader.Article{HTML: "<p>Kept.</p>", Words: 40}, nil
	}
	essay, book := e.items[1], e.items[3]
	c := client()
	do(t, c, "POST", e.srv.URL+"/items/"+book.ID+"/save", nil, nil)
	do(t, c, "POST", e.srv.URL+"/items/"+essay.ID+"/save", nil, nil)
	for deadline := time.Now().Add(2 * time.Second); !e.st.HasArticle(context.Background(), essay.ID); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("saved piece's text was not stored")
		}
	}
	if e.st.HasArticle(context.Background(), book.ID) {
		t.Error("stored text for a book")
	}
}

func TestHighlightsAndNotes(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	e.s.ReadText = func(context.Context, string) (reader.Article, error) { return reader.Article{}, errors.New("offline") }
	c := client()
	essay := e.items[1]
	post := func(path string, form url.Values) (int, string) {
		t.Helper()
		resp, body := do(t, c, "POST", e.srv.URL+path, form, nil)
		return resp.StatusCode, body
	}

	code, body := post("/items/"+essay.ID+"/highlights", url.Values{"start": {"40"}, "end": {"52"}, "quote": {"a <b>second</b>\r\nline"}})
	if code != 200 || !strings.Contains(body, `data-start="40" data-end="52"`) || !strings.Contains(body, "a &lt;b&gt;second&lt;/b&gt;\nline") {
		t.Fatalf("add: %d %s", code, body)
	}
	post("/items/"+essay.ID+"/highlights", url.Values{"start": {"4"}, "end": {"9"}, "quote": {"first"}})
	// The same span twice is one highlight.
	_, body = post("/items/"+essay.ID+"/highlights", url.Values{"start": {"4"}, "end": {"9"}, "quote": {"first"}})
	if strings.Count(body, `class="hl"`) != 2 || strings.Index(body, "first") > strings.Index(body, "second") || !strings.Contains(body, "Highlights · 2") {
		t.Errorf("want 2 highlights in reading order: %s", body)
	}
	for _, bad := range []url.Values{
		{"start": {"9"}, "end": {"4"}, "quote": {"x"}},
		{"start": {"-1"}, "end": {"4"}, "quote": {"x"}},
		{"start": {"1"}, "end": {"4"}, "quote": {"  "}},
		{"start": {"1"}, "end": {"4"}, "quote": {strings.Repeat("x", maxQuoteChars+1)}},
		{"start": {"x"}, "end": {"4"}, "quote": {"x"}},
	} {
		if code, _ := post("/items/"+essay.ID+"/highlights", bad); code != 400 {
			t.Errorf("%v: status %d, want 400", bad, code)
		}
	}
	if code, _ := post("/items/0000000000000000/highlights", url.Values{"start": {"1"}, "end": {"4"}, "quote": {"x"}}); code != 404 {
		t.Errorf("unknown item: %d", code)
	}

	hs, _ := e.st.Highlights(context.Background(), essay.ID)
	if len(hs) != 2 {
		t.Fatalf("stored %d highlights", len(hs))
	}
	first, second := itoa(hs[0].ID), itoa(hs[1].ID)
	if code, _ := post("/highlights/"+first+"/note", url.Values{"note": {" why this matters "}}); code != 204 {
		t.Errorf("note: %d", code)
	}
	if code, _ := post("/highlights/999/note", url.Values{"note": {"x"}}); code != 404 {
		t.Errorf("note on unknown highlight: %d", code)
	}
	if code, _ := post("/items/"+essay.ID+"/note", url.Values{"note": {"The whole piece, in a line."}}); code != 204 {
		t.Errorf("item note: %d", code)
	}

	// Both show on the reader page (even with no text) and in the library.
	_, body = do(t, c, "GET", e.srv.URL+"/read/"+essay.ID, nil, nil)
	for _, want := range []string{">why this matters</textarea>", ">The whole piece, in a line.</textarea>", `id="hl-` + second + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("reader page missing %q", want)
		}
	}
	_, body = do(t, c, "GET", e.srv.URL+"/library?shelf=highlights", nil, nil)
	for _, want := range []string{`Highlights <span class="tab-count">2</span>`, "why this matters", "The whole piece, in a line.", `<a href="/read/` + essay.ID + `">`} {
		if !strings.Contains(body, want) {
			t.Errorf("highlights shelf missing %q", want)
		}
	}

	code, body = post("/highlights/"+first+"/delete", nil)
	if code != 200 || strings.Contains(body, `id="hl-`+first+`"`) || !strings.Contains(body, `id="hl-`+second+`"`) {
		t.Errorf("delete: %d %s", code, body)
	}
	if code, _ = post("/highlights/"+first+"/delete", nil); code != 404 {
		t.Errorf("delete twice: %d", code)
	}
	// Clearing the note removes it.
	post("/items/"+essay.ID+"/note", url.Values{"note": {""}})
	if n, _ := e.st.ItemNote(context.Background(), essay.ID); n != "" {
		t.Errorf("note not cleared: %q", n)
	}
}
