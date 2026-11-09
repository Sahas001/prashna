package score

import "math"

// vector is a sparse, L2-normalized TF-IDF vector.
type vector map[string]float64

// buildIDF computes smoothed inverse document frequencies:
// idf(t) = ln((1+N)/(1+df(t))) + 1.
func buildIDF(docs [][]string) map[string]float64 {
	df := make(map[string]int)
	for _, doc := range docs {
		seen := make(map[string]bool, len(doc))
		for _, t := range doc {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	n := float64(len(docs))
	idf := make(map[string]float64, len(df))
	for t, d := range df {
		idf[t] = math.Log((1+n)/(1+float64(d))) + 1
	}
	return idf
}

// tfidf builds a normalized vector with sublinear term frequency
// (1 + ln tf). Terms missing from idf are ignored.
func tfidf(tokens []string, idf map[string]float64) vector {
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	v := make(vector, len(tf))
	var norm float64
	for t, c := range tf {
		w, ok := idf[t]
		if !ok {
			continue
		}
		x := (1 + math.Log(float64(c))) * w
		v[t] = x
		norm += x * x
	}
	if norm == 0 {
		return v
	}
	norm = math.Sqrt(norm)
	for t := range v {
		v[t] /= norm
	}
	return v
}

// cosine of two normalized vectors is their dot product.
func cosine(a, b vector) float64 {
	if len(a) > len(b) {
		a, b = b, a
	}
	var dot float64
	for t, x := range a {
		dot += x * b[t]
	}
	return dot
}
