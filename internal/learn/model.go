package learn

import (
	"cmp"
	"math"
	"slices"
	"strings"
)

// Example is one rated item.
type Example struct {
	Features []Feature
	Liked    bool
}

type hashed struct {
	idx []uint32
	val []float64
}

func hash(fs []Feature) hashed {
	h := hashed{idx: make([]uint32, len(fs)), val: make([]float64, len(fs))}
	for i, f := range fs {
		h.idx[i], h.val[i] = index(f.Name), f.Value
	}
	return h
}

// Model is a trained logistic regression.
type Model struct {
	w      []float64
	bias   float64
	labels map[uint32]string // display labels of features seen in training
	names  map[uint32]string
}

// TrainOptions tunes the optimizer; zero values use sensible defaults.
type TrainOptions struct {
	Epochs       int     // default 300
	LearningRate float64 // AdaGrad step, default 0.5
	L2           float64 // default 0.01
}

func (o TrainOptions) withDefaults() TrainOptions {
	if o.Epochs <= 0 {
		o.Epochs = 300
	}
	if o.LearningRate <= 0 {
		o.LearningRate = 0.5
	}
	if o.L2 <= 0 {
		o.L2 = 0.01
	}
	return o
}

// Train fits the model with full-batch AdaGrad on class-balanced log
// loss plus L2 (bias unregularized). Full-batch keeps it deterministic:
// the same ratings always give the same model.
func Train(examples []Example, o TrainOptions) *Model {
	o = o.withDefaults()
	m := &Model{w: make([]float64, dims), labels: map[uint32]string{}, names: map[uint32]string{}}
	if len(examples) == 0 {
		return m
	}
	xs := make([]hashed, len(examples))
	var pos float64
	for i, ex := range examples {
		xs[i] = hash(ex.Features)
		for j, f := range ex.Features {
			m.labels[xs[i].idx[j]] = f.Label
			m.names[xs[i].idx[j]] = f.Name
		}
		if ex.Liked {
			pos++
		}
	}
	// Class weights so 40 likes and 4 dislikes count equally.
	n := float64(len(examples))
	wPos, wNeg := 1.0, 1.0
	if pos > 0 && pos < n {
		wPos, wNeg = n/(2*pos), n/(2*(n-pos))
	}

	// Only features that occur can have non-zero gradient from the data;
	// L2 alone drives the rest towards zero, where they already are.
	active := make([]uint32, 0, len(m.names))
	for idx := range m.names {
		active = append(active, idx)
	}
	slices.Sort(active)

	grad := make([]float64, dims)
	hist := make([]float64, dims)
	var biasHist float64
	const eps = 1e-8
	for range o.Epochs {
		for _, idx := range active {
			grad[idx] = o.L2 * m.w[idx]
		}
		var gBias float64
		for i, x := range xs {
			p := sigmoid(m.raw(x))
			y, cw := 0.0, wNeg
			if examples[i].Liked {
				y, cw = 1, wPos
			}
			g := cw * (p - y) / n
			gBias += g
			for j, idx := range x.idx {
				grad[idx] += g * x.val[j]
			}
		}
		for _, idx := range active {
			hist[idx] += grad[idx] * grad[idx]
			m.w[idx] -= o.LearningRate * grad[idx] / (math.Sqrt(hist[idx]) + eps)
		}
		biasHist += gBias * gBias
		m.bias -= o.LearningRate * gBias / (math.Sqrt(biasHist) + eps)
	}
	return m
}

func (m *Model) raw(x hashed) float64 {
	z := m.bias
	for j, idx := range x.idx {
		z += m.w[idx] * x.val[j]
	}
	return z
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// Predict returns the probability that the user likes an item with these features.
func (m *Model) Predict(fs []Feature) float64 { return sigmoid(m.raw(hash(fs))) }

// Explain returns the labels of up to k features that most increased this
// item's prediction, skipping the generic signal features.
func (m *Model) Explain(fs []Feature, k int) []string {
	type contrib struct {
		label string
		v     float64
	}
	var cs []contrib
	seen := map[string]bool{}
	for _, f := range fs {
		if strings.HasPrefix(f.Name, prefixSignal) || seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		if v := m.w[index(f.Name)] * f.Value; v > 0.05 {
			cs = append(cs, contrib{f.Label, v})
		}
	}
	slices.SortFunc(cs, func(a, b contrib) int { return cmp.Compare(b.v, a.v) })
	var out []string
	for _, c := range cs[:min(k, len(cs))] {
		out = append(out, c.label)
	}
	return out
}

// Weight is a learned feature weight, for reports.
type Weight struct {
	Label  string
	Weight float64
}

// Top returns the k most positive and k most negative non-signal features.
func (m *Model) Top(k int) (liked, disliked []Weight) {
	var ws []Weight
	for idx, name := range m.names {
		if strings.HasPrefix(name, prefixSignal) || m.w[idx] == 0 {
			continue
		}
		ws = append(ws, Weight{Label: describe(name, m.labels[idx]), Weight: m.w[idx]})
	}
	slices.SortFunc(ws, func(a, b Weight) int {
		return cmp.Or(cmp.Compare(b.Weight, a.Weight), cmp.Compare(a.Label, b.Label))
	})
	for _, w := range ws {
		if w.Weight > 0 && len(liked) < k {
			liked = append(liked, w)
		}
	}
	for i := len(ws) - 1; i >= 0 && len(disliked) < k; i-- {
		if ws[i].Weight < 0 {
			disliked = append(disliked, ws[i])
		}
	}
	return liked, disliked
}

// describe makes a feature readable in reports: "tag: cs.DC".
func describe(name, label string) string {
	kind, _, _ := strings.Cut(name, ":")
	switch kind {
	case "w":
		return "“" + label + "”"
	case "tag", "author", "domain", "feed", "source", "type":
		return kind + ": " + label
	}
	return label
}

// SignalWeights reports how much the model trusts the heuristic signals.
func (m *Model) SignalWeights() (keyword, similarity float64) {
	return m.w[index(prefixSignal+"keyword")], m.w[index(prefixSignal+"similarity")]
}
