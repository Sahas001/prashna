// Package config loads Radar's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"radar/internal/item"
)

type Config struct {
	DBPath    string     `yaml:"db_path"`
	UserAgent string     `yaml:"user_agent"`
	Sources   Sources    `yaml:"sources"`
	Scoring   Scoring    `yaml:"scoring"`
	Interests []Interest `yaml:"interests"`
	Digest    Digest     `yaml:"digest"`
	Email     Email      `yaml:"email"`
	Server    Server     `yaml:"server"`
	Schedule  Schedule   `yaml:"schedule"`
}

type Digest struct {
	// Sections maps item type to the max items shown in that section.
	// Sections render in fixed order (paper, article, news); a type that is
	// missing or 0 is left out. Books have their own weekly block below.
	Sections map[string]int `yaml:"sections"`
	MinScore float64        `yaml:"min_score"`
	// Lookback limits a digest to items fetched this recently that have
	// not been in a previous digest.
	Lookback time.Duration `yaml:"lookback"`
	Books    DigestBooks   `yaml:"books"`
}

// DigestBooks configures the weekly books block: one "pick of the week"
// plus recent releases by followed authors.
type DigestBooks struct {
	// Weekday the books block appears on ("sunday"); "" = every digest.
	// If that day's digest is missed, books go out in the next one.
	Weekday          string        `yaml:"weekday"`
	PickOfWeek       bool          `yaml:"pick_of_week"`
	NewReleases      int           `yaml:"new_releases"`       // max new releases shown; 0 = none
	NewReleaseWindow time.Duration `yaml:"new_release_window"` // "new" = published within this window
}

// ParseWeekday returns the configured weekday; ok is false for "" (every digest).
func (b DigestBooks) ParseWeekday() (time.Weekday, bool, error) {
	if b.Weekday == "" {
		return 0, false, nil
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if w := strings.ToLower(b.Weekday); w == name || w == name[:3] {
			return d, true, nil
		}
	}
	return 0, false, fmt.Errorf("digest.books.weekday %q: want a day name like sunday", b.Weekday)
}

type Email struct {
	SMTPHost string `yaml:"smtp_host"`
	SMTPPort int    `yaml:"smtp_port"`
	// TLS is "starttls" (port 587), "tls" (implicit TLS, port 465) or
	// "none" (local relay only).
	TLS      string `yaml:"tls"`
	Username string `yaml:"username"`
	// PasswordEnv names the environment variable holding the SMTP
	// password, so the secret never lives in the config file.
	PasswordEnv string   `yaml:"password_env"`
	From        string   `yaml:"from"`
	To          []string `yaml:"to"`
}

type Server struct {
	Addr string `yaml:"addr"`
	// BaseURL is the externally reachable URL of `radar serve`; feedback
	// links in emails point here.
	BaseURL string `yaml:"base_url"`
	// UIAuth is "password" (default) or "none" for networks that already
	// restrict access (Tailscale, LAN, an authenticating proxy).
	UIAuth string `yaml:"ui_auth"`
	// PasswordEnv names the env var holding the web UI password.
	PasswordEnv string `yaml:"password_env"`
}

// UIPassword reads the web UI password from the configured env var.
func (s Server) UIPassword() string {
	if s.PasswordEnv == "" {
		return ""
	}
	return os.Getenv(s.PasswordEnv)
}

type Schedule struct {
	FetchEvery time.Duration `yaml:"fetch_every"` // 0 disables scheduled fetching
	DigestAt   string        `yaml:"digest_at"`   // "HH:MM"; "" disables the scheduled digest
	Timezone   string        `yaml:"timezone"`    // IANA name; "" = server local time
}

// DigestClock parses DigestAt and Timezone. ok is false when the scheduled
// digest is disabled.
func (s Schedule) DigestClock() (hour, minute int, loc *time.Location, ok bool, err error) {
	loc = time.Local
	if s.Timezone != "" {
		if loc, err = time.LoadLocation(s.Timezone); err != nil {
			return 0, 0, nil, false, fmt.Errorf("schedule.timezone: %w", err)
		}
	}
	if s.DigestAt == "" {
		return 0, 0, loc, false, nil
	}
	t, err := time.Parse("15:04", s.DigestAt)
	if err != nil {
		return 0, 0, nil, false, fmt.Errorf("schedule.digest_at %q: want HH:MM", s.DigestAt)
	}
	return t.Hour(), t.Minute(), loc, true, nil
}

// Password reads the SMTP password from the configured environment variable.
func (e Email) Password() string {
	if e.PasswordEnv == "" {
		return ""
	}
	return os.Getenv(e.PasswordEnv)
}

// ValidateForSending checks the fields needed to actually send mail. It is
// separate from Validate so fetching and previews work without SMTP set up.
func (e Email) ValidateForSending() error {
	var errs []error
	if e.SMTPHost == "" {
		errs = append(errs, errors.New("email.smtp_host is empty"))
	}
	if e.From == "" {
		errs = append(errs, errors.New("email.from is empty"))
	}
	if len(e.To) == 0 {
		errs = append(errs, errors.New("email.to is empty"))
	}
	if e.Username != "" && e.Password() == "" {
		errs = append(errs, fmt.Errorf("email.username is set but $%s is empty", e.PasswordEnv))
	}
	return errors.Join(errs...)
}

