package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func launcher() specLauncher { return specLauncher{} }

// specLauncher keeps the old call shape while going through the real spec path,
// so these tests exercise what production runs.
type specLauncher struct{}

func (specLauncher) LaunchCommand(agent, workdir, tmuxName, model string,
	resume bool, prompt string) string {
	spec, ok := Find(Builtins(), agent)
	if !ok {
		return ""
	}
	return spec.LaunchCommand(Start{Workdir: workdir, TmuxName: tmuxName,
		Model: model, Resume: resume, Prompt: prompt})
}

func TestLaunchCommandIsInteractiveNotHeadless(t *testing.T) {
	cmd := launcher().LaunchCommand("claude", "/srv/repo", "lec-s7", "opus", false, "")
	if !strings.HasPrefix(cmd, "tmux new-session -d -s lec-s7 ") {
		t.Fatalf("prefix: %s", cmd)
	}
	// the whole point of a session is that a human is at the keyboard: no -p,
	// no stream-json, no exit-code file
	for _, unwanted := range []string{" -p ", "stream-json", "exit_code", "prompt.md"} {
		if strings.Contains(cmd, unwanted) {
			t.Errorf("interactive launch must not carry %q: %s", unwanted, cmd)
		}
	}
	if !strings.Contains(cmd, "cd /srv/repo") || !strings.Contains(cmd, "--model opus") {
		t.Errorf("launch: %s", cmd)
	}
	// the pane survives the agent exiting, so the scrollback is still there
	if !strings.Contains(cmd, "exec bash") {
		t.Errorf("pane should outlive the agent: %s", cmd)
	}
}

func TestLaunchCommandResumesPerAgent(t *testing.T) {
	if got := launcher().LaunchCommand("claude", "/r", "s", "", true, ""); !strings.Contains(got, "--continue") {
		t.Errorf("claude resume: %s", got)
	}
	if got := launcher().LaunchCommand("codex", "/r", "s", "", true, ""); !strings.Contains(got, "resume --last") {
		t.Errorf("codex resume: %s", got)
	}
	if got := launcher().LaunchCommand("claude", "/r", "s", "", false, ""); strings.Contains(got, "--continue") {
		t.Errorf("a fresh session must not resume: %s", got)
	}
}

func TestInteractiveMCPArgsSurviveResumeAndFork(t *testing.T) {
	spec, ok := Find(Builtins(), "codex")
	if !ok {
		t.Fatal("codex builtin missing")
	}
	args := []string{"-c", `mcp_servers.ops_tools.command="python3"`}
	for _, start := range []Start{
		{Workdir: "/r", TmuxName: "fresh", ToolArgs: args},
		{Workdir: "/r", TmuxName: "resume", ResumeID: "session-1", ToolArgs: args},
		{Workdir: "/r", TmuxName: "fork", ForkID: "session-1", ToolArgs: args},
	} {
		cmd := spec.LaunchCommand(start)
		if !strings.Contains(cmd, `-c`) || !strings.Contains(cmd, `mcp_servers.ops_tools.command="python3"`) {
			t.Errorf("MCP args missing from %s launch: %s", start.TmuxName, cmd)
		}
	}
}

func TestInteractiveMCPArgsReachSyntheticProcess(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "agent")
	// The argv file appears only once it is complete: the shell opens the
	// redirect target before printf writes a byte, and a reader that polls for
	// existence can see the empty file in between (it did, on CI).
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shellq.Quote(log+".tmp")+" && mv "+shellq.Quote(log+".tmp")+" "+shellq.Quote(log)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Command: bin, ResumeIDArgs: []string{"resume", "{id}"}, ForkArgs: []string{"fork", "{id}"}}
	args := []string{"-c", `mcp_servers.ops_tools.command="python3"`}
	for _, tc := range []struct {
		name  string
		start Start
		want  []string
	}{
		{"fresh", Start{Workdir: dir, TmuxName: "lec-mcp-fresh", ToolArgs: args}, []string{"-c", `mcp_servers.ops_tools.command="python3"`}},
		{"resume", Start{Workdir: dir, TmuxName: "lec-mcp-resume", ResumeID: "s1", ToolArgs: args}, []string{"-c", `mcp_servers.ops_tools.command="python3"`, "resume", "s1"}},
		{"fork", Start{Workdir: dir, TmuxName: "lec-mcp-fork", ForkID: "s1", ToolArgs: args}, []string{"-c", `mcp_servers.ops_tools.command="python3"`, "fork", "s1"}},
	} {
		_ = exec.Command("tmux", "kill-session", "-t", tc.start.TmuxName).Run()
		_ = os.Remove(log)
		if err := exec.Command("bash", "-c", spec.LaunchCommand(tc.start)).Run(); err != nil {
			t.Fatalf("%s launch: %v", tc.name, err)
		}
		var raw []byte
		var err error
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
			raw, err = os.ReadFile(log)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s argv: %v", tc.name, err)
		}
		got := strings.Fields(string(raw))
		if len(got) != len(tc.want) {
			t.Fatalf("%s argv: got %q want %q", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s argv: got %q want %q", tc.name, got, tc.want)
			}
		}
		_ = exec.Command("tmux", "kill-session", "-t", tc.start.TmuxName).Run()
	}
}

