package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

// MetaFollowedAuthor marks books found by querying a followed author; the
// edition's "new from authors you follow" section selects on it.
const MetaFollowedAuthor = "followed_author"

// MetaOLReaders is Open Library's want-to-read count, a popularity signal.
const MetaOLReaders = "ol_readers"

// BookQueries are the reader's book interests, shared by the book fetchers.
type BookQueries struct {
	Authors  []string // followed authors: their books, newest first
	Subjects []string // subjects: well-loved books, candidates for book of the week
}

// junkTitle catches study guides and summaries that piggyback on popular
// books and authors.
var junkTitle = regexp.MustCompile(`(?i)\b(summary|study guide|workbook|sparknotes|cliffsnotes|analysis of|book companion|trivia|coloring book)\b`)

func matchesAuthor(authors []string, want string) bool {
	w := item.TitleKey(want)
	for _, a := range authors {
		if item.TitleKey(a) == w {
			return true
		}
	}
	return false
}

// paceRequests waits d between requests to the same API (skipped before the first).
func paceRequests(ctx context.Context, i int, d time.Duration) error {
	if i == 0 || d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// OpenLibraryDefaultBaseURL is the Open Library API. It is free and needs
// no key. Docs: https://openlibrary.org/developers/api
const OpenLibraryDefaultBaseURL = "https://openlibrary.org"

// OpenLibrary finds the most-read works per subject (by reading-log
// count: edition count favours public-domain classics and conference
// proceedings, the plain subject listing favours recent uploads) and
// recent works by followed authors. Search results carry subjects but no
// descriptions, so it also fetches descriptions for a few works per run.
type OpenLibrary struct {
	Client   *Client
	BaseURL  string
	Queries  BookQueries
	Language string // search language filter, e.g. "eng"; "" = any
	PerQuery int    // default 20
	Delay    time.Duration
	// Describe is how many descriptions to fetch per run (default 10).
	// Descriptions accumulate across runs because merging keeps the longer
	// summary, so this stays polite without leaving books blank for long.
	Describe int
	// NeedsSummary reports whether an item still lacks a description in
	// the database; nil means always fetch.
	NeedsSummary func(ctx context.Context, it item.Item) bool
}

func (o *OpenLibrary) Name() string { return "openlibrary" }

type olWork struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	FirstPublishYear int      `json:"first_publish_year"`
	Subject          []string `json:"subject"`
	AuthorName       []string `json:"author_name"`
	CoverI           int      `json:"cover_i"`
	Pages            int      `json:"number_of_pages_median"`
	WantToRead       int      `json:"want_to_read_count"`
}

const olSearchFields = "key,title,subtitle,author_name,first_publish_year,cover_i,subject,number_of_pages_median,want_to_read_count"

func (o *OpenLibrary) base() string {
	if o.BaseURL == "" {
		return OpenLibraryDefaultBaseURL
	}
	return strings.TrimRight(o.BaseURL, "/")
}

func (o *OpenLibrary) Fetch(ctx context.Context) ([]item.Item, error) {
	per := o.PerQuery
	if per <= 0 {
		per = 20
	}
	type query struct {
		params          url.Values
		author, subject string
	}
	var queries []query
	for _, a := range o.Queries.Authors {
		queries = append(queries, query{params: url.Values{"author": {a}, "sort": {"new"}}, author: a})
	}
	for _, sub := range o.Queries.Subjects {
		queries = append(queries, query{params: url.Values{"subject": {sub}, "sort": {"readinglog"}}, subject: sub})
	}

	var items []item.Item
	var errs []error
	requests := 0
	for _, q := range queries {
		if err := paceRequests(ctx, requests, o.Delay); err != nil {
			return items, err
		}
		requests++
		q.params.Set("limit", strconv.Itoa(per))
		q.params.Set("fields", olSearchFields)
		if o.Language != "" {
			q.params.Set("language", o.Language)
		}
		var resp struct {
			Docs []olWork `json:"docs"`
		}
		if err := o.getJSON(ctx, o.base()+"/search.json?"+q.params.Encode(), &resp); err != nil {
			errs = append(errs, fmt.Errorf("openlibrary %s: %w", q.params.Encode(), err))
			continue
		}
		for _, w := range resp.Docs {
			it, ok := o.convert(w)
			if !ok {
				continue
			}
			if q.author != "" {
				if !matchesAuthor(it.Authors, q.author) {
					continue
				}
				it.Meta[MetaFollowedAuthor] = q.author
			}
			if q.subject != "" {
				it.Tags = append(it.Tags, q.subject)
			}
			items = append(items, it)
		}
	}

	// Descriptions, in query order: followed authors first, then the most
	// read books per subject.
	limit := o.Describe
	if limit == 0 {
		limit = 10
	}
	described := map[string]bool{}
	for i := range items {
		if limit <= 0 || ctx.Err() != nil {
			break
		}
		key := items[i].Meta["ol_key"]
		if described[key] || (o.NeedsSummary != nil && !o.NeedsSummary(ctx, items[i])) {
			continue
		}
		described[key] = true
		if err := paceRequests(ctx, requests, o.Delay); err != nil {
			return items, err
		}
		requests++
		limit--
		desc, err := o.description(ctx, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("openlibrary %s: %w", key, err))
			continue
		}
		for j := range items { // the same work can come from several queries
			if items[j].Meta["ol_key"] == key {
				items[j].Summary = desc
			}
		}
	}
	return items, errors.Join(errs...)
}

