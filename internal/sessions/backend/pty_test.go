package backend

import (
	"errors"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The PTY backend speaks the same tmux language through `lectern pty`, so
// every command is the tmux one with the program word replaced — except the
// few it answers natively.
func TestPtyCommandsAreTmuxLanguageThroughLectern(t *testing.T) {
	p := Pty("/opt/lec tern/lectern")
	prog := "'/opt/lec tern/lectern' pty"
	for _, tc := range []struct{ got, want string }{
		{p.NewSession(NewSession{Name: "lec-s4", Dir: "/w", Argv: "bash -c x", ExtendedKeys: true}),
			prog + " new-session -d -s lec-s4 -c /w -- bash -c x"},
		{p.HasSession(Exact("lec-3"), true), prog + " has-session -t =lec-3 2>/dev/null"},
		{p.KillIf(Pane("a"), "#{==:#{@x},1}", Exact("a")), prog + ` if-shell -F -t =a: '#{==:#{@x},1}' 'kill-session -t =a'`},
		{p.CapturePane(Pane("a"), 500, true), prog + " capture-pane -p -t =a: -S -500 -J"},
		{p.SendText(Pane("a"), "/tmp/s"), prog + " paste-file -t =a: /tmp/s && " + prog + " send-keys -t =a: Enter && rm -f /tmp/s"},
		{p.Poll([]string{"a", "b c"}, 40), prog + " poll -S 40 -- a 'b c'"},
		{p.AgentProbe([]string{"a"}), AgentProbeMarker + " " + prog + " probe a"},
	} {
		if tc.got != tc.want {
			t.Errorf("got  %s\nwant %s", tc.got, tc.want)
		}
	}
	if _, ok := p.Discover(); ok {
		t.Fatal("the PTY host has nothing started by hand to discover")
	}
	if got := strings.Join(p.AttachArgv("lec-7", true), " "); got != "/opt/lec tern/lectern pty attach-session -t =lec-7" {
		t.Fatalf("attach = %q", got)
	}
	if got := p.ShellArgv("lec-sh1", "/w", true); got[2] != "new-session" || got[3] != "-A" {
		t.Fatalf("shell = %q", got)
	}
}

func TestResolverPicksABackendPerTarget(t *testing.T) {
	tmuxThere := func(string) (string, error) { return "/usr/bin/tmux", nil }
	noTmux := func(string) (string, error) { return "", errors.New("not found") }
	none := func(string) bool { return false }
	local := &store.Target{Kind: "local"}
	for _, tc := range []struct {
		name, setting, goos string
		look                func(string) (string, error)
		holds               func(string) bool
		want                string
	}{
		{"linux with tmux", "auto", "linux", tmuxThere, none, NameTmux},
		{"linux without tmux", "auto", "linux", noTmux, none, NamePty},
		{"macOS", "auto", "darwin", tmuxThere, none, NamePty},
		{"windows", "auto", "windows", noTmux, none, NamePty},
		{"macOS keeps running tmux sessions", "auto", "darwin", tmuxThere, func(b string) bool { return b == NameTmux }, NameTmux},
		{"linux keeps running pty sessions", "auto", "linux", tmuxThere, func(b string) bool { return b == NamePty }, NamePty},
		// 2026-10-01: a stray PTY host must not move a machine whose
		// sessions live in tmux.
		{"linux with both keeps tmux", "auto", "linux", tmuxThere, func(string) bool { return true }, NameTmux},
		{"forced tmux", "tmux", "darwin", tmuxThere, none, NameTmux},
		{"forced pty", "pty", "linux", tmuxThere, none, NamePty},
	} {
		r := Resolver{Setting: tc.setting, Self: "/bin/lectern", GOOS: tc.goos, LookPath: tc.look, HoldsSessions: tc.holds}
		if got := r.Env(local, nil); got.SessionBackend != tc.want || got.Lectern != "/bin/lectern" || got.Helpers != nil {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	// Without a binary to run (tests), nothing changes: tmux and Python.
	if got := (Resolver{Setting: "pty"}).Env(local, nil); got.SessionBackend != NameTmux || got.Lectern != "" {
		t.Fatalf("no binary: %+v", got)
	}
	if got := ShellPath(`C:\Users\me\lectern.exe`, "windows"); got != "C:/Users/me/lectern.exe" {
		t.Fatalf("windows path = %q", got)
	}
}

func TestResolverReadsARemoteTargetsProbe(t *testing.T) {
	r := Resolver{Setting: "auto", GOOS: "linux"}
	withTmux := &store.Target{Kind: "ssh", InfoJSON: `{"tmux":"tmux 3.4","lectern":"/usr/local/bin/lectern\npty\nhelper:workspace-files\n"}`}
	got := r.Env(withTmux, nil)
	if got.SessionBackend != NameTmux || got.Lectern != "/usr/local/bin/lectern" || strings.Join(got.Helpers, ",") != "workspace-files" {
		t.Fatalf("remote with tmux: %+v", got)
	}
	noTmux := &store.Target{Kind: "ssh", InfoJSON: `{"tmux":null,"lectern":"/usr/local/bin/lectern\npty\n"}`}
	if got := r.Env(noTmux, nil); got.SessionBackend != NamePty {
		t.Fatalf("remote without tmux: %+v", got)
	}
	bare := &store.Target{Kind: "ssh", InfoJSON: `{"tmux":"tmux 3.4","lectern":null}`}
	got = r.Env(bare, nil)
	if got.SessionBackend != NameTmux || got.Lectern != "" || got.Helpers == nil || len(got.Helpers) != 0 {
		t.Fatalf("remote without lectern keeps tmux and Python: %+v", got)
	}
	if b := FromEnv(executor.TargetEnv{SessionBackend: NamePty}); b.Name() != NameTmux {
		t.Fatal("pty without a binary must fall back to tmux")
	}
}

func TestEveryTargetListsTheBackendsItCanDrive(t *testing.T) {
	tmuxThere := func(string) (string, error) { return "/usr/bin/tmux", nil }
	noTmux := func(string) (string, error) { return "", errors.New("not found") }
	none := func(string) bool { return false }
	local := &store.Target{Kind: "local"}
	for _, tc := range []struct {
		name, setting, goos string
		look                func(string) (string, error)
		want                string
	}{
		{"linux with tmux", "auto", "linux", tmuxThere, "tmux,pty"},
		{"linux without tmux", "auto", "linux", noTmux, "pty"},
		{"forced tmux without it", "tmux", "linux", noTmux, "tmux,pty"},
		{"windows", "auto", "windows", tmuxThere, "pty"},
	} {
		r := Resolver{Setting: tc.setting, Self: "/bin/lectern", GOOS: tc.goos, LookPath: tc.look, HoldsSessions: none}
		if got := strings.Join(r.Env(local, nil).Backends, ","); got != tc.want {
			t.Errorf("%s: backends %s, want %s", tc.name, got, tc.want)
		}
	}
	remote := Resolver{Setting: "auto", GOOS: "linux"}
	for info, want := range map[string]string{
		`{"tmux":"tmux 3.4","lectern":"/usr/local/bin/lectern\npty\n"}`: "tmux,pty",
		`{"tmux":null,"lectern":"/usr/local/bin/lectern\npty\n"}`:       "pty",
		`{"tmux":"tmux 3.4","lectern":null}`:                              "tmux",
		`{}`:                                                              "tmux",
	} {
		if got := strings.Join(remote.Env(&store.Target{Kind: "ssh", InfoJSON: info}, nil).Backends, ","); got != want {
			t.Errorf("remote %s: backends %s, want %s", info, got, want)
		}
	}
}

func TestASessionIsDrivenThroughItsOwnBackend(t *testing.T) {
	ex := &envOnly{id: 1}
	executor.SetTargetEnv(ex, executor.TargetEnv{SessionBackend: NamePty, Lectern: "/bin/lectern", Backends: []string{NameTmux, NamePty}})
	if be, ok := ForSession(ex, &store.Session{SessionBackend: NameTmux}); !ok || be.Name() != NameTmux {
		t.Fatal("a tmux session on a target now starting pty sessions must stay on tmux")
	}
	if be, ok := ForSession(ex, &store.Session{}); !ok || be.Name() != NamePty {
		t.Fatal("an unrecorded session starts from the target's backend")
	}
	if others := Others(ex, NamePty); len(others) != 1 || others[0].Name() != NameTmux {
		t.Fatalf("others = %v", others)
	}
	// A PTY session on a target whose lectern binary is no longer known
	// cannot be driven, and is not handed to tmux to be called missing.
	bare := &envOnly{id: 2}
	executor.SetTargetEnv(bare, executor.TargetEnv{SessionBackend: NameTmux})
	if _, ok := ForSession(bare, &store.Session{SessionBackend: NamePty}); ok {
		t.Fatal("an undrivable pty session must be reported")
	}
	if SessionOrTarget(bare, &store.Session{SessionBackend: NamePty}).Name() != NameTmux {
		t.Fatal("SessionOrTarget falls back to the target's backend")
	}
}

// envOnly is an executor that only carries a TargetEnv; its size keeps two of
// them distinct map keys.
type envOnly struct {
	executor.Executor
	id int
}
