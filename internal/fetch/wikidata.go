package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

// WikidataDefaultEndpoint is the Wikidata Query Service. It is free, needs
// no key, and asks clients to send a descriptive User-Agent.
// Docs: https://www.wikidata.org/wiki/Wikidata:SPARQL_query_service
const WikidataDefaultEndpoint = "https://query.wikidata.org/sparql"

// Wikidata finds books by followed authors with exact publication dates,
// which Open Library usually lacks (it knows first-publication years).
// Works that carry an Open Library ID link to Open Library, so both
// sources merge into one item.
type Wikidata struct {
	Client    *Client
	Endpoint  string
	Authors   []string
	PerAuthor int // default 25
}

func (w *Wikidata) Name() string { return "wikidata" }

// Book-like work types; individual stories are excluded below.
const wikidataQuery = `SELECT ?work ?workLabel ?workDescription ?date ?precision ?olid ?name WHERE {
  VALUES ?name { %s }
  ?author rdfs:label|skos:altLabel ?name ; wdt:P31 wd:Q5 .
  ?work wdt:P50 ?author ;
        p:P577/psv:P577 [ wikibase:timeValue ?date ; wikibase:timePrecision ?precision ] ;
        wdt:P31 ?type .
  VALUES ?type { wd:Q571 wd:Q7725634 wd:Q8261 wd:Q1279564 wd:Q47461344 wd:Q49848 wd:Q35760 }
  FILTER NOT EXISTS { ?work wdt:P31 wd:Q49084 }
  FILTER NOT EXISTS { ?work wdt:P31 wd:Q13442814 }
  FILTER NOT EXISTS { ?work wdt:P1433 ?publishedIn }
  FILTER NOT EXISTS { ?work wdt:P7937 ?form . VALUES ?form { wd:Q49084 wd:Q506099 } }
  OPTIONAL { ?work wdt:P648 ?olid }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "en". }
} ORDER BY DESC(?date) LIMIT %d`

type sparqlValue struct {
	Value string `json:"value"`
}

type sparqlResponse struct {
	Results struct {
		Bindings []map[string]sparqlValue `json:"bindings"`
	} `json:"results"`
}

var (
	olWorkID   = regexp.MustCompile(`^OL\d+W$`)
	wikidataID = regexp.MustCompile(`^Q\d+$`)
)

func (w *Wikidata) Fetch(ctx context.Context) ([]item.Item, error) {
	if len(w.Authors) == 0 {
		return nil, nil
	}
	per := w.PerAuthor
	if per <= 0 {
		per = 25
	}
	// Labels match exactly, so also try the name in title case ("ursula k.
	// le guin" -> "Ursula K. Le Guin"), and map both back to what the
	// reader typed.
	typed := map[string]string{}
	var values []string
	for _, a := range w.Authors {
		for _, v := range []string{strings.TrimSpace(a), titleCase(a)} {
			if _, dup := typed[v]; !dup && v != "" {
				typed[v] = a
				values = append(values, strconv.Quote(v)+"@en")
			}
		}
	}
	endpoint := w.Endpoint
	if endpoint == "" {
		endpoint = WikidataDefaultEndpoint
	}
	q := fmt.Sprintf(wikidataQuery, strings.Join(values, " "), per*len(w.Authors))
	body, err := w.Client.Do(ctx, endpoint+"?"+url.Values{"query": {q}, "format": {"json"}}.Encode(),
		map[string][]string{"Accept": {"application/sparql-results+json"}})
	if err != nil {
		return nil, fmt.Errorf("wikidata: %w", err)
	}
	var resp sparqlResponse
	if err := json.Unmarshal(body.Body, &resp); err != nil {
		return nil, fmt.Errorf("wikidata: decode: %w", err)
	}

	var items []item.Item
	seen := map[string]bool{}
	for _, b := range resp.Results.Bindings {
		title := b["workLabel"].Value
		qid := strings.TrimPrefix(b["work"].Value, "http://www.wikidata.org/entity/")
		if title == "" || wikidataID.MatchString(title) || seen[qid] || junkTitle.MatchString(title) {
			continue // unlabelled works show their QID as the label
		}
		seen[qid] = true
		published, err := time.Parse(time.RFC3339, b["date"].Value)
		if err != nil {
			continue
		}
		meta := map[string]string{"wikidata_id": qid, MetaFollowedAuthor: typed[b["name"].Value]}
		switch b["precision"].Value {
		case "11":
			meta[item.MetaDatePrecision] = "day"
		case "10":
			meta[item.MetaDatePrecision] = "month"
		default:
			meta[item.MetaDatePrecision] = "year"
		}
		link := "https://www.wikidata.org/wiki/" + qid
		if olid := b["olid"].Value; olWorkID.MatchString(olid) {
			meta["ol_key"] = "/works/" + olid
			link = OpenLibraryDefaultBaseURL + "/works/" + olid
		}
		items = append(items, item.Item{
			Type:      item.TypeBook,
			Source:    "wikidata",
			Title:     title,
			URL:       link,
			Summary:   b["workDescription"].Value,
			Authors:   []string{typed[b["name"].Value]},
			Meta:      meta,
			Published: published,
		})
	}
	return items, nil
}

// titleCase capitalizes each word, keeping initials like "K." intact.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
