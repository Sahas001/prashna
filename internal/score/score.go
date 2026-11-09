// Package score ranks Items with classical, explainable signals:
//
//	keyword    weighted interest-term matches in title, summary and tags
//	similarity TF-IDF cosine similarity to the closest liked item
//	recency    exponential decay with a per-type half-life
//
// Total = Wk*keyword + Ws*similarity + Wr*recency. Every signal is in
// [0, 1], so with weights summing to 1 the total is too.
package score

import (
	"math"
	"strings"
	"time"

	"github.com/Sahas001/radar/internal/item"
)

type Weights struct {
	Keyword, Similarity, Recency float64
}

// Interest is one weighted term of the user's profile. Negative weights
// count against an item.
type Interest struct {
	Term   string
	Weight float64
}

type Config struct {
	Weights         Weights
	Interests       []Interest
	HalfLife        map[item.Type]time.Duration
	DefaultHalfLife time.Duration // used for types missing from HalfLife
}

type Scorer struct {
	cfg     Config
	terms   []term
	termSrc []int // terms[i] came from cfg.Interests[termSrc[i]]
}

type term struct {
	lower  string   // for exact tag matches ("cs.dc")
	tokens []string // for phrase matches in text
	weight float64
}

func New(cfg Config) *Scorer {
	if cfg.DefaultHalfLife <= 0 {
		cfg.DefaultHalfLife = 72 * time.Hour
	}
	s := &Scorer{cfg: cfg}
	for i, in := range cfg.Interests {
		lower := strings.ToLower(strings.TrimSpace(in.Term))
		if lower == "" || in.Weight == 0 {
			continue
		}
		s.terms = append(s.terms, term{lower: lower, tokens: Tokenize(in.Term), weight: in.Weight})
		s.termSrc = append(s.termSrc, i)
	}
	return s
}

// Match strengths per location. Additive, so a term in both title and
// abstract counts for more than either alone.
const (
	titleHit   = 1.0
	tagHit     = 0.8 // tag or author name
	summaryHit = 0.5
)

// Keyword scores it against the interest profile, squashed into [0, 1)
// with 1-exp(-x): one strong title hit ≈ 0.63, diminishing returns after.
func (s *Scorer) Keyword(it item.Item) float64 {
	if len(s.terms) == 0 {
		return 0
	}
	title, summary := Tokenize(it.Title), Tokenize(it.Summary)
	tags := exactLabels(it)
	var raw float64
	for _, t := range s.terms {
		var hit float64
		if tags[t.lower] {
			hit += tagHit
		}
		if containsPhrase(title, t.tokens) {
			hit += titleHit
		}
		if containsPhrase(summary, t.tokens) {
			hit += summaryHit
		}
		raw += t.weight * hit
	}
	if raw <= 0 {
		return 0
	}
	return 1 - math.Exp(-raw)
}

// MatchedTerms returns the positively weighted interest terms that match
// it, in profile order, for "why you're seeing this" explanations.
func (s *Scorer) MatchedTerms(it item.Item) []string {
	title, summary := Tokenize(it.Title), Tokenize(it.Summary)
	labels := exactLabels(it)
	var out []string
	for i, t := range s.terms {
		if t.weight <= 0 {
			continue
		}
		hit := containsPhrase(title, t.tokens) || containsPhrase(summary, t.tokens) || labels[t.lower]
		if hit {
			out = append(out, s.cfg.Interests[s.termSrc[i]].Term)
		}
	}
	return out
}

// exactLabels are matched against interest terms whole and
// case-insensitively: tags (arXiv categories, feed tags, book subjects)
// and author names, so following "Ted Chiang" or a researcher works.
func exactLabels(it item.Item) map[string]bool {
	m := make(map[string]bool, len(it.Tags)+len(it.Authors))
	for _, t := range it.Tags {
		m[strings.ToLower(strings.TrimSpace(t))] = true
	}
	for _, a := range it.Authors {
		m[strings.ToLower(strings.TrimSpace(a))] = true
	}
	return m
}

