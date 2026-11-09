// Package digest selects the best unsent items into sections and renders
// them as an email (HTML + plain text) with signed 👍/👎 links.
package digest

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"slices"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode/utf8"

	"radar/internal/item"
	"radar/internal/links"
	"radar/internal/store"
)

// Source is the subset of the store the digest needs.
type Source interface {
	DigestCandidates(ctx context.Context, q store.DigestQuery) ([]item.Item, error)
}

type Options struct {
	Sections map[item.Type]int // max entries per section; 0/missing = omitted
	MinScore float64
	Lookback time.Duration
	Books    BookOptions
	// Explain optionally returns the interest terms an item matched.
	Explain func(item.Item) []string
}

// BookOptions controls the books block. The caller decides when it is
// due (weekly) and sets Include.
type BookOptions struct {
	Include          bool
	PickOfWeek       bool
	NewReleases      int
	NewReleaseWindow time.Duration
}

// MetaFollowedAuthor matches fetch.MetaFollowedAuthor (not imported, to
// keep digest independent of the fetchers).
const MetaFollowedAuthor = "followed_author"

type Digest struct {
	Date     time.Time
	Sections []Section
}

type Section struct {
	Type    item.Type
	Title   string
	Noun    string // singular, for the subject line: "paper", "new release"
	Entries []Entry
}

type Entry struct {
	Item       item.Item
	Byline     string
	Snippet    string
	Discussion string // HN thread, when it differs from the item URL
	Cover      string // book cover image URL
	Why        string
	Up, Down   string // feedback URLs, set by Render
}

// sectionOrder fixes the order of the per-type sections.
var sectionOrder = []struct {
	typ         item.Type
	title, noun string
}{
	{item.TypePaper, "Papers", "paper"},
	{item.TypeArticle, "Articles", "article"},
	{item.TypeNews, "Discussions", "discussion"},
}

const (
	snippetRunes        = 320
	maxBylineAuthors    = 3
	similarityThreshold = 0.15
	popularHNPoints     = 300
	popularOLReaders    = 100 // want-to-read count
)

