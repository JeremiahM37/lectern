package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

func launcher() Launcher {
	return Launcher{ClaudeBin: "claude", CodexBin: "codex", GeminiBin: "gemini"}
}

func mustCommand(t *testing.T, s LaunchSpec) string {
	t.Helper()
	cmd, err := launcher().Command(s)
	if err != nil {
		t.Fatalf("Command(%+v): %v", s, err)
	}
	return cmd
}

func hasAll(t *testing.T, cmd string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(cmd, w) {
			t.Errorf("command missing %q\n%s", w, cmd)
		}
	}
}

func lacks(t *testing.T, cmd string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(cmd, w) {
			t.Errorf("command should not contain %q\n%s", w, cmd)
		}
	}
}

func TestClaudeLaunchShape(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "claude", Worktree: "/wt/task1-a1",
		TmuxSession: "lec-1", PermissionMode: "acceptEdits", Model: "opus"})
	if !strings.HasPrefix(cmd, "tmux new-session -d -s lec-1 ") {
		t.Fatalf("prefix: %s", cmd)
	}
	hasAll(t, cmd, "claude -p", "--permission-mode acceptEdits", "--model opus",
		"stream-json", "exit_code",
		// settings.json ships in EVERY mode: it is the only way to grant a
		// headless run a tool, since there is no prompt to fall back on
		"--settings .lectern/settings.json")

	gated := mustCommand(t, LaunchSpec{Agent: "claude", Worktree: "/wt/x",
		TmuxSession: "lec-2", PermissionMode: "default", ResumeSession: "s-9"})
	hasAll(t, gated, "--settings .lectern/settings.json", "--resume s-9")
}

func TestClaudeMCPFlags(t *testing.T) {
	plain := mustCommand(t, LaunchSpec{Agent: "claude", Worktree: "/wt/x",
		TmuxSession: "lec-3", PermissionMode: "acceptEdits"})
	lacks(t, plain, "--mcp-config", "--strict-mcp-config")

	withMCP := mustCommand(t, LaunchSpec{Agent: "claude", Worktree: "/wt/x",
		TmuxSession: "lec-4", PermissionMode: "acceptEdits",
		MCPConfig: ".lectern/mcp.json", StrictMCP: true})
	hasAll(t, withMCP, "--mcp-config .lectern/mcp.json", "--strict-mcp-config")

	// strict is opt-in: it hides the host's own servers
	loose := mustCommand(t, LaunchSpec{Agent: "claude", Worktree: "/wt/x",
		TmuxSession: "lec-5", PermissionMode: "acceptEdits",
		MCPConfig: ".lectern/mcp.json"})
	lacks(t, loose, "--strict-mcp-config")
}

func TestCodexMCPOverridesPrecedeSubcommand(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "codex", Worktree: "/wt", TmuxSession: "s", PermissionMode: "acceptEdits",
		ExtraArgs: []string{"-c", `mcp_servers."my.server".command="srv"`}})
	if strings.Index(cmd, "codex -c") < 0 || strings.Index(cmd, "exec --json") < strings.Index(cmd, "codex -c") {
		t.Fatalf("codex MCP override: %s", cmd)
	}
	if strings.Contains(cmd, "CODEX_HOME") {
		t.Fatalf("MCP flags must not replace CODEX_HOME: %s", cmd)
	}
}

func TestCodexLaunchFlags(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "codex", Worktree: "/wt",
		TmuxSession: "lec-2", PermissionMode: "acceptEdits", Model: "o4-mini"})
	hasAll(t, cmd, "codex exec --json", "-m o4-mini", "exit_code",
		// --full-auto was removed upstream; workspace-write is the sandbox that
		// lets codex edit the worktree without the dangerous escape hatch
		"--sandbox workspace-write")
	lacks(t, cmd, "--full-auto")

	hasAll(t, mustCommand(t, LaunchSpec{Agent: "codex", Worktree: "/wt",
		TmuxSession: "s", PermissionMode: "plan"}), "--sandbox read-only")
	hasAll(t, mustCommand(t, LaunchSpec{Agent: "codex", Worktree: "/wt",
		TmuxSession: "s", PermissionMode: "bypassPermissions"}),
		"--dangerously-bypass-approvals-and-sandbox")
}

