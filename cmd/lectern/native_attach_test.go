package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
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

// hostedArgv is the exact shape attachmentCommandAt generates for a remote peer.
func hostedArgv(host, kind, id string) []string {
	return []string{"env", "TERM=xterm-256color", "ssh", "-tt", host, "/usr/local/bin/lectern", "--hosted-attach", "attach", kind, id}
}

// A wrapper-backed client marks only the generated hosted SSH command, copying
// it and leaving the caller's argv untouched. The marker rides in the remote
// command's argv, so it is set on the peer and never in the local environment.
func TestWithClientControlsMarkerCopiesTheHostedShape(t *testing.T) {
	argv := hostedArgv("server.example", "session", "17")
	original := append([]string(nil), argv...)
	got := withClientControlsMarker(argv)
	want := []string{"env", "TERM=xterm-256color", "ssh", "-tt", "server.example", "env", clientControlsEnv + "=1", "/usr/local/bin/lectern", "--hosted-attach", "attach", "session", "17"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("marked argv %q want %q", got, want)
	}
	if !reflect.DeepEqual(argv, original) {
		t.Fatalf("marker leaked into the caller's argv: %q", argv)
	}
}

// Anything that is not the exact generated shape keeps the peer's default
// server-side controls: local tmux attachments, tweaked commands, bad targets.
func TestWithClientControlsMarkerLeavesOtherCommandsAlone(t *testing.T) {
	shapes := map[string][]string{
		"local tmux":       {"tmux", "attach", "-t", "agent"},
		"plain ssh":        {"ssh", "-tt", "server.example", "bash"},
		"other binary":     {"env", "TERM=xterm-256color", "ssh", "-tt", "server.example", "/opt/lectern", "--hosted-attach", "attach", "session", "17"},
		"dash host":        hostedArgv("-oProxyCommand=evil", "session", "17"),
		"blank host":       hostedArgv("", "session", "17"),
		"host with space":  hostedArgv("two hosts", "session", "17"),
		"task kind":        hostedArgv("server.example", "task", "17"),
		"unsupported kind": hostedArgv("server.example", "widget", "17"),
		"zero id":          hostedArgv("server.example", "session", "0"),
		"non-numeric id":   hostedArgv("server.example", "session", "abc"),
		"truncated":        hostedArgv("server.example", "session", ""),
	}
	for name, argv := range shapes {
		if got := withClientControlsMarker(argv); !reflect.DeepEqual(got, argv) {
			t.Errorf("%s: rewrote a non-hosted command: %q", name, got)
		}
	}
}

// The resolver is shared by wrapper-backed and direct clients, so it must keep
// the peer's controls on by default: only the wrapper adds the marker later.
func TestAttachmentCommandAtLeavesControlsToThePeer(t *testing.T) {
	argv, err := attachmentCommandAt(&config.Config{}, []string{"session", "17"}, "", "server.example")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(argv, hostedArgv("server.example", "session", "17")) {
		t.Fatalf("unexpected resolver argv: %q", argv)
	}
	for _, word := range argv {
		if strings.Contains(word, clientControlsEnv) {
			t.Fatalf("resolver set the client-controls marker: %q", argv)
		}
	}
}

// Creating an actual private wrapper is what applies the marker to its inner
// attachment, and the inner script keeps it inside the ssh argv.
func TestNativeWrapMarksItsHostedInnerCommand(t *testing.T) {
	plan := testWrapPlan(t, hostedArgv("server.example", "session", "17"), "")
	want := []string{"env", "TERM=xterm-256color", "ssh", "-tt", "server.example", "env", clientControlsEnv + "=1", "/usr/local/bin/lectern", "--hosted-attach", "attach", "session", "17"}
	if !reflect.DeepEqual(plan.innerArgv, want) {
		t.Fatalf("wrapper inner argv: %q", plan.innerArgv)
	}
	if !strings.Contains(plan.inner, clientControlsEnv+"=1") {
		t.Fatalf("inner script does not carry the marker: %q", plan.inner)
	}
	if !strings.HasPrefix(plan.inner, "#!/bin/sh\nexec env -u TMUX -u LECTERN_AUTH_TOKEN ") {
		t.Fatalf("inner script lost its scrubbing prefix: %q", plan.inner)
	}
}

// The marker is consumed once and cleared so a client started later from this
// environment cannot inherit the wrapper's decision.
func TestTakeClientControlsClearsTheMarker(t *testing.T) {
	t.Setenv(clientControlsEnv, "1")
	if !takeClientControls() {
		t.Fatal("marker was not honoured")
	}
	if got := os.Getenv(clientControlsEnv); got != "" {
		t.Fatalf("marker survived: %q", got)
	}
	if takeClientControls() {
		t.Fatal("cleared marker still counted as set")
	}
}

func TestClientControlsMarkedHonoursOnlyTruthyValues(t *testing.T) {
	for _, value := range []string{"1", "on", "TRUE", "yes", "enabled"} {
		t.Setenv(clientControlsEnv, value)
		if !clientControlsMarked() {
			t.Errorf("%q did not mark client controls", value)
		}
	}
	for _, value := range []string{"", "0", "off", "false", "no", "disabled", "2"} {
		t.Setenv(clientControlsEnv, value)
		if clientControlsMarked() {
			t.Errorf("%q marked client controls", value)
		}
	}
}

// A narrow client clips the status row from the right, so the primary shortcut
// must lead it and the window list must not crowd it out.
func TestNativeWrapStatusLineLeadsWithTheShortcut(t *testing.T) {
	plan := testWrapPlan(t, []string{"tmux", "attach", "-t", "agent"}, "")
	for _, want := range []string{
		"set -g status-position top",
		"set -g status-left-length 44",
		"set -g window-status-format ''",
		"set -g window-status-current-format ''",
		"set -g status-left '#[bold]Ctrl+] m#[default] controls · Ctrl-b d detach '",
	} {
		if !strings.Contains(plan.conf, want) {
			t.Errorf("config is missing %q:\n%s", want, plan.conf)
		}
	}
	if strings.Contains(plan.conf, "Lectern#[default] Ctrl+] then m") {
		t.Fatalf("status line still leads with the long prefix:\n%s", plan.conf)
	}
}