func TestLaunchCommandQuotesHostileWorkdirs(t *testing.T) {
	cmd := launcher().LaunchCommand("claude", "/srv/'; rm -rf /; '", "s", "", false, "")
	if strings.Count(cmd, "rm -rf /") != 1 || !strings.Contains(cmd, `'\''`) {
		t.Fatalf("hostile path was not quoted as one word: %s", cmd)
	}
}

func TestPollBatchesEverySessionIntoOneCommand(t *testing.T) {
	// one exec per target per tick, not one per session — over SSH the round
	// trip is what costs, not the capture
	cmd := PollCommand([]string{"lec-s1", "lec-s2", "lec-s3"})
	if n := strings.Count(cmd, "capture-pane"); n != 3 {
		t.Fatalf("expected 3 captures in one command, got %d", n)
	}
	for _, name := range []string{"lec-s1", "lec-s2", "lec-s3"} {
		if !strings.Contains(cmd, name) {
			t.Errorf("missing %s", name)
		}
	}
}

func TestParsePollSplitsPanesBackApart(t *testing.T) {
	raw := PollDelimiter + "lec-s1\nhello\nworld" + PollDelimiter + "lec-s2\nother"
	panes := ParsePoll(raw)
	if panes["lec-s1"] != "hello\nworld" {
		t.Errorf("first pane: %q", panes["lec-s1"])
	}
	if panes["lec-s2"] != "other" {
		t.Errorf("second pane: %q", panes["lec-s2"])
	}
}

func TestDeriveStatus(t *testing.T) {
	// an explicit interrupt hint is the agent telling us it is mid-turn, and it
	// beats every other signal
	if got := DeriveStatus("thinking...\n✻ Working… (esc to interrupt)", Hash("x")); got != StatusRunning {
		t.Errorf("busy marker: %s", got)
	}
	// a pane that moved is working, whatever it printed
	if got := DeriveStatus("some new output", "stale-hash"); got != StatusRunning {
		t.Errorf("changed pane: %s", got)
	}
	// unchanged and sitting at a prompt: it wants you
	prompt := "done.\n\n❯ "
	if got := DeriveStatus(prompt, Hash(prompt)); got != StatusWaiting {
		t.Errorf("prompt: %s", got)
	}
	// Aider renders a bare > followed by blank tmux viewport rows. The prompt
	// must still be visible to the priming path after those rows are captured.
	aiderPrompt := "Aider v0.86.3.dev53+g5dc9490bb\nModel: openai/compat-fixture with whole edit format\nGit repo: none\nRepo-map: disabled\n>\n\n\n\n\n\n\n\n\n\n\n\n\n\n"
	if got := DeriveStatus(aiderPrompt, Hash(aiderPrompt)); got != StatusWaiting {
		t.Errorf("bare Aider prompt: %s", got)
	}
	busyAiderPrompt := "Aider\nEsc to interrupt\n>\n" + strings.Repeat("\n", 30)
	if got := DeriveStatus(busyAiderPrompt, Hash(busyAiderPrompt)); got != StatusRunning {
		t.Errorf("busy Aider prompt with viewport padding: %s", got)
	}
	if got := DeriveStatus(aiderPrompt, "stale-hash"); got != StatusRunning {
		t.Errorf("changed Aider pane: %s", got)
	}
	// unchanged and unrecognisable: say idle rather than guess
	quiet := "some output with no prompt shape at all"
	if got := DeriveStatus(quiet, Hash(quiet)); got != StatusIdle {
		t.Errorf("quiet: %s", got)
	}
	if got := DeriveStatus("   \n ", ""); got != StatusStarting {
		t.Errorf("empty pane: %s", got)
	}
}

func TestContextPctIsReadNotInvented(t *testing.T) {
	pane := "footer · Context left until auto-compact: 17%"
	pct := ContextPct(pane)
	if pct == nil || *pct != 17 {
		t.Fatalf("got %v", pct)
	}
	// agents only surface the gauge when it starts to matter; absent must stay
	// absent rather than becoming a made-up number
	if got := ContextPct("no gauge here"); got != nil {
		t.Fatalf("expected nil, got %v", *got)
	}
	if got := ContextPct("Context left until auto-compact: 900%"); got != nil {
		t.Fatal("an impossible reading must be rejected")
	}
}

