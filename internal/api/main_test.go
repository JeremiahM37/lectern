package api

import (
	"os"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/testutil/lecternbin"
)

// A handoff polls for the agent's wrap every few seconds in production. Test
// agents write theirs at once, so poll quickly rather than spend three
// seconds per handoff test waiting for the first check.
//
// The test binary also stands in for lectern on the local target, for the
// tests that run the PTY host (lecternbin).
func TestMain(m *testing.M) {
	lecternbin.Run()
	sessions.HandoffPollInterval = 50 * time.Millisecond
	os.Exit(m.Run())
}