type Sources struct {
	ArXiv      ArXiv      `yaml:"arxiv"`
	HackerNews HackerNews `yaml:"hackernews"`
	RSS        RSS        `yaml:"rss"`
	Books      Books      `yaml:"books"`
}

// Every source accepts `every`: the minimum time between fetches of that
// source, however often the fetch job runs. 0 = every run.

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

type Books struct {
	Enabled  bool          `yaml:"enabled"`
	Every    time.Duration `yaml:"every"`
	Authors  []string      `yaml:"authors"`  // followed authors: new releases
	Subjects []string      `yaml:"subjects"` // candidates for pick of the week
	Language string        `yaml:"language"` // ISO 639-1, e.g. "en"; "" = any
	PerQuery int           `yaml:"per_query"`
	// GoogleAPIKeyEnv optionally names an env var with a Google Books API
	// key; without one, requests use the shared anonymous quota.
	GoogleAPIKeyEnv string `yaml:"google_api_key_env"`
	OpenLibrary     bool   `yaml:"openlibrary"`
	GoogleBooks     bool   `yaml:"googlebooks"`
}

// OpenLibraryLanguage maps Language to the MARC code Open Library's search
// filter uses ("en" -> "eng"); unknown codes disable the filter.
func (b Books) OpenLibraryLanguage() string {
	return map[string]string{
		"en": "eng", "fr": "fre", "de": "ger", "es": "spa", "it": "ita", "pt": "por",
		"nl": "dut", "ja": "jpn", "zh": "chi", "ru": "rus", "hi": "hin", "ne": "nep",
	}[strings.ToLower(b.Language)]
}

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

type Scoring struct {
	Weights  Weights                  `yaml:"weights"`
	HalfLife map[string]time.Duration `yaml:"half_life"`
	// Window is how far back (by fetch time) items are rescored and
	// considered for digests.
	Window time.Duration `yaml:"window"`
}

type Weights struct {
	Keyword    float64 `yaml:"keyword"`
	Similarity float64 `yaml:"similarity"`
	Recency    float64 `yaml:"recency"`
}

// Interest is a weighted term in the user's profile. In YAML it can be a
// bare string (weight 1) or {term, weight}. Negative weights push matching
// items down.
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

// Default returns a working configuration; Load overlays the file on top.
func Default() Config {
	return Config{
		DBPath:    "radar.db",
		UserAgent: "radar/0.1 (self-hosted reading recommender)",
		Sources: Sources{
			ArXiv: ArXiv{
				Enabled:    true,
				Categories: []string{"cs.LG", "cs.CL", "cs.DC"},
				MaxResults: 300,
			},
			HackerNews: HackerNews{
				Enabled:    true,
				MinPoints:  50,
				Lookback:   48 * time.Hour,
				MaxResults: 200,
			},
			RSS: RSS{
				Enabled: true,
				MaxAge:  30 * 24 * time.Hour,
			},
			Books: Books{
				Enabled:         true,
				Every:           24 * time.Hour,
				Language:        "en",
				PerQuery:        20,
				GoogleAPIKeyEnv: "RADAR_GOOGLE_BOOKS_KEY",
				OpenLibrary:     true,
				GoogleBooks:     true,
			},
		},
		Scoring: Scoring{
			Weights: Weights{Keyword: 0.5, Similarity: 0.3, Recency: 0.2},
			HalfLife: map[string]time.Duration{
				string(item.TypePaper):   72 * time.Hour,
				string(item.TypeArticle): 72 * time.Hour,
				string(item.TypeNews):    24 * time.Hour,
				// Long: the book pick is about books worth reading, not new
				// ones; new releases have their own section.
				string(item.TypeBook): 3 * 365 * 24 * time.Hour,
			},
			Window: 30 * 24 * time.Hour,
		},
		Digest: Digest{
			Sections: map[string]int{
				string(item.TypePaper):   8,
				string(item.TypeArticle): 8,
				string(item.TypeNews):    8,
			},
			MinScore: 0.1,
			Lookback: 7 * 24 * time.Hour,
			Books: DigestBooks{
				Weekday:          "sunday",
				PickOfWeek:       true,
				NewReleases:      5,
				NewReleaseWindow: 180 * 24 * time.Hour,
			},
		},
		Email: Email{
			SMTPPort:    587,
			TLS:         "starttls",
			PasswordEnv: "RADAR_SMTP_PASSWORD",
		},
		Server: Server{
			Addr:        "127.0.0.1:8080",
			BaseURL:     "http://localhost:8080",
			UIAuth:      "password",
			PasswordEnv: "RADAR_UI_PASSWORD",
		},
		Schedule: Schedule{
			FetchEvery: 6 * time.Hour,
			DigestAt:   "07:00",
		},
	}
}

