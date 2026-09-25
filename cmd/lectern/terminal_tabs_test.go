package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalTabScriptQuotesContextAndPreservesArguments(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "client's executable")
	evidence := filepath.Join(dir, "evidence")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$LECTERN_API\" \"$LECTERN_AUTH_TOKEN\" \"$LECTERN_ATTACH_HOST\" \"${TMUX-unset}\" \"$@\" > \"$EVIDENCE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launchDir := filepath.Join(dir, "launch")
	os.Mkdir(launchDir, 0700)
	script := filepath.Join(launchDir, "open.sh")
	api := "http://localhost:9110/$(touch never)'"
	token := "private'$(touch never)"
	body := terminalTabScript(executable, api, token, "ssh-alias", "session", "42", script, launchDir)
	os.WriteFile(script, []byte(body), 0700)
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(), "EVIDENCE="+evidence, "TMUX=old-workspace")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	got, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.Join([]string{api, token, "ssh-alias", "unset", "attach", "session", "42", ""}, "\n") {
		t.Fatalf("arguments changed: %q", got)
	}
	if _, err := os.Stat(launchDir); !os.IsNotExist(err) {
		t.Fatal("credential launch script retained")
	}
	for _, id := range []string{"", "1", "$1;kill-server", "$-1", "$a"} {
		if validWorkspaceSession(id) {
			t.Fatal("invalid tmux identity", id)
		}
	}
	if !validWorkspaceSession("$12") {
		t.Fatal("valid tmux identity refused")
	}
}

func TestWorkspaceCommandsPinTheOwningSocket(t *testing.T) {
	if got := workspaceSocket("/tmp/path,with,commas/socket,123,0"); got != "/tmp/path,with,commas/socket" {
		t.Fatal(got)
	}
	argv := workspacePopup([]string{"printf", "test"}, "/tmp/private/socket,123,0", "test")
	if len(argv) < 4 || argv[1] != "-S" || argv[2] != "/tmp/private/socket" || argv[3] != "display-popup" {
		t.Fatal("popup could reach default tmux server", argv)
	}
	p := &nativeWrapPlan{controls: nativeControls{TabView: true}}
	if !strings.Contains(p.tmuxConfig(), "Ctrl+] d close tab") || strings.Contains(p.tmuxConfig(), "Ctrl-b d detach") {
		t.Fatal("tab hints collide with existing tmux prefix")
	}
}
