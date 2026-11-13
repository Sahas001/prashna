package fetch

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/htmlindex"

	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/store"
)

// Feed is one user-configured RSS/Atom feed.
type Feed struct {
	URL  string
	Name string    // display name; defaults to the feed's own title
	Tags []string  // added to every item, e.g. ["distributed systems"]
	Type item.Type // defaults to article
}

// FeedStateStore persists per-feed cache validators and health.
type FeedStateStore interface {
	FeedState(ctx context.Context, url string) (store.FeedState, error)
	SaveFeedState(ctx context.Context, fs store.FeedState) error
}

// RSS fetches a list of RSS 2.0, RSS 1.0 (RDF) and Atom feeds concurrently,
// using conditional GET (ETag / Last-Modified) when State is set. A broken
// feed doesn't stop the others: their items are returned along with an
// error naming the failed feeds.
type RSS struct {
	Client      *Client
	Feeds       []Feed
	State       FeedStateStore // optional
	MaxAge      time.Duration  // skip items published longer ago than this; 0 keeps all
	Concurrency int            // default 4
	Now         func() time.Time
}

func (r *RSS) Name() string { return "rss" }

const maxSummaryRunes = 1500

func (r *RSS) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RSS) Fetch(ctx context.Context) ([]item.Item, error) {
	workers := r.Concurrency
	if workers <= 0 {
		workers = 4
	}
	type result struct {
		items []item.Item
		err   error
	}
	results := make([]result, len(r.Feeds))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, f := range r.Feeds {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			items, err := r.fetchFeed(ctx, f)
			if err != nil {
				err = fmt.Errorf("feed %s: %w", f.URL, err)
			}
			results[i] = result{items, err}
		})
	}
	wg.Wait()

	var items []item.Item
	var errs []error
	for _, res := range results {
		items = append(items, res.items...)
		if res.err != nil {
			errs = append(errs, res.err)
		}
	}
	return items, errors.Join(errs...)
}

func (r *RSS) fetchFeed(ctx context.Context, f Feed) ([]item.Item, error) {
	now := r.now()
	st := store.FeedState{URL: f.URL}
	if r.State != nil {
		var err error
		if st, err = r.State.FeedState(ctx, f.URL); err != nil {
			return nil, err
		}
	}
	save := func() {
		if r.State != nil {
			// Best effort: losing cache validators only costs a full download next time.
			_ = r.State.SaveFeedState(ctx, st)
		}
	}

	hdr := http.Header{}
	if st.ETag != "" {
		hdr.Set("If-None-Match", st.ETag)
	}
	if st.LastModified != "" {
		hdr.Set("If-Modified-Since", st.LastModified)
	}
	hdr.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")

	st.LastFetchedAt = now
	resp, err := r.Client.Do(ctx, f.URL, hdr)
	if err != nil {
		st.LastError = err.Error()
		save()
		return nil, err
	}
	if resp.NotModified {
		// LastItems keeps describing the last full download.
		st.LastOKAt, st.LastError = now, ""
		save()
		return nil, nil
	}
	parsed, err := ParseFeed(resp.Body, f.URL)
	if err != nil {
		st.LastError = err.Error()
		save()
		return nil, err
	}

	name := f.Name
	if name == "" {
		name = parsed.Title
	}
	typ := f.Type
	if typ == "" {
		typ = item.TypeArticle
	}
	var items []item.Item
	for _, it := range parsed.Items {
		if r.MaxAge > 0 && !it.Published.IsZero() && now.Sub(it.Published) > r.MaxAge {
			continue
		}
		it.Type = typ
		it.Tags = append(it.Tags, f.Tags...)
		if name != "" {
			it.Meta["feed"] = name
		}
		items = append(items, it)
	}

	st.Title = parsed.Title
	st.ETag = resp.Header.Get("ETag")
	st.LastModified = resp.Header.Get("Last-Modified")
	st.LastOKAt, st.LastError, st.LastItems = now, "", len(items)
	save()
	return items, nil
}

// ErrNotAFeed means a URL served a web page with no discoverable feed.
var ErrNotAFeed = errors.New("that page isn't a feed and doesn't link to one")

