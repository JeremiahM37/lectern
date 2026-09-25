package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestSystemdUnitContent(t *testing.T) {
	t.Setenv("PATH", "/opt/agent tools:/usr/bin")
	unit := systemdUnit("/home/me/agent tools/lectern", "/home/me/state/lectern/local")
	for _, want := range []string{`ExecStart="/home/me/agent tools/lectern" local supervise`, `"XDG_STATE_HOME=/home/me/state"`, `"PATH=/opt/agent tools:/usr/bin"`, "KillMode=process", "Restart=on-failure"} {
		if !strings.Contains(unit, want) {
			t.Errorf("missing %q: %s", want, unit)
		}
	}
	if strings.Contains(unit, "LECTERN_DB") || strings.Contains(unit, "9110") {
		t.Fatal("service must reuse local runtime state and endpoint")
	}
}
func TestLaunchdPlistContent(t *testing.T) {
	t.Setenv("PATH", "/opt/tools&agents:/usr/bin")
	plist := launchdPlist("/opt/tools&agents/lectern", "/Users/me/state/lectern/local", "/tmp/service.log")
	var parsed any
	if err := xml.Unmarshal([]byte(plist), &parsed); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tools&amp;agents", "<string>supervise</string>", "XDG_STATE_HOME", "/Users/me/state", "AbandonProcessGroup"} {
		if !strings.Contains(plist, want) {
			t.Errorf("missing %q: %s", want, plist)
		}
	}
}
func TestServiceStateDirUsesLocalRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	got, err := serviceStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir+"/lectern/local" {
		t.Fatalf("wrong board: %s", got)
	}
}
