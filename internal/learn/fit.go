package learn

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// AUC is the probability that a random liked item scores above a random
// disliked one (ties count half). 0.5 is chance; 1 is a perfect ranking.
func AUC(scores []float64, liked []bool) float64 {
	type pt struct {
		s float64
		y bool
	}
	pts := make([]pt, len(scores))
	var pos, neg float64
	for i := range scores {
		pts[i] = pt{scores[i], liked[i]}
		if liked[i] {
			pos++
		} else {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		return 0.5
	}
	slices.SortFunc(pts, func(a, b pt) int { return cmp.Compare(a.s, b.s) })
	// Sum of (mid-)ranks of positives (Mann-Whitney U).
	var rankSum float64
	for i := 0; i < len(pts); {
		j := i
		for j < len(pts) && pts[j].s == pts[i].s {
			j++
		}
		mid := float64(i+j+1) / 2 // average 1-based rank of the tie group
		for k := i; k < j; k++ {
			if pts[k].y {
				rankSum += mid
			}
		}
		i = j
	}
	return (rankSum - pos*(pos+1)/2) / (pos * neg)
}

// CrossValidate returns the pooled out-of-fold AUC of the model over k
// stratified folds. Fold assignment is deterministic.
func CrossValidate(examples []Example, k int, o TrainOptions) float64 {
	folds := make([]int, len(examples))
	var p, n int
	for i, ex := range examples {
		if ex.Liked {
			folds[i] = p % k
			p++
		} else {
			folds[i] = n % k
			n++
		}
	}
	preds := make([]float64, len(examples))
	labels := make([]bool, len(examples))
	for f := range k {
		var train []Example
		for i, ex := range examples {
			if folds[i] != f {
				train = append(train, ex)
			}
		}
		m := Train(train, o)
		for i, ex := range examples {
			if folds[i] == f {
				preds[i] = m.Predict(ex.Features)
			}
		}
	}
	for i, ex := range examples {
		labels[i] = ex.Liked
	}
	return AUC(preds, labels)
}

// Config controls when the learned model is used and how much.
type Config struct {
	Enabled     bool
	MinRatings  int     // total ratings before trying; default 20
	MinPerClass int     // likes and dislikes each; default 5
	MaxWeight   float64 // share of the content score at most; default 0.8
	Folds       int     // cross-validation folds; default 5
	Options     TrainOptions
}

func (c Config) withDefaults() Config {
	if c.MinRatings <= 0 {
		c.MinRatings = 20
	}
	if c.MinPerClass <= 0 {
		c.MinPerClass = 5
	}
	if c.MaxWeight <= 0 {
		c.MaxWeight = 0.8
	}
	if c.Folds <= 1 {
		c.Folds = 5
	}
	return c
}

// rampRatings sets how fast trust grows: at this many ratings the model
// gets half of MaxWeight. It can afford to be quick because the model is
// only used at all once cross-validation shows it beats the built-in score.
const rampRatings = 20

// ReportKey is the settings key under which the last Report is stored.
const ReportKey = "model_report"

// Report describes the last training run, for `radar model` and the UI.
type Report struct {
	TrainedAt    time.Time
	Ratings      int
	Liked        int
	Disliked     int
	ModelAUC     float64 // cross-validated
	BaselineAUC  float64 // built-in keyword+similarity score on the same ratings
	Active       bool
	Weight       float64 // share of the content score given to the model
	Status       string  // one-line explanation
	TopLiked     []Weight
	TopDisliked  []Weight
	TrustKeyword float64 // learned weight on the interest-match signal
	TrustSimilar float64 // learned weight on the similarity signal
}

// Fit decides whether there is enough signal to learn from, evaluates a
// model against the built-in ranking by cross-validation, and returns the
// model trained on all ratings when it wins. baseline[i] is the built-in
// content score of examples[i]. A nil model means "don't use one".
func Fit(examples []Example, baseline []float64, cfg Config, now time.Time) (*Model, Report) {
	cfg = cfg.withDefaults()
	r := Report{TrainedAt: now, Ratings: len(examples)}
	labels := make([]bool, len(examples))
	for i, ex := range examples {
		labels[i] = ex.Liked
		if ex.Liked {
			r.Liked++
		} else {
			r.Disliked++
		}
	}
	switch {
	case !cfg.Enabled:
		r.Status = "Learned ranking is turned off."
		return nil, r
	case r.Ratings < cfg.MinRatings:
		r.Status = fmt.Sprintf("Rate %d more items to start learning (%d of %d).", cfg.MinRatings-r.Ratings, r.Ratings, cfg.MinRatings)
		return nil, r
	case r.Liked < cfg.MinPerClass || r.Disliked < cfg.MinPerClass:
		r.Status = fmt.Sprintf("Needs at least %d 👍 and %d 👎 (have %d and %d).", cfg.MinPerClass, cfg.MinPerClass, r.Liked, r.Disliked)
		return nil, r
	}

	r.BaselineAUC = AUC(baseline, labels)
	r.ModelAUC = CrossValidate(examples, min(cfg.Folds, r.Liked, r.Disliked), cfg.Options)
	m := Train(examples, cfg.Options)
	r.TopLiked, r.TopDisliked = m.Top(8)
	r.TrustKeyword, r.TrustSimilar = m.SignalWeights()
	if r.ModelAUC <= r.BaselineAUC {
		r.Status = fmt.Sprintf("Not used yet: it ranks your ratings no better than the built-in score (%.2f vs %.2f AUC).", r.ModelAUC, r.BaselineAUC)
		return nil, r
	}
	n := float64(r.Ratings)
	r.Active = true
	r.Weight = cfg.MaxWeight * n / (n + rampRatings)
	r.Status = fmt.Sprintf("Active: %.0f%% of the content score, from %d ratings (%.2f vs %.2f AUC).", r.Weight*100, r.Ratings, r.ModelAUC, r.BaselineAUC)
	return m, r
}
