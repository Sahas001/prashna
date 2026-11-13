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
	// ErrNoHTML means a paper has no web version, only a PDF.
	ErrNoHTML = errors.New("no web version of this paper")
)

const (
	maxPageBytes = 8 << 20
	minWords     = 30
)

// Extractor fetches and extracts pages.
type Extractor struct {
	HTTP      *http.Client
	UserAgent string
	// ArxivHTML lists where web versions of arXiv papers are looked for,
	// in order; the paper's ID is appended. Empty means arXiv itself and
	// then ar5iv, its archive of older papers.
	ArxivHTML []string
}

// New returns an Extractor that refuses to connect to local addresses, so
// a link in a feed can't make Prashna read from the reader's own network.
func New(userAgent string) *Extractor {
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	direct.DialContext = (&net.Dialer{Timeout: 15 * time.Second, Control: publicOnly}).DialContext
	proxied := http.DefaultTransport.(*http.Transport).Clone()
	return &Extractor{HTTP: &http.Client{Transport: guarded{direct, proxied}, Timeout: 30 * time.Second}, UserAgent: userAgent}
}

// guarded sends each request (every redirect hop is one) either through
// the reader's proxy, when the environment names one for that address, or
// straight out through a dialer that only connects to public addresses.
// Deciding per request matters: Go never uses a proxy for localhost or
// NO_PROXY hosts, and those are exactly the ones to refuse.
type guarded struct{ direct, proxied http.RoundTripper }

func (g guarded) RoundTrip(req *http.Request) (*http.Response, error) {
	if p, err := http.ProxyFromEnvironment(req); err == nil && p != nil {
		return g.proxied.RoundTrip(req)
	}
	return g.direct.RoundTrip(req)
}

// cgnat is shared address space (RFC 6598): carrier NAT and overlay
// networks such as Tailscale. Not public, though net/netip doesn't call
// it private.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicOnly runs after DNS resolution, on the address actually dialed.
func publicOnly(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlocked, address)
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, ip)
	}
	return nil
}

// Extract fetches pageURL and returns its readable content. An arXiv
// paper's address gets the paper itself, from arXiv's web version of it.
func (e *Extractor) Extract(ctx context.Context, pageURL string) (Article, error) {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Article{}, fmt.Errorf("%w: %s", ErrNotArticle, pageURL)
	}
	if id := ArxivID(pageURL); id != "" {
		return e.extractArxiv(ctx, id)
	}
	body, ct, final, err := e.get(ctx, u.String())
	if err != nil {
		return Article{}, err
	}
	return FromHTML(body, ct, final)
}

// statusError is a response other than 200 OK.
type statusError struct{ code int }

func (e *statusError) Error() string {
	return fmt.Sprintf("the site answered %d %s", e.code, http.StatusText(e.code))
}

// get fetches an HTML page: its bytes, content type and final address
// (after redirects).
func (e *Extractor) get(ctx context.Context, pageURL string) ([]byte, string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", nil, err
	}
	if e.UserAgent != "" {
		req.Header.Set("User-Agent", e.UserAgent)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.5")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", nil, &statusError{resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, "", nil, err
	}
	if len(body) > maxPageBytes {
		return nil, "", nil, fmt.Errorf("%w: page is too large", ErrNotArticle)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
	}
	if !strings.Contains(strings.ToLower(ct), "html") {
		return nil, "", nil, fmt.Errorf("%w: %s", ErrNotArticle, ct)
	}
	return body, ct, resp.Request.URL, nil
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
