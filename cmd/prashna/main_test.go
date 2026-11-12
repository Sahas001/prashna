package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAndHelpTouchNothing(t *testing.T) {
	data := filepath.Join(t.TempDir(), "prashna-data")
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"-data", data, "version"}, &out, &errOut); err != nil || !strings.HasPrefix(out.String(), "prashna ") {
		t.Fatalf("version: %q %v", out.String(), err)
	}
	for _, args := range [][]string{{"-data", data, "help"}, {"-data", data, "fetch", "-h"}, {"-data", data, "top", "-h"}} {
		run(context.Background(), args, &out, &errOut)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Errorf("help created the data folder (%v)", err)
	}
}

func TestFetchWithNoProfile(t *testing.T) {
	data := t.TempDir()
	var out, errOut bytes.Buffer
	// A fresh install has nothing to fetch and must not fail or reach the network.
	if err := run(context.Background(), []string{"-data", data, "fetch"}, &out, &errOut); err != nil {
		t.Fatalf("fetch: %v\n%s", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "nothing to fetch yet") {
		t.Errorf("log = %s", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(data, "prashna.db")); err != nil {
		t.Errorf("database not created in the data folder: %v", err)
	}
}

func TestAutostartContents(t *testing.T) {
	if got := shellJoin([]string{"/opt/my apps/prashna", "-data", "/home/a b/.config/prashna", "run"}); got != `"/opt/my apps/prashna" -data "/home/a b/.config/prashna" run` {
		t.Errorf("shellJoin = %s", got)
	}
	if f, err := autostartFile(); err != nil || f == "" {
		t.Errorf("autostartFile = %q %v", f, err)
	}
}

func TestRunningURLDetectsPrashna(t *testing.T) {
	prashnaLike := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			io.WriteString(w, "prashna ok\n")
		}
	}))
	defer prashnaLike.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") }))
	defer other.Close()

	a := &app{}
	a.cfg.Addr = strings.TrimPrefix(prashnaLike.URL, "http://")
	if url, ok := a.runningURL(context.Background()); !ok || url != prashnaLike.URL+"/" {
		t.Errorf("Prashna not detected: %q %v", url, ok)
	}
	a.cfg.Addr = strings.TrimPrefix(other.URL, "http://")
	if _, ok := a.runningURL(context.Background()); ok {
		t.Error("another program on the port was mistaken for Prashna")
	}
	a.cfg.Addr = "127.0.0.1:1"
	if _, ok := a.runningURL(context.Background()); ok {
		t.Error("nothing listening, but detected")
	}
	a.cfg.Addr = "0.0.0.0:7878"
	if got := a.appURL(); got != "http://127.0.0.1:7878/" {
		t.Errorf("appURL for 0.0.0.0 = %s", got)
	}
}
