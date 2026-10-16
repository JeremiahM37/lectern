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

func TestRemoteTokenNeverPrintsTheWholeToken(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("SCRIBE_TELEGRAM_TOKEN", "")
	const tok = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	m := &Model{}
	if got := m.remoteToken("status"); !strings.Contains(got, "No Telegram bot token") {
		t.Fatalf("empty status = %q", got)
	}
	if got := m.remoteToken("set " + tok); strings.Contains(got, "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDs") {
		t.Fatalf("set reply leaks the token: %q", got)
	}
	if got := m.remoteToken("status"); strings.Contains(got, "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDs") || !strings.Contains(got, "…Dsaw") {
		t.Fatalf("status = %q", got)
	}
	bad := "not-a-token-at-all"
	if got := m.remoteToken("set " + bad); strings.Contains(got, bad) || !strings.Contains(got, "BotFather") {
		t.Fatalf("bad token reply = %q", got)
	}
	if got := m.remoteToken("clear"); !strings.Contains(got, "removed") {
		t.Fatalf("clear = %q", got)
	}
	if got := m.remoteToken("clear"); !strings.Contains(got, "No token was stored") {
		t.Fatalf("second clear = %q", got)
	}
	if got := m.remoteToken("bogus"); !strings.Contains(got, "Usage: /remote token") {
		t.Fatalf("usage = %q", got)
	}
}

func TestRemotePairingsAndLogsReportWhenOff(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	if got := pairingsReport(); !strings.Contains(got, "No chats are paired") {
		t.Fatalf("pairings = %q", got)
	}
	if got := logsReport(); !strings.Contains(got, "No Telegram log lines") {
		t.Fatalf("logs = %q", got)
	}
}

func TestHearthIsRegisteredWithItsVerbs(t *testing.T) {
	c, _ := findCommand("/hearth")
	if c == nil {
		t.Fatal("/hearth is not registered")
	}
	if !strings.Contains(c.Args, "bind") || !strings.Contains(c.Args, "unbind") {
		t.Fatalf("args = %q", c.Args)
	}
}
