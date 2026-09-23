package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

func testWrapPlan(t *testing.T, argv []string, workspace string) *nativeWrapPlan {
	t.Helper()
	dir := t.TempDir()
	plan, err := newNativeWrapPlan(dir, filepath.Join(dir, "sock"), nativeControls{
		Kind: "session", ID: "17", Base: "http://127.0.0.1:9110", Token: "scoped-secret",
	}, argv, workspace)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// The inner attachment is the agent's own tmux or an SSH client. Its separators
// and spaces must survive as single arguments, and it must not inherit the
// wrapper's TMUX socket or the API credential.
func TestNativeWrapInnerScriptPreservesArguments(t *testing.T) {
	argv := []string{"printf", "%s\n", "path with spaces", ";", "$(touch must-not-run)", "it's quoted"}
	plan := testWrapPlan(t, argv, "")
	if !reflect.DeepEqual(plan.innerArgv, argv) {
		t.Fatalf("plan mutated its argv: %q", plan.innerArgv)
	}
	if !strings.HasPrefix(plan.inner, "#!/bin/sh\nexec env -u TMUX -u LECTERN_AUTH_TOKEN ") {
		t.Fatalf("inner script does not scrub TMUX and the token: %q", plan.inner)
	}
	script := filepath.Join(plan.dir, "attach.sh")
	if err := os.WriteFile(script, []byte(plan.inner), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "/tmp/outer-socket,1,0")
	t.Setenv("LECTERN_AUTH_TOKEN", "scoped-secret")
	out, err := exec.Command("sh", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), strings.Join(argv[2:], "\n")+"\n"; got != want {
		t.Fatalf("changed inner arguments: %q want %q", got, want)
	}
}

func TestNativeWrapConfigBindings(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "")
	for _, want := range []string{
		"set -g prefix C-]",
		"bind-key -T prefix C-] send-prefix",
		"bind-key -T prefix m display-popup",
		"bind-key -T prefix u display-popup",
		"status-left",
	} {
		if !strings.Contains(plan.conf, want) {
			t.Errorf("config is missing %q:\n%s", want, plan.conf)
		}
	}
	// Every credential stays out of the on-disk config and the popup commands.
	for name, body := range map[string]string{"conf": plan.conf, "controls": plan.controlsBody, "upload": plan.uploadBody} {
		if strings.Contains(body, "scoped-secret") || strings.Contains(body, "LECTERN_AUTH_TOKEN=") {
			t.Errorf("%s carries a credential: %s", name, body)
		}
	}
	if !strings.Contains(plan.controlsBody, " controls session 17 --popup") {
		t.Fatalf("controls command: %q", plan.controlsBody)
	}
	if !strings.HasSuffix(strings.TrimSpace(plan.uploadBody), "--popup --action upload") {
		t.Fatalf("upload command: %q", plan.uploadBody)
	}
}

func TestNativeWrapServerEnvScopesTheToken(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "")
	env := plan.serverEnv()
	if !containsEnv(env, "LECTERN_API=http://127.0.0.1:9110") || !containsEnv(env, "LECTERN_AUTH_TOKEN=scoped-secret") {
		t.Fatalf("scoped child env missing credentials: %v", env)
	}
}

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func TestNativeWrapWriteModesAndSocketLayout(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "")
	if err := plan.write(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		plan.confPath:       0o600,
		plan.innerScript:    0o700,
		plan.controlsScript: 0o700,
		plan.uploadScript:   0o700,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	if plan.socket != filepath.Join(plan.dir, "sock") {
		t.Fatalf("private socket is not in its own directory: %s", plan.socket)
	}
	if !strings.Contains(plan.conf, shellq.Quote(plan.controlsScript)) {
		t.Fatalf("popup binding does not point at the generated controls script: %s", plan.conf)
	}
}

func TestNativeWrapWorkspaceUsesOuterPopup(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "/tmp/tmux-1000/default,123,0")
	popup := workspacePopup(plan.clientArgv(), plan.inWorkspace, "Lectern · Ctrl-] m actions")
	if popup[0] != "tmux" || popup[1] != "display-popup" {
		t.Fatalf("no workspace popup: %v", popup)
	}
	if popup[len(popup)-2] != "Lectern · Ctrl-] m actions" {
		t.Fatalf("popup title: %q", popup)
	}
	command := popup[len(popup)-1]
	if !strings.HasPrefix(command, "env -u TMUX ") || !strings.Contains(command, plan.socket) {
		t.Fatalf("popup does not attach this private socket: %q", command)
	}
}

func TestNativeControlsOffHonoursExplicitDisable(t *testing.T) {
	for _, value := range []string{"0", "off", "FALSE", "no", "disabled"} {
		t.Setenv(nativeControlsEnv, value)
		if !nativeControlsOff() {
			t.Errorf("%q did not disable native controls", value)
		}
	}
	for _, value := range []string{"", "1", "on", "yes"} {
		t.Setenv(nativeControlsEnv, value)
		if nativeControlsOff() {
			t.Errorf("%q disabled native controls", value)
		}
	}
}

func TestWithoutEnv(t *testing.T) {
	env := withoutEnv([]string{"TMUX=/tmp/sock", "PATH=/bin", "TMUX=", "HOME=/home/x"}, "TMUX")
	if !reflect.DeepEqual(env, []string{"PATH=/bin", "HOME=/home/x"}) {
		t.Fatalf("unexpected env: %v", env)
	}
}

func TestParseControlsArgs(t *testing.T) {
	kind, id, action, popup, err := parseControlsArgs([]string{"session", "17", "--popup", "--action", "upload"})
	if err != nil || kind != "session" || id != "17" || action != "upload" || !popup {
		t.Fatalf("parsed %q %q %q %v %v", kind, id, action, popup, err)
	}
	if _, _, _, _, err := parseControlsArgs([]string{"session"}); err == nil {
		t.Fatal("accepted a kind without an id")
	}
	if _, _, _, _, err := parseControlsArgs([]string{"attempt", "12", "--action", "explode"}); err == nil {
		t.Fatal("accepted an unknown action")
	}
	if _, _, _, _, err := parseControlsArgs([]string{"session", "17", "--bogus"}); err == nil {
		t.Fatal("accepted an unknown option")
	}
	shellKind, shellID, _, _, err := parseControlsArgs([]string{"attempt-shell", "5"})
	if err != nil || shellKind != "attempt-shell" || shellID != "5" {
		t.Fatalf("shell kinds should resolve to their owning row: %q %q %v", shellKind, shellID, err)
	}
}

func TestValidateControlsTargetRejectsUnsupportedKinds(t *testing.T) {
	if err := validateControlsTarget("target", "3"); err == nil {
		t.Fatal("accepted a target terminal")
	}
	if err := validateControlsTarget("session", "0"); err == nil {
		t.Fatal("accepted a non-positive id")
	}
	if err := validateControlsTarget("project", "9"); err != nil {
		t.Fatalf("rejected a supported kind: %v", err)
	}
}
