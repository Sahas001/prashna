package server

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/reader"
	"github.com/Sahas001/prashna/internal/store"
)

// readable reports whether an item opens in the reader: pieces that are
// web pages. Papers and books keep linking to their own pages.
func readable(it item.Item) bool {
	return it.Type == item.TypeArticle || it.Type == item.TypeNews
}

const (
	readTimeout   = 40 * time.Second
	wordsPerMin   = 230
	maxQuoteChars = 4000
	maxNoteChars  = 20000
)

type highlightsData struct {
	ItemID string
	List   []store.Highlight
}

type readerData struct {
	Card       card
	Host       string        // the original site, for "Read on ..."
	Body       template.HTML // the stored text; empty if there is none
	Site       string
	Minutes    int
	Pending    bool   // the text is fetched after the page loads
	Problem    string // why there is no text
	Highlights highlightsData
	Note       string
}

func (s *Server) readerData(ctx context.Context, it item.Item) (readerData, error) {
	d := readerData{Card: newCard(it, nil, variantReader), Highlights: highlightsData{ItemID: it.ID}}
	if u, err := url.Parse(it.URL); err == nil {
		d.Host = strings.TrimPrefix(u.Hostname(), "www.")
	}
	var err error
	if d.Highlights.List, err = s.Store.Highlights(ctx, it.ID); err != nil {
		return d, err
	}
	if d.Note, err = s.Store.ItemNote(ctx, it.ID); err != nil {
		return d, err
	}
	art, err := s.Store.Article(ctx, it.ID)
	switch {
	case err == nil:
		d.setArticle(art)
	case errors.Is(err, store.ErrNotFound):
		d.Pending = s.ReadText != nil
		if !d.Pending {
			d.Problem = "The reader isn't available."
		}
	default:
		return d, err
	}
	return d, nil
}

func (d *readerData) setArticle(a store.Article) {
	// Safe: stored HTML only ever comes from reader.Sanitize.
	d.Body = template.HTML(a.HTML)
	d.Site = a.Site
	d.Minutes = max(1, (a.Words+wordsPerMin/2)/wordsPerMin)
	d.Pending, d.Problem = false, ""
}

// readerItem loads the item named in the path, answering the request
// itself (and returning false) if it can't be shown in the reader.
func (s *Server) readerItem(w http.ResponseWriter, r *http.Request) (item.Item, bool) {
	id := r.PathValue("id")
	if !fullID(id) {
		http.NotFound(w, r)
		return item.Item{}, false
	}
	it, err := s.Store.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return it, false
	}
	if err != nil {
		s.serverError(w, "reader", err)
		return it, false
	}
	return it, true
}

func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	it, ok := s.readerItem(w, r)
	if !ok {
		return
	}
	if !readable(it) {
		http.Redirect(w, r, it.URL, http.StatusSeeOther)
		return
	}
	if opened, err := s.Store.RecordOpen(r.Context(), it.ID); err == nil {
		it = opened
	}
	d, err := s.readerData(r.Context(), it)
	if err != nil {
		s.serverError(w, "reader", err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "read", it.Title, "", d)
}

// readText fetches the text if it isn't stored yet and renders it (or
// what went wrong). The reader page calls it after loading, so opening a
// piece never waits on someone else's server.
func (s *Server) readText(w http.ResponseWriter, r *http.Request) {
	it, ok := s.readerItem(w, r)
	if !ok {
		return
	}
	d, err := s.readerData(r.Context(), it)
	if err != nil {
		s.serverError(w, "reader", err)
		return
	}
	if d.Pending && readable(it) {
		art, err := s.fetchArticle(r.Context(), it)
		if err != nil {
			s.log().Info("reader: no text", "url", it.URL, "err", err)
			d.Pending, d.Problem = false, readProblem(err)
		} else {
			d.setArticle(art)
		}
	}
	s.renderPartial(w, http.StatusOK, "reader-text", d)
}

