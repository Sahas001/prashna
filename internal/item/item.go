// Package item defines Radar's normalized content model. Every fetcher
// converts its source format into Items; everything downstream (dedupe,
// scoring, storage, digests) only ever sees Items.
package item

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"html"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Type is the kind of content an Item represents.
type Type string

const (
	TypePaper   Type = "paper"
	TypeArticle Type = "article"
	TypeNews    Type = "news"
	TypeBook    Type = "book"
)

// Types lists every valid Type.
var Types = []Type{TypePaper, TypeArticle, TypeNews, TypeBook}

// Valid reports whether t is a known Type.
func (t Type) Valid() bool { return slices.Contains(Types, t) }

// rank orders types by how much information they usually carry. When two
// sources describe the same thing (an HN post linking an arXiv paper), the
// richer description wins.
func (t Type) rank() int {
	switch t {
	case TypePaper:
		return 4
	case TypeBook:
		return 3
	case TypeArticle:
		return 2
	case TypeNews:
		return 1
	}
	return 0
}

// Feedback is the user's explicit rating of an Item.
type Feedback int

const (
	Disliked Feedback = -1
	Neutral  Feedback = 0
	Liked    Feedback = 1
)

// Signals are the individual scoring components, kept so a digest can
// explain why something was recommended.
type Signals struct {
	Keyword    float64
	Similarity float64
	Recency    float64
	// Learned is the feedback-trained model's probability that the user
	// likes the item (0 when no model is active); LearnedWhy lists the
	// features that raised it most.
	Learned    float64
	LearnedWhy []string
}

// Item is one recommendable piece of content.
type Item struct {
	ID        string // first 16 hex chars of sha1(canonical URL)
	Type      Type
	Title     string
	URL       string // canonical URL
	Source    string // fetcher name: "arxiv", "hackernews", ...
	Summary   string // source-provided abstract/description, never generated
	Authors   []string
	Tags      []string
	Meta      map[string]string // source-specific extras, keys prefixed by source (hn_points, arxiv_id, ...)
	Published time.Time
	FetchedAt time.Time
	Score     float64
	Signals   Signals
	Feedback  Feedback
	SavedAt   time.Time // on the reading list since; zero = not saved
	ReadAt    time.Time // marked read; zero = unread
}

// Normalize cleans text fields, canonicalizes the URL and derives the ID.
// Fetchers fill raw fields; the pipeline calls Normalize before dedupe.
func (it *Item) Normalize() error {
	it.Title = CleanText(it.Title)
	it.Summary = CleanText(it.Summary)
	if it.Title == "" {
		return fmt.Errorf("item from %s (%s): empty title", it.Source, it.URL)
	}
	if !it.Type.Valid() {
		return fmt.Errorf("item %q: invalid type %q", it.Title, it.Type)
	}
	u, err := CanonicalURL(it.URL)
	if err != nil {
		return fmt.Errorf("item %q: %w", it.Title, err)
	}
	it.URL = u
	it.ID = IDFor(u)
	it.Tags = uniqFold(it.Tags)
	it.Authors = uniqFold(it.Authors)
	if it.FetchedAt.IsZero() {
		it.FetchedAt = time.Now()
	}
	if it.Published.IsZero() {
		it.Published = it.FetchedAt
	}
	it.FetchedAt = it.FetchedAt.UTC()
	it.Published = it.Published.UTC()
	return nil
}

// IDFor derives a stable Item ID from a canonical URL.
func IDFor(canonicalURL string) string {
	sum := sha1.Sum([]byte(canonicalURL))
	return hex.EncodeToString(sum[:8])
}

// TitleKey reduces a title to lowercase alphanumeric words separated by
// single spaces, so trivially different titles compare equal.
func TitleKey(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, " ")
}

// minDedupeWords guards title-based dedupe against generic titles
// ("Introduction", "Weekly update") that would merge unrelated items.
const minDedupeWords = 4

