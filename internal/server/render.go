package server

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"radar/internal/digest"
	"radar/internal/item"
)

//go:embed templates/layout.html templates/pages templates/partials
var uiFS embed.FS

// pages maps page name ("today") to a template set of layout + partials +
// that page; partials maps partial name to a set for htmx fragment responses.
var pages, partials = loadTemplates()

var funcs = template.FuncMap{
	"ago":       ago,
	"typeLabel": typeLabel,
	"asset":     func(p string) string { return "/static/" + p + "?v=" + assetVersion },
	"join":      strings.Join,
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}

func loadTemplates() (map[string]*template.Template, *template.Template) {
	base := template.Must(template.New("").Funcs(funcs).ParseFS(uiFS, "templates/layout.html", "templates/partials/*.html"))
	names, err := fs.Glob(uiFS, "templates/pages/*.html")
	if err != nil {
		panic(err)
	}
	out := map[string]*template.Template{}
	for _, n := range names {
		t := template.Must(template.Must(base.Clone()).ParseFS(uiFS, n))
		out[strings.TrimSuffix(path.Base(n), ".html")] = t
	}
	return out, base
}

// layoutData is what templates/layout.html receives.
type layoutData struct {
	Title   string
	Nav     string // active nav entry
	Unread  int    // unread reading-list items, for the nav badge
	ShowNav bool
	CanOut  bool // show "sign out"
	Data    any
}

// renderPage renders a full page into a buffer first, so a template error
// becomes a clean 500 instead of half a page.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title, nav string, data any) {
	t, ok := pages[name]
	if !ok {
		http.Error(w, "unknown page "+name, http.StatusInternalServerError)
		return
	}
	ld := layoutData{Title: title, Nav: nav, Data: data}
	if name != "login" && name != "setup" && name != "offline" {
		ld.ShowNav = true
		ld.CanOut = !s.Auth.Disabled
		if n, err := s.Store.CountUnreadSaved(r.Context()); err == nil {
			ld.Unread = n
		}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", ld); err != nil {
		s.log().Error("render page", "page", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	s.uiHeaders(w)
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// renderPartial renders one named partial (an htmx fragment).
func (s *Server) renderPartial(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := partials.ExecuteTemplate(&buf, name, data); err != nil {
		s.log().Error("render partial", "partial", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	s.uiHeaders(w)
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// uiHeaders sets security headers for UI responses. Scripts and styles
// come only from /static (htmx is vendored), so no inline code is allowed.
func (s *Server) uiHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; "+
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
}

// card is an item as shown in the UI: the digest entry (byline, snippet,
// reasons) plus reading-list state.
type card struct {
	digest.Entry
	Saved, Read bool
	Liked       bool
	Disliked    bool
}

func newCard(it item.Item, explain func(item.Item) []string) card {
	return card{
		Entry:    digest.NewEntry(it, explain),
		Saved:    !it.SavedAt.IsZero(),
		Read:     !it.ReadAt.IsZero(),
		Liked:    it.Feedback == item.Liked,
		Disliked: it.Feedback == item.Disliked,
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return t.Format("2 Jan 2006")
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return t.Format("2 Jan")
	default:
		return t.Format("Jan 2006")
	}
}

func typeLabel(t item.Type) string {
	switch t {
	case item.TypePaper:
		return "Paper"
	case item.TypeArticle:
		return "Article"
	case item.TypeNews:
		return "Discussion"
	case item.TypeBook:
		return "Book"
	}
	return string(t)
}
