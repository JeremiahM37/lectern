package tui

import (
	"strings"
	"testing"
)

func TestRemoteCommandIsRegisteredInSystem(t *testing.T) {
	c, _ := findCommand("/remote")
	if c == nil {
		t.Fatal("/remote is not registered")
	}
	if c.Group != "System" {
		t.Fatalf("group = %q", c.Group)
	}
}

func TestRemoteStatusWhileOffSaysHowToTurnItOn(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	if got := remoteStatus(nil); !strings.Contains(got, "/remote on") {
		t.Fatalf("status = %q", got)
	}
}
