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
	if os.Getenv("PRASHNA_HOME") != "" {
		return false, nil
	}
	if entries, err := os.ReadDir(dataDir); err == nil {
		if len(entries) > 0 {
			return false, nil // already in use
		}
		// An empty folder (made by a command that never opened the
		// database) must not hide the old data.
		if err := os.Remove(dataDir); err != nil {
			return false, nil
		}
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
// Only call it when the database is at its default place: a config that
// names its own db_path is left exactly as it is.
func adoptLegacyFiles(dir string) (renamed bool, err error) {
	db := filepath.Join(dir, config.DefaultDBName)
	old := filepath.Join(dir, legacyName+".db")
	if exists(db) || !exists(old) {
		return false, nil
	}
	// Companions first and the database last, so a failure halfway leaves
	// "old database present, new one absent" and the next run tries again
	// instead of opening the database without its write-ahead log.
	var done []string
	for _, suffix := range []string{"-wal", "-shm", ""} {
		err := os.Rename(old+suffix, db+suffix)
		if err != nil && !(suffix != "" && errors.Is(err, fs.ErrNotExist)) {
			for _, s := range done {
				os.Rename(db+s, old+s)
			}
			return false, err
		}
		if err == nil {
			done = append(done, suffix)
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
