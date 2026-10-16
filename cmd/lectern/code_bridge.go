package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/lecternbridge"
)

var lecternBridgeOnce sync.Once

// maybeLecternBridge connects Scribe to Lectern when this process runs inside
// a Lectern session (LECTERN_HOOK_URL and LECTERN_HOOK_TOKEN are set). Every
// Scribe session opened afterwards in this process reports to Lectern and
// lets it decide approvals. Outside Lectern it does nothing.
func maybeLecternBridge() {
	if _, ok := lecternbridge.FromEnv(); !ok {
		return
	}
	lecternBridgeOnce.Do(func() {
		app.OnOpen(func(a *app.App) {
			if _, err := lecternbridge.Attach(a, nil); err != nil {
				fmt.Fprintf(os.Stderr, "lectern code: not connected to Lectern: %v\n", err)
			}
		})
	})
}
