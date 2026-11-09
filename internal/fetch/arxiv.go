package fetch

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"radar/internal/item"
)

// ArXivDefaultBaseURL is the arXiv query API endpoint.
// Docs: https://info.arxiv.org/help/api/user-manual.html
const ArXivDefaultBaseURL = "https://export.arxiv.org/api/query"

// ArXiv fetches the newest submissions in a set of categories with a
// single API request. Abstracts become Item summaries.
type ArXiv struct {
	Client     *Client
	BaseURL    string
	Categories []string
	MaxResults int
}

func (a *ArXiv) Name() string { return "arxiv" }

func (a *ArXiv) Fetch(ctx context.Context) ([]item.Item, error) {
	if len(a.Categories) == 0 {
		return nil, errors.New("arxiv: no categories configured")
	}
	base := a.BaseURL
	if base == "" {
		base = ArXivDefaultBaseURL
	}
	terms := make([]string, len(a.Categories))
	for i, c := range a.Categories {
		terms[i] = "cat:" + c
	}
	q := url.Values{
		"search_query": {strings.Join(terms, " OR ")},
		"sortBy":       {"submittedDate"},
		"sortOrder":    {"descending"},
		"start":        {"0"},
		"max_results":  {strconv.Itoa(a.MaxResults)},
	}
	body, err := a.Client.Get(ctx, base+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	items, err := parseArXiv(body)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	return items, nil
}

type arxivFeed struct {
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	ID        string    `xml:"id"`
	Title     string    `xml:"title"`
	Summary   string    `xml:"summary"`
	Published time.Time `xml:"published"`
	Updated   time.Time `xml:"updated"`
	Authors   []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Links []struct {
		Href  string `xml:"href,attr"`
		Rel   string `xml:"rel,attr"`
		Title string `xml:"title,attr"`
	} `xml:"link"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
	PrimaryCategory struct {
		Term string `xml:"term,attr"`
	} `xml:"http://arxiv.org/schemas/atom primary_category"`
	Comment    string `xml:"http://arxiv.org/schemas/atom comment"`
	JournalRef string `xml:"http://arxiv.org/schemas/atom journal_ref"`
	DOI        string `xml:"http://arxiv.org/schemas/atom doi"`
}

var arxivVersion = regexp.MustCompile(`v\d+$`)

func parseArXiv(body []byte) ([]item.Item, error) {
	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("decode atom: %w", err)
	}
	items := make([]item.Item, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		// The API reports bad queries as a single entry with an errors id.
		if strings.Contains(e.ID, "/api/errors") {
			return nil, fmt.Errorf("api error: %s", item.CleanText(e.Summary))
		}

		absURL, pdfURL := e.ID, ""
		for _, l := range e.Links {
			switch {
			case l.Rel == "alternate":
				absURL = l.Href
			case l.Title == "pdf":
				pdfURL = l.Href
			}
		}
		arxivID := e.ID
		if i := strings.Index(arxivID, "/abs/"); i >= 0 {
			arxivID = arxivID[i+len("/abs/"):]
		}
		arxivID = arxivVersion.ReplaceAllString(arxivID, "")

		tags := make([]string, 0, len(e.Categories)+1)
		if e.PrimaryCategory.Term != "" {
			tags = append(tags, e.PrimaryCategory.Term)
		}
		for _, c := range e.Categories {
			tags = append(tags, c.Term)
		}
		authors := make([]string, 0, len(e.Authors))
		for _, au := range e.Authors {
			authors = append(authors, item.CleanText(au.Name))
		}

		meta := map[string]string{"arxiv_id": arxivID}
		setIf(meta, "pdf_url", pdfURL)
		setIf(meta, "arxiv_comment", item.CleanText(e.Comment))
		setIf(meta, "journal_ref", item.CleanText(e.JournalRef))
		setIf(meta, "doi", e.DOI)
		if !e.Updated.IsZero() && !e.Updated.Equal(e.Published) {
			meta["arxiv_updated"] = e.Updated.UTC().Format(time.RFC3339)
		}

		items = append(items, item.Item{
			Type:      item.TypePaper,
			Source:    "arxiv",
			Title:     e.Title,
			URL:       absURL,
			Summary:   e.Summary,
			Authors:   authors,
			Tags:      tags,
			Meta:      meta,
			Published: e.Published,
		})
	}
	return items, nil
}

func setIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}
