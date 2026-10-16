// Command demo runs Scribe's full-screen UI against the scripted FakeBackend,
// so the interface can be tried without a model or a project.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

func main() {
	fb := func() (tui.Backend, error) {
		b := tui.NewFakeBackend()
		return b, nil
	}
	if err := tui.Run(context.Background(), fb, tui.Options{}); err != nil {
		fmt.Fprintln(os.Stderr, "scribe demo:", err)
		os.Exit(1)
	}
}
