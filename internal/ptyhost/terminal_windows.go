//go:build windows

package ptyhost

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// enableVT turns on escape-sequence processing in a Windows console, which
// is what lets it draw what the session sends.
func enableVT(out *os.File) func() {
	h := windows.Handle(out.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return func() {}
	}
	_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN)
	return func() { _ = windows.SetConsoleMode(h, mode) }
}

// watchResize polls the console size: Windows has no SIGWINCH.
func watchResize(out *os.File, resized func(cols, rows int)) (stop func()) {
	quit := make(chan struct{})
	go func() {
		w0, h0, _ := term.GetSize(int(out.Fd()))
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				if w, h, err := term.GetSize(int(out.Fd())); err == nil && (w != w0 || h != h0) {
					w0, h0 = w, h
					resized(w, h)
				}
			}
		}
	}()
	return func() { close(quit) }
}