// ProbeFeed checks that rawURL is a feed and returns its title and URL.
// Given a web page instead (a blog's home page), it follows the page's
// <link rel="alternate"> to its RSS/Atom feed, as feed readers do.
func ProbeFeed(ctx context.Context, c *Client, rawURL string) (title, feedURL string, err error) {
	body, err := c.Get(ctx, rawURL)
	if err != nil {
		return "", "", err
	}
	f, err := ParseFeed(body, rawURL)
	if err != nil {
		found := discoverFeed(body, rawURL)
		if found == "" {
			return "", "", ErrNotAFeed
		}
		if body, err = c.Get(ctx, found); err != nil {
			return "", "", err
		}
		if f, err = ParseFeed(body, found); err != nil {
			return "", "", err
		}
		rawURL = found
	}
	if f.Title == "" {
		f.Title = rawURL
	}
	return f.Title, rawURL, nil
}

var (
	htmlLinkTag = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	htmlAttr    = regexp.MustCompile(`(?is)\b(rel|type|href)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

// discoverFeed returns the first RSS/Atom feed advertised by an HTML page.
func discoverFeed(page []byte, pageURL string) string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	for _, tag := range htmlLinkTag.FindAll(page, -1) {
		attrs := map[string]string{}
		for _, m := range htmlAttr.FindAllSubmatch(tag, -1) {
			attrs[strings.ToLower(string(m[1]))] = html.UnescapeString(string(m[2]) + string(m[3]) + string(m[4]))
		}
		typ := strings.ToLower(attrs["type"])
		if strings.Contains(strings.ToLower(attrs["rel"]), "alternate") &&
			(strings.Contains(typ, "rss+xml") || strings.Contains(typ, "atom+xml")) {
			if u := resolve(base, attrs["href"]); u != "" {
				return u
			}
		}
	}
	return ""
}

// ParsedFeed is the result of ParseFeed. Items have Source "rss", Type
// article and Meta initialized; URLs are absolute but not canonicalized.
type ParsedFeed struct {
	Title string
	Items []item.Item
}

type xmlLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// xmlText is an RSS/Atom text construct. Atom type="xhtml" carries markup
// as child elements; everything else carries (possibly escaped) text.
type xmlText struct {
	Type  string `xml:"type,attr"`
	Text  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

func (t xmlText) plain() string {
	if t.Type == "xhtml" {
		return item.PlainText(t.Inner)
	}
	return item.PlainText(t.Text)
}

type rssItem struct {
	Title xmlText   `xml:"title"`
	Links []xmlLink `xml:"link"`
	GUID  struct {
		Value       string `xml:",chardata"`
		IsPermaLink string `xml:"isPermaLink,attr"`
	} `xml:"guid"`
	Description xmlText  `xml:"description"`
	Content     xmlText  `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	PubDate     string   `xml:"pubDate"`
	DCDate      string   `xml:"http://purl.org/dc/elements/1.1/ date"`
	Creators    []string `xml:"http://purl.org/dc/elements/1.1/ creator"`
	Author      string   `xml:"author"`
	Categories  []string `xml:"category"`
}

type atomEntry struct {
	Title     xmlText   `xml:"title"`
	Links     []xmlLink `xml:"link"`
	ID        string    `xml:"id"`
	Published string    `xml:"published"`
	Updated   string    `xml:"updated"`
	Summary   xmlText   `xml:"summary"`
	Content   xmlText   `xml:"content"`
	Authors   []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Categories []struct {
		Term  string `xml:"term,attr"`
		Label string `xml:"label,attr"`
	} `xml:"category"`
}

type feedDoc struct {
	XMLName xml.Name
	Channel struct { // RSS 2.0
		Title xmlText   `xml:"title"`
		Links []xmlLink `xml:"link"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	Items   []rssItem   `xml:"item"` // RSS 1.0: items are siblings of <channel>
	Title   xmlText     `xml:"title"`
	Links   []xmlLink   `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

// ParseFeed parses an RSS 2.0, RSS 1.0 or Atom document. feedURL is used to
// resolve relative links.
func ParseFeed(body []byte, feedURL string) (ParsedFeed, error) {
	base, err := url.Parse(feedURL)
	if err != nil {
		return ParsedFeed{}, err
	}
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	dec.Strict = false // real-world feeds contain HTML entities and sloppy markup
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(label)
		if err != nil {
			return nil, fmt.Errorf("unsupported charset %q", label)
		}
		return enc.NewDecoder().Reader(in), nil
	}
	var doc feedDoc
	if err := dec.Decode(&doc); err != nil {
		return ParsedFeed{}, fmt.Errorf("decode feed: %w", err)
	}

	var out ParsedFeed
	switch strings.ToLower(doc.XMLName.Local) {
	case "rss", "rdf":
		out.Title = doc.Channel.Title.plain()
		if home := rssLink(doc.Channel.Links); home != "" {
			if u, err := base.Parse(home); err == nil {
				base = u
			}
		}
		for _, ri := range append(doc.Channel.Items, doc.Items...) {
			if it, ok := convertRSS(ri, base); ok {
				out.Items = append(out.Items, it)
			}
		}
	case "feed":
		out.Title = doc.Title.plain()
		for _, e := range doc.Entries {
			if it, ok := convertAtom(e, base); ok {
				out.Items = append(out.Items, it)
			}
		}
	default:
		return ParsedFeed{}, fmt.Errorf("not a feed: root element <%s>", doc.XMLName.Local)
	}
	return out, nil
}

// rssLink picks the RSS <link>text</link>, falling back to an atom:link
// alternate href (RSS feeds often also contain <atom:link rel="self">).
func rssLink(links []xmlLink) string {
	for _, l := range links {
		if s := strings.TrimSpace(l.Text); s != "" {
			return s
		}
	}
	return atomLink(links)
}

func atomLink(links []xmlLink) string {
	for _, l := range links {
		if l.Href != "" && (l.Rel == "" || l.Rel == "alternate") && (l.Type == "" || strings.Contains(l.Type, "html")) {
			return l.Href
		}
	}
	for _, l := range links {
		if l.Href != "" && l.Rel != "self" && l.Rel != "enclosure" {
			return l.Href
		}
	}
	return ""
}

func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}

func newFeedItem(title, link, summary string, published time.Time, authors, tags []string) item.Item {
	meta := map[string]string{}
	if u, err := url.Parse(link); err == nil {
		setIf(meta, "domain", strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
	}
	return item.Item{
		Type:      item.TypeArticle,
		Source:    "rss",
		Title:     title,
		URL:       link,
		Summary:   item.Truncate(summary, maxSummaryRunes),
		Authors:   authors,
		Tags:      tags,
		Meta:      meta,
		Published: published,
	}
}

func convertRSS(ri rssItem, base *url.URL) (item.Item, bool) {
	link := resolve(base, rssLink(ri.Links))
	if link == "" && !strings.EqualFold(ri.GUID.IsPermaLink, "false") {
		link = resolve(base, ri.GUID.Value)
	}
	title := ri.Title.plain()
	if link == "" || title == "" {
		return item.Item{}, false
	}
	summary := ri.Description.plain()
	if summary == "" {
		summary = ri.Content.plain()
	}
	published, _ := parseDate(ri.PubDate)
	if published.IsZero() {
		published, _ = parseDate(ri.DCDate)
	}
	authors := ri.Creators
	if a := rssAuthorName(ri.Author); a != "" {
		authors = append(authors, a)
	}
	return newFeedItem(title, link, summary, published, authors, ri.Categories), true
}

// rssAuthorName extracts a name from RSS <author>, which is specified as
// an email address, commonly "jane@example.com (Jane Doe)".
func rssAuthorName(s string) string {
	s = strings.TrimSpace(s)
	if i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')'); i >= 0 && j > i {
		return strings.TrimSpace(s[i+1 : j])
	}
	if strings.Contains(s, "@") {
		return ""
	}
	return s
}

func convertAtom(e atomEntry, base *url.URL) (item.Item, bool) {
	link := resolve(base, atomLink(e.Links))
	if link == "" {
		link = resolve(base, e.ID)
	}
	title := e.Title.plain()
	if link == "" || title == "" {
		return item.Item{}, false
	}
	summary := e.Summary.plain()
	if summary == "" {
		summary = e.Content.plain()
	}
	published, _ := parseDate(e.Published)
	if published.IsZero() {
		published, _ = parseDate(e.Updated)
	}
	var authors, tags []string
	for _, a := range e.Authors {
		authors = append(authors, a.Name)
	}
	for _, c := range e.Categories {
		if c.Label != "" {
			tags = append(tags, c.Label)
		} else {
			tags = append(tags, c.Term)
		}
	}
	return newFeedItem(title, link, summary, published, authors, tags), true
}

// dateLayouts covers RFC 822/1123 (RSS), RFC 3339 (Atom) and the common
// ways feeds get them wrong.
var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	time.RFC3339Nano,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 02 Jan 2006 15:04 -0700",
	"Mon, 2 January 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 MST",
	time.RFC822Z,
	time.RFC822,
	"2006-01-02T15:04:05-0700",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	// Some feeds use a wrong weekday or none at all; retry without it.
	if _, rest, ok := strings.Cut(s, ", "); ok {
		for _, layout := range dateLayouts {
			if _, l, ok := strings.Cut(layout, ", "); ok {
				if t, err := time.Parse(l, rest); err == nil {
					return t.UTC(), true
				}
			}
		}
	}
	return time.Time{}, false
}
