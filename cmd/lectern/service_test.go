package main

import (
	"strings"
	"testing"
)

func TestSystemdUnitContent(t *testing.T) {
	unit := systemdUnit("/home/me/.local/bin/lectern", "/home/me/.local/share/lectern/lectern.db")
	for _, want := range []string{
		"ExecStart=/home/me/.local/bin/lectern serve",
		"Environment=LECTERN_HOST=127.0.0.1",
		"Environment=LECTERN_PORT=9110",
		"Environment=LECTERN_DB=/home/me/.local/share/lectern/lectern.db",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestLaunchdPlistContent(t *testing.T) {
	plist := launchdPlist("/opt/homebrew/bin/lectern", "/Users/me/Library/Application Support/lectern/lectern.db", "/Users/me/Library/Application Support/lectern/service.log")
	for _, want := range []string{
		"<string>/opt/homebrew/bin/lectern</string>",
		"<string>serve</string>",
		"<key>LECTERN_HOST</key>",
		"<string>127.0.0.1</string>",
		"RunAtLoad",
		"KeepAlive",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
}

func TestServiceStateDirIsUnderXDGDataHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	got, err := serviceStateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := dir + "/lectern"
	if got != want {
		t.Errorf("serviceStateDir() = %q, want %q", got, want)
	}
}
