package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAndOverlay(t *testing.T) {
	dir := t.TempDir()
	cfg, missing, err := Load(filepath.Join(dir, "config.yaml"), dir)
	if err != nil || !missing {
		t.Fatalf("missing file: missing=%v err=%v", missing, err)
	}
	if cfg.DBPath != filepath.Join(dir, "radar.db") || !cfg.LocalOnly() {
		t.Errorf("defaults: db=%q local=%v", cfg.DBPath, cfg.LocalOnly())
	}

	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("addr: 0.0.0.0:9000\nsources:\n  hackernews:\n    min_points: 300\ninterests:\n  - rust\n  - term: crypto\n    weight: -1\n"), 0o600)
	cfg, _, err = Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalOnly() || cfg.Sources.HackerNews.MinPoints != 300 || cfg.Sources.HackerNews.Lookback != 48*time.Hour {
		t.Errorf("overlay: %+v", cfg.Sources.HackerNews)
	}
	if len(cfg.Interests) != 2 || cfg.Interests[0].Weight != 1 || cfg.Interests[1].Weight != -1 {
		t.Errorf("interests = %+v", cfg.Interests)
	}

	os.WriteFile(path, []byte("sorces: {}\n"), 0o600)
	if _, _, err := Load(path, dir); err == nil {
		t.Error("unknown key should be rejected")
	}
	os.WriteFile(path, []byte("\n"), 0o600)
	if _, _, err := Load(path, dir); err != nil {
		t.Errorf("empty file: %v", err)
	}
}

func TestDataDir(t *testing.T) {
	t.Setenv("RADAR_HOME", "/tmp/radar-test-home")
	if d, err := DataDir(); err != nil || d != "/tmp/radar-test-home" {
		t.Errorf("RADAR_HOME: %q %v", d, err)
	}
	t.Setenv("RADAR_HOME", "")
	if d, err := DataDir(); err != nil || d == "" {
		t.Errorf("default data dir: %q %v", d, err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	cfg := Default()
	s := cfg.Settings()
	if s.Papers != 3 || s.Articles != 4 || s.Discussions != 3 || s.BooksWeekday != "sunday" || s.FetchEveryHours != 4 {
		t.Errorf("default settings = %+v", s)
	}
	s.ArxivEnabled, s.ArxivCategories = true, []string{" cs.LG", "cs.LG", "", "math.GT"}
	s.Papers, s.BooksWeekday, s.FetchEveryHours = 5, "Sat", 12
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	c2 := cfg.With(s)
	if !c2.Sources.ArXiv.Enabled || len(c2.Sources.ArXiv.Categories) != 2 || c2.Edition.Sections["paper"] != 5 ||
		c2.FetchEvery != 12*time.Hour || c2.Edition.Books.Weekday != "sat" {
		t.Errorf("applied = %+v %+v", c2.Sources.ArXiv, c2.Edition)
	}
	if day, weekly, _ := c2.Edition.Books.ParseWeekday(); !weekly || day != time.Saturday {
		t.Errorf("weekday = %v %v", day, weekly)
	}

	bad := Settings{ArxivCategories: []string{"not a category!"}, HNMinPoints: 0, Papers: 50, FetchEveryHours: 0, BooksWeekday: "someday", Theme: "neon"}
	if err := bad.Normalize(); err == nil {
		t.Error("bad settings accepted")
	}
	if bad.Papers != 10 || bad.FetchEveryHours != 1 || len(bad.ArxivCategories) != 0 || bad.BooksWeekday != "sunday" || bad.Theme != "system" {
		t.Errorf("bad settings not clamped: %+v", bad)
	}
	// arXiv with no categories stays off.
	if c := cfg.With(Settings{ArxivEnabled: true, FetchEveryHours: 4}); c.Sources.ArXiv.Enabled {
		t.Error("arXiv enabled without categories")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	dir := t.TempDir()
	cfg, missing, err := Load("../../radar.example.yaml", dir)
	if err != nil || missing {
		t.Fatalf("example config: missing=%v err=%v", missing, err)
	}
	if cfg.Edition.Books.NewReleases != 4 || !cfg.LocalOnly() {
		t.Errorf("example values: %+v", cfg.Edition.Books)
	}
}
