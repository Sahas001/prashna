package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// windowCookie marks requests from the desktop app's window. Its web
// view keeps its own cookies, so a browser on the same computer never
// has it.
const windowCookie = "prashna_window"

// windowStart is the first address the desktop window loads.
func (s *Server) windowStart(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: windowCookie, Value: "1", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	s.log().Info("window opened")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func inWindow(r *http.Request) bool {
	c, err := r.Cookie(windowCookie)
	return err == nil && c.Value == "1"
}

// openOutside hands a link to another site over to the real browser.
func (s *Server) openOutside(w http.ResponseWriter, r *http.Request) {
	// Only for someone at this computer: when Prashna is shared on the
	// network, a visitor must not open things on this screen.
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !isLoopback(host) {
		http.Error(w, "Forbidden.", http.StatusForbidden)
		return
	}
	u, err := url.Parse(r.FormValue("url"))
	if s.OpenExternal == nil || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "Bad request.", http.StatusBadRequest)
		return
	}
	if err := s.OpenExternal(u.String()); err != nil {
		s.serverError(w, "open in browser", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// localHostOnly refuses requests whose Host isn't this computer's own
// name. A Prashna without a password trusts whoever can reach it, and a
// web page can point a domain of its own at 127.0.0.1 (DNS rebinding) to
// be "same origin" with it; such requests carry that domain as Host.
func localHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if !isLoopback(host) {
			http.Error(w, "Prashna only answers at its own local address.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