func TestNonClaudeAgentsGetStdinRedirect(t *testing.T) {
	// codex/gemini read stdin even with the prompt as an argument, and a tmux
	// pane never EOFs — without this the attempt hangs forever, looking alive
	for _, agent := range []string{"codex", "gemini"} {
		cmd := mustCommand(t, LaunchSpec{Agent: agent, Worktree: "/wt",
			TmuxSession: "s", PermissionMode: "acceptEdits"})
		hasAll(t, cmd, "< /dev/null")
	}
}

func TestAgentBinariesAreOverridable(t *testing.T) {
	l := Launcher{CodexBin: "/home/me/.local/bin/codex"}
	cmd, err := l.Command(LaunchSpec{Agent: "codex", Worktree: "/wt",
		TmuxSession: "s", PermissionMode: "acceptEdits"})
	if err != nil {
		t.Fatal(err)
	}
	hasAll(t, cmd, "/home/me/.local/bin/codex exec --json")
}

func TestGeminiLaunchFlags(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "gemini", Worktree: "/wt",
		TmuxSession: "lec-3", PermissionMode: "acceptEdits"})
	hasAll(t, cmd, "gemini -p", "--yolo")
	lacks(t, mustCommand(t, LaunchSpec{Agent: "gemini", Worktree: "/wt",
		TmuxSession: "s", PermissionMode: "plan"}), "--yolo")
}

func TestUnknownAgentIsRejected(t *testing.T) {
	if _, err := launcher().Command(LaunchSpec{Agent: "cursor", Worktree: "/wt",
		TmuxSession: "s", PermissionMode: "acceptEdits"}); err == nil {
		t.Fatal("an unknown agent must be rejected, not silently launched")
	}
}

func TestEnvPrefixBuilder(t *testing.T) {
	if p, _ := EnvPrefix(nil, false); p != "" {
		t.Errorf("nil env: %q", p)
	}
	if p, _ := EnvPrefix(map[string]string{}, false); p != "" {
		t.Errorf("empty env: %q", p)
	}
	p, err := EnvPrefix(map[string]string{
		"ANTHROPIC_BASE_URL":   "http://ollama:11434",
		"ANTHROPIC_AUTH_TOKEN": "it's local"}, false)
	if err != nil {
		t.Fatal(err)
	}
	hasAll(t, p, "ANTHROPIC_BASE_URL=http://ollama:11434",
		`ANTHROPIC_AUTH_TOKEN='it'\''s local'`) // shell-quoted
	if got, _ := EnvPrefix(map[string]string{}, true); got != "IS_SANDBOX=1 " {
		t.Errorf("sandbox default: %q", got)
	}
	// an explicit value beats the sandbox default
	if got, _ := EnvPrefix(map[string]string{"IS_SANDBOX": "0"}, true); got != "IS_SANDBOX=0 " {
		t.Errorf("explicit override: %q", got)
	}
	if _, err := EnvPrefix(map[string]string{"BAD-NAME": "x"}, false); err == nil {
		t.Error("a hyphenated env name must be rejected")
	}
	if _, err := EnvPrefix(map[string]string{"$(evil)": "x"}, false); err == nil {
		t.Error("a shell-substitution env name must be rejected")
	}
}

