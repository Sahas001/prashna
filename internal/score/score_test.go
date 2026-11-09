package score

import (
	"math"
	"slices"
	"testing"
	"time"

	"radar/internal/item"
)

func TestTokenize(t *testing.T) {
	got := Tokenize("The Transformers & Libraries of LLMs: a C survey")
	want := []string{"transformer", "library", "llm", "survey"}
	if !slices.Equal(got, want) {
		t.Errorf("Tokenize = %v, want %v", got, want)
	}
}

func TestKeyword(t *testing.T) {
	s := New(Config{Interests: []Interest{
		{Term: "distributed systems", Weight: 1},
		{Term: "cs.DC", Weight: 0.5},
		{Term: "cryptocurrency", Weight: -2},
	}})

	inTitle := s.Keyword(item.Item{Title: "Testing Distributed Systems at Scale"})
	inSummary := s.Keyword(item.Item{Title: "Jepsen", Summary: "notes on distributed systems"})
	both := s.Keyword(item.Item{Title: "Distributed systems", Summary: "a distributed system primer", Tags: []string{"cs.dc"}})
	none := s.Keyword(item.Item{Title: "Systems that are distributed"}) // words present, phrase not
	negative := s.Keyword(item.Item{Title: "Distributed systems for cryptocurrency"})

	if !(both > inTitle && inTitle > inSummary && inSummary > 0) {
		t.Errorf("ordering wrong: both=%.3f title=%.3f summary=%.3f", both, inTitle, inSummary)
	}
	if none != 0 {
		t.Errorf("non-phrase match scored %.3f", none)
	}
	if negative != 0 {
		t.Errorf("negative interest should cancel: %.3f", negative)
	}
	if math.Abs(inTitle-(1-math.Exp(-1))) > 1e-9 {
		t.Errorf("title hit = %.4f", inTitle)
	}
}

func TestMatchedTerms(t *testing.T) {
	s := New(Config{Interests: []Interest{
		{Term: "Distributed Systems", Weight: 1},
		{Term: "cs.DC", Weight: 0.5},
		{Term: "rust", Weight: 1},
		{Term: "blockchain", Weight: -1},
	}})
	got := s.MatchedTerms(item.Item{Title: "Blockchain distributed systems", Tags: []string{"cs.dc"}})
	if !slices.Equal(got, []string{"Distributed Systems", "cs.DC"}) {
		t.Errorf("MatchedTerms = %v", got)
	}
}

func TestRecency(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	s := New(Config{HalfLife: map[item.Type]time.Duration{item.TypeNews: 24 * time.Hour}})
	cases := []struct {
		it   item.Item
		want float64
	}{
		{item.Item{Type: item.TypeNews, Published: now}, 1},
		{item.Item{Type: item.TypeNews, Published: now.Add(time.Hour)}, 1},
		{item.Item{Type: item.TypeNews, Published: now.Add(-24 * time.Hour)}, 0.5},
		{item.Item{Type: item.TypeNews, Published: now.Add(-48 * time.Hour)}, 0.25},
		{item.Item{Type: item.TypePaper, Published: now.Add(-72 * time.Hour)}, 0.5}, // default half-life
	}
	for _, c := range cases {
		if got := s.Recency(c.it, now); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Recency(%s, %v) = %.4f, want %.4f", c.it.Type, now.Sub(c.it.Published), got, c.want)
		}
	}
}

func TestScoreSimilarity(t *testing.T) {
	now := time.Now()
	liked := []item.Item{{ID: "l1", Title: "Raft consensus under network partitions", Summary: "leader election and log replication in raft"}}
	cands := []item.Item{
		{ID: "a", Title: "A faster Raft: log replication with batching", Published: now},
		{ID: "b", Title: "Diffusion models for image synthesis", Published: now},
		{ID: "l1", Title: liked[0].Title, Summary: liked[0].Summary, Published: now},
	}
	s := New(Config{Weights: Weights{Keyword: 0.5, Similarity: 0.3, Recency: 0.2}})
	s.Score(cands, liked, now)

	if cands[0].Signals.Similarity <= cands[1].Signals.Similarity {
		t.Errorf("related item should be more similar: %.3f vs %.3f", cands[0].Signals.Similarity, cands[1].Signals.Similarity)
	}
	if cands[1].Signals.Similarity != 0 {
		t.Errorf("unrelated item similarity = %.3f", cands[1].Signals.Similarity)
	}
	if cands[2].Signals.Similarity != 0 {
		t.Errorf("liked item compared with itself: %.3f", cands[2].Signals.Similarity)
	}
	want := 0.3*cands[0].Signals.Similarity + 0.2*1
	if math.Abs(cands[0].Score-want) > 1e-9 {
		t.Errorf("total = %.4f, want %.4f", cands[0].Score, want)
	}
}

func TestScoreNoLikes(t *testing.T) {
	cands := []item.Item{{ID: "a", Title: "Anything"}}
	New(Config{Weights: Weights{Similarity: 1}}).Score(cands, nil, time.Now())
	if cands[0].Score != 0 {
		t.Errorf("score with no likes = %v", cands[0].Score)
	}
}

func TestAuthorMatches(t *testing.T) {
	s := New(Config{Interests: []Interest{{Term: "Ted Chiang", Weight: 1}}})
	book := item.Item{Type: item.TypeBook, Title: "Exhalation", Authors: []string{"Ted Chiang"}}
	if s.Keyword(book) == 0 || !slices.Equal(s.MatchedTerms(book), []string{"Ted Chiang"}) {
		t.Errorf("author interest not matched: kw=%v terms=%v", s.Keyword(book), s.MatchedTerms(book))
	}
}

func TestBlend(t *testing.T) {
	s := New(Config{Weights: Weights{Keyword: 0.5, Similarity: 0.3, Recency: 0.2}})
	it := item.Item{Signals: item.Signals{Keyword: 0.8, Similarity: 0.4, Recency: 0.5}}
	heuristic := 0.5*0.8 + 0.3*0.4 + 0.2*0.5
	if c := s.Content(it.Signals); math.Abs(c-(0.5*0.8+0.3*0.4)/0.8) > 1e-9 {
		t.Errorf("content = %v", c)
	}
	s.Blend(&it, 0.9, []string{"raft"}, 0)
	if math.Abs(it.Score-heuristic) > 1e-9 {
		t.Errorf("alpha 0 changed the score: %v vs %v", it.Score, heuristic)
	}
	s.Blend(&it, 0.9, []string{"raft"}, 1)
	if want := 0.8*0.9 + 0.2*0.5; math.Abs(it.Score-want) > 1e-9 || it.Signals.Learned != 0.9 || it.Signals.LearnedWhy[0] != "raft" {
		t.Errorf("alpha 1: score %v want %v, signals %+v", it.Score, want, it.Signals)
	}
}