// DedupeKey returns the key used for cross-source dedupe, or "" when the
// item has no reliable identity beyond its URL.
//
// Books are keyed on main title (before any ":" subtitle) plus the first
// author's surname, because book titles are often a single word and the
// same book has different URLs on Open Library and Google Books. Other
// items are keyed on the full title, if it has enough words.
func DedupeKey(it Item) string {
	if it.Type == TypeBook {
		main, _, _ := strings.Cut(it.Title, ":")
		k := TitleKey(main)
		if k == "" || len(it.Authors) == 0 {
			return ""
		}
		names := strings.Fields(TitleKey(it.Authors[0]))
		if len(names) == 0 {
			return ""
		}
		return "book:" + k + "|" + names[len(names)-1]
	}
	k := TitleKey(it.Title)
	if strings.Count(k, " ")+1 < minDedupeWords {
		return ""
	}
	return k
}

// Truncate shortens s to at most n runes (plus an ellipsis), cutting at a
// word boundary when one is reasonably close.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndexByte(cut, ' '); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// MetaDatePrecision records how precise Published is ("year", "month",
// "day") for sources like book APIs that often only know the year.
const MetaDatePrecision = "date_precision"

var precisionRank = map[string]int{"year": 1, "month": 2, "": 3, "day": 3}

// finerDate reports whether b's Published is the same year as a's but
// more precise, e.g. Google Books "2026-10-01" vs Open Library "2026".
func finerDate(a, b Item) bool {
	return a.Published.Year() == b.Published.Year() &&
		precisionRank[b.Meta[MetaDatePrecision]] > precisionRank[a.Meta[MetaDatePrecision]]
}

// Merge folds a newly fetched duplicate into an existing Item. Identity
// (ID, URL) and user state (Feedback, Score, FetchedAt) always come from
// existing; descriptive fields come from whichever side is richer.
func Merge(existing, incoming Item) Item {
	out := existing
	if incoming.Type.rank() > existing.Type.rank() {
		out.Type = incoming.Type
		out.Title = incoming.Title
		out.Source = incoming.Source
	}
	if len(incoming.Summary) > len(out.Summary) {
		out.Summary = incoming.Summary
	}
	out.Tags = uniqFold(append(slices.Clone(existing.Tags), incoming.Tags...))
	out.Authors = uniqFold(append(slices.Clone(existing.Authors), incoming.Authors...))
	if len(existing.Meta)+len(incoming.Meta) > 0 {
		out.Meta = make(map[string]string, len(existing.Meta)+len(incoming.Meta))
		maps.Copy(out.Meta, existing.Meta)
		maps.Copy(out.Meta, incoming.Meta) // newer values (points, comments) win
	}
	// Earliest date wins (the original, not a reprint or revision), except
	// that a more precise date in the same year beats a year-only one.
	switch {
	case incoming.Published.IsZero():
		out.Meta = withMeta(out.Meta, MetaDatePrecision, existing.Meta[MetaDatePrecision])
	case out.Published.IsZero() || finerDate(existing, incoming) ||
		(incoming.Published.Before(out.Published) && !finerDate(incoming, existing)):
		out.Published = incoming.Published
		out.Meta = withMeta(out.Meta, MetaDatePrecision, incoming.Meta[MetaDatePrecision])
	default:
		out.Meta = withMeta(out.Meta, MetaDatePrecision, existing.Meta[MetaDatePrecision])
	}
	return out
}

// withMeta sets or (for "") deletes key in m, allocating m if needed.
func withMeta(m map[string]string, key, val string) map[string]string {
	if val == "" {
		delete(m, key)
		return m
	}
	if m == nil {
		m = map[string]string{}
	}
	m[key] = val
	return m
}

// CleanText collapses all runs of whitespace into single spaces.
func CleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// PlainText converts a small HTML fragment (HN story text, RSS
// descriptions) to plain text.
func PlainText(fragment string) string {
	return CleanText(html.UnescapeString(htmlTag.ReplaceAllString(fragment, " ")))
}

// uniqFold trims, drops empties and removes case-insensitive duplicates,
// keeping first occurrence order.
func uniqFold(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}
