package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptLegacyData(t *testing.T) {
	t.Setenv("PRASHNA_HOME", "")
	base := t.TempDir()
	old := filepath.Join(base, "radar")
	os.MkdirAll(old, 0o700)
	for _, f := range []string{"radar.db", "radar.db-wal", "radar.log", "config.yaml"} {
		os.WriteFile(filepath.Join(old, f), []byte(f), 0o600)
	}
	dir := filepath.Join(base, "prashna")

	if moved, err := adoptLegacyDataDir(dir); err != nil || !moved {
		t.Fatalf("move: %v %v", moved, err)
	}
	if renamed, err := adoptLegacyFiles(dir); err != nil || !renamed {
		t.Fatalf("rename: %v %v", renamed, err)
	}
	for _, f := range []string{"prashna.db", "prashna.db-wal", "prashna.log", "config.yaml"} {
		if !exists(filepath.Join(dir, f)) {
			t.Errorf("missing %s", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "prashna.db")); string(b) != "radar.db" {
		t.Errorf("database contents changed: %q", b)
	}
	if exists(old) {
		t.Error("old folder still there")
	}

	// A second run, or a fresh install, changes nothing.
	if moved, _ := adoptLegacyDataDir(dir); moved {
		t.Error("moved twice")
	}
	if renamed, _ := adoptLegacyFiles(dir); renamed {
		t.Error("renamed twice")
	}
	fresh := filepath.Join(t.TempDir(), "prashna")
	if moved, err := adoptLegacyDataDir(fresh); moved || err != nil {
		t.Errorf("fresh install: %v %v", moved, err)
	}

	// An empty new folder doesn't hide the old data.
	base2 := t.TempDir()
	os.MkdirAll(filepath.Join(base2, "radar"), 0o700)
	os.WriteFile(filepath.Join(base2, "radar", "radar.db"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(base2, "prashna"), 0o700)
	if moved, err := adoptLegacyDataDir(filepath.Join(base2, "prashna")); !moved || err != nil {
		t.Errorf("empty new folder: %v %v", moved, err)
	}

	// Never over an existing new database.
	both := t.TempDir()
	os.WriteFile(filepath.Join(both, "radar.db"), []byte("old"), 0o600)
	os.WriteFile(filepath.Join(both, "prashna.db"), []byte("new"), 0o600)
	if renamed, _ := adoptLegacyFiles(both); renamed {
		t.Error("overwrote the new database")
	}
	if b, _ := os.ReadFile(filepath.Join(both, "prashna.db")); string(b) != "new" {
		t.Errorf("new database = %q", b)
	}
}