func TestPreviewKeepsTheTail(t *testing.T) {
	pane := "one\n\ntwo\n   \nthree\nfour"
	got := Preview(pane, 2)
	if got != "three\nfour" {
		t.Fatalf("preview: %q", got)
	}
}

func TestSendTextGoesThroughABufferNotSendKeys(t *testing.T) {
	// send-keys -l would re-interpret newlines as submissions and quotes as
	// shell syntax; a buffer paste delivers the text exactly as written
	cmd := SendTextCommand("lec-s2", "/tmp/stage")
	for _, want := range []string{"load-buffer", "paste-buffer", "send-keys -t =lec-s2: Enter", "rm -f"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q: %s", want, cmd)
		}
	}
}

func TestSendKeyIsAnAllowlist(t *testing.T) {
	if _, ok := SendKeyCommand("s", "escape"); !ok {
		t.Error("escape is how you interrupt a turn")
	}
	if _, ok := SendKeyCommand("s", "rm -rf /"); ok {
		t.Fatal("this is a raw input channel — only named keys may pass")
	}
}

// Discovery is the half a task board misses: the sessions you care about most
// were started by hand, in a terminal, weeks ago.
func TestParseDiscoverJoinsPanesToProcessesByTTY(t *testing.T) {
	out := "claude-2\t/dev/pts/13\t/home/me/proj\n" +
		"plain-shell\t/dev/pts/4\t/home/me\n" +
		DiscoverDelimiter +
		"pts/13   claude --continue --model opus\n" +
		"pts/4    bash\n" +
		"pts/99   vim notes.md\n"
	got := ParseDiscover(out)
	if len(got) != 1 {
		t.Fatalf("expected one agent, got %+v", got)
	}
	c := got[0]
	if c.TmuxSession != "claude-2" || c.Agent != "claude" || c.Model != "opus" {
		t.Errorf("candidate: %+v", c)
	}
	if c.Workdir != "/home/me/proj" {
		t.Errorf("workdir: %q", c.Workdir)
	}
}

// pane_current_command reports the login shell for an agent started from one,
// which is exactly why discovery joins on the tty instead.
func TestParseDiscoverFindsAgentsUnderALoginShell(t *testing.T) {
	out := "wrapped\t/dev/pts/1\t/srv/x\n" + DiscoverDelimiter +
		"pts/1    bash -lc /home/me/.local/bin/claude --dangerously-skip-permissions\n" +
		"pts/1    /home/me/.local/bin/claude --dangerously-skip-permissions\n"
	got := ParseDiscover(out)
	if len(got) != 1 || got[0].Agent != "claude" {
		t.Fatalf("candidates: %+v", got)
	}
}

func TestMatchProjectPrefersTheLongestRepo(t *testing.T) {
	repos := map[int64]string{1: "/home/me", 2: "/home/me/projects/app"}
	// a repo nested inside another must win over its parent
	if id, ok := MatchProject("/home/me/projects/app/internal", repos); !ok || id != 2 {
		t.Fatalf("got %d %v", id, ok)
	}
	if id, ok := MatchProject("/home/me/other", repos); !ok || id != 1 {
		t.Fatalf("got %d %v", id, ok)
	}
	if _, ok := MatchProject("/srv/elsewhere", repos); ok {
		t.Fatal("an unrelated path must not be claimed by a project")
	}
	// a prefix that is not a path boundary is not a match
	if _, ok := MatchProject("/home/mexico", map[int64]string{1: "/home/me"}); ok {
		t.Fatal("/home/me must not claim /home/mexico")
	}
}

func TestHandoffPromptAsksForAFileNotAChatReply(t *testing.T) {
	p := HandoffPrompt("/tmp/lectern-handoff-4.md")
	if !strings.Contains(p, "/tmp/lectern-handoff-4.md") {
		t.Error("the path must be explicit")
	}
	if !strings.Contains(p, "do not print it here") {
		t.Error("a wrap printed into the transcript is not durable")
	}
	for _, section := range []string{"WHERE WE ARE", "NEXT", "DECISIONS", "GOTCHAS", "STATE"} {
		if !strings.Contains(p, section) {
			t.Errorf("prompt is missing the %s section", section)
		}
	}
}

