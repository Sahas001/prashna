package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"slices"
	"strings"
)

//go:embed static
var staticFS embed.FS

// assetVersion is a hash of every static file. It busts browser caches via
// ?v= and names the service worker's cache, so a new build is picked up.
var assetVersion, staticPaths = hashStatic()

func hashStatic() (string, []string) {
	h := sha256.New()
	var paths []string
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := staticFS.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
		paths = append(paths, "/"+p)
		return nil
	})
	slices.Sort(paths)
	return hex.EncodeToString(h.Sum(nil))[:12], paths
}

// staticHandler serves /static/*. Versioned URLs are immutable.
func staticHandler() http.Handler {
	files := http.FileServerFS(staticFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") == assetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
}

const manifest = `{
  "name": "Radar",
  "short_name": "Radar",
  "description": "Your daily reading digest",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "background_color": "#f4f2ee",
  "theme_color": "#1d2a24",
  "icons": [
    {"src": "/static/icons/icon-192.png", "sizes": "192x192", "type": "image/png"},
    {"src": "/static/icons/icon-512.png", "sizes": "512x512", "type": "image/png"},
    {"src": "/static/icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
    {"src": "/static/icons/icon.svg", "sizes": "any", "type": "image/svg+xml"}
  ]
}
`

func serveManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(manifest))
}

// serviceWorker caches the app shell for offline use. Pages are
// network-first; the latest copies of Today and the reading list are kept
// so they can be read offline. htmx requests and POSTs always go to the
// network. It must be served from / to control the whole origin.
const serviceWorker = `const VERSION = "radar-__VERSION__";
const SHELL = __SHELL__;
const OFFLINE_PAGES = ["/", "/saved"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(VERSION).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== VERSION && k !== "radar-pages").map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin || req.headers.get("HX-Request")) return;

  if (url.pathname.startsWith("/static/")) {
    // Exact match: SHELL holds versioned URLs, so a new build misses and
    // goes to the network instead of getting a stale cached file.
    e.respondWith(caches.match(req).then((hit) => hit || fetch(req)));
    return;
  }
  if (req.mode === "navigate") {
    e.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok && !res.redirected && OFFLINE_PAGES.includes(url.pathname) && !url.search) {
            const copy = res.clone();
            caches.open("radar-pages").then((c) => c.put(url.pathname, copy));
          }
          return res;
        })
        .catch(() =>
          caches.open("radar-pages")
            .then((c) => c.match(url.pathname))
            .then((hit) => hit || caches.match("/offline"))
        )
    );
  }
});
`

func serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	shell := []string{`"/offline"`}
	for _, p := range staticPaths {
		shell = append(shell, `"`+p+"?v="+assetVersion+`"`)
	}
	js := strings.NewReplacer("__VERSION__", assetVersion, "__SHELL__", "["+strings.Join(shell, ", ")+"]").Replace(serviceWorker)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Write([]byte(js))
}
