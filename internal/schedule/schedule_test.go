package schedule

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestDailyAt(t *testing.T) {
	ktm, err := time.LoadLocation("Asia/Kathmandu")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	next := DailyAt(7, 0, ktm)
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 11, 9, 6, 59, 0, 0, ktm), time.Date(2026, 11, 9, 7, 0, 0, 0, ktm)},
		{time.Date(2026, 11, 9, 7, 0, 0, 0, ktm), time.Date(2026, 11, 10, 7, 0, 0, 0, ktm)},
		{time.Date(2026, 12, 31, 23, 0, 0, 0, ktm), time.Date(2027, 1, 1, 7, 0, 0, 0, ktm)},
		// 01:00 UTC is 06:45 in Kathmandu: still today.
		{time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 7, 0, 0, 0, ktm)},
	}
	for _, c := range cases {
		if got := next(c.now); !got.Equal(c.want) {
			t.Errorf("next(%v) = %v, want %v", c.now, got, c.want)
		}
	}

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// Across the November DST change the wall-clock time stays 07:00.
	got := DailyAt(7, 0, ny)(time.Date(2026, 10, 31, 8, 0, 0, 0, ny))
	got = DailyAt(7, 0, ny)(got)
	if got.Hour() != 7 || got.Day() != 2 {
		t.Errorf("DST: got %v", got)
	}
}

func TestRunStartsAndStops(t *testing.T) {
	var a, b atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)),
			Job{Name: "a", Next: Every(10 * time.Millisecond), RunAtStart: true, Run: func(context.Context) error { a.Add(1); return nil }},
			Job{Name: "b", Next: Every(time.Hour), Run: func(context.Context) error { b.Add(1); return nil }},
		)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for a.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if a.Load() < 3 || b.Load() != 0 {
		t.Errorf("a=%d b=%d", a.Load(), b.Load())
	}
}