// Load reads path over the defaults. A missing file is not an error: the
// defaults are returned and missing is set to true.
func Load(path string) (cfg Config, missing bool, err error) {
	cfg = Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, true, cfg.Validate()
	}
	if err != nil {
		return cfg, false, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, false, cfg.Validate()
}

func (c Config) Validate() error {
	var errs []error
	if c.DBPath == "" {
		errs = append(errs, errors.New("db_path is empty"))
	}
	if a := c.Sources.ArXiv; a.Enabled {
		if len(a.Categories) == 0 {
			errs = append(errs, errors.New("sources.arxiv.categories is empty"))
		}
		if a.MaxResults < 1 || a.MaxResults > 2000 {
			errs = append(errs, errors.New("sources.arxiv.max_results must be 1..2000"))
		}
	}
	if h := c.Sources.HackerNews; h.Enabled {
		if h.Lookback <= 0 {
			errs = append(errs, errors.New("sources.hackernews.lookback must be positive"))
		}
		if h.MaxResults < 1 || h.MaxResults > 1000 {
			errs = append(errs, errors.New("sources.hackernews.max_results must be 1..1000"))
		}
	}
	w := c.Scoring.Weights
	if w.Keyword < 0 || w.Similarity < 0 || w.Recency < 0 {
		errs = append(errs, errors.New("scoring.weights must be non-negative"))
	}
	for k, d := range c.Scoring.HalfLife {
		if !item.Type(k).Valid() {
			errs = append(errs, fmt.Errorf("scoring.half_life: unknown type %q", k))
		}
		if d <= 0 {
			errs = append(errs, fmt.Errorf("scoring.half_life.%s must be positive", k))
		}
	}
	if c.Scoring.Window <= 0 {
		errs = append(errs, errors.New("scoring.window must be positive"))
	}
	for i, in := range c.Interests {
		if strings.TrimSpace(in.Term) == "" {
			errs = append(errs, fmt.Errorf("interests[%d]: empty term", i))
		}
	}
	for i, f := range c.Sources.RSS.Feeds {
		if u, err := url.Parse(f.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("sources.rss.feeds[%d]: %q is not an http(s) URL", i, f.URL))
		}
		if f.Type != "" && f.Type != string(item.TypeArticle) && f.Type != string(item.TypeNews) && f.Type != string(item.TypePaper) {
			errs = append(errs, fmt.Errorf("sources.rss.feeds[%d].type %q: want article, news or paper", i, f.Type))
		}
	}
	if b := c.Sources.Books; b.PerQuery < 1 || b.PerQuery > 40 {
		errs = append(errs, errors.New("sources.books.per_query must be 1..40"))
	}
	for _, e := range []struct {
		name string
		d    time.Duration
	}{
		{"sources.arxiv.every", c.Sources.ArXiv.Every}, {"sources.hackernews.every", c.Sources.HackerNews.Every},
		{"sources.rss.every", c.Sources.RSS.Every}, {"sources.books.every", c.Sources.Books.Every},
		{"sources.rss.max_age", c.Sources.RSS.MaxAge},
	} {
		if e.d < 0 {
			errs = append(errs, fmt.Errorf("%s must be >= 0", e.name))
		}
	}
	if _, _, err := c.Digest.Books.ParseWeekday(); err != nil {
		errs = append(errs, err)
	}
	if c.Digest.Books.NewReleases < 0 || c.Digest.Books.NewReleaseWindow < 0 {
		errs = append(errs, errors.New("digest.books.new_releases and new_release_window must be >= 0"))
	}
	for k, n := range c.Digest.Sections {
		if k == string(item.TypeBook) {
			errs = append(errs, errors.New("digest.sections.book: books are configured under digest.books"))
			continue
		}
		if !item.Type(k).Valid() {
			errs = append(errs, fmt.Errorf("digest.sections: unknown type %q", k))
		}
		if n < 0 {
			errs = append(errs, fmt.Errorf("digest.sections.%s must be >= 0", k))
		}
	}
	if c.Digest.Lookback <= 0 {
		errs = append(errs, errors.New("digest.lookback must be positive"))
	}
	switch c.Email.TLS {
	case "starttls", "tls", "none":
	default:
		errs = append(errs, fmt.Errorf("email.tls %q: want starttls, tls or none", c.Email.TLS))
	}
	if c.Email.SMTPPort < 1 || c.Email.SMTPPort > 65535 {
		errs = append(errs, errors.New("email.smtp_port must be 1..65535"))
	}
	if u, err := url.Parse(c.Server.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Errorf("server.base_url %q must be an absolute http(s) URL", c.Server.BaseURL))
	}
	if c.Server.UIAuth != "password" && c.Server.UIAuth != "none" {
		errs = append(errs, fmt.Errorf("server.ui_auth %q: want password or none", c.Server.UIAuth))
	}
	if c.Schedule.FetchEvery < 0 {
		errs = append(errs, errors.New("schedule.fetch_every must be >= 0"))
	}
	if _, _, _, _, err := c.Schedule.DigestClock(); err != nil {
		errs = append(errs, err)
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
