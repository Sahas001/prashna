package server

import (
	"net/http"
	"net/url"
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