func TestResumePromptCarriesTheWrapAndHoldsOff(t *testing.T) {
	p := ResumePrompt("sglang", "we were mid-refactor", "known: the fan is loud")
	if !strings.Contains(p, "sglang") || !strings.Contains(p, "we were mid-refactor") {
		t.Errorf("prompt: %s", p)
	}
	if !strings.Contains(p, "known: the fan is loud") {
		t.Error("project knowledge should reach the successor too")
	}
	// a fresh context confirming state before changing things is the whole
	// safety property of a handoff
	if !strings.Contains(p, "Do not start changing things") {
		t.Error("the successor must confirm state before acting")
	}
}

func TestIdleForFallsBackToCreation(t *testing.T) {
	s := &store.Session{CreatedAt: store.Now() - 30}
	if d := IdleFor(s); d < 25*time.Second || d > 40*time.Second {
		t.Fatalf("a session that never moved has been idle since it started: %s", d)
	}
	recent := store.Now() - 5
	s.LastActivityAt = &recent
	if d := IdleFor(s); d > 10*time.Second {
		t.Fatalf("idle should track last activity: %s", d)
	}
}

// The mock reproduces the wire format by hand because the sessions package
// depends on the executor package, not the other way round. If these ever drift,
// every session test would pass against a protocol the real target never speaks.
func TestMockDelimitersMatchSessions(t *testing.T) {
	m := executor.NewMock(0)
	_ = m
	if executor.MockPollEnd != PollEnd {
		t.Fatal("poll footer drifted")
	}
	if executor.MockPaneDelimiter != PollDelimiter {
		t.Errorf("pane delimiter drifted: %q vs %q", executor.MockPaneDelimiter, PollDelimiter)
	}
	if executor.MockDiscoverDelimiter != DiscoverDelimiter {
		t.Errorf("discover delimiter drifted: %q vs %q",
			executor.MockDiscoverDelimiter, DiscoverDelimiter)
	}
}

// A delimiter travels inside a shell command line. exec() truncates argv at the
// first NUL, so a NUL delimiter silently cuts the command in half on a real
// target — while every mock test still passes.
func TestDelimitersSurviveAShellCommandLine(t *testing.T) {
	for name, d := range map[string]string{
		"poll": PollDelimiter, "discover": DiscoverDelimiter,
	} {
		if strings.ContainsRune(d, 0) {
			t.Errorf("%s delimiter contains a NUL: exec would truncate the command", name)
		}
		if strings.ContainsAny(d, "\n'\"\\") {
			t.Errorf("%s delimiter contains shell-significant characters: %q", name, d)
		}
	}
	// and the built commands must be NUL-free end to end
	if strings.ContainsRune(PollCommand([]string{"a", "b"}), 0) {
		t.Error("the poll command carries a NUL")
	}
	if strings.ContainsRune(DiscoverCommand(), 0) {
		t.Error("the discover command carries a NUL")
	}
}

