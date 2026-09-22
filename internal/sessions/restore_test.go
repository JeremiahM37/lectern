package sessions

import (
	"strings"
	"testing"
)

func TestTrackingIdentityRequiresFullRandomMarker(t *testing.T) {
	for _, value := range []string{"", "abc", strings.Repeat("g", 32), strings.Repeat("0", 30), strings.Repeat("0", 34)} {
		if validTrackingIdentity(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	if !validTrackingIdentity("0123456789abcdef0123456789abcdef") {
		t.Fatal("valid marker rejected")
	}
}

func TestRestoreReadsIdentityWithoutCreatingIt(t *testing.T) {
	cmd := trackingIdentityCommand("session's name", "")
	if strings.Contains(cmd, "set-option") || !strings.Contains(cmd, "show-options -qv") || !strings.Contains(cmd, "=session") {
		t.Fatal(cmd)
	}
	seeded := trackingIdentityCommand("test", "0123456789abcdef0123456789abcdef")
	if !strings.Contains(seeded, "set-option -o") || !strings.Contains(seeded, "&& tmux show-options") {
		t.Fatal(seeded)
	}
	// A session that predates the rename must keep the identity it has.
	if strings.Index(seeded, legacyTrackingOption) > strings.Index(seeded, "set-option") {
		t.Fatalf("seeds before reading the legacy option: %s", seeded)
	}
}
