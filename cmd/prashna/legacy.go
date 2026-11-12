package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Sahas001/prashna/internal/config"
)

// Prashna used to be called Radar. These helpers carry a reader's data
// over the first time the renamed app runs, so nothing is lost.

const legacyName = "radar"

// adoptLegacyDataDir moves the old default data folder (~/.config/radar,
// .../Radar) to dataDir, the new default, if only the old one exists.
func adoptLegacyDataDir(dataDir string) (moved bool, err error) {
	if os.Getenv("PRASHNA_HOME") != "" || exists(dataDir) {
		return false, nil
	}
	parent := filepath.Dir(dataDir)
	for _, name := range []string{legacyName, "Radar"} {
		old := filepath.Join(parent, name)
		if !exists(filepath.Join(old, legacyName+".db")) {
			continue
		}
		if err := os.Rename(old, dataDir); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// adoptLegacyFiles renames radar.db (and its companions) and radar.log
// inside dir to their new names, unless the new database is already there.
func adoptLegacyFiles(dir string) (renamed bool, err error) {
	db := filepath.Join(dir, config.DefaultDBName)
	old := filepath.Join(dir, legacyName+".db")
	if exists(db) || !exists(old) {
		return false, nil
	}
	if err := os.Rename(old, db); err != nil {
		return false, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Rename(old+suffix, db+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return true, err
		}
	}
	// The log is only history: best effort.
	os.Rename(filepath.Join(dir, legacyName+".log"), filepath.Join(dir, "prashna.log"))
	return true, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
