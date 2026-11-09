// Package server is Radar's HTTP server: the web UI (htmx + Go templates,
// installable as a PWA) and the public feedback endpoints that digest
// emails link to.
//
// Feedback links are a two-step flow: GET /f/{id}/{vote} shows a page that
// auto-submits a POST to the same URL. Mail scanners and link prefetchers
// (Outlook Safe Links, corporate gateways) issue GETs but don't run
// scripts or submit forms, so they can't cast votes by accident.
package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"sync/atomic"

	"radar/internal/item"
	"radar/internal/links"
	"radar/internal/score"
	"radar/internal/store"
)

type Server struct {
	Store  *store.Store
	Signer *links.Signer
	// Rescore runs after feedback or profile changes, since likes and
	// interests change every item's score. Optional.
	Rescore func(ctx context.Context) error
	// Scorer returns a scorer for the current profile, used to explain
	// matches on cards. Optional.
	Scorer func(ctx context.Context) (*score.Scorer, error)
	// ProbeFeed validates a feed added in the UI and returns its title and
	// actual feed URL (which differs when a web page links to its feed). Optional.
	ProbeFeed func(ctx context.Context, url string) (title, feedURL string, err error)
	// FetchFeeds runs an RSS fetch now ("Fetch now" on the feeds page). Optional.
	FetchFeeds func(ctx context.Context) error
	Auth       Auth
	SessionKey []byte // HMAC key for session cookies
	TopDays    int    // window for the Today page; default 7
	Log        *slog.Logger

	fetching atomic.Bool
}

//go:embed templates/feedback.html.tmpl
var templateFS embed.FS

var feedbackPage = template.Must(template.ParseFS(templateFS, "templates/feedback.html.tmpl"))

var fullID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Handler returns the full HTTP handler. Every state-changing request is
// checked by http.CrossOriginProtection (Sec-Fetch-Site / Origin), which
// together with SameSite=Lax session cookies stands in for CSRF tokens.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /f/{id}/{vote}", s.confirm)
	mux.HandleFunc("POST /f/{id}/{vote}", s.record)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /manifest.webmanifest", serveManifest)
	mux.HandleFunc("GET /sw.js", serveServiceWorker)
	mux.HandleFunc("GET /offline", func(w http.ResponseWriter, r *http.Request) {
		s.renderPage(w, r, http.StatusOK, "offline", "Offline", "", nil)
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	// Signed in.
	ui := map[string]http.HandlerFunc{
		"GET /{$}":                     s.today,
		"GET /more":                    s.more,
		"GET /saved":                   s.saved,
		"GET /history":                 s.history,
		"GET /history/{id}":            s.digestPage,
		"GET /interests":               s.interestsPage,
		"GET /feeds":                   s.feedsPage,
		"POST /items/{id}/rate/{vote}": s.rateItem,
		"POST /items/{id}/{action}":    s.itemAction,
		"POST /interests":              s.addInterest,
		"POST /interests/{id}/weight":  s.setInterestWeight,
		"POST /interests/{id}/delete":  s.deleteInterest,
		"POST /follows":                s.addFollow,
		"POST /follows/{id}/delete":    s.deleteFollow,
		"POST /feeds":                  s.addFeed,
		"POST /feeds/fetch":            s.fetchFeedsNow,
		"POST /feeds/{id}/toggle":      s.toggleFeed,
		"POST /feeds/{id}/delete":      s.deleteFeed,
	}
	for pattern, h := range ui {
		mux.HandleFunc(pattern, s.requireAuth(h))
	}
	return http.NewCrossOriginProtection().Handler(mux)
}

type action struct {
	Path, Label string
	Primary     bool
}

type pageData struct {
	Nonce   string
	Error   string
	Done    bool
	Item    item.Item
	Kicker  string
	Message string
	Actions []action
}

var voteLabels = map[links.Vote]string{links.Up: "👍 More like this", links.Down: "👎 Less like this", links.Clear: "Clear rating"}

// resolve validates the request's id, vote and token and loads the item.
// On failure it has already written an error page.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request) (item.Item, links.Vote, item.Feedback, bool) {
	id, rawVote, token := r.PathValue("id"), r.PathValue("vote"), r.FormValue("t")
	vote, fb, ok := links.ParseVote(rawVote)
	if !ok || !fullID.MatchString(id) {
		s.render(w, http.StatusNotFound, pageData{Error: "This link isn't valid."})
		return item.Item{}, "", 0, false
	}
	if !s.Signer.Valid(id, vote, token) {
		s.render(w, http.StatusForbidden, pageData{Error: "This link isn't valid. It may come from an older Radar database."})
		return item.Item{}, "", 0, false
	}
	it, err := s.Store.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, http.StatusNotFound, pageData{Error: "That item no longer exists."})
		return item.Item{}, "", 0, false
	}
	if err != nil {
		s.log().Error("load item", "id", id, "err", err)
		s.render(w, http.StatusInternalServerError, pageData{Error: "Something went wrong."})
		return item.Item{}, "", 0, false
	}
	return it, vote, fb, true
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	it, vote, _, ok := s.resolve(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, pageData{
		Item:    it,
		Kicker:  "Recording your rating…",
		Actions: []action{{Path: s.Signer.Path(it.ID, vote), Label: voteLabels[vote], Primary: true}},
	})
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	it, vote, fb, ok := s.resolve(w, r)
	if !ok {
		return
	}
	if _, err := s.Store.SetFeedback(r.Context(), it.ID, fb); err != nil {
		s.log().Error("set feedback", "id", it.ID, "err", err)
		s.render(w, http.StatusInternalServerError, pageData{Error: "Couldn't save your rating."})
		return
	}
	s.log().Info("feedback", "id", it.ID, "vote", vote, "title", it.Title)
	if s.Rescore != nil {
		if err := s.Rescore(r.Context()); err != nil {
			s.log().Error("rescore after feedback", "err", err)
		}
	}

	d := pageData{Done: true, Item: it}
	switch vote {
	case links.Up:
		d.Kicker, d.Message = "Recorded 👍", "Items like this will rank higher in future digests."
	case links.Down:
		d.Kicker, d.Message = "Recorded 👎", "Items like this will rank lower in future digests."
	case links.Clear:
		d.Kicker, d.Message = "Rating cleared", "This item no longer affects your recommendations."
	}
	for _, v := range []links.Vote{links.Up, links.Down, links.Clear} {
		if v != vote {
			d.Actions = append(d.Actions, action{Path: s.Signer.Path(it.ID, v), Label: voteLabels[v]})
		}
	}
	s.render(w, http.StatusOK, d)
}

func (s *Server) render(w http.ResponseWriter, status int, d pageData) {
	nonce := make([]byte, 16)
	rand.Read(nonce)
	d.Nonce = base64.RawStdEncoding.EncodeToString(nonce)

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+d.Nonce+
		"'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := feedbackPage.Execute(w, d); err != nil {
		s.log().Error("render page", "err", err)
	}
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
