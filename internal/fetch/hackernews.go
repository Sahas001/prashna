package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sahas001/radar/internal/item"
)

// HNDefaultBaseURL is the Algolia HN Search API. Algolia (rather than the
// Firebase API) lets us filter by points and time in one paged query
// instead of fetching every story individually.
// Docs: https://hn.algolia.com/api
const HNDefaultBaseURL = "https://hn.algolia.com/api/v1"

// HackerNews fetches stories above a points threshold from the lookback
// window. HN has no abstracts: link posts get an empty summary (scored on
// title only); Ask/Show HN posts use their text body.
type HackerNews struct {
	Client     *Client
	BaseURL    string
	MinPoints  int
	Lookback   time.Duration
	MaxResults int
	Now        func() time.Time // for tests; defaults to time.Now
}

func (h *HackerNews) Name() string { return "hackernews" }

type algoliaResponse struct {
	Hits    []algoliaHit `json:"hits"`
	NbPages int          `json:"nbPages"`
}

type algoliaHit struct {
	ObjectID    string   `json:"objectID"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Author      string   `json:"author"`
	Points      int      `json:"points"`
	NumComments int      `json:"num_comments"`
	CreatedAtI  int64    `json:"created_at_i"`
	StoryText   string   `json:"story_text"`
	Tags        []string `json:"_tags"`
}

func (h *HackerNews) Fetch(ctx context.Context) ([]item.Item, error) {
	base := h.BaseURL
	if base == "" {
		base = HNDefaultBaseURL
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	since := now().Add(-h.Lookback).Unix()
	perPage := min(h.MaxResults, 1000)

	var items []item.Item
	for page := 0; len(items) < h.MaxResults; page++ {
		q := url.Values{
			"tags":           {"story"},
			"numericFilters": {fmt.Sprintf("created_at_i>%d,points>=%d", since, h.MinPoints)},
			"hitsPerPage":    {strconv.Itoa(perPage)},
			"page":           {strconv.Itoa(page)},
		}
		body, err := h.Client.Get(ctx, base+"/search_by_date?"+q.Encode())
		if err != nil {
			return nil, fmt.Errorf("hackernews: %w", err)
		}
		var resp algoliaResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("hackernews: decode page %d: %w", page, err)
		}
		for _, hit := range resp.Hits {
			if it, ok := hit.toItem(); ok {
				items = append(items, it)
			}
		}
		if len(resp.Hits) == 0 || page+1 >= resp.NbPages {
			break
		}
	}
	if len(items) > h.MaxResults {
		items = items[:h.MaxResults]
	}
	return items, nil
}

func (hit algoliaHit) toItem() (item.Item, bool) {
	if hit.ObjectID == "" || strings.TrimSpace(hit.Title) == "" {
		return item.Item{}, false
	}
	discussion := "https://news.ycombinator.com/item?id=" + hit.ObjectID
	link := hit.URL
	if link == "" {
		link = discussion // Ask HN / text posts
	}

	tags := []string{"hn"}
	for _, t := range hit.Tags {
		if t == "show_hn" || t == "ask_hn" {
			tags = append(tags, t)
		}
	}
	meta := map[string]string{
		"hn_id":       hit.ObjectID,
		"hn_url":      discussion,
		"hn_points":   strconv.Itoa(hit.Points),
		"hn_comments": strconv.Itoa(hit.NumComments),
	}
	setIf(meta, "hn_author", hit.Author)
	if u, err := url.Parse(link); err == nil {
		setIf(meta, "domain", strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."))
	}

	return item.Item{
		Type:      item.TypeNews,
		Source:    "hackernews",
		Title:     hit.Title,
		URL:       link,
		Summary:   item.PlainText(hit.StoryText),
		Tags:      tags,
		Meta:      meta,
		Published: time.Unix(hit.CreatedAtI, 0).UTC(),
	}, true
}
