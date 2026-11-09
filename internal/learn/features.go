// Package learn trains a per-user ranking model from 👍/👎 feedback:
// L2-regularized logistic regression over hashed sparse features, fitted
// with full-batch AdaGrad. It is small, deterministic and explainable,
// and needs no external services.
package learn

import (
	"hash/fnv"
	"math"
	"strconv"
	"strings"

	"github.com/Sahas001/radar/internal/item"
	"github.com/Sahas001/radar/internal/score"
)

// Feature is one named input. Name is the identity (hashed into the
// weight vector); Label is how it reads in explanations.
type Feature struct {
	Name  string
	Label string
	Value float64
}

// Features prefixes. Signal features carry the heuristic's own judgement
// so the model can learn how much to trust it.
const (
	prefixSignal = "sig:"
	prefixWord   = "w:"
)

// Extract turns an item (with Signals already computed) into features.
// Recency is deliberately excluded: items are rated while fresh, so
// recency at training time says nothing about taste, and the final score
// keeps its own recency term.
func Extract(it item.Item) []Feature {
	fs := []Feature{
		{Name: prefixSignal + "keyword", Label: "interest match", Value: it.Signals.Keyword},
		{Name: prefixSignal + "similarity", Label: "similar to liked", Value: it.Signals.Similarity},
		{Name: "type:" + string(it.Type), Label: string(it.Type) + "s", Value: 1},
		{Name: "source:" + it.Source, Label: it.Source, Value: 1},
	}
	add := func(prefix, label string, v float64) {
		label = strings.TrimSpace(label)
		if label != "" {
			fs = append(fs, Feature{Name: prefix + strings.ToLower(label), Label: label, Value: v})
		}
	}
	add("domain:", it.Meta["domain"], 1)
	add("feed:", it.Meta["feed"], 1)
	for _, t := range it.Tags {
		add("tag:", t, 1)
	}
	for _, a := range it.Authors {
		add("author:", a, 1)
	}
	// Popularity, log-scaled into roughly [0, 1].
	if n, err := strconv.Atoi(it.Meta["hn_points"]); err == nil && n > 0 {
		fs = append(fs, Feature{Name: "pop:hn", Label: "popular on HN", Value: math.Min(1, math.Log1p(float64(n))/math.Log(1000))})
	}
	if n, err := strconv.Atoi(it.Meta["ol_readers"]); err == nil && n > 0 {
		fs = append(fs, Feature{Name: "pop:openlibrary", Label: "popular on Open Library", Value: math.Min(1, math.Log1p(float64(n))/math.Log(5000))})
	}

	// Words: presence, title words count double, scaled so long abstracts
	// don't drown out the other features.
	weight := map[string]float64{}
	var order []string
	for _, part := range []struct {
		text string
		w    float64
	}{{it.Title, 1}, {it.Summary, 0.5}} {
		for _, tok := range score.Tokenize(part.text) {
			if _, seen := weight[tok]; !seen {
				order = append(order, tok)
			}
			weight[tok] = math.Max(weight[tok], part.w)
		}
	}
	if len(order) > 0 {
		norm := 1 / math.Sqrt(float64(len(order)))
		for _, tok := range order {
			fs = append(fs, Feature{Name: prefixWord + tok, Label: tok, Value: weight[tok] * norm * 2})
		}
	}
	return fs
}

// dims is the size of the hashed weight vector. Collisions are rare at
// personal scale (thousands of distinct features) and harmless.
const dims = 1 << 16

func index(name string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(name))
	return h.Sum32() % dims
}
