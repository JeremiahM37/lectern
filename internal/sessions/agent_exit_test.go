package sessions

import (
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestAgentExitedRules(t *testing.T) {
	launched := &store.Session{Origin: "lectern", Agent: "claude"}
	adopted := &store.Session{Origin: "discovered", Agent: "claude"}
	cases := []struct {
		row  *store.Session
		p    AgentProbe
		want bool
		why  string
	}{
		{launched, AgentProbe{RootArgs: "bash"}, true, "the launch wrapper exec'd its shell"},
		{launched, AgentProbe{RootArgs: "bash -c cd /w && claude; exec bash", Current: "bash"}, false, "a wrapper still waiting on the agent"},
		{&store.Session{Origin: "lectern", Agent: "shell"}, AgentProbe{RootArgs: "bash"}, false, "a shell session has no agent"},
		{launched, AgentProbe{RootArgs: "bash", Current: "claude", TTYArgs: []string{"bash", "claude --resume 1234"}}, false, "the agent was started again from the shell"},
		{&store.Session{Origin: "lectern", Agent: "codex"}, AgentProbe{RootArgs: "bash", Current: "codex", TTYArgs: []string{"bash", "/usr/local/bin/codex resume"}}, false, "codex resumed at the prompt"},
		{&store.Session{Origin: "lectern", Agent: "opencode"}, AgentProbe{RootArgs: "bash", Current: "node", TTYArgs: []string{"bash", "node /home/u/.local/bin/opencode"}}, false, "a catalog agent resumed at the prompt"},
		{&store.Session{Origin: "lectern", Agent: "opencode"}, AgentProbe{RootArgs: "bash", Current: "bash", TTYArgs: []string{"bash"}}, true, "a catalog agent that is really gone"},
		{adopted, AgentProbe{Current: "zsh", TTYArgs: []string{"-zsh"}}, true, "only the user's shell is left"},
		{adopted, AgentProbe{Current: "zsh", TTYArgs: []string{"-zsh", "node /usr/bin/claude --model opus"}}, false, "the agent is still on the terminal"},
		{adopted, AgentProbe{Current: "node", TTYArgs: []string{"-zsh"}}, false, "the foreground is not a shell"},
	}
	for _, c := range cases {
		if got := AgentExited(c.row, c.p); got != c.want {
			t.Errorf("%s: got %v", c.why, got)
		}
	}
}

func TestAgentProbeRoundTripsThroughTheMock(t *testing.T) {
	mock := executor.NewMock(time.Millisecond)
	mock.Run(t.Context(), "tmux new-session -d -s lec-s1 -- bash -c 'cd /w && claude; exec bash'", executor.RunOpts{})
	names := []string{"lec-s1", "gone"}
	r, _ := mock.Run(t.Context(), buildAgentProbe(names), executor.RunOpts{})
	probes, ok := parseAgentProbe(r.Stdout, names)
	if !ok || AgentExited(&store.Session{Origin: "lectern", Agent: "claude"}, probes["lec-s1"]) {
		t.Fatalf("running agent: ok=%v %#v", ok, probes)
	}
	if _, seen := probes["gone"]; seen {
		t.Fatal("a missing pane must carry no probe")
	}
	mock.ExitPaneAgent("lec-s1")
	r, _ = mock.Run(t.Context(), buildAgentProbe(names), executor.RunOpts{})
	probes, ok = parseAgentProbe(r.Stdout, names)
	if !ok || !AgentExited(&store.Session{Origin: "lectern", Agent: "claude"}, probes["lec-s1"]) {
		t.Fatalf("exited agent: ok=%v %#v", ok, probes)
	}
	if _, ok := parseAgentProbe(strings.TrimSuffix(r.Stdout, PollEnd+"\n"), names); ok {
		t.Fatal("a truncated answer must be rejected")
	}
}

func TestMatchAdoptedConversationNeedsOneCandidateNearTheLoss(t *testing.T) {
	last, ended := 10_000.0, 10_400.0
	near := NativeCandidate{ID: "a", Modified: last - 30}
	far := NativeCandidate{ID: "b", Modified: last - 5000}
	if got := MatchAdoptedConversation([]NativeCandidate{near, far}, last, ended, nil); got != "a" {
		t.Fatalf("one near candidate: %q", got)
	}
	also := NativeCandidate{ID: "c", Modified: last + 60}
	if got := MatchAdoptedConversation([]NativeCandidate{near, also}, last, ended, nil); got != "" {
		t.Fatalf("two near candidates must not guess: %q", got)
	}
	if got := MatchAdoptedConversation([]NativeCandidate{near, also}, last, ended, map[string]bool{"c": true}); got != "a" {
		t.Fatalf("a conversation bound elsewhere is not a candidate: %q", got)
	}
	if got := MatchAdoptedConversation([]NativeCandidate{far}, last, ended, nil); got != "" {
		t.Fatalf("nothing near the loss: %q", got)
	}
}