func TestParseTimesReadsTmuxsOwnClock(t *testing.T) {
	created, activity, ok := ParseTimes(" 1788336741 1788733986 \n")
	if !ok || created != 1788336741 || activity != 1788733986 {
		t.Fatalf("got %v %v %v", created, activity, ok)
	}
	for _, bad := range []string{"", "not numbers", "1788336741"} {
		if _, _, ok := ParseTimes(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// An opening message rides on the command line, not a timed paste: the paste is
// a race against whatever the CLI shows first, and codex once answered its own
// self-update prompt with it.
func TestLaunchCommandCarriesTheOpeningPrompt(t *testing.T) {
	cmd := launcher().LaunchCommand("codex", "/srv/repo", "lec-s9", "", false,
		"read HANDOFF.md and tell me where we are")
	if !strings.Contains(cmd, "read HANDOFF.md and tell me where we are") {
		t.Fatalf("prompt missing: %s", cmd)
	}
	// still interactive — no -p, no exec-and-exit
	if strings.Contains(cmd, " -p ") || strings.Contains(cmd, "exec --json") {
		t.Errorf("a session must stay interactive: %s", cmd)
	}
	// a hostile prompt stays one argument
	evil := launcher().LaunchCommand("claude", "/r", "s", "", false, "'; rm -rf /; '")
	if strings.Count(evil, "rm -rf /") != 1 || !strings.Contains(evil, `'\''`) {
		t.Errorf("prompt was not quoted as one word: %s", evil)
	}
	// resume replays a conversation; the CLIs take no opening message with it
	claude, _ := Find(Builtins(), "claude")
	codex, _ := Find(Builtins(), "codex")
	gemini, _ := Find(Builtins(), "gemini")
	if !claude.PromptArg || !codex.PromptArg {
		t.Error("claude and codex both accept a positional prompt")
	}
	if gemini.PromptArg {
		t.Error("gemini is not known to, so it must fall back to typing")
	}
}

// The set of agents is the operator's, not a constant here. Any CLI that can be
// started in a terminal should be startable from the board.
func TestCustomAgentsMergeOverTheBuiltins(t *testing.T) {
	raw := `[{"name":"aider","command":"aider","model_flag":"--model","prompt_arg":true},
	         {"name":"claude","command":"/opt/claude/bin/claude","model_flag":"--model",
	          "resume_args":["--continue"],"prompt_arg":true}]`
	specs := ParseSpecs(raw)

	aider, ok := Find(specs, "aider")
	if !ok || aider.Command != "aider" || aider.Builtin {
		t.Fatalf("custom agent: %+v", aider)
	}
	// same name overrides, so a built-in whose CLI drifted can be corrected
	// without waiting for a release
	claude, _ := Find(specs, "claude")
	if claude.Command != "/opt/claude/bin/claude" || claude.Builtin {
		t.Fatalf("override: %+v", claude)
	}
	// the untouched built-ins survive
	if _, ok := Find(specs, "codex"); !ok {
		t.Error("codex went missing")
	}
	// a corrupt definition must not take the built-ins with it
	if len(ParseSpecs("{not json")) < 3 {
		t.Error("a bad agents setting should degrade to the built-ins")
	}
}

func TestCustomAgentLaunches(t *testing.T) {
	specs := ParseSpecs(`[{"name":"aider","command":"aider","args":["--no-auto-commits"],
	                       "model_flag":"--model","prompt_arg":true,
	                       "env":{"AIDER_DARK_MODE":"1"}}]`)
	spec, _ := Find(specs, "aider")
	env, err := EnvPrefix(map[string]string{"OPENAI_API_BASE": "http://ollama:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := spec.LaunchCommand(Start{Workdir: "/srv/repo", TmuxName: "lec-s3",
		Model: "qwen3.6:35b-a3b", Prompt: "where are we?", EnvPrefix: env})
	for _, want := range []string{
		"aider --no-auto-commits", "--model qwen3.6:35b-a3b", "where are we?",
		"OPENAI_API_BASE=http://ollama:11434/v1", "cd /srv/repo", "exec bash",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q:\n%s", want, cmd)
		}
	}
}

// An agent with no model switch must ignore a model rather than invent a flag.
func TestSpecWithoutAModelFlagIgnoresTheModel(t *testing.T) {
	spec := Spec{Name: "x", Command: "x"}
	cmd := spec.LaunchCommand(Start{Workdir: "/r", TmuxName: "s", Model: "opus"})
	if strings.Contains(cmd, "opus") {
		t.Fatalf("a model was passed to a CLI with no model flag: %s", cmd)
	}
}

func TestValidateSpecsRejectsWhatCannotLaunch(t *testing.T) {
	for _, bad := range []string{
		`[{"command":"x"}]`, // no name
		`[{"name":"x"}]`,    // no command
		`[{"name":"a","command":"a"},{"name":"a","command":"b"}]`,                                                         // duplicate
		`[{"name":"a","command":"a","env":{"BAD-NAME":"1"}}]`,                                                             // hostile env key
		`[{"name":"a","command":"a","task":{"prompt_template":"{prompt}","permission_args":{"plan":[]}}}]`,                // empty capability
		`[{"name":"a","command":"a","task":{"prompt_template":"{prompt}","permission_args":{"bypassPermissions":[""]}}}]`, // blank flag
		`{"name":"a"}`, // not a list
	} {
		if err := ValidateSpecs(bad); err == nil {
			t.Errorf("should have been rejected: %s", bad)
		}
	}
	if err := ValidateSpecs(`[{"name":"aider","command":"aider"}]`); err != nil {
		t.Errorf("valid definition rejected: %v", err)
	}
	if err := ValidateSpecs(""); err != nil {
		t.Errorf("empty is valid: %v", err)
	}
}

// A session reaching a local model is the same mechanism a dispatched task uses.
func TestEnvPrefixQuotesAndSorts(t *testing.T) {
	got, err := EnvPrefix(map[string]string{
		"OPENAI_BASE_URL": "http://ollama:11434/v1", "OPENAI_API_KEY": "it's local"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "OPENAI_API_KEY=") {
		t.Errorf("keys should be sorted for a stable command: %s", got)
	}
	if !strings.Contains(got, `'it'\''s local'`) {
		t.Errorf("value not shell-quoted: %s", got)
	}
	if _, err := EnvPrefix(map[string]string{"$(evil)": "x"}); err == nil {
		t.Error("a hostile env name must be rejected")
	}
}
