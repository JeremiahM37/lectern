package sessions

import (
	"os"
	"testing"
	"time"
)

// A handoff polls for the agent's wrap every few seconds in production. Test
// agents write theirs at once, so poll quickly rather than spend three
// seconds per handoff test waiting for the first check.
func TestMain(m *testing.M) {
	HandoffPollInterval = 50 * time.Millisecond
	os.Exit(m.Run())
}
