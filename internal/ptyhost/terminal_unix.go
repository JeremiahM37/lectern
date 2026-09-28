//go:build !windows

package ptyhost

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

func enableVT(*os.File) func() { return func() {} }

// watchResize reports the terminal's size whenever it changes.
func watchResize(out *os.File, resized func(cols, rows int)) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-quit:
				return
			case <-ch:
				if w, h, err := term.GetSize(int(out.Fd())); err == nil {
					resized(w, h)
				}
			}
		}
	}()
	return func() { signal.Stop(ch); close(quit) }
}