func containsPhrase(doc, phrase []string) bool {
	if len(phrase) == 0 || len(phrase) > len(doc) {
		return false
	}
outer:
	for i := 0; i+len(phrase) <= len(doc); i++ {
		for j, p := range phrase {
			if doc[i+j] != p {
				continue outer
			}
		}
		return true
	}
	return false
}

// Recency is 1 for items published now (or in the future) and halves every
// half-life for the item's type.
func (s *Scorer) Recency(it item.Item, now time.Time) float64 {
	age := now.Sub(it.Published)
	if age <= 0 {
		return 1
	}
	hl, ok := s.cfg.HalfLife[it.Type]
	if !ok || hl <= 0 {
		hl = s.cfg.DefaultHalfLife
	}
	return math.Exp2(-age.Hours() / hl.Hours())
}

// Score sets Signals and Score on every candidate in place. liked is the
// user's full set of 👍 items; the TF-IDF vocabulary is built over
// candidates ∪ liked. With no likes yet, similarity is 0 for everything
// and ranking falls back to keyword + recency.
func (s *Scorer) Score(candidates, liked []item.Item, now time.Time) {
	candTokens := make([][]string, len(candidates))
	docs := make([][]string, 0, len(candidates)+len(liked))
	inCands := make(map[string]bool, len(candidates))
	for i, it := range candidates {
		candTokens[i] = docTokens(it)
		docs = append(docs, candTokens[i])
		inCands[it.ID] = true
	}
	likedTokens := make([][]string, len(liked))
	for i, it := range liked {
		likedTokens[i] = docTokens(it)
		if !inCands[it.ID] {
			docs = append(docs, likedTokens[i])
		}
	}

	idf := buildIDF(docs)
	likedVecs := make([]vector, len(liked))
	for i := range liked {
		likedVecs[i] = tfidf(likedTokens[i], idf)
	}

	w := s.cfg.Weights
	for i := range candidates {
		it := &candidates[i]
		vec := tfidf(candTokens[i], idf)
		var best float64
		for j, lv := range likedVecs {
			if liked[j].ID == it.ID {
				continue // don't let a liked item vouch for itself
			}
			best = max(best, cosine(vec, lv))
		}
		it.Signals = item.Signals{
			Keyword:    s.Keyword(*it),
			Similarity: best,
			Recency:    s.Recency(*it, now),
		}
		it.Score = w.Keyword*it.Signals.Keyword + w.Similarity*it.Signals.Similarity + w.Recency*it.Signals.Recency
	}
}

// Content is the heuristic's judgement of the item itself: the keyword and
// similarity signals, weighted and normalized to [0, 1].
func (s *Scorer) Content(sig item.Signals) float64 {
	w := s.cfg.Weights
	if w.Keyword+w.Similarity == 0 {
		return 0
	}
	return (w.Keyword*sig.Keyword + w.Similarity*sig.Similarity) / (w.Keyword + w.Similarity)
}

// Blend mixes a learned like-probability p into an already scored item.
// The model replaces a share alpha of the content part; recency keeps its
// configured weight, so fresh items still rise. With alpha 0 the score is
// unchanged:
//
//	total = (Wk+Ws)·((1-α)·content + α·p) + Wr·recency
func (s *Scorer) Blend(it *item.Item, p float64, why []string, alpha float64) {
	w := s.cfg.Weights
	it.Signals.Learned, it.Signals.LearnedWhy = p, why
	content := (1-alpha)*s.Content(it.Signals) + alpha*p
	it.Score = (w.Keyword+w.Similarity)*content + w.Recency*it.Signals.Recency
}

// docTokens is the text an item is compared on. The title is repeated so
// it outweighs a long abstract; tags are included verbatim-ish.
func docTokens(it item.Item) []string {
	var b strings.Builder
	b.WriteString(it.Title)
	b.WriteByte(' ')
	b.WriteString(it.Title)
	b.WriteByte(' ')
	b.WriteString(it.Summary)
	for _, t := range it.Tags {
		b.WriteByte(' ')
		b.WriteString(t)
	}
	return Tokenize(b.String())
}
