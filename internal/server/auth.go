package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Auth configures access to the web UI. Feedback links (/f/...) are
// always public: they carry their own signature.
type Auth struct {
	// Disabled turns off the login entirely, for setups where the network
	// already restricts access (Tailscale, LAN-only, an authenticating proxy).
	Disabled bool
	// Password is the single user's password. Empty (with Disabled false)
	// keeps the UI locked and shows setup instructions.
	Password string
	// SecureCookie sets the Secure flag; on when base_url is https.
	SecureCookie bool
}

const (
	sessionCookie = "prashna_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// loginFailWait slows password guessing; a var so tests can shorten it.
var loginFailWait = time.Second

// sessionMAC signs an expiry. The password's hash is mixed in, so changing
// the password logs out every existing session.
func (s *Server) sessionMAC(exp string) []byte {
	pw := sha256.Sum256([]byte(s.Auth.Password))
	m := hmac.New(sha256.New, s.SessionKey)
	m.Write([]byte("prashna-session\x00" + exp + "\x00"))
	m.Write(pw[:])
	return m.Sum(nil)
}

func (s *Server) newSession(now time.Time) *http.Cookie {
	exp := strconv.FormatInt(now.Add(sessionTTL).Unix(), 10)
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    exp + "." + base64.RawURLEncoding.EncodeToString(s.sessionMAC(exp)),
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.Auth.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *Server) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > n {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	return err == nil && hmac.Equal(got, s.sessionMAC(exp))
}

// requireAuth wraps UI handlers. Unauthenticated page loads go to the
// login page; htmx requests get HX-Redirect so the whole page navigates.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case s.Auth.Disabled:
			h(w, r)
		case s.Auth.Password == "":
			s.renderPage(w, r, http.StatusServiceUnavailable, "setup", "Set up Prashna", "", nil)
		case s.validSession(r):
			h(w, r)
		default:
			target := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
			if r.Header.Get("HX-Request") != "" {
				w.Header().Set("HX-Redirect", target)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
		}
	}
}

// safeNext only allows local paths, so ?next= can't redirect off-site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

type loginData struct {
	Next  string
	Error string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Disabled || (s.Auth.Password != "" && s.validSession(r)) {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "login", "Sign in", "", loginData{Next: safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	if s.Auth.Disabled {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	given, want := sha256.Sum256([]byte(r.FormValue("password"))), sha256.Sum256([]byte(s.Auth.Password))
	if s.Auth.Password == "" || subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
		time.Sleep(loginFailWait) // slow down guessing
		s.log().Warn("failed login", "remote", r.RemoteAddr)
		s.renderPage(w, r, http.StatusUnauthorized, "login", "Sign in", "", loginData{Next: next, Error: "Wrong password."})
		return
	}
	http.SetCookie(w, s.newSession(time.Now()))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.Auth.SecureCookie, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
