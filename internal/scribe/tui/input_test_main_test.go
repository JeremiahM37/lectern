package tui

import (
	"io"
	"os"
	"testing"
)

// TestMain points the Scribe home at a scratch directory for the whole package,
// so the frecency and stash files a test writes never reach the real home.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "scribe-tui-home-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("SCRIBE_HOME", dir); err != nil {
		panic(err)
	}
	// Copy commands must not put test text on the developer's clipboard.
	clipboardOut = io.Discard
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
