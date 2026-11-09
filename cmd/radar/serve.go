package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"radar/internal/fetch"
	"radar/internal/links"
	"radar/internal/schedule"
	"radar/internal/server"
)

func (a *app) serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", a.cfg.Server.Addr, "listen address")
	noSchedule := fs.Bool("no-schedule", false, "serve feedback links only; no scheduled fetches or digests")
	if err := fs.Parse(args); err != nil {
		return err
	}

	key, err := a.store.FeedbackKey(ctx)
	if err != nil {
		return err
	}
	sessionKey := hmac.New(sha256.New, key)
	sessionKey.Write([]byte("radar web session key"))
	baseURL, _ := url.Parse(a.cfg.Server.BaseURL) // validated at load
	auth := server.Auth{
		Disabled:     a.cfg.Server.UIAuth == "none",
		Password:     a.cfg.Server.UIPassword(),
		SecureCookie: baseURL.Scheme == "https",
	}
	if !auth.Disabled && auth.Password == "" {
		a.log.Warn("web UI is locked until a password is set", "env", a.cfg.Server.PasswordEnv)
	}
	probeClient := fetch.NewClient(a.cfg.UserAgent)
	probeClient.MaxRetries = 0
	srv := &http.Server{
		Addr: *addr,
		Handler: (&server.Server{
			Store:   a.store,
			Signer:  links.NewSigner(a.cfg.Server.BaseURL, key),
			Rescore: a.rescoreAll,
			Scorer:  a.scorer,
			ProbeFeed: func(ctx context.Context, u string) (string, string, error) {
				return fetch.ProbeFeed(ctx, probeClient, u)
			},
			FetchFeeds: func(ctx context.Context) error { return a.runFetch(ctx, []string{"rss"}, true) },
			Auth:       auth,
			SessionKey: sessionKey.Sum(nil),
			TopDays:    max(1, int(a.cfg.Digest.Lookback.Hours()/24)),
			Log:        a.log,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	a.log.Info("serving", "addr", ln.Addr().String(), "base_url", a.cfg.Server.BaseURL)

	var wg sync.WaitGroup
	if !*noSchedule {
		if jobs := a.jobs(); len(jobs) > 0 {
			wg.Go(func() { schedule.Run(ctx, a.log, jobs...) })
		}
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
	wg.Wait() // let a running fetch/digest observe cancellation and finish
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (a *app) jobs() []schedule.Job {
	var jobs []schedule.Job
	sc := a.cfg.Schedule
	if sc.FetchEvery > 0 {
		jobs = append(jobs, schedule.Job{
			Name: "fetch", Next: schedule.Every(sc.FetchEvery), RunAtStart: true,
			Run: func(ctx context.Context) error { return a.runFetch(ctx, nil, false) },
		})
	}
	hour, minute, loc, ok, _ := sc.DigestClock() // validated at load
	if ok {
		if err := a.cfg.Email.ValidateForSending(); err != nil {
			a.log.Warn("scheduled digest disabled: email not configured", "err", err)
		} else {
			jobs = append(jobs, schedule.Job{
				Name: "digest", Next: schedule.DailyAt(hour, minute, loc),
				Run: func(ctx context.Context) error { return a.sendDigest(ctx, false) },
			})
		}
	}
	return jobs
}
