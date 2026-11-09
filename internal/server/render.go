package server

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Sahas001/radar/internal/edition"
	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/store"
)

//go:embed templates/layout.html templates/pages templates/partials
var uiFS embed.FS

// pages maps page name ("today") to layout + partials + that page;
// partials renders fragments for htmx responses.
var pages, partials = loadTemplates()

var funcs = template.FuncMap{
	"ago":       ago,
	"typeLabel": typeLabel,
	"asset":     func(p string) string { return "/static/" + p + "?v=" + assetVersion },
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
	"longDate":  func(t time.Time) string { return t.Local().Format("Monday, 2 January") },
	"shortDate": func(t time.Time) string { return t.Local().Format("2 Jan 2006") },
	"weekday":   func(t time.Time) string { return t.Local().Format("Monday") },
	"dayMonth":  func(t time.Time) string { return t.Local().Format("2 January") },
	"year":      func(t time.Time) int { return t.Local().Year() },
	"list":      func(xs ...int) []int { return xs },
}

func loadTemplates() (map[string]*template.Template, *template.Template) {
	base := template.Must(template.New("").Funcs(funcs).ParseFS(uiFS, "templates/layout.html", "templates/partials/*.html"))
	names, err := fs.Glob(uiFS, "templates/pages/*.html")
	if err != nil {
		panic(err)
	}
	out := map[string]*template.Template{}
	for _, n := range names {
		out[strings.TrimSuffix(path.Base(n), ".html")] = template.Must(template.Must(base.Clone()).ParseFS(uiFS, n))
	}
	return out, base
}

// bare pages have no navigation.
var bare = map[string]bool{"login": true, "setup": true, "offline": true, "welcome": true}

// layoutData is what templates/layout.html receives.
type layoutData struct {
	Title  string
	Nav    string // active section
	ToRead int    // unread saved items, for the Library badge
	Bare   bool
	CanOut bool   // show "Sign out"
	Theme  string // "light" or "dark" when chosen in Settings; "" follows the system
	Data   any
}

// renderPage renders into a buffer first, so a template error becomes a
// clean 500 instead of half a page.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title, nav string, data any) {
	t, ok := pages[name]
	if !ok {
		http.Error(w, "unknown page "+name, http.StatusInternalServerError)
		return
	}
	ld := layoutData{Title: title, Nav: nav, Data: data, Bare: bare[name], Theme: s.theme(r.Context())}
	if !ld.Bare {
		ld.CanOut = !s.Auth.Disabled
		if counts, err := s.Store.ShelfCounts(r.Context()); err == nil {
			ld.ToRead = counts[store.ShelfToRead]
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

// theme is the Appearance choice to render, or "" to follow the system.
func (s *Server) theme(ctx context.Context) string {
	if s.Settings == nil {
		return ""
	}
	if t := s.Settings(ctx).Theme; t == "light" || t == "dark" {
		return t
	}
	return ""
}

// renderPartial renders one named fragment for htmx.
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

// uiHeaders sets security headers. Scripts, styles and fonts come only
// from /static, so no inline code is allowed; images may come from book
// cover services.
func (s *Server) uiHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; "+
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
}

// Card variants pick the markup an item is rendered with.
const (
	variantStory = "story"
	variantLead  = "lead"
	variantBook  = "book"
	variantPick  = "pick"
)

// card is an item as displayed: the shared entry (byline, snippet,
// reasons) plus the reader's state.
type card struct {
	edition.Entry
	Variant  string
	Saved    bool
	Read     bool
	Liked    bool
	Disliked bool
	Reading  string // "Opened 3 times, last 2d ago"
}

func newCard(it item.Item, explain func(item.Item) []string, variant string) card {
	if variant == "" {
		variant = variantStory
	}
	c := card{
		Entry:    edition.NewEntry(it, explain),
		Variant:  variant,
		Saved:    !it.SavedAt.IsZero(),
		Read:     !it.ReadAt.IsZero(),
		Liked:    it.Feedback == item.Liked,
		Disliked: it.Feedback == item.Disliked,
	}
	switch {
	case it.OpenCount > 1:
		c.Reading = fmt.Sprintf("Opened %d times, last %s", it.OpenCount, ago(it.OpenedAt))
	case it.OpenCount == 1:
		c.Reading = "Opened " + ago(it.OpenedAt)
	case !it.ReadAt.IsZero():
		c.Reading = "Marked read " + ago(it.ReadAt)
	}
	return c
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return t.Local().Format("2 Jan 2006")
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return t.Local().Format("2 Jan")
	default:
		return t.Local().Format("Jan 2006")
	}
}

func typeLabel(t item.Type) string {
	switch t {
	case item.TypePaper:
		return "Paper"
	case item.TypeArticle:
		return "Essay"
	case item.TypeNews:
		return "Discussion"
	case item.TypeBook:
		return "Book"
	}
	return string(t)
}
