package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"radar/internal/item"
)

// MetaFollowedAuthor marks books found by querying a followed author; the
// digest's "new from authors you follow" section selects on it.
const MetaFollowedAuthor = "followed_author"

// MetaOLReaders is Open Library's want-to-read count, a popularity signal.
const MetaOLReaders = "ol_readers"

// BookQueries are the user's book interests, shared by both book fetchers.
type BookQueries struct {
	Authors  []string // followed authors: newest books
	Subjects []string // subjects: well-regarded books for "pick of the week"
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

// ---- Google Books ----

// GoogleBooksDefaultBaseURL is the Google Books API. Keyless requests are
// billed to a quota shared by every anonymous caller, which is routinely
// exhausted, so in practice an API key is needed (free, from Google Cloud).
// Docs: https://developers.google.com/books/docs/v1/using
const GoogleBooksDefaultBaseURL = "https://www.googleapis.com/books/v1"

// GoogleBooks finds the newest books by followed authors and the most
// relevant books per subject. It has good descriptions and exact
// publication dates, which makes it the main source for new releases.
type GoogleBooks struct {
	Client   *Client
	BaseURL  string
	APIKey   string
	Queries  BookQueries
	Language string // langRestrict, e.g. "en"; "" = any
	PerQuery int    // results per query (max 40), default 20
	Delay    time.Duration
}

func (g *GoogleBooks) Name() string { return "googlebooks" }

type gbResponse struct {
	Items []struct {
		ID         string `json:"id"`
		VolumeInfo struct {
			Title               string   `json:"title"`
			Subtitle            string   `json:"subtitle"`
			Authors             []string `json:"authors"`
			Publisher           string   `json:"publisher"`
			PublishedDate       string   `json:"publishedDate"`
			Description         string   `json:"description"`
			PageCount           int      `json:"pageCount"`
			Categories          []string `json:"categories"`
			Language            string   `json:"language"`
			IndustryIdentifiers []struct {
				Type       string `json:"type"`
				Identifier string `json:"identifier"`
			} `json:"industryIdentifiers"`
			ImageLinks struct {
				Thumbnail string `json:"thumbnail"`
			} `json:"imageLinks"`
		} `json:"volumeInfo"`
	} `json:"items"`
}

func (g *GoogleBooks) Fetch(ctx context.Context) ([]item.Item, error) {
	type query struct{ q, orderBy, author, subject string }
	var queries []query
	for _, a := range g.Queries.Authors {
		queries = append(queries, query{q: `inauthor:"` + a + `"`, orderBy: "newest", author: a})
	}
	for _, s := range g.Queries.Subjects {
		queries = append(queries, query{q: `subject:"` + s + `"`, orderBy: "relevance", subject: s})
	}

	var items []item.Item
	var errs []error
	for i, q := range queries {
		if err := paceRequests(ctx, i, g.Delay); err != nil {
			return items, err
		}
		got, err := g.query(ctx, q.q, q.orderBy)
		if IsStatus(err, http.StatusTooManyRequests) {
			// Out of quota: every remaining query would fail the same way.
			hint := "add an API key (sources.books.google_api_key_env)"
			if g.APIKey != "" {
				hint = "the API key's daily quota is used up"
			}
			errs = append(errs, fmt.Errorf("googlebooks: quota exceeded, skipping remaining %d queries; %s: %w",
				len(queries)-i-1, hint, err))
			break
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("googlebooks %s: %w", q.q, err))
			continue
		}
		for _, it := range got {
			if q.author != "" {
				if !matchesAuthor(it.Authors, q.author) {
					continue // co-mentions, other people with the name in metadata
				}
				it.Meta[MetaFollowedAuthor] = q.author
			}
			if q.subject != "" {
				it.Tags = append(it.Tags, q.subject)
			}
			items = append(items, it)
		}
	}
	return items, errors.Join(errs...)
}

