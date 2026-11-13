//go:build desktop

package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// hasWindow is true in the desktop build (go build -tags desktop,production,
// or `wails build` in this folder): Prashna opens in a window of its own,
// drawn by the system's web view, instead of a browser tab.
const hasWindow = true

//go:embed build/appicon.png
var appIcon []byte

// The same icon as a drawing, for the Linux applications menu, which
// wants either an exact pixel size per folder or a scalable image.
//
//go:embed build/appicon.svg
var appIconSVG []byte

// showWindow opens the app window on url (Prashna's own local address) and
// returns when the window is closed or ctx is done. It must be called on
// the main goroutine. id keeps one window per data folder: starting Prashna
// again brings the open window forward instead.
func showWindow(ctx context.Context, id, url string) error {
	// The window's first page only sends it to the local server, so the
	// app in the window is exactly the app in a browser.
	page := `<!doctype html><meta charset="utf-8"><title>Prashna</title>` +
		`<meta http-equiv="refresh" content="0;url=` + url + `">` +
		`<body style="background:#f6f2ea"></body>`

	var (
		mu   sync.Mutex
		wctx context.Context // set once the window exists
	)
	window := func() context.Context {
		mu.Lock()
		defer mu.Unlock()
		return wctx
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done(): // Ctrl-C, or the system shutting down
			if w := window(); w != nil {
				wruntime.Quit(w)
			}
		case <-done:
		}
	}()

	opts := &options.App{
		Title:            "Prashna",
		Width:            1180,
		Height:           880,
		MinWidth:         380,
		MinHeight:        480,
		BackgroundColour: &options.RGBA{R: 0xf6, G: 0xf2, B: 0xea, A: 0xff},
		AssetServer: &assetserver.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, page)
		})},
		OnStartup: func(c context.Context) {
			mu.Lock()
			wctx = c
			mu.Unlock()
			if ctx.Err() != nil { // asked to stop before the window existed
				wruntime.Quit(c)
			}
		},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: id,
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				if w := window(); w != nil {
					wruntime.WindowUnminimise(w)
					wruntime.Show(w)
				}
			},
		},
		Linux: &linux.Options{Icon: appIcon, ProgramName: "prashna"},
		Mac:   &mac.Options{About: &mac.AboutInfo{Title: "Prashna", Message: "A reading app that learns what you like.", Icon: appIcon}},
	}
	if runtime.GOOS == "darwin" {
		// macOS routes Cmd+C/V/Q through the menu bar.
		opts.Menu = menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.WindowMenu())
	}
	return wails.Run(opts)
}

// launcher adds Prashna to (or removes it from) the Linux applications
// menu. On macOS and Windows the app bundle and installer do this.
func (a *app) launcher(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return errors.New("usage: prashna launcher on|off")
	}
	if runtime.GOOS != "linux" {
		return errors.New("only needed on Linux: on macOS and Windows, Prashna is already among your apps")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	share := os.Getenv("XDG_DATA_HOME")
	if share == "" {
		share = filepath.Join(home, ".local", "share")
	}
	entry := filepath.Join(share, "applications", "prashna.desktop")
	icon := filepath.Join(share, "icons", "hicolor", "scalable", "apps", "prashna.svg")
	// Earlier versions put a PNG here; a newer drawing must not lose to it.
	oldIcon := filepath.Join(share, "icons", "hicolor", "512x512", "apps", "prashna.png")
	os.Remove(oldIcon)
	if args[0] == "off" {
		for _, f := range []string{entry, icon} {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		fmt.Fprintln(a.out, "Prashna is no longer in your applications menu.")
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	content := "[Desktop Entry]\nType=Application\nName=Prashna\nGenericName=Reading app\n" +
		"Comment=A daily edition of things worth reading, chosen for you\n" +
		"Exec=" + shellJoin([]string{exe, "-data", a.dataDir}) + "\nIcon=prashna\nTerminal=false\n" +
		"Categories=News;Education;Literature;\nStartupWMClass=prashna\n"
	for f, data := range map[string][]byte{entry: []byte(content), icon: appIconSVG} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f, data, 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "Prashna is in your applications menu (%s).\n", entry)
	return nil
}
