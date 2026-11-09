package score

import (
	"strings"
	"unicode"
)

// Tokenize lowercases s, splits on anything that is not a letter or digit,
// drops stopwords and single characters, and applies a crude plural
// stemmer. The same function is used for documents and interest terms, so
// matching is consistent even where the stemming is linguistically wrong.
func Tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) < 2 || stopwords[f] {
			continue
		}
		out = append(out, stem(f))
	}
	return out
}

// stem strips English plural endings: "transformers" -> "transformer",
// "libraries" -> "library". Deliberately minimal; swap in a Porter stemmer
// if matching quality needs it.
func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") &&
		!strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:len(w)-1]
	}
	return w
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a about above after again against all also am an and any are as at be because been
		before being below between both but by can could did do does doing down during each
		few for from further had has have having he her here hers herself him himself his how
		however i if in into is it its itself just me more most my myself no nor not now of off
		on once only or other our ours ourselves out over own same she should so some such than
		that the their theirs them themselves then there these they this those through to too
		under until up us very via was we were what when where which while who whom why will
		with would you your yours yourself yourselves new using use used based show shows
		paper propose proposed present approach method methods results`) {
		m[w] = true
	}
	return m
}()
