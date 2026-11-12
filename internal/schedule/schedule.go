// Package schedule runs periodic jobs (fetch every N hours, digest daily
// at a set time) inside `prashna serve`, so no cron is needed.
package schedule

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Job struct {
	Name string
	// Next returns the next run time strictly after t.
	Next       func(t time.Time) time.Time
	Run        func(ctx context.Context) error
	RunAtStart bool
}

// Every runs at fixed intervals.
func Every(d time.Duration) func(time.Time) time.Time {
	return func(t time.Time) time.Time { return t.Add(d) }
}

// DailyAt runs once a day at hour:minute in loc (DST-aware).
func DailyAt(hour, minute int, loc *time.Location) func(time.Time) time.Time {
	return func(t time.Time) time.Time {
		t = t.In(loc)
		next := time.Date(t.Year(), t.Month(), t.Day(), hour, minute, 0, 0, loc)
		if !next.After(t) {
			next = time.Date(t.Year(), t.Month(), t.Day()+1, hour, minute, 0, 0, loc)
		}
		return next
	}
}

// pollInterval bounds each sleep, so a machine that suspends (monotonic
// timers pause during sleep) notices a missed wall-clock deadline soon
// after waking.
const pollInterval = time.Minute

// Run runs jobs until ctx is cancelled. Jobs never overlap one another:
// a digest never runs half-way through a fetch.
func Run(ctx context.Context, log *slog.Logger, jobs ...Job) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Go(func() {
			exec := func() {
				mu.Lock()
				defer mu.Unlock()
				if ctx.Err() != nil {
					return
				}
				start := time.Now()
				if err := j.Run(ctx); err != nil {
					log.Error("job failed", "job", j.Name, "err", err, "took", time.Since(start).Round(time.Millisecond))
					return
				}
				log.Info("job done", "job", j.Name, "took", time.Since(start).Round(time.Millisecond))
			}
			if j.RunAtStart {
				exec()
			}
			for {
				next := j.Next(time.Now())
				log.Info("job scheduled", "job", j.Name, "at", next.Format(time.RFC3339))
				if !sleepUntil(ctx, next) {
					return
				}
				exec()
			}
		})
	}
	wg.Wait()
}

func sleepUntil(ctx context.Context, t time.Time) bool {
	for {
		d := time.Until(t)
		if d <= 0 {
			return true
		}
		timer := time.NewTimer(min(d, pollInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
