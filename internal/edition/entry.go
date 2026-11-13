package edition

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Sahas001/prashna/internal/item"
)

// Entry is an item ready to display.
type Entry struct {
	Item       item.Item
	Byline     string
	Snippet    string
	Discussion string // HN thread, when it differs from the item URL
	Cover      string // book cover image URL
	Why        string
}

const (
	snippetRunes        = 320
	maxBylineAuthors    = 3
	similarityThreshold = 0.15
	popularHNPoints     = 300
	popularOLReaders    = 100  // want-to-read count
	learnedThreshold    = 0.65 // model like-probability worth mentioning
)

// NewEntry builds the display form of an item: byline, snippet, cover and
// the reasons it was recommended. Shared by every page that lists items.
func NewEntry(it item.Item, explain func(item.Item) []string) Entry {
	e := Entry{Item: it, Snippet: snippet(it.Summary), Cover: it.Meta["cover_url"]}
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
			if !slices.ContainsFunc(it.Authors, func(a string) bool { return strings.EqualFold(a, f) }) {
				by = append(by, f) // "Aphyr · Aphyr" otherwise
			}
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
	if it.Signals.Learned >= learnedThreshold && len(it.Signals.LearnedWhy) > 0 {
		why = append(why, "you tend to like "+strings.Join(it.Signals.LearnedWhy, ", "))
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

// snippet is the summary as shown under a title. Some feeds put only
// link furniture where a description should be (Lobsters' "Comments",
// Hacker News' "Article URL: ... Comments URL: ..."): better nothing.
func snippet(summary string) string {
	s := strings.TrimSpace(summary)
	if strings.EqualFold(s, "comments") || strings.HasPrefix(s, "Article URL:") || strings.HasPrefix(s, "Comments URL:") {
		return ""
	}
	return item.Truncate(s, snippetRunes)
}
