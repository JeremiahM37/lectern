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

func TestCheckpointAcceptsSessionsNamedBeforeTheRename(t *testing.T) {
	for _, name := range []string{"lec-s12", "adk-s117"} {
		if !checkpointTmuxName.MatchString(name) {
			t.Fatalf("%s rejected", name)
		}
	}
	for _, name := range []string{"lec-sh3", "adk-", "s12", "lec-s12x"} {
		if checkpointTmuxName.MatchString(name) {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestStopConditionAcceptsTheIdentityUnderEitherOptionName(t *testing.T) {
	// A session started before the rename carries only the old option; the
	// stop and archive conditions must still recognise its identity, or every
	// such session becomes unstoppable after an upgrade.
	c := trackingCondition("0123456789abcdef0123456789abcdef")
	for _, want := range []string{trackingOption, legacyTrackingOption, "#{==:", ",0123456789abcdef0123456789abcdef}"} {
		if !strings.Contains(c, want) {
			t.Fatalf("condition lacks %q: %s", want, c)
		}
	}
}
