package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadExampleAndOverlay(t *testing.T) {
	cfg, missing, err := Load("../../radar.example.yaml")
	if err != nil || missing {
		t.Fatalf("example config: missing=%v err=%v", missing, err)
	}
	if len(cfg.Interests) == 0 || cfg.Interests[0].Weight != 1 {
		t.Errorf("bare-string interest should get weight 1: %+v", cfg.Interests)
	}

	path := filepath.Join(t.TempDir(), "radar.yaml")
	os.WriteFile(path, []byte("sources:\n  hackernews:\n    min_points: 100\ninterests:\n  - term: rust\n"), 0o600)
	cfg, _, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources.HackerNews.MinPoints != 100 || cfg.Sources.HackerNews.Lookback != 48*time.Hour || !cfg.Sources.ArXiv.Enabled {
		t.Errorf("overlay on defaults failed: %+v", cfg.Sources)
	}
	if cfg.Interests[0].Weight != 1 {
		t.Errorf("weight default: %+v", cfg.Interests)
	}

	os.WriteFile(path, []byte("sorces: {}\n"), 0o600)
	if _, _, err := Load(path); err == nil {
		t.Error("unknown key should be rejected")
	}
	if _, missing, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err != nil || !missing {
		t.Errorf("missing file: missing=%v err=%v", missing, err)
	}
}
