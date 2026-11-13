// Package config holds Prashna's settings. Everything has a working
// default: a fresh install needs no config file at all. The settings
// people change day to day (sources, edition size, learning) are edited in
// the app and stored in the database (see Settings); config.yaml in the
// data folder is only for less common knobs.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Sahas001/prashna/internal/item"
)

type Config struct {
	DBPath     string        `yaml:"db_path"` // default: <data folder>/prashna.db
	UserAgent  string        `yaml:"user_agent"`
	Addr       string        `yaml:"addr"`        // where the app listens
	FetchEvery time.Duration `yaml:"fetch_every"` // how often sources are checked while Prashna runs
	Sources    Sources       `yaml:"sources"`
	Scoring    Scoring       `yaml:"scoring"`
	Edition    Edition       `yaml:"edition"`
	Learning   Learning      `yaml:"learning"`
	// PasswordEnv names an env var holding a password for the web UI. It is
	// only needed when Prashna listens beyond this computer (Addr not on
	// localhost); then the UI stays locked until it is set.
	PasswordEnv string `yaml:"password_env"`
	// Interests, feeds and book follows given here are imported once into
	// a new database; after that they are edited in the app.
	Interests []Interest `yaml:"interests"`
}

type Sources struct {
	ArXiv      ArXiv      `yaml:"arxiv"`
	HackerNews HackerNews `yaml:"hackernews"`
	RSS        RSS        `yaml:"rss"`
	Books      Books      `yaml:"books"`
}

// Every source accepts `every`: the minimum time between fetches of that
// source, however often the fetch job runs. 0 = every run.

type ArXiv struct {
	Enabled    bool          `yaml:"enabled"`
	Every      time.Duration `yaml:"every"`
	Categories []string      `yaml:"categories"`
	MaxResults int           `yaml:"max_results"`
}

type HackerNews struct {
	Enabled    bool          `yaml:"enabled"`
	Every      time.Duration `yaml:"every"`
	MinPoints  int           `yaml:"min_points"`
	Lookback   time.Duration `yaml:"lookback"`
	MaxResults int           `yaml:"max_results"`
}

type RSS struct {
	Enabled bool          `yaml:"enabled"`
	Every   time.Duration `yaml:"every"`
	// MaxAge skips posts published longer ago, so adding a feed doesn't
	// import its whole archive.
	MaxAge time.Duration `yaml:"max_age"`
	Feeds  []Feed        `yaml:"feeds"`
}

// Feed is a bare URL string or {url, name, tags, type}.
type Feed struct {
	URL  string   `yaml:"url"`
	Name string   `yaml:"name"`
	Tags []string `yaml:"tags"`
	Type string   `yaml:"type"` // article (default), news or paper
}

func (f *Feed) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		f.URL = n.Value
		return nil
	}
	type plain Feed
	return n.Decode((*plain)(f))
}

// Books come from Open Library and Wikidata, both free and keyless.
type Books struct {
	Enabled  bool          `yaml:"enabled"`
	Every    time.Duration `yaml:"every"`
	Authors  []string      `yaml:"authors"`  // followed authors: new releases
	Subjects []string      `yaml:"subjects"` // candidates for book of the week
	Language string        `yaml:"language"` // ISO 639-1, e.g. "en"; "" = any
	PerQuery int           `yaml:"per_query"`
}

// OpenLibraryLanguage maps Language to the MARC code Open Library's search
// filter uses ("en" -> "eng"); unknown codes disable the filter.
func (b Books) OpenLibraryLanguage() string {
	return map[string]string{
		"en": "eng", "fr": "fre", "de": "ger", "es": "spa", "it": "ita", "pt": "por",
		"nl": "dut", "ja": "jpn", "zh": "chi", "ru": "rus", "hi": "hin", "ne": "nep",
	}[strings.ToLower(b.Language)]
}

type Scoring struct {
	Weights  Weights                  `yaml:"weights"`
	HalfLife map[string]time.Duration `yaml:"half_life"`
	// Window is how far back (by fetch time) items are rescored.
	Window time.Duration `yaml:"window"`
}

type Weights struct {
	Keyword    float64 `yaml:"keyword"`
	Similarity float64 `yaml:"similarity"`
	Recency    float64 `yaml:"recency"`
}

// Edition configures the daily edition.
type Edition struct {
	// Sections maps item type (paper, article, news) to its number of items.
	Sections map[string]int `yaml:"sections"`
	MinScore float64        `yaml:"min_score"`
	// Lookback limits an edition to items fetched this recently.
	Lookback time.Duration `yaml:"lookback"`
	Books    EditionBooks  `yaml:"books"`
}

