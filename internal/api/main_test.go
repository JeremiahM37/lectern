package api

import (
	"os"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// A handoff polls for the agent's wrap every few seconds in production. Test
// agents write theirs at once, so poll quickly rather than spend three
// seconds per handoff test waiting for the first check.
func TestMain(m *testing.M) {
	sessions.HandoffPollInterval = 50 * time.Millisecond
	os.Exit(m.Run())
}
