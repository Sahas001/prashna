// Command radar is a self-hosted reading recommender.
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
	"slices"
	"syscall"
	_ "time/tzdata" // schedule.timezone works on minimal hosts without /usr/share/zoneinfo

	"radar/internal/config"
	"radar/internal/item"
	"radar/internal/pipeline"
	"radar/internal/score"
	"radar/internal/store"
)

const usage = `radar - personal reading recommender

Usage:
  radar [-config radar.yaml] [-v] <command> [flags]

Commands:
  serve      run the feedback server plus scheduled fetches and digests
  fetch      fetch all enabled sources, store, and rescore
  digest     email a digest of the best unsent items (-preview / -text to just look)
  top        print the highest-scoring unrated items
  feedback   rate an item: radar feedback <id> up|down|clear
  rescore    recompute scores (after editing interests)

Run 'radar <command> -h' for command flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "radar:", err)
		}
		os.Exit(1)
	}
}

type app struct {
	cfg   config.Config
	store *store.Store
	log   *slog.Logger
	out   io.Writer
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("radar", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	cfgPath := fs.String("config", "radar.yaml", "config file")
	verbose := fs.Bool("v", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return flag.ErrHelp
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	cfg, missing, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if missing {
		log.Warn("config file not found, using defaults (see radar.example.yaml)", "path", *cfgPath)
	}

	cmd, cmdArgs := fs.Arg(0), fs.Args()[1:]
	// `radar <cmd> -h` must not create or migrate a database. Commands
	// parse their flags before touching the store, so a nil store is safe.
	if slices.ContainsFunc(cmdArgs, func(a string) bool { return a == "-h" || a == "-help" || a == "--help" }) {
		a := &app{cfg: cfg, log: log, out: stdout}
		switch cmd {
		case "serve":
			return a.serve(ctx, cmdArgs)
		case "fetch":
			return a.fetch(ctx, cmdArgs)
		case "digest":
			return a.digest(ctx, cmdArgs)
		case "top":
			return a.top(ctx, cmdArgs)
		}
		fs.Usage()
		return flag.ErrHelp
	}

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	a := &app{cfg: cfg, store: st, log: log, out: stdout}
	if err := a.seedProfile(ctx); err != nil {
		return err
	}

	switch cmd {
	case "serve":
		return a.serve(ctx, cmdArgs)
	case "fetch":
		return a.fetch(ctx, cmdArgs)
	case "digest":
		return a.digest(ctx, cmdArgs)
	case "top":
		return a.top(ctx, cmdArgs)
	case "feedback":
		return a.feedback(ctx, cmdArgs)
	case "rescore":
		return a.rescore(ctx)
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
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
	return &pipeline.Pipeline{
		Store:  a.store,
		Scorer: sc,
		Window: a.cfg.Scoring.Window,
		Log:    a.log,
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
// file into a fresh database. After that the database (edited in the web
// UI) is the source of truth and those config lists are ignored.
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
	if seeded {
		a.log.Info("imported interests, feeds and book follows from config; edit them in the web UI from now on",
			"interests", len(p.Interests), "feeds", len(p.Feeds), "authors", len(p.Authors), "subjects", len(p.Subjects))
	}
	return nil
}

// digestSections converts config section limits to typed keys.
func (a *app) digestSections() map[item.Type]int {
	out := make(map[item.Type]int, len(a.cfg.Digest.Sections))
	for k, v := range a.cfg.Digest.Sections {
		out[item.Type(k)] = v
	}
	return out
}
