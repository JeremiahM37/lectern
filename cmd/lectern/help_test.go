package main

import (
	"bytes"
	"strings"
	"testing"
)

// Every command a user can type has its own help, and asking for it
// succeeds (clig.dev: --help is never an error).
func TestEveryCommandHasItsOwnHelp(t *testing.T) {
	internal := map[string]bool{"autonomy-overlay": true, "autonomy-overlay-inspect": true, "supervise": true, "status": true, "stop": true}
	var names []string
	for _, set := range []map[string]bool{clientVerbs, reservedVerbs, agentQuickVerbs} {
		for name := range set {
			if !internal[name] {
				names = append(names, name)
			}
		}
	}
	names = append(names, "local status", "local stop", "local claude")
	for _, name := range names {
		for _, flag := range []string{"--help", "-h"} {
			var out, errOut bytes.Buffer
			args := append(strings.Fields(name), flag)
			if name == "help" || name == "--help" || name == "-h" {
				args = []string{name}
			}
			if name == "relay" {
				continue // relay prints its flag set's own help (main.go)
			}
			code, handled := helpCommand(args, &out, &errOut)
			if !handled || code != 0 || !strings.Contains(out.String(), "Usage:") {
				t.Errorf("lectern %s: handled=%v code=%d out=%q err=%q", strings.Join(args, " "), handled, code, out.String(), errOut.String())
			}
		}
	}
}

func TestOverviewIsGroupedAndHelpTopicsWork(t *testing.T) {
	var out, errOut bytes.Buffer
	if code, handled := helpCommand([]string{"help"}, &out, &errOut); !handled || code != 0 {
		t.Fatal("lectern help failed")
	}
	for _, group := range helpGroups {
		if !strings.Contains(out.String(), "\n"+group+"\n") {
			t.Errorf("overview is missing the %q group:\n%s", group, out.String())
		}
	}
	out.Reset()
	if code, _ := helpCommand([]string{"help", "local", "stop"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "lectern local stop") {
		t.Errorf("help local stop: %d %s", code, out.String())
	}
	out.Reset()
	// The local runtime test relies on these lines.
	helpCommand([]string{"local", "--help"}, &out, &errOut)
	if !strings.Contains(out.String(), "lectern local status") || !strings.Contains(out.String(), "lectern local [COMMAND ...]") {
		t.Errorf("local help: %s", out.String())
	}
	if code, _ := helpCommand([]string{"help", "restor"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `Did you mean "lectern restore"?`) {
		t.Errorf("help restor: %d %s", code, errOut.String())
	}
	// Arguments after "--" belong to someone else.
	if _, handled := helpCommand([]string{"api", "POST", "/x", "--", "-h"}, &out, &errOut); handled {
		t.Error("help after -- was intercepted")
	}
}

func TestDidYouMean(t *testing.T) {
	for typo, want := range map[string]string{"restor": "restore", "dcotor": "doctor", "claud": "claude", "stauts": "status"} {
		got := didYouMean(typo)
		if want == "status" {
			// status is only a subcommand of local, never suggested alone.
			if strings.Contains(got, "status") {
				t.Errorf("%s suggested a subcommand: %q", typo, got)
			}
			continue
		}
		if !strings.Contains(got, `"lectern `+want+`"`) {
			t.Errorf("didYouMean(%q) = %q, want %s", typo, got, want)
		}
	}
	if got := didYouMean("xyzzyplugh"); got != "" {
		t.Errorf("unrelated word got a suggestion: %q", got)
	}
}