func (g *GoogleBooks) query(ctx context.Context, q, orderBy string) ([]item.Item, error) {
	base := g.BaseURL
	if base == "" {
		base = GoogleBooksDefaultBaseURL
	}
	per := g.PerQuery
	if per <= 0 || per > 40 {
		per = 20
	}
	params := url.Values{
		"q":          {q},
		"orderBy":    {orderBy},
		"maxResults": {strconv.Itoa(per)},
		"printType":  {"books"},
	}
	if g.Language != "" {
		params.Set("langRestrict", g.Language)
	}
	if g.APIKey != "" {
		params.Set("key", g.APIKey)
	}
	body, err := g.Client.Get(ctx, base+"/volumes?"+params.Encode())
	if err != nil {
		return nil, err
	}
	var resp gbResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	var items []item.Item
	for _, v := range resp.Items {
		vi := v.VolumeInfo
		if v.ID == "" || vi.Title == "" || len(vi.Authors) == 0 || junkTitle.MatchString(vi.Title) {
			continue
		}
		title := vi.Title
		if vi.Subtitle != "" {
			title += ": " + vi.Subtitle
		}
		published, precision := parseBookDate(vi.PublishedDate)
		meta := map[string]string{"google_id": v.ID}
		setIf(meta, item.MetaDatePrecision, precision)
		setIf(meta, "publisher", vi.Publisher)
		if vi.PageCount > 0 {
			meta["pages"] = strconv.Itoa(vi.PageCount)
		}
		for _, id := range vi.IndustryIdentifiers {
			if id.Type == "ISBN_13" {
				meta["isbn"] = id.Identifier
			}
		}
		if t := vi.ImageLinks.Thumbnail; t != "" {
			t = strings.Replace(t, "http://", "https://", 1)
			meta["cover_url"] = strings.Replace(t, "&edge=curl", "", 1)
		}
		var tags []string
		for _, c := range vi.Categories {
			for part := range strings.SplitSeq(c, "/") {
				tags = append(tags, strings.TrimSpace(part))
			}
		}
		items = append(items, item.Item{
			Type:      item.TypeBook,
			Source:    "googlebooks",
			Title:     title,
			URL:       "https://books.google.com/books?id=" + url.QueryEscape(v.ID),
			Summary:   item.Truncate(item.PlainText(vi.Description), maxSummaryRunes),
			Authors:   vi.Authors,
			Tags:      tags,
			Meta:      meta,
			Published: published,
		})
	}
	return items, nil
}

// parseBookDate parses "2024", "2024-05" or "2024-05-14" and reports the
// precision. Unknown formats give a zero time.
func parseBookDate(s string) (time.Time, string) {
	s = strings.TrimSpace(s)
	var layout, precision string
	switch {
	case len(s) >= 10:
		s, layout, precision = s[:10], "2006-01-02", "day"
	case len(s) == 7:
		layout, precision = "2006-01", "month"
	case len(s) == 4:
		layout, precision = "2006", "year"
	default:
		return time.Time{}, ""
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, ""
	}
	return t, precision
}

// ---- Open Library ----

// OpenLibraryDefaultBaseURL is the Open Library API.
// Docs: https://openlibrary.org/developers/api
const OpenLibraryDefaultBaseURL = "https://openlibrary.org"

// OpenLibrary finds the most-read works per subject (by reading-log
// count: edition count favours public-domain classics and conference
// proceedings, the plain subject listing favours recent uploads) and
// recent works by followed authors. It has rich subject tags but no
// descriptions and only first-publication years.
type OpenLibrary struct {
	Client   *Client
	BaseURL  string
	Queries  BookQueries
	Language string // search language filter, e.g. "eng"; "" = any
	PerQuery int    // default 20
	Delay    time.Duration
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

func (o *OpenLibrary) Fetch(ctx context.Context) ([]item.Item, error) {
	base := o.BaseURL
	if base == "" {
		base = OpenLibraryDefaultBaseURL
	}
	per := o.PerQuery
	if per <= 0 {
		per = 20
	}

	type query struct {
		params          url.Values
		author, subject string
	}
	var queries []query
	for _, sub := range o.Queries.Subjects {
		queries = append(queries, query{params: url.Values{"subject": {sub}, "sort": {"readinglog"}}, subject: sub})
	}
	for _, a := range o.Queries.Authors {
		queries = append(queries, query{params: url.Values{"author": {a}, "sort": {"new"}}, author: a})
	}

	var items []item.Item
	var errs []error
	for i, q := range queries {
		if err := paceRequests(ctx, i, o.Delay); err != nil {
			return items, err
		}
		q.params.Set("limit", strconv.Itoa(per))
		q.params.Set("fields", olSearchFields)
		if o.Language != "" {
			q.params.Set("language", o.Language)
		}
		var resp struct {
			Docs []olWork `json:"docs"`
		}
		if err := o.getJSON(ctx, base+"/search.json?"+q.params.Encode(), &resp); err != nil {
			errs = append(errs, fmt.Errorf("openlibrary %s: %w", q.params.Encode(), err))
			continue
		}
		for _, w := range resp.Docs {
			it, ok := o.convert(base, w)
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
	return items, errors.Join(errs...)
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

func (o *OpenLibrary) convert(base string, w olWork) (item.Item, bool) {
	authors := w.AuthorName
	if !strings.HasPrefix(w.Key, "/works/") || w.Title == "" || len(authors) == 0 || junkTitle.MatchString(w.Title) {
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
	return item.Item{
		Type:      item.TypeBook,
		Source:    "openlibrary",
		Title:     title,
		URL:       strings.TrimRight(base, "/") + w.Key,
		Authors:   authors,
		Tags:      tags,
		Meta:      meta,
		Published: published,
	}, true
}