func TestHookSettingsCarryURLAndToken(t *testing.T) {
	s := HookSettings("http://cp:9110", "tok123", "", 900)
	raw, _ := json.Marshal(s)
	var parsed struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	cmd := parsed.Hooks.PreToolUse[0].Hooks[0].Command
	hasAll(t, cmd, "LECTERN_URL=http://cp:9110", "LECTERN_TOKEN=tok123")
	// '*' — anything the matcher misses is silently DENIED in headless mode
	if parsed.Hooks.PreToolUse[0].Matcher != "*" {
		t.Errorf("default matcher: %q", parsed.Hooks.PreToolUse[0].Matcher)
	}
	narrow := HookSettings("u", "t", "Bash", 900)
	raw, _ = json.Marshal(narrow)
	json.Unmarshal(raw, &parsed)
	if parsed.Hooks.PreToolUse[0].Matcher != "Bash" {
		t.Errorf("narrowed matcher: %q", parsed.Hooks.PreToolUse[0].Matcher)
	}
}

func TestCodexFollowupResumesWithTheOriginalPermissions(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "codex", Worktree: "/wt/task1-a1", TmuxSession: "lec-2", PermissionMode: "acceptEdits", ResumeSession: "thread-123"})
	hasAll(t, cmd, "codex exec --json --sandbox workspace-write resume thread-123", "prompt.md")
}

func TestGenericTaskUsesIndependentCommandAndPromptTemplate(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "opencode", Worktree: "/tmp/work dir",
		TmuxSession: "lec-99", PermissionMode: "acceptEdits", Model: "local/qwen",
		Env: map[string]string{"OPENAI_BASE_URL": "http://127.0.0.1:11434/v1"},
		Definition: &TaskDefinition{Name: "opencode", Command: "opencode",
			Args: []string{"run", "--format", "json"}, ModelFlag: "--model",
			PromptTemplate: "--prompt {prompt}", OutputMode: "jsonl"}})
	hasAll(t, cmd, "opencode run --format json --model local/qwen --prompt",
		"OPENAI_BASE_URL=http://127.0.0.1:11434/v1", "< /dev/null",
		"/tmp/work dir", "events.jsonl")
	if strings.Contains(cmd, "claude -p") || strings.Contains(cmd, "codex exec") {
		t.Fatalf("custom task received a built-in adapter: %s", cmd)
	}
}

func TestGenericTaskCanDeliverPromptOnStdin(t *testing.T) {
	cmd := mustCommand(t, LaunchSpec{Agent: "aider", Worktree: "/wt", TmuxSession: "s",
		PermissionMode: "acceptEdits", Definition: &TaskDefinition{Name: "aider",
			Command: "aider", PromptTemplate: "stdin"}})
	hasAll(t, cmd, "cat .lectern/prompt.md | aider")
	if strings.Contains(cmd, "< /dev/null") {
		t.Fatalf("stdin prompt must remain attached to the command: %s", cmd)
	}
}

func TestGenericPromptTemplateQuotesLiteralTokens(t *testing.T) {
	rendered, err := renderPromptTemplate(`--message '{prompt}' --name "two words"`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(rendered, " "), `--message "$(cat .lectern/prompt.md)" --name 'two words'`; got != want {
		t.Fatalf("rendered template: %q, want %q", got, want)
	}
	cmd := mustCommand(t, LaunchSpec{Agent: "custom", Worktree: "/wt/with space", TmuxSession: "s",
		PermissionMode: "acceptEdits", Definition: &TaskDefinition{Name: "custom", Command: "runner",
			Args:           []string{"--label", "snow ☃", "--literal", "$(echo p)"},
			PromptTemplate: `--message '{prompt}' --name "two words"`}})
	hasAll(t, cmd, `--literal`, `--name`, `"$(cat .lectern/prompt.md)"`)
}

func TestGenericTaskRejectsUnsupportedPermissionMode(t *testing.T) {
	for _, args := range []([]string){nil, {}, {""}, {"  "}} {
		_, err := launcher().Command(LaunchSpec{Agent: "custom", Worktree: "/wt", TmuxSession: "s",
			PermissionMode: "plan", Definition: &TaskDefinition{Name: "custom", Command: "custom",
				PromptTemplate: "{prompt}", PermissionArgs: map[string][]string{"plan": args}}})
		if err == nil || !strings.Contains(err.Error(), "permission mode") {
			t.Fatalf("expected an actionable permission capability error for %#v, got %v", args, err)
		}
	}
}
