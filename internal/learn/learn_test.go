package learn

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

func TestAUC(t *testing.T) {
	cases := []struct {
		s    []float64
		y    []bool
		want float64
	}{
		{[]float64{0.9, 0.8, 0.2, 0.1}, []bool{true, true, false, false}, 1},
		{[]float64{0.1, 0.2, 0.8, 0.9}, []bool{true, true, false, false}, 0},
		{[]float64{0.5, 0.5, 0.5, 0.5}, []bool{true, false, true, false}, 0.5},
		{[]float64{0.9, 0.3, 0.6, 0.1}, []bool{true, true, false, false}, 0.75},
		{[]float64{0.9, 0.1}, []bool{true, true}, 0.5}, // one class only
	}
	for _, c := range cases {
		if got := AUC(c.s, c.y); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("AUC(%v, %v) = %v, want %v", c.s, c.y, got, c.want)
		}
	}
}

func TestExtract(t *testing.T) {
	it := item.Item{
		Type: item.TypeArticle, Source: "rss", Title: "Raft consensus explained", Summary: "Leader election in Raft.",
		Tags: []string{"Distributed Systems"}, Authors: []string{"Jane Doe"},
		Meta:    map[string]string{"domain": "blog.example.com", "feed": "Jane's Blog", "hn_points": "999"},
		Signals: item.Signals{Keyword: 0.6, Similarity: 0.2, Recency: 0.9},
	}
	names := map[string]float64{}
	for _, f := range Extract(it) {
		names[f.Name] = f.Value
	}
	for _, want := range []string{"sig:keyword", "sig:similarity", "type:article", "source:rss", "domain:blog.example.com",
		"feed:jane's blog", "tag:distributed systems", "author:jane doe", "pop:hn", "w:raft", "w:leader"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing feature %q in %v", want, names)
		}
	}
	if _, ok := names["sig:recency"]; ok {
		t.Error("recency must not be a learned feature")
	}
	if names["w:raft"] <= names["w:leader"] {
		t.Errorf("title words should outweigh summary-only words: raft=%v leader=%v", names["w:raft"], names["w:leader"])
	}
	if v := names["pop:hn"]; v <= 0.9 || v > 1 {
		t.Errorf("pop:hn = %v", v)
	}
}

// synthetic builds rated items where the user likes Rust and systems
// posts from one blog and dislikes crypto hype, with noise in the heuristic.
func synthetic(n int, seed uint64) ([]Example, []float64) {
	r := rand.New(rand.NewPCG(seed, 1))
	likedTopics := []string{"rust borrow checker", "database internals", "kernel scheduling", "consensus raft"}
	dislikedTopics := []string{"crypto token launch", "nft marketplace", "growth hacking tips", "celebrity gossip"}
	var ex []Example
	var base []float64
	for i := range n {
		liked := i%3 != 0
		topics, domain := likedTopics, "good.example"
		if !liked {
			topics, domain = dislikedTopics, "hype.example"
		}
		if r.Float64() < 0.15 { // some cross-over noise
			domain = map[string]string{"good.example": "hype.example", "hype.example": "good.example"}[domain]
		}
		it := item.Item{
			Type: item.TypeArticle, Source: "rss",
			Title:   fmt.Sprintf("%s part %d", topics[r.IntN(len(topics))], i),
			Meta:    map[string]string{"domain": domain},
			Signals: item.Signals{Keyword: r.Float64() * 0.5, Similarity: r.Float64() * 0.3}, // heuristic is noise
		}
		ex = append(ex, Example{Features: Extract(it), Liked: liked})
		base = append(base, 0.6*it.Signals.Keyword+0.4*it.Signals.Similarity)
	}
	return ex, base
}