func (s *Server) fetchArticle(ctx context.Context, it item.Item) (store.Article, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	got, err := s.ReadText(ctx, it.URL)
	if err != nil {
		return store.Article{}, err
	}
	art := store.Article{ItemID: it.ID, HTML: got.HTML, Byline: got.Byline, Site: got.Site, Words: got.Words, FetchedAt: time.Now()}
	return art, s.Store.SaveArticle(ctx, art)
}

// keepText stores the text of a saved piece in the background, so the
// reading list can be read later without the internet.
func (s *Server) keepText(it item.Item) {
	if s.ReadText == nil || !readable(it) || s.Store.HasArticle(context.Background(), it.ID) {
		return
	}
	go func() {
		if _, err := s.fetchArticle(context.Background(), it); err != nil {
			s.log().Info("reader: no text for saved piece", "url", it.URL, "err", err)
		}
	}()
}

func readProblem(err error) string {
	switch {
	case errors.Is(err, reader.ErrNotArticle):
		return "This isn’t a page Prashna can show here. It may be a PDF, a video or a download."
	case errors.Is(err, reader.ErrNoText):
		return "Prashna couldn’t find the text of this piece. Some sites need a sign-in, or only work in a browser."
	case errors.Is(err, reader.ErrBlocked):
		return "This link points to a private address, so Prashna didn’t open it."
	case errors.Is(err, context.DeadlineExceeded):
		return "The site took too long to answer."
	}
	return "Prashna couldn’t get this piece from its site. You may be offline, or the site may be refusing."
}

// ---- Highlights and notes ----

func (s *Server) respondHighlights(w http.ResponseWriter, r *http.Request, itemID string) {
	list, err := s.Store.Highlights(r.Context(), itemID)
	if err != nil {
		s.serverError(w, "highlights", err)
		return
	}
	s.renderPartial(w, http.StatusOK, "highlights", highlightsData{ItemID: itemID, List: list})
}

func (s *Server) addHighlight(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	start, err1 := strconv.Atoi(r.FormValue("start"))
	end, err2 := strconv.Atoi(r.FormValue("end"))
	// Browsers may send line breaks as CRLF; the page's text has only LF.
	quote := strings.ReplaceAll(r.FormValue("quote"), "\r\n", "\n")
	if !fullID(id) || err1 != nil || err2 != nil || start < 0 || end <= start ||
		strings.TrimSpace(quote) == "" || len([]rune(quote)) > maxQuoteChars {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	_, err := s.Store.AddHighlight(r.Context(), store.Highlight{ItemID: id, Start: start, End: end, Quote: quote})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such item.", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, "add highlight", err)
		return
	}
	s.respondHighlights(w, r, id)
}

func highlightID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("hid"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) noteHighlight(w http.ResponseWriter, r *http.Request) {
	id, ok := highlightID(r)
	note := r.FormValue("note")
	if !ok || len(note) > maxNoteChars {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.SetHighlightNote(r.Context(), id, note); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such highlight.", http.StatusNotFound)
		return
	} else if err != nil {
		s.serverError(w, "highlight note", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteHighlight(w http.ResponseWriter, r *http.Request) {
	id, ok := highlightID(r)
	if !ok {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	itemID, err := s.Store.DeleteHighlight(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such highlight.", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, "delete highlight", err)
		return
	}
	s.respondHighlights(w, r, itemID)
}

func (s *Server) noteItem(w http.ResponseWriter, r *http.Request) {
	id, note := r.PathValue("id"), r.FormValue("note")
	if !fullID(id) || len(note) > maxNoteChars {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if err := s.Store.SetItemNote(r.Context(), id, note); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such item.", http.StatusNotFound)
		return
	} else if err != nil {
		s.serverError(w, "item note", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// annotated is a piece on the Highlights shelf.
type annotated struct {
	Card       card
	Note       string
	Highlights []store.Highlight
}
