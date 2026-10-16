//go:build !windows

package tui

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// Each of the stop signals cancels the context the UI runs under, so the
// session is saved and the terminal restored, and not killed mid-write.
func TestWithTermSignalsCancelsOnStopSignals(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		ctx, stop := WithTermSignals(context.Background())
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			stop()
			t.Fatalf("kill %v: %v", sig, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Errorf("%v did not cancel the context", sig)
		}
		stop()
	}
}