// Build picks up to Sections[type] unsent, unrated items per section,
// fetched within Lookback and scoring at least MinScore, then the books
// block if Books.Include is set.
func Build(ctx context.Context, src Source, opts Options, now time.Time) (*Digest, error) {
	d := &Digest{Date: now}
	for _, s := range sectionOrder {
		limit := opts.Sections[s.typ]
		if limit <= 0 {
			continue
		}
		items, err := src.DigestCandidates(ctx, store.DigestQuery{
			Type: s.typ, Since: now.Add(-opts.Lookback), MinScore: opts.MinScore, Limit: limit,
		})
		if err != nil {
			return nil, fmt.Errorf("digest %s: %w", s.typ, err)
		}
		d.add(Section{Type: s.typ, Title: s.title, Noun: s.noun}, items, opts.Explain)
	}
	if opts.Books.Include {
		if err := d.addBooks(ctx, src, opts, now); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (d *Digest) add(sec Section, items []item.Item, explain func(item.Item) []string) {
	if len(items) == 0 {
		return
	}
	for _, it := range items {
		sec.Entries = append(sec.Entries, NewEntry(it, explain))
	}
	d.Sections = append(d.Sections, sec)
}

// addBooks adds "Book of the week" (the best unsent book of any age) and
// "New from authors you follow" (recent books by followed authors,
// regardless of score: you asked to follow them).
func (d *Digest) addBooks(ctx context.Context, src Source, opts Options, now time.Time) error {
	b := opts.Books
	var releases []item.Item
	if b.NewReleases > 0 {
		var err error
		releases, err = src.DigestCandidates(ctx, store.DigestQuery{
			Type: item.TypeBook, PublishedSince: now.Add(-b.NewReleaseWindow),
			// Require a known date: an undated book would otherwise look
			// published on the day it was fetched.
			MetaKeys: []string{MetaFollowedAuthor, item.MetaDatePrecision}, Limit: b.NewReleases,
		})
		if err != nil {
			return fmt.Errorf("digest new releases: %w", err)
		}
	}
	if b.PickOfWeek {
		cands, err := src.DigestCandidates(ctx, store.DigestQuery{
			Type: item.TypeBook, MinScore: opts.MinScore, Limit: len(releases) + 1,
		})
		if err != nil {
			return fmt.Errorf("digest book pick: %w", err)
		}
		for _, c := range cands {
			if !slices.ContainsFunc(releases, func(r item.Item) bool { return r.ID == c.ID }) {
				d.add(Section{Type: item.TypeBook, Title: "Book of the week", Noun: "book pick"}, []item.Item{c}, opts.Explain)
				break
			}
		}
	}
	d.add(Section{Type: item.TypeBook, Title: "New from authors you follow", Noun: "new release"}, releases, opts.Explain)
	return nil
}

// HasBooks reports whether the digest contains a books section.
func (d *Digest) HasBooks() bool {
	return slices.ContainsFunc(d.Sections, func(s Section) bool { return s.Type == item.TypeBook })
}

// NewEntry builds the display form of an item: byline, snippet, cover and
// the reasons it was recommended. Shared by the email and the web UI.
func NewEntry(it item.Item, explain func(item.Item) []string) Entry {
	e := Entry{Item: it, Snippet: item.Truncate(it.Summary, snippetRunes), Cover: it.Meta["cover_url"]}
	if hn := it.Meta["hn_url"]; hn != "" && hn != it.URL {
		e.Discussion = hn
	}

	var by []string
	if n := len(it.Authors); n > 0 {
		a := strings.Join(it.Authors[:min(n, maxBylineAuthors)], ", ")
		if n > maxBylineAuthors {
			a += " et al."
		}
		by = append(by, a)
	}
	switch it.Type {
	case item.TypePaper:
		if len(it.Tags) > 0 {
			by = append(by, it.Tags[0])
		}
	case item.TypeBook:
		if it.Meta[item.MetaDatePrecision] != "" { // otherwise Published is just the fetch date
			by = append(by, strconv.Itoa(it.Published.Year()))
		}
		if p := it.Meta["publisher"]; p != "" {
			by = append(by, p)
		}
		if p := it.Meta["pages"]; p != "" {
			by = append(by, p+" pages")
		}
	default:
		if f := it.Meta["feed"]; f != "" {
			by = append(by, f)
		} else if d := it.Meta["domain"]; d != "" {
			by = append(by, d)
		}
	}
	if p := it.Meta["hn_points"]; p != "" {
		by = append(by, p+" points", it.Meta["hn_comments"]+" comments")
	}
	e.Byline = strings.Join(by, " · ")

	var why []string
	if explain != nil {
		if terms := explain(it); len(terms) > 0 {
			why = append(why, "matches "+strings.Join(terms, ", "))
		}
	}
	if it.Signals.Similarity >= similarityThreshold {
		why = append(why, "similar to items you liked")
	}
	if p, _ := strconv.Atoi(it.Meta["hn_points"]); p >= popularHNPoints {
		why = append(why, "popular on HN")
	}
	if r, _ := strconv.Atoi(it.Meta["ol_readers"]); r >= popularOLReaders {
		why = append(why, "popular on Open Library")
	}
	e.Why = strings.Join(why, " · ")
	return e
}

// Count is the total number of entries.
func (d *Digest) Count() int {
	n := 0
	for _, s := range d.Sections {
		n += len(s.Entries)
	}
	return n
}

// IDs returns item IDs in display order.
func (d *Digest) IDs() []string {
	var ids []string
	for _, s := range d.Sections {
		for _, e := range s.Entries {
			ids = append(ids, e.Item.ID)
		}
	}
	return ids
}

// Subject is e.g. "Radar · Mon 9 Nov · 8 papers, 3 discussions, 1 book pick".
func (d *Digest) Subject() string {
	var parts []string
	for _, s := range d.Sections {
		noun := s.Noun
		if len(s.Entries) != 1 {
			noun += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", len(s.Entries), noun))
	}
	return "Radar · " + d.Date.Format("Mon 2 Jan") + " · " + strings.Join(parts, ", ")
}

//go:embed templates
var templateFS embed.FS

var (
	textFuncs = texttemplate.FuncMap{"wrap": wrap}
	htmlTmpl  = htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/email.html.tmpl"))
	textTmpl  = texttemplate.Must(texttemplate.New("email.txt.tmpl").Funcs(textFuncs).ParseFS(templateFS, "templates/email.txt.tmpl"))
)

type Rendered struct {
	Subject string
	HTML    string
	Text    string
}

// Render fills in feedback links and renders both email bodies.
func Render(d *Digest, signer *links.Signer) (Rendered, error) {
	for i := range d.Sections {
		for j := range d.Sections[i].Entries {
			e := &d.Sections[i].Entries[j]
			e.Up = signer.URL(e.Item.ID, links.Up)
			e.Down = signer.URL(e.Item.ID, links.Down)
		}
	}
	data := struct {
		*Digest
		Subject string
	}{d, d.Subject()}

	var h, t bytes.Buffer
	if err := htmlTmpl.Execute(&h, data); err != nil {
		return Rendered{}, fmt.Errorf("render html: %w", err)
	}
	if err := textTmpl.Execute(&t, data); err != nil {
		return Rendered{}, fmt.Errorf("render text: %w", err)
	}
	return Rendered{Subject: data.Subject, HTML: h.String(), Text: t.String()}, nil
}

// wrap word-wraps s to width columns, prefixing every line with indent.
func wrap(width int, indent, s string) string {
	var b strings.Builder
	line := 0
	for _, w := range strings.Fields(s) {
		n := utf8.RuneCountInString(w)
		if line > 0 && line+1+n > width {
			b.WriteByte('\n')
			line = 0
		}
		if line == 0 {
			b.WriteString(indent)
			line = utf8.RuneCountInString(indent)
		} else {
			b.WriteByte(' ')
			line++
		}
		b.WriteString(w)
		line += n
	}
	return b.String()
}
