package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestWindow(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	var opened []string
	e.s.OpenExternal = func(u string) error { opened = append(opened, u); return nil }

	// A browser sees the plain app.
	browser := client()
	if _, body := do(t, browser, "GET", e.srv.URL+"/", nil, nil); strings.Contains(body, "data-window") || strings.Contains(body, "data-back") {
		t.Error("browser got the window's page")
	}
	// The window starts at /window and from then on gets a back button.
	win := client()
	resp, _ := do(t, win, "GET", e.srv.URL+"/window", nil, nil)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("window start: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := do(t, win, "GET", e.srv.URL+"/", nil, nil); !strings.Contains(body, " data-window>") || !strings.Contains(body, "data-back") {
		t.Error("window page lacks data-window or the back button")
	}

	if resp, _ := do(t, win, "POST", e.srv.URL+"/open-outside", url.Values{"url": {"https://blog.example.com/post"}}, nil); resp.StatusCode != 204 {
		t.Errorf("open outside: %d", resp.StatusCode)
	}
	for _, bad := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "/relative", "x-scheme://thing"} {
		if resp, _ := do(t, win, "POST", e.srv.URL+"/open-outside", url.Values{"url": {bad}}, nil); resp.StatusCode != 400 {
			t.Errorf("%q: %d, want 400", bad, resp.StatusCode)
		}
	}
	if len(opened) != 1 || opened[0] != "https://blog.example.com/post" {
		t.Errorf("opened %v", opened)
	}
	// Off unless the app was started with it (a Prashna shared on the network).
	e.s.OpenExternal = nil
	if resp, _ := do(t, win, "POST", e.srv.URL+"/open-outside", url.Values{"url": {"https://blog.example.com/post"}}, nil); resp.StatusCode != 400 {
		t.Errorf("without an opener: %d", resp.StatusCode)
	}
}

// A page that points its own domain at 127.0.0.1 is "same origin" to the
// browser; the Host header gives it away.
func TestLocalHostOnly(t *testing.T) {
	e := newEnv(t, Auth{Disabled: true})
	get := func(base, host string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", base+"/healthz", nil)
		req.Host = host // "" keeps the address's own
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for host, want := range map[string]int{"": 200, "localhost:7878": 200, "[::1]:7878": 200, "evil.example": 403, "evil.example:7878": 403, "127.0.0.1.evil.example": 403} {
		if got := get(e.srv.URL, host); got != want {
			t.Errorf("Host %q: %d, want %d", host, got, want)
		}
	}
	// With a password, Prashna may be reached by any name.
	locked := newEnv(t, Auth{Password: "pw"})
	if got := get(locked.srv.URL, "reader.example"); got != 200 {
		t.Errorf("with a password: %d", got)
	}
}
