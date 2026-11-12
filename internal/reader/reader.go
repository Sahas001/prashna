// Package reader fetches a web page and reduces it to its readable text:
// the article itself as a small, safe subset of HTML, without the site's
// navigation, scripts, styling or tracking.
package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Article is the readable content of a page.
type Article struct {
	HTML    string // sanitized: only the tags and attributes in sanitize.go
	Title   string
	Excerpt string // the page's own description, if it has one
	Byline  string
	Site    string
	Words   int
}

var (
	// ErrNotArticle means the address isn't a web page (a PDF, an image...).
	ErrNotArticle = errors.New("not a web page")
	// ErrNoText means the page has no article text Prashna could find, as
	// with pages that only work with JavaScript or sit behind a sign-in.
	ErrNoText = errors.New("no readable text found")
	// ErrBlocked means the address points at this computer or the local
	// network, which pages from the internet have no business linking to.
	ErrBlocked = errors.New("address is on the local network")
)

const (
	maxPageBytes = 8 << 20
	minWords     = 30
)

// Extractor fetches and extracts pages.
type Extractor struct {
	HTTP      *http.Client
	UserAgent string
}

// New returns an Extractor that refuses to connect to local addresses, so
// a link in a feed can't make Prashna read from the reader's own network.
// (When a proxy is configured, connections go to the proxy and the check
// is left to it.)
func New(userAgent string) *Extractor {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if !proxyConfigured() {
		dialer.Control = publicOnly
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	return &Extractor{HTTP: &http.Client{Transport: tr, Timeout: 30 * time.Second}, UserAgent: userAgent}
}

func proxyConfigured() bool {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// publicOnly runs after DNS resolution, on the address actually dialed.
func publicOnly(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlocked, address)
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%w: %s", ErrBlocked, ip)
	}
	return nil
}

// Extract fetches pageURL and returns its readable content.
func (e *Extractor) Extract(ctx context.Context, pageURL string) (Article, error) {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Article{}, fmt.Errorf("%w: %s", ErrNotArticle, pageURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Article{}, err
	}
	if e.UserAgent != "" {
		req.Header.Set("User-Agent", e.UserAgent)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return Article{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Article{}, fmt.Errorf("the site answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return Article{}, err
	}
	if len(body) > maxPageBytes {
		return Article{}, fmt.Errorf("%w: page is too large", ErrNotArticle)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
	}
	if !strings.Contains(strings.ToLower(ct), "html") {
		return Article{}, fmt.Errorf("%w: %s", ErrNotArticle, ct)
	}
	return FromHTML(body, ct, resp.Request.URL)
}

// FromHTML extracts the article from a page's bytes. base is the page's
// address, used to make links and images absolute.
func FromHTML(page []byte, contentType string, base *url.URL) (Article, error) {
	r, err := charset.NewReader(bytes.NewReader(page), contentType)
	if err != nil {
		return Article{}, err
	}
	doc, err := html.Parse(r)
	if err != nil {
		return Article{}, err
	}
	art, err := readability.FromDocument(doc, base)
	if err != nil || art.Node == nil {
		return Article{}, ErrNoText
	}
	out, words := Sanitize(art.Node, base)
	if words < minWords {
		return Article{}, ErrNoText
	}
	site := art.SiteName()
	if site == "" {
		site = strings.TrimPrefix(base.Hostname(), "www.")
	}
	return Article{HTML: out, Title: strings.TrimSpace(art.Title()), Excerpt: strings.TrimSpace(art.Excerpt()), Byline: strings.TrimSpace(art.Byline()), Site: strings.TrimSpace(site), Words: words}, nil
}
