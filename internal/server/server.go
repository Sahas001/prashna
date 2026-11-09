// Package server is Radar's app: server-rendered pages enhanced with htmx,
// installable as a PWA. It runs on the reader's own computer.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Sahas001/radar/internal/config"
	"github.com/Sahas001/radar/internal/score"
	"github.com/Sahas001/radar/internal/store"
)

type Server struct {
	Store *store.Store
	// Rescore runs after ratings or interest changes. Optional.
	Rescore func(ctx context.Context) error
	// Scorer returns a scorer for the current profile, used to explain
	// matches. Optional.
	Scorer func(ctx context.Context) (*score.Scorer, error)
	// ProbeFeed validates a feed added in the app and returns its title and
	// real feed URL (a blog's home page resolves to its feed). Optional.
	ProbeFeed func(ctx context.Context, url string) (title, feedURL string, err error)
	// StartFetch begins fetching every source in the background (no-op if
	// one is running); FetchStatus reports on it. Optional.
	StartFetch  func()
	FetchStatus func() (running bool, last time.Time, lastErr string)
	// EnsureEdition makes today's edition if possible. Optional.
	EnsureEdition func(ctx context.Context) (bool, error)
	// Settings and SaveSettings back the Settings page. Optional.
	Settings     func(ctx context.Context) config.Settings
	SaveSettings func(ctx context.Context, s config.Settings) error
	Auth         Auth
	SessionKey   []byte // HMAC key for session cookies
	About        About
	Log          *slog.Logger
}

// About is shown on the Settings page.
type About struct {
	Version, DataDir, ConfigPath, Addr string
}

// Handler returns the full HTTP handler. State-changing requests are
// checked by http.CrossOriginProtection (Sec-Fetch-Site / Origin), which
// with SameSite=Lax cookies stands in for CSRF tokens.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("radar ok\n"))
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

	// The app.
	ui := map[string]http.HandlerFunc{
		"GET /{$}":                     s.today,
		"GET /welcome":                 s.welcomePage,
		"POST /welcome":                s.welcome,
		"GET /explore":                 s.explore,
		"GET /explore/more":            s.exploreMore,
		"GET /library":                 s.library,
		"GET /archive":                 s.archive,
		"GET /archive/{id}":            s.archiveEdition,
		"GET /search":                  s.search,
		"GET /more":                    s.morePage,
		"POST /items/{id}/rate/{vote}": s.rateItem,
		"POST /items/{id}/opened":      s.openedItem,
		"POST /items/{id}/{action}":    s.itemAction,
		"GET /interests":               s.interestsPage,
		"POST /interests":              s.addInterest,
		"POST /interests/{id}/weight":  s.setInterestWeight,
		"POST /interests/{id}/delete":  s.deleteInterest,
		"POST /follows":                s.addFollow,
		"POST /follows/{id}/delete":    s.deleteFollow,
		"GET /feeds":                   s.feedsPage,
		"POST /feeds":                  s.addFeed,
		"POST /feeds/import":           s.importOPML,
		"GET /feeds/export":            s.exportOPML,
		"POST /feeds/{id}/toggle":      s.toggleFeed,
		"POST /feeds/{id}/delete":      s.deleteFeed,
		"GET /settings":                s.settingsPage,
		"POST /settings":               s.saveSettings,
		"POST /settings/theme":         s.saveTheme,
		"POST /fetch":                  s.fetchNow,
		"GET /status":                  s.status,
	}
	for pattern, h := range ui {
		mux.HandleFunc(pattern, s.requireAuth(h))
	}
	return http.NewCrossOriginProtection().Handler(mux)
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) fetchStatus() (running bool, last time.Time, lastErr string) {
	if s.FetchStatus == nil {
		return false, time.Time{}, ""
	}
	return s.FetchStatus()
}