// EditionBooks configures the weekly books block.
type EditionBooks struct {
	Weekday          string        `yaml:"weekday"` // "sunday"; "" = every edition
	PickOfWeek       bool          `yaml:"pick_of_week"`
	NewReleases      int           `yaml:"new_releases"`
	NewReleaseWindow time.Duration `yaml:"new_release_window"`
}

// ParseWeekday returns the configured weekday; weekly is false for "".
func (b EditionBooks) ParseWeekday() (day time.Weekday, weekly bool, err error) {
	if b.Weekday == "" {
		return 0, false, nil
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if w := strings.ToLower(b.Weekday); w == name || w == name[:3] {
			return d, true, nil
		}
	}
	return 0, false, fmt.Errorf("edition.books.weekday %q: want a day name like sunday", b.Weekday)
}

// Learning configures the ranking model trained on your ratings.
type Learning struct {
	Enabled    bool    `yaml:"enabled"`
	MinRatings int     `yaml:"min_ratings"` // before the model is even evaluated
	MaxWeight  float64 `yaml:"max_weight"`  // cap on its share of the content score
}

// Interest is a weighted term. In YAML it can be a bare string (weight 1)
// or {term, weight}. Negative weights push matching items down.
type Interest struct {
	Term   string  `yaml:"term"`
	Weight float64 `yaml:"weight"`
}

func (in *Interest) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		in.Term, in.Weight = n.Value, 1
		return nil
	}
	type plain Interest
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*in = Interest(p)
	if in.Weight == 0 {
		in.Weight = 1
	}
	return nil
}

// DefaultAddr is where Prashna listens: this computer only.
const DefaultAddr = "127.0.0.1:7878"

// Default returns the configuration a fresh install runs with.
func Default() Config {
	return Config{
		UserAgent:   "Prashna/1.0 (personal reading app; +https://github.com/Sahas001/prashna)",
		Addr:        DefaultAddr,
		FetchEvery:  4 * time.Hour,
		PasswordEnv: "PRASHNA_PASSWORD",
		Sources: Sources{
			ArXiv: ArXiv{Enabled: false, MaxResults: 200},
			// Off until the reader picks a technical topic (or turns it on):
			// HN is noise for someone who came for novels and history.
			HackerNews: HackerNews{Enabled: false, MinPoints: 100, Lookback: 48 * time.Hour, MaxResults: 150},
			RSS:        RSS{Enabled: true, MaxAge: 30 * 24 * time.Hour},
			Books:      Books{Enabled: true, Every: 24 * time.Hour, Language: "en", PerQuery: 20},
		},
		Scoring: Scoring{
			Weights: Weights{Keyword: 0.5, Similarity: 0.3, Recency: 0.2},
			HalfLife: map[string]time.Duration{
				string(item.TypePaper):   72 * time.Hour,
				string(item.TypeArticle): 96 * time.Hour,
				string(item.TypeNews):    24 * time.Hour,
				// Long: the book pick is about books worth reading, not new
				// ones; new releases have their own section.
				string(item.TypeBook): 3 * 365 * 24 * time.Hour,
			},
			Window: 30 * 24 * time.Hour,
		},
		Edition: Edition{
			Sections: map[string]int{
				string(item.TypePaper):   3,
				string(item.TypeArticle): 4,
				string(item.TypeNews):    3,
			},
			MinScore: 0.05,
			Lookback: 7 * 24 * time.Hour,
			Books: EditionBooks{
				Weekday:          "sunday",
				PickOfWeek:       true,
				NewReleases:      4,
				NewReleaseWindow: 365 * 24 * time.Hour,
			},
		},
		Learning: Learning{Enabled: true, MinRatings: 20, MaxWeight: 0.8},
	}
}

// DefaultDBName is the database file inside the data folder.
const DefaultDBName = "prashna.db"

// DataDir is where Prashna keeps its database and optional config.yaml:
// $PRASHNA_HOME if set, else the OS's per-user config folder
// (~/.config/prashna, ~/Library/Application Support/Prashna, %AppData%\Prashna).
func DataDir() (string, error) {
	if d := os.Getenv("PRASHNA_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no per-user config folder (set PRASHNA_HOME): %w", err)
	}
	name := "prashna"
	if os.PathSeparator == '\\' || strings.Contains(base, "Library/Application Support") {
		name = "Prashna"
	}
	return filepath.Join(base, name), nil
}

