package server

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/reader"
	"github.com/Sahas001/prashna/internal/store"
)

type addData struct {
	URL         string
	Error       string
	Bookmarklet template.URL
	// In the desktop window there is no bookmarks bar to drag to: the
	// page says where to find the button in a browser instead.
	Window  bool
	AddPage string
}

func (s *Server) renderAdd(w http.ResponseWriter, r *http.Request, rawURL, problem string) {
	// The bookmarklet opens this page with the address filled in. Saving
	// still takes a click here, so no other site can add things by
	// sending the reader to a link.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	js := "javascript:void(open('" + scheme + "://" + r.Host + "/add?url='+encodeURIComponent(location.href)))"
	s.renderPage(w, r, http.StatusOK, "add", "Add a link", "library", addData{
		URL: rawURL, Error: problem, Window: inWindow(r), AddPage: scheme + "://" + r.Host + "/add",
		Bookmarklet: template.URL(js), // built from our own address only
	})
}

func (s *Server) addPage(w http.ResponseWriter, r *http.Request) {
	s.renderAdd(w, r, strings.TrimSpace(r.URL.Query().Get("url")), "")
}

// addLink saves a page the reader found elsewhere: it becomes a piece in
// the library like any other, with its text stored for the reader.
func (s *Server) addLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := strings.TrimSpace(r.FormValue("url"))
	link := raw
	if link != "" && !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.Contains(u.Hostname(), ".") {
		s.renderAdd(w, r, raw, "That doesn’t look like a web address.")
		return
	}
	canon, err := item.CanonicalURL(link)
	if err != nil {
		s.renderAdd(w, r, raw, "That doesn’t look like a web address.")
		return
	}

	// Already known (from a feed, or added before): just save it.
	if it, err := s.Store.Get(ctx, item.IDFor(canon)); err == nil {
		s.finishAdd(w, r, it, false)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "add link", err)
		return
	}

	now := time.Now()
	it := item.Item{Type: item.TypeArticle, Source: item.SourceSaved, URL: canon, Published: now, FetchedAt: now,
		Title: placeholderTitle(canon)}
	var art *reader.Article
	if reader.ArxivID(canon) != "" {
		it.Type = item.TypePaper
	}
	if s.ReadText != nil {
		fctx, cancel := context.WithTimeout(ctx, readTimeout)
		got, err := s.ReadText(fctx, link) // as given: the canonical form drops "www."
		cancel()
		switch {
		case err == nil:
			art = &got
			if got.Title != "" {
				it.Title = got.Title
			}
			it.Summary = got.Excerpt
			if got.Byline != "" {
				it.Authors = []string{got.Byline}
			}
		case errors.Is(err, reader.ErrBlocked):
			s.renderAdd(w, r, raw, "That address is on your own network, which Prashna doesn’t read from.")
			return
		default:
			// Saved all the same, as a link: a PDF, a page behind a
			// sign-in, or a site that is down right now.
			s.log().Info("add link: no text", "url", canon, "err", err)
		}
	}
	if err := it.Normalize(); err != nil {
		s.renderAdd(w, r, raw, "That doesn’t look like a web address.")
		return
	}
	if _, err := s.Store.SaveItems(ctx, []item.Item{it}); err != nil {
		s.serverError(w, "add link", err)
		return
	}
	saved, err := s.Store.Stored(ctx, it)
	if err != nil {
		s.serverError(w, "add link", err)
		return
	}
	if art != nil && !s.Store.HasArticle(ctx, saved.ID) {
		if err := s.Store.SaveArticle(ctx, store.Article{ItemID: saved.ID, HTML: art.HTML, Byline: art.Byline, Site: art.Site, Words: art.Words}); err != nil {
			s.serverError(w, "add link", err)
			return
		}
	}
	if s.Rescore != nil {
		if err := s.Rescore(ctx); err != nil {
			s.log().Error("rescore after adding a link", "err", err)
		}
	}
	s.finishAdd(w, r, saved, true)
}

// finishAdd puts the piece on the reading list and shows the list. fetched says
// its text was just asked for, so there is no point asking again.
func (s *Server) finishAdd(w http.ResponseWriter, r *http.Request, it item.Item, fetched bool) {
	if it.SavedAt.IsZero() {
		saved, err := s.Store.SetSaved(r.Context(), it.ID, true)
		if err != nil {
			s.serverError(w, "add link", err)
			return
		}
		it = saved
	}
	if !fetched {
		s.keepText(it)
	}
	// To the reading list, not the reader: opening a piece counts as
	// reading it, and this one was saved for later.
	dest := "/library"
	http.Redirect(w, r, dest, http.StatusSeeOther)
}