// description fetches a work's description, which Open Library stores as
// either a string or {"type": ..., "value": ...}.
func (o *OpenLibrary) description(ctx context.Context, key string) (string, error) {
	var w struct {
		Description json.RawMessage `json:"description"`
	}
	if err := o.getJSON(ctx, o.base()+key+".json", &w); err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(w.Description, &s); err != nil {
		var v struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(w.Description, &v) == nil {
			s = v.Value
		}
	}
	return item.Truncate(cleanDescription(s), maxSummaryRunes), nil
}

// Open Library descriptions are markdown-ish and often end with source
// footnotes ("----------\n[1]: https://..."). Keep the prose.
var (
	olFootnotes = regexp.MustCompile(`(?s)\n\s*-{3,}.*$|\[\d+\]:\s*\S+`)
	olMDLink    = regexp.MustCompile(`\[([^\]]+)\]\[\d+\]|\[([^\]]+)\]\([^)]+\)`)
)

func cleanDescription(s string) string {
	s = olFootnotes.ReplaceAllString(s, "")
	s = olMDLink.ReplaceAllString(s, "$1$2")
	return item.PlainText(s)
}

func (o *OpenLibrary) getJSON(ctx context.Context, u string, v any) error {
	body, err := o.Client.Get(ctx, u)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// olJunkSubject drops Open Library's catalogue-housekeeping subjects.
var olJunkSubject = regexp.MustCompile(`(?i)accessible book|protected daisy|in library|lending library|large type|overdrive|open library|:`)

const maxBookTags = 12

func (o *OpenLibrary) convert(w olWork) (item.Item, bool) {
	if !strings.HasPrefix(w.Key, "/works/") || w.Title == "" || len(w.AuthorName) == 0 || junkTitle.MatchString(w.Title) {
		return item.Item{}, false
	}
	title := w.Title
	if w.Subtitle != "" {
		title += ": " + w.Subtitle
	}
	var tags []string
	for _, s := range w.Subject {
		if len(tags) == maxBookTags {
			break
		}
		if !olJunkSubject.MatchString(s) {
			tags = append(tags, s)
		}
	}
	meta := map[string]string{"ol_key": w.Key}
	var published time.Time
	if w.FirstPublishYear > 0 {
		published = time.Date(w.FirstPublishYear, 1, 1, 0, 0, 0, 0, time.UTC)
		meta[item.MetaDatePrecision] = "year"
	}
	if w.CoverI > 0 {
		meta["cover_url"] = "https://covers.openlibrary.org/b/id/" + strconv.Itoa(w.CoverI) + "-M.jpg"
	}
	if w.WantToRead > 0 {
		meta[MetaOLReaders] = strconv.Itoa(w.WantToRead)
	}
	if w.Pages > 0 {
		meta["pages"] = strconv.Itoa(w.Pages)
	}
	// Links always point at openlibrary.org (whatever BaseURL a test uses),
	// so Open Library and Wikidata results for the same work share an ID.
	return item.Item{
		Type:      item.TypeBook,
		Source:    "openlibrary",
		Title:     title,
		URL:       OpenLibraryDefaultBaseURL + w.Key,
		Authors:   w.AuthorName,
		Tags:      tags,
		Meta:      meta,
		Published: published,
	}, true
}
