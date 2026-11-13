//go:build !desktop

package main

import (
	"context"
	"errors"
)

// hasWindow is false in the plain build: Prashna shows itself in the
// browser. The desktop build (see window_desktop.go) has its own window.
const hasWindow = false

func showWindow(context.Context, string, string) error {
	return errors.New("this build of Prashna has no window of its own")
}

func (a *app) launcher([]string) error {
	return errors.New("the applications-menu entry is for the desktop app; this build opens in your browser")
}
