package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Sahas001/prashna/internal/item"
)

// SettingsKey is where Settings are stored in the database.
const SettingsKey = "app_settings"

// Settings are the options edited on the Settings page. They are stored
// in the database and override the config file.
type Settings struct {
	ArxivEnabled    bool     `json:"arxiv_enabled"`
	ArxivCategories []string `json:"arxiv_categories"`
	HNEnabled       bool     `json:"hn_enabled"`
	HNMinPoints     int      `json:"hn_min_points"`
	BooksEnabled    bool     `json:"books_enabled"`
	Papers          int      `json:"papers"` // items per edition section
	Articles        int      `json:"articles"`
	Discussions     int      `json:"discussions"`
	BooksWeekday    string   `json:"books_weekday"` // "" = every edition
	FetchEveryHours int      `json:"fetch_every_hours"`
	LearningEnabled bool     `json:"learning_enabled"`
	Theme           string   `json:"theme"` // "system" (default), "light" or "dark"
}

// Themes are the Appearance choices.
var Themes = []string{"system", "light", "dark"}

// Settings returns the current values of the app-editable options.
func (c Config) Settings() Settings {
	return Settings{
		ArxivEnabled:    c.Sources.ArXiv.Enabled,
		ArxivCategories: c.Sources.ArXiv.Categories,
		HNEnabled:       c.Sources.HackerNews.Enabled,
		HNMinPoints:     c.Sources.HackerNews.MinPoints,
		BooksEnabled:    c.Sources.Books.Enabled,
		Papers:          c.Edition.Sections[string(item.TypePaper)],
		Articles:        c.Edition.Sections[string(item.TypeArticle)],
		Discussions:     c.Edition.Sections[string(item.TypeNews)],
		BooksWeekday:    c.Edition.Books.Weekday,
		FetchEveryHours: int(c.FetchEvery.Hours()),
		LearningEnabled: c.Learning.Enabled,
		Theme:           "system",
	}
}

// With returns c with s applied.
func (c Config) With(s Settings) Config {
	c.Sources.ArXiv.Enabled = s.ArxivEnabled && len(s.ArxivCategories) > 0
	c.Sources.ArXiv.Categories = s.ArxivCategories
	c.Sources.HackerNews.Enabled = s.HNEnabled
	c.Sources.HackerNews.MinPoints = s.HNMinPoints
	c.Sources.Books.Enabled = s.BooksEnabled
	c.Edition.Sections = map[string]int{
		string(item.TypePaper):   s.Papers,
		string(item.TypeArticle): s.Articles,
		string(item.TypeNews):    s.Discussions,
	}
	c.Edition.Books.Weekday = s.BooksWeekday
	c.FetchEvery = time.Duration(s.FetchEveryHours) * time.Hour
	c.Learning.Enabled = s.LearningEnabled
	return c
}

var arxivCategory = regexp.MustCompile(`^[a-z-]+(\.[A-Za-z-]+)?$`)

// Normalize cleans user input and reports what is wrong with it.
func (s *Settings) Normalize() error {
	var errs []error
	var cats []string
	seen := map[string]bool{}
	for _, c := range s.ArxivCategories {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if !arxivCategory.MatchString(c) {
			errs = append(errs, fmt.Errorf("%q doesn't look like an arXiv category (e.g. cs.LG)", c))
			continue
		}
		seen[c] = true
		cats = append(cats, c)
	}
	s.ArxivCategories = cats
	clamp := func(v *int, lo, hi int, what string) {
		if *v < lo || *v > hi {
			errs = append(errs, fmt.Errorf("%s must be between %d and %d", what, lo, hi))
			*v = max(lo, min(hi, *v))
		}
	}
	clamp(&s.HNMinPoints, 1, 2000, "Hacker News points")
	clamp(&s.Papers, 0, 10, "Papers per edition")
	clamp(&s.Articles, 0, 10, "Articles per edition")
	clamp(&s.Discussions, 0, 10, "Discussions per edition")
	clamp(&s.FetchEveryHours, 1, 48, "Fetch interval")
	if _, _, err := (EditionBooks{Weekday: s.BooksWeekday}).ParseWeekday(); err != nil {
		errs = append(errs, err)
		s.BooksWeekday = "sunday"
	}
	s.BooksWeekday = strings.ToLower(s.BooksWeekday)
	if !slices.Contains(Themes, s.Theme) {
		s.Theme = "system" // unknown values are harmless: fall back quietly
	}
	return errors.Join(errs...)
}
