package backend

import (
	"strings"
	"testing"
)

// The tmux backend must build exactly the command lines Lectern built before
// the seam existed; these are the strings the call sites used to spell out.
func TestTmuxCommandsAreTheOnesLecternAlwaysSent(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{Tmux.NewSession(NewSession{Name: "lec-s4", Dir: "/w d", Argv: `env A=1 "${SHELL:-/bin/sh}" -i`}),
			`tmux new-session -d -s lec-s4 -c '/w d' -- env A=1 "${SHELL:-/bin/sh}" -i`},
		{Tmux.NewSession(NewSession{Name: "lec-s4", Env: []string{"LECTERN_SETUP_TOKEN=abc"}, Argv: "bash -c 'x'"}),
			`tmux new-session -d -e LECTERN_SETUP_TOKEN=abc -s lec-s4 -- bash -c 'x'`},
		{Tmux.NewSession(NewSession{Name: "lec-12", Shell: "cd /w && run; echo $? > rc"}),
			`tmux new-session -d -s lec-12 'cd /w && run; echo $? > rc'`},
		{Tmux.HasSession(Exact("lec-3"), false), "tmux has-session -t =lec-3"},
		{Tmux.HasSession(Exact("lec-3"), true), "tmux has-session -t =lec-3 2>/dev/null"},
		{Tmux.KillSession(Exact("lec-3"), true), "tmux kill-session -t =lec-3 2>/dev/null || true"},
		{Tmux.KillIf(Pane("lec-s1"), "#{==:#{@x},ab}", Exact("lec-s1")),
			`tmux if-shell -F -t =lec-s1: '#{==:#{@x},ab}' 'kill-session -t =lec-s1'`},
		{Tmux.SendText(Pane("lec-s2"), "/tmp/stage"),
			"tmux load-buffer -b lectern /tmp/stage && tmux paste-buffer -b lectern -t =lec-s2: -d -p && tmux send-keys -t =lec-s2: Enter && rm -f /tmp/stage"},
		{Tmux.SendKeys(Pane("s"), "Escape"), "tmux send-keys -t =s: Escape"},
		{Tmux.CapturePane(Pane("lec-s1"), 200, true), "tmux capture-pane -p -t =lec-s1: -S -200 -J"},
		{Tmux.Display("lec-s1", "#{session_created} #{session_activity}"),
			"tmux display-message -p -t lec-s1 '#{session_created} #{session_activity}'"},
		{Tmux.ShowOption(Pane("a"), "@lectern-tracking-identity"), "tmux show-options -qv -t =a: @lectern-tracking-identity"},
		{Tmux.SetOptionOnce(Pane("a"), "@lectern-tracking-identity", "ff"), "tmux set-option -o -t =a: @lectern-tracking-identity ff"},
		{Tmux.ShowEnvironment(Pane("a"), "LECTERN_SETUP_TOKEN"), "tmux show-environment -t =a: LECTERN_SETUP_TOKEN"},
	} {
		if tc.got != tc.want {
			t.Errorf("got  %s\nwant %s", tc.got, tc.want)
		}
	}
	if got := Tmux.NewSession(NewSession{Name: "x", Shell: "y", ExtendedKeys: true}); !strings.Contains(got, "extended-keys") {
		t.Fatalf("extended keys were not turned on: %s", got)
	}
	if cmd, ok := Tmux.Discover(); !ok || !strings.HasPrefix(cmd, "tmux list-panes -a") {
		t.Fatalf("discover = %q, %v", cmd, ok)
	}
}

func TestTmuxAttachArgv(t *testing.T) {
	got := strings.Join(Tmux.AttachArgv("lec-7", false), " ")
	if got != "tmux attach -t lec-7 ; set-option -w -t =lec-7: window-size latest" {
		t.Fatalf("native attach = %q", got)
	}
	shell := strings.Join(Tmux.ShellArgv("lec-sh1", "/w", false), " ")
	if !strings.HasPrefix(shell, "tmux new-session -A -s lec-sh1 -c /w -- /bin/sh -c") {
		t.Fatalf("shell attach = %q", shell)
	}
	web := Tmux.AttachArgv("lec-7", true)
	if web[0] != "sh" || !strings.Contains(web[2], "extended-keys") || !strings.Contains(web[2], "-T extkeys,hyperlinks") {
		t.Fatalf("web attach = %q", web)
	}
}
