package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Sahas001/prashna/internal/fetch"
	"github.com/Sahas001/prashna/internal/reader"
	"github.com/Sahas001/prashna/internal/schedule"
	"github.com/Sahas001/prashna/internal/server"
)

// serve runs Prashna: the web app plus background fetching. With
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
		a.log.Warn("Prashna is reachable from other machines, so the app stays locked until a password is set", "env", a.cfg.PasswordEnv)
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
			OpenExternal:  a.externalOpener(),
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
	a.log.Info("Prashna is running", "url", url, "data", a.dataDir)
	if openInBrowser && !hasWindow {
		if err := openBrowser(url); err != nil {
			a.log.Info("open this address in your browser", "url", url)
		}
	}

	jobs, stopJobs := context.WithCancel(ctx)
	defer stopJobs()
	var wg sync.WaitGroup
	if !*noFetch {
		wg.Go(func() {
			schedule.Run(jobs, a.log, schedule.Job{
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
				// A new day can start while Prashna sits idle: make the
				// edition soon after midnight rather than at the next fetch.
				Name: "edition", Next: nextMorning,
				Run: func(ctx context.Context) error { _, err := a.ensureEdition(ctx); return err },
			})
		})
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	windowed := false
	if openInBrowser && hasWindow {
		// The desktop app: Prashna lives as long as its window. (This is
		// the main goroutine, which the window needs.)
		if err := showWindow(ctx, a.windowID(), url+"window"); err != nil {
			a.log.Error("can't open a window; showing Prashna in the browser instead", "err", err)
			openBrowser(url)
		} else {
			windowed = true
		}
	}
	if !windowed {
		select {
		case err := <-serveErr:
			return err
		case <-ctx.Done():
		}
	}
	a.log.Info("shutting down")
	stopJobs()
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

// externalOpener lets the app window hand links to other sites over to
// the real browser. Only on the reader's own computer: when Prashna is
// shared on the network, a visitor must not open things on this screen.
func (a *app) externalOpener() func(string) error {
	if !a.cfg.LocalOnly() {
		return nil
	}
	return openBrowser
}

// windowID names this Prashna's window, one per data folder.
func (a *app) windowID() string {
	sum := sha256.Sum256([]byte(a.dataDir))
	return "prashna-" + hex.EncodeToString(sum[:6])
}
