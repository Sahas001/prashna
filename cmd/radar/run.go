package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Sahas001/radar/internal/fetch"
	"github.com/Sahas001/radar/internal/reader"
	"github.com/Sahas001/radar/internal/schedule"
	"github.com/Sahas001/radar/internal/server"
)

// serve runs Radar: the web app plus background fetching. With
// openInBrowser it shows the app once it is listening.
func (a *app) serve(ctx context.Context, args []string, openInBrowser bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	addr := fs.String("addr", a.cfg.Addr, "listen address")
	noFetch := fs.Bool("no-fetch", false, "don't fetch in the background")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a.cfg.Addr = *addr

	key, err := a.store.SecretKey(ctx, "session")
	if err != nil {
		return err
	}
	auth := server.Auth{Password: a.cfg.Password(), Disabled: a.cfg.LocalOnly() && a.cfg.Password() == ""}
	if !auth.Disabled && auth.Password == "" {
		a.log.Warn("Radar is reachable from other machines, so the app stays locked until a password is set", "env", a.cfg.PasswordEnv)
	}
	probe := fetch.NewClient(a.cfg.UserAgent)
	probe.MaxRetries = 0

	srv := &http.Server{
		Handler: (&server.Server{
			Store:   a.store,
			Rescore: a.rescoreAll,
			Scorer:  a.scorer,
			ProbeFeed: func(ctx context.Context, u string) (string, string, error) {
				return fetch.ProbeFeed(ctx, probe, u)
			},
			StartFetch:    func() { a.startFetch(true) },
			FetchStatus:   a.fetchStatus,
			EnsureEdition: a.ensureEdition,
			ReadText:      reader.New(a.cfg.UserAgent).Extract,
			Settings:      a.settings,
			SaveSettings:  a.saveSettings,
			Auth:          auth,
			SessionKey:    key,
			About:         server.About{Version: version, DataDir: a.dataDir, ConfigPath: a.cfgPath, Addr: a.cfg.Addr},
			Log:           a.log,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("can't listen on %s (is another program using it? set addr in %s): %w", *addr, a.cfgPath, err)
	}
	url := a.appURL()
	a.log.Info("Radar is running", "url", url, "data", a.dataDir)
	if openInBrowser {
		if err := openBrowser(url); err != nil {
			a.log.Info("open this address in your browser", "url", url)
		}
	}

	var wg sync.WaitGroup
	if !*noFetch {
		wg.Go(func() {
			schedule.Run(ctx, a.log, schedule.Job{
				Name: "fetch", RunAtStart: true,
				Next: func(t time.Time) time.Time {
					every := a.current(ctx).FetchEvery
					if every <= 0 {
						every = 4 * time.Hour
					}
					return t.Add(every)
				},
				Run: func(ctx context.Context) error {
					err := a.fetchAll(ctx, false)
					if errors.Is(err, errFetchRunning) {
						return nil
					}
					return err
				},
			}, schedule.Job{
				// A new day can start while Radar sits idle: make the
				// edition soon after midnight rather than at the next fetch.
				Name: "edition", Next: nextMorning,
				Run: func(ctx context.Context) error { _, err := a.ensureEdition(ctx); return err },
			})
		})
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	a.log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// nextMorning is five past midnight, local time.
func nextMorning(t time.Time) time.Time {
	return schedule.DailyAt(0, 5, time.Local)(t)
}
