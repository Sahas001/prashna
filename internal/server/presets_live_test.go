package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Sahas001/prashna/internal/fetch"
)

// TestPresetFeedsLive checks every welcome-screen feed parses. It needs the
// network, so it only runs with PRASHNA_LIVE=1.
func TestPresetFeedsLive(t *testing.T) {
	if os.Getenv("PRASHNA_LIVE") == "" {
		t.Skip("set PRASHNA_LIVE=1 to check preset feeds over the network")
	}
	c := fetch.NewClient("Prashna/1.0 (personal reading app)")
	c.MaxRetries = 1
	for _, tp := range topics {
		for _, f := range tp.Feeds {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			title, u, err := fetch.ProbeFeed(ctx, c, f.URL)
			cancel()
			if err != nil {
				t.Errorf("%s: %s: %v", tp.Key, f.URL, err)
				continue
			}
			if u != f.URL {
				t.Errorf("%s: %s redirects to feed %s", tp.Key, f.URL, u)
			}
			t.Logf("ok %-11s %s (%s)", tp.Key, title, f.URL)
		}
	}
}
