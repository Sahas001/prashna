// Command prashna is a personal reading app: it gathers papers, articles,
// discussions and books from free sources, learns what you like, and puts
// together a small daily edition. It runs entirely on your own computer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	_ "time/tzdata" // correct local dates on minimal systems without zoneinfo

	"github.com/Sahas001/prashna/internal/config"
	"github.com/Sahas001/prashna/internal/item"
	"github.com/Sahas001/prashna/internal/learn"
	"github.com/Sahas001/prashna/internal/pipeline"
	"github.com/Sahas001/prashna/internal/score"
	"github.com/Sahas001/prashna/internal/store"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

const usage = `Prashna: a reading app that learns what you like.

Usage:
  prashna                 open Prashna (starts it if needed): in its own window in
                        the desktop app, otherwise in your browser
  prashna <command>

Commands:
  run         run Prashna without opening a browser (for autostart and servers)
  fetch       check all sources now
  top         list the best unread items in the terminal
  model       show what Prashna has learned from your ratings
  autostart   start Prashna when you log in: prashna autostart on|off|status
  launcher    desktop app on Linux: add Prashna to the applications menu
              (prashna launcher on|off)
  version     print the version

Options:
  -data DIR   where Prashna keeps its data (default: your user config folder,
              or $PRASHNA_HOME)
  -config F   optional config file (default: <data>/config.yaml)
  -v          verbose logging
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "prashna:", err)
		}
		os.Exit(1)
	}
}

type app struct {
	cfg     config.Config // from defaults and the config file
	dataDir string
	cfgPath string
	store   *store.Store
	log     *slog.Logger
	out     io.Writer
	fetches fetchState
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("prashna", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	dataFlag := fs.String("data", "", "data folder")
	cfgFlag := fs.String("config", "", "config file")
	verbose := fs.Bool("v", false, "verbose logging")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd, cmdArgs := "open", fs.Args()
	if len(cmdArgs) > 0 {
		cmd, cmdArgs = cmdArgs[0], cmdArgs[1:]
	}
	switch cmd {
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, "prashna", version)
		return nil
	case "help":
		fs.Usage()
		return nil
	}

	dataDir := *dataFlag
	if dataDir == "" {
		var err error
		if dataDir, err = config.DataDir(); err != nil {
			return err
		}
	}
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	// Data from before the app was renamed comes along. (Config is loaded
	// after this, from the folder's new place.)
	var adopted []string
	if *dataFlag == "" {
		if moved, err := adoptLegacyDataDir(dataDir); err != nil {
			return fmt.Errorf("move your Radar data to %s: %w", dataDir, err)
		} else if moved {
			adopted = append(adopted, "moved your Radar data folder to "+dataDir)
		}
	}
	cfgPath := *cfgFlag
	if cfgPath == "" {
		cfgPath = filepath.Join(dataDir, "config.yaml")
	}
	cfg, _, err := config.Load(cfgPath, dataDir)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.DBPath == filepath.Join(dataDir, config.DefaultDBName) {
		if renamed, err := adoptLegacyFiles(dataDir); err != nil {
			return fmt.Errorf("rename your Radar database in %s: %w", dataDir, err)
		} else if renamed {
			adopted = append(adopted, "renamed radar.db to "+config.DefaultDBName)
		}
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logOut := stderr
	a := &app{cfg: cfg, dataDir: dataDir, cfgPath: cfgPath, out: stdout}

	// `prashna <cmd> -h` must not create a data folder or database. Commands
	// parse their flags before touching the store, so a nil store is safe.
	if slices.ContainsFunc(cmdArgs, func(s string) bool { return s == "-h" || s == "-help" || s == "--help" }) {
		a.log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
		return a.dispatch(ctx, cmd, cmdArgs, fs)
	}

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data folder: %w", err)
	}
	if cmd == "open" || cmd == "run" {
		// Long-running: also log to a file, since autostarted copies have
		// no terminal.
		if f, err := openLog(filepath.Join(dataDir, "prashna.log")); err == nil {
			defer f.Close()
			logOut = io.MultiWriter(stderr, f)
		}
	}
	a.log = slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))
	for _, what := range adopted {
		a.log.Info("Radar is now Prashna: " + what)
	}

	if cmd == "open" {
		// Already running? Just show it.
		if url, ok := a.runningURL(ctx); ok {
			a.log.Info("Prashna is already running", "url", url)
			if hasWindow {
				// Started at login and fetching in the background: this
				// is just its window.
				return showWindow(ctx, a.windowID(), url+"window")
			}
			return openBrowser(url)
		}
	}
	if cmd == "autostart" || cmd == "launcher" {
		return a.dispatch(ctx, cmd, cmdArgs, fs)
	}

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	a.store = st
	if err := a.seedProfile(ctx); err != nil {
		return err
	}
	return a.dispatch(ctx, cmd, cmdArgs, fs)
}

func (a *app) dispatch(ctx context.Context, cmd string, args []string, fs *flag.FlagSet) error {
	switch cmd {
	case "open":
		return a.serve(ctx, args, true)
	case "run", "serve":
		return a.serve(ctx, args, false)
	case "fetch":
		return a.fetch(ctx, args)
	case "top":
		return a.top(ctx, args)
	case "feedback":
		return a.feedback(ctx, args)
	case "rescore":
		return a.rescore(ctx)
	case "model":
		return a.model(ctx)
	case "autostart":
		return a.autostart(args)
	case "launcher":
		return a.launcher(args)
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// openLog opens the log file for appending, starting afresh once it
// grows past a few megabytes.
func openLog(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// current is the effective configuration: the config file with the
// settings saved in the app applied on top.
func (a *app) current(ctx context.Context) config.Config {
	var s config.Settings
	if ok, err := a.store.JSONSetting(ctx, config.SettingsKey, &s); err != nil {
		a.log.Error("load settings", "err", err)
	} else if ok {
		return a.cfg.With(s)
	}
	return a.cfg
}

// settings returns the app-editable settings as stored, or the config
// file's values if nothing was saved yet. (Read them from here, not from
// current(): settings like the theme don't live in the config.)
func (a *app) settings(ctx context.Context) config.Settings {
	var s config.Settings
	if ok, err := a.store.JSONSetting(ctx, config.SettingsKey, &s); err == nil && ok {
		s.Normalize()
		return s
	}
	return a.cfg.Settings()
}

func (a *app) saveSettings(ctx context.Context, s config.Settings) error {
	return a.store.SetJSONSetting(ctx, config.SettingsKey, s)
}

// scorer builds a Scorer from the interest profile in the database.
func (a *app) scorer(ctx context.Context) (*score.Scorer, error) {
	stored, err := a.store.Interests(ctx)
	if err != nil {
		return nil, err
	}
	interests := make([]score.Interest, len(stored))
	for i, in := range stored {
		interests[i] = score.Interest{Term: in.Term, Weight: in.Weight}
	}
	w := a.cfg.Scoring.Weights
	return score.New(score.Config{
		Weights:   score.Weights{Keyword: w.Keyword, Similarity: w.Similarity, Recency: w.Recency},
		Interests: interests,
		HalfLife:  a.cfg.Scoring.HalfLives(),
	}), nil
}

func (a *app) pipeline(ctx context.Context) (*pipeline.Pipeline, error) {
	sc, err := a.scorer(ctx)
	if err != nil {
		return nil, err
	}
	l := a.current(ctx).Learning
	return &pipeline.Pipeline{
		Store:    a.store,
		Scorer:   sc,
		Window:   a.cfg.Scoring.Window,
		Learning: learn.Config{Enabled: l.Enabled, MinRatings: l.MinRatings, MaxWeight: l.MaxWeight},
		Log:      a.log,
	}, nil
}

func (a *app) rescoreAll(ctx context.Context) error {
	p, err := a.pipeline(ctx)
	if err != nil {
		return err
	}
	_, err = p.Rescore(ctx)
	return err
}

// seedProfile imports interests, feeds and book follows from the config
// file into a fresh database. After that the database (edited in the app)
// is the source of truth and those config lists are ignored.
func (a *app) seedProfile(ctx context.Context) error {
	p := store.Profile{Authors: a.cfg.Sources.Books.Authors, Subjects: a.cfg.Sources.Books.Subjects}
	for _, in := range a.cfg.Interests {
		p.Interests = append(p.Interests, store.Interest{Term: in.Term, Weight: in.Weight})
	}
	for _, f := range a.cfg.Sources.RSS.Feeds {
		p.Feeds = append(p.Feeds, store.Feed{URL: f.URL, Name: f.Name, Tags: f.Tags, Type: item.Type(f.Type)})
	}
	seeded, err := a.store.SeedProfile(ctx, p)
	if err != nil {
		return fmt.Errorf("import profile from config: %w", err)
	}
	if seeded && len(p.Interests)+len(p.Feeds)+len(p.Authors)+len(p.Subjects) > 0 {
		a.log.Info("imported interests, feeds and book follows from the config file",
			"interests", len(p.Interests), "feeds", len(p.Feeds), "authors", len(p.Authors), "subjects", len(p.Subjects))
	}
	return nil
}
