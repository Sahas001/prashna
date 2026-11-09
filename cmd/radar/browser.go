package main

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// openBrowser shows url in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// appURL is the address to open in a browser.
func (a *app) appURL() string {
	addr := a.cfg.Addr
	if strings.HasPrefix(addr, "0.0.0.0:") || strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1:" + addr[strings.LastIndex(addr, ":")+1:]
	}
	return "http://" + addr + "/"
}

// runningURL reports whether a Radar is already serving on the configured
// address (e.g. started at login), so `radar` can just open it.
func (a *app) runningURL(ctx context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	url := a.appURL()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return url, resp.StatusCode == http.StatusOK && strings.HasPrefix(string(body), "radar ok")
}