func TestTrainLearnsTaste(t *testing.T) {
	ex, _ := synthetic(60, 1)
	m := Train(ex, TrainOptions{})
	good := Extract(item.Item{Type: item.TypeArticle, Source: "rss", Title: "rust borrow checker deep dive", Meta: map[string]string{"domain": "good.example"}})
	bad := Extract(item.Item{Type: item.TypeArticle, Source: "rss", Title: "nft marketplace launch", Meta: map[string]string{"domain": "hype.example"}})
	pg, pb := m.Predict(good), m.Predict(bad)
	if pg < 0.7 || pb > 0.3 {
		t.Errorf("predictions: good=%.2f bad=%.2f", pg, pb)
	}
	why := m.Explain(good, 3)
	if len(why) == 0 || !slices.ContainsFunc(why, func(s string) bool { return s == "good.example" || s == "rust" || s == "borrow" || s == "checker" }) {
		t.Errorf("explanation = %v", why)
	}
	liked, disliked := m.Top(5)
	if len(liked) == 0 || len(disliked) == 0 {
		t.Fatalf("top weights: %v / %v", liked, disliked)
	}
	// Topic words separate the classes perfectly; the domain is flipped on
	// 15% of items, so words should dominate the top dislikes.
	if !slices.ContainsFunc(disliked, func(w Weight) bool { return strings.Contains(w.Label, "nft") || strings.Contains(w.Label, "crypto") }) {
		t.Errorf("disliked topic words should be among top dislikes: %v", disliked)
	}
	// Deterministic: same data, same model.
	if m2 := Train(ex, TrainOptions{}); m2.Predict(good) != pg {
		t.Error("training is not deterministic")
	}
}

func TestFitGating(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	cfg := Config{Enabled: true}

	ex, base := synthetic(10, 2)
	if m, r := Fit(ex, base, cfg, now); m != nil || r.Active || !strings.Contains(r.Status, "Rate 10 more") {
		t.Errorf("too few ratings: %+v", r)
	}
	allLiked := make([]Example, 30)
	for i := range allLiked {
		allLiked[i] = Example{Features: ex[i%len(ex)].Features, Liked: true}
	}
	if m, r := Fit(allLiked, make([]float64, 30), cfg, now); m != nil || !strings.Contains(r.Status, "👎") {
		t.Errorf("one-sided ratings: %+v", r)
	}

	ex, base = synthetic(90, 3)
	m, r := Fit(ex, base, cfg, now)
	if m == nil || !r.Active || r.ModelAUC < 0.85 || r.ModelAUC <= r.BaselineAUC {
		t.Fatalf("learnable data: %+v", r)
	}
	if want := 0.8 * 90 / 110.0; math.Abs(r.Weight-want) > 1e-9 {
		t.Errorf("weight = %v, want %v", r.Weight, want)
	}
	if r.Liked != 60 || r.Disliked != 30 || len(r.TopLiked) == 0 {
		t.Errorf("report = %+v", r)
	}

	// Labels unrelated to the features: the model must not switch on.
	rnd := rand.New(rand.NewPCG(9, 9))
	for i := range ex {
		ex[i].Liked = rnd.IntN(2) == 0
	}
	// Give the baseline genuine skill on these labels.
	for i := range base {
		base[i] = map[bool]float64{true: 0.6, false: 0.4}[ex[i].Liked] + rnd.Float64()*0.3
	}
	if m, r := Fit(ex, base, cfg, now); m != nil || r.Active || !strings.Contains(r.Status, "no better") {
		t.Errorf("noise labels: model used: %+v", r)
	}

	if m, r := Fit(ex, base, Config{}, now); m != nil || !strings.Contains(r.Status, "off") {
		t.Errorf("disabled: %+v", r)
	}
}

// BenchmarkFit1000 is a long-time user: 1000 ratings, training plus
// 5-fold cross-validation, as run on every rescore.
func BenchmarkFit1000(b *testing.B) {
	ex, base := synthetic(1000, 7)
	now := time.Now()
	for b.Loop() {
		Fit(ex, base, Config{Enabled: true}, now)
	}
}