// Load reads path over the defaults. A missing file is fine: the defaults
// are returned and missing is true. A relative DBPath is resolved against
// dataDir.
func Load(path, dataDir string) (cfg Config, missing bool, err error) {
	cfg = Default()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		missing = true
	case err != nil:
		return cfg, false, err
	case len(strings.TrimSpace(string(data))) > 0:
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return cfg, false, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if cfg.DBPath == "" {
		cfg.DBPath = DefaultDBName
	}
	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(dataDir, cfg.DBPath)
	}
	return cfg, missing, cfg.Validate()
}

// LocalOnly reports whether Addr only accepts connections from this computer.
func (c Config) LocalOnly() bool {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Password reads the web UI password from the configured env var.
func (c Config) Password() string {
	if c.PasswordEnv == "" {
		return ""
	}
	return os.Getenv(c.PasswordEnv)
}

func (c Config) Validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		errs = append(errs, fmt.Errorf("addr %q: want host:port", c.Addr))
	}
	if c.FetchEvery < 0 {
		errs = append(errs, errors.New("fetch_every must be >= 0"))
	}
	if a := c.Sources.ArXiv; a.Enabled && (a.MaxResults < 1 || a.MaxResults > 2000) {
		errs = append(errs, errors.New("sources.arxiv.max_results must be 1..2000"))
	}
	if h := c.Sources.HackerNews; h.Enabled {
		if h.Lookback <= 0 {
			errs = append(errs, errors.New("sources.hackernews.lookback must be positive"))
		}
		if h.MaxResults < 1 || h.MaxResults > 1000 {
			errs = append(errs, errors.New("sources.hackernews.max_results must be 1..1000"))
		}
	}
	for i, f := range c.Sources.RSS.Feeds {
		if !strings.HasPrefix(f.URL, "http://") && !strings.HasPrefix(f.URL, "https://") {
			errs = append(errs, fmt.Errorf("sources.rss.feeds[%d]: %q is not an http(s) URL", i, f.URL))
		}
		if f.Type != "" && !slices.Contains([]string{"article", "news", "paper"}, f.Type) {
			errs = append(errs, fmt.Errorf("sources.rss.feeds[%d].type %q: want article, news or paper", i, f.Type))
		}
	}
	if b := c.Sources.Books; b.PerQuery < 1 || b.PerQuery > 100 {
		errs = append(errs, errors.New("sources.books.per_query must be 1..100"))
	}
	w := c.Scoring.Weights
	if w.Keyword < 0 || w.Similarity < 0 || w.Recency < 0 {
		errs = append(errs, errors.New("scoring.weights must be non-negative"))
	}
	for k, d := range c.Scoring.HalfLife {
		if !item.Type(k).Valid() || d <= 0 {
			errs = append(errs, fmt.Errorf("scoring.half_life.%s: unknown type or non-positive", k))
		}
	}
	if c.Scoring.Window <= 0 {
		errs = append(errs, errors.New("scoring.window must be positive"))
	}
	for k, n := range c.Edition.Sections {
		if !slices.Contains([]string{"paper", "article", "news"}, k) || n < 0 {
			errs = append(errs, fmt.Errorf("edition.sections.%s: want paper, article or news with a count >= 0", k))
		}
	}
	if c.Edition.Lookback <= 0 {
		errs = append(errs, errors.New("edition.lookback must be positive"))
	}
	if _, _, err := c.Edition.Books.ParseWeekday(); err != nil {
		errs = append(errs, err)
	}
	if c.Learning.MinRatings < 10 {
		errs = append(errs, errors.New("learning.min_ratings must be at least 10"))
	}
	if c.Learning.MaxWeight <= 0 || c.Learning.MaxWeight > 1 {
		errs = append(errs, errors.New("learning.max_weight must be in (0, 1]"))
	}
	for i, in := range c.Interests {
		if strings.TrimSpace(in.Term) == "" {
			errs = append(errs, fmt.Errorf("interests[%d]: empty term", i))
		}
	}
	return errors.Join(errs...)
}

// HalfLives converts the half_life map to typed keys.
func (s Scoring) HalfLives() map[item.Type]time.Duration {
	out := make(map[item.Type]time.Duration, len(s.HalfLife))
	for k, v := range s.HalfLife {
		out[item.Type(k)] = v
	}
	return out
}
