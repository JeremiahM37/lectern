package api_test

// Real-process proof for docs/agent-events.md section 2's launch wiring: a
// stub "claude" binary — reusing e2e_real_test.go's real rig (real tmux, real
// local executor, real HTTP server, no mocks) — checks its own argv for
// --settings, checks that file actually exists on disk and carries the hook
// wiring, checks LECTERN_HOOK_TOKEN/LECTERN_HOOK_URL landed in its own
// environment, and then does the one thing that matters: POSTs real hook
// events to the real server using exactly that env, and this test asserts
// the session row's agent_state genuinely moved because of it — not a mock,
// not a screen-scrape guess.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeInteractiveHookAgent extends e2e_real_test.go's fakeInteractiveAgent
// pattern (argv/cwd to session-log.txt) with the hook-plumbing proof: it
// parses its own --settings argument, inspects that file, and — only if the
// hook env it was actually given is non-empty — posts a real UserPromptSubmit
// then a real Stop to $LECTERN_HOOK_URL, logging each step so a failure here
// is debuggable from session-log.txt alone.
const fakeInteractiveHookAgent = `#!/bin/bash
log="$PWD/session-log.txt"
printf 'argv:%s\n' "$*" >> "$log"
settings=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--settings" ]; then settings="$arg"; fi
  prev="$arg"
done
printf 'settings-path:%s\n' "$settings" >> "$log"
if [ -n "$settings" ] && [ -f "$settings" ]; then
  printf 'settings-file-exists:1\n' >> "$log"
  if grep -q LECTERN_HOOK_TOKEN "$settings"; then
    printf 'settings-has-hook-wiring:1\n' >> "$log"
  fi
fi
printf 'hook-token-present:%s\n' "${LECTERN_HOOK_TOKEN:+yes}" >> "$log"
printf 'hook-url-present:%s\n' "${LECTERN_HOOK_URL:+yes}" >> "$log"
if [ -n "$LECTERN_HOOK_TOKEN" ] && [ -n "$LECTERN_HOOK_URL" ]; then
  curl -s -m 5 -o /dev/null -X POST \
    -H "Authorization: Bearer $LECTERN_HOOK_TOKEN" -H "Content-Type: application/json" \
    --data-binary '{"session_id":"fake-hook","hook_event_name":"UserPromptSubmit","prompt":"hi"}' \
    "$LECTERN_HOOK_URL/UserPromptSubmit"
  printf 'posted:UserPromptSubmit\n' >> "$log"
  curl -s -m 5 -o /dev/null -X POST \
    -H "Authorization: Bearer $LECTERN_HOOK_TOKEN" -H "Content-Type: application/json" \
    --data-binary '{"session_id":"fake-hook","hook_event_name":"Stop","last_assistant_message":"done","stop_hook_active":false}' \
    "$LECTERN_HOOK_URL/Stop"
  printf 'posted:Stop\n' >> "$log"
fi
echo "fake agent ready"
while IFS= read -r line; do
  printf 'typed:%s\n' "$line" >> "$log"
done
`

func waitForAgentState(t *testing.T, db *store.DB, id int64, want string, limit time.Duration) *store.Session {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last *store.Session
	for time.Now().Before(deadline) {
		row, err := db.Session(id)
		if err == nil {
			last = row
			if row.AgentState == want {
				return row
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil {
		t.Fatalf("session %d never became readable", id)
	}
	t.Fatalf("agent_state never reached %q, last was %q (state_source=%q)", want, last.AgentState, last.StateSource)
	return nil
}

func TestARealClaudeSessionHookEnvAndSettingsReachTheProcess(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed; this test needs the real thing")
	}
	// The claude settings installer (agentevents.ClaudeSettingsInstallCommand)
	// writes under ~/.lectern/hooks/ on the TARGET, which for a "local" target
	// is this very test process's $HOME — redirect it into the test's own temp
	// tree first, same as project_workflows_test.go does for the same reason
	// (the local executor's exec.Cmd inherits this process's environment, so
	// this reaches the installer's `python3 -` subprocess too).
	t.Setenv("HOME", t.TempDir())

	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(fakeInteractiveHookAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude"})

	log := r.waitForLog(r.repo, "posted:Stop", 20*time.Second)
	if !strings.Contains(log, "settings-path:/") {
		t.Fatalf("no --settings path reached the process:\n%s", log)
	}
	if !strings.Contains(log, "settings-file-exists:1") {
		t.Fatalf("the settings file the argv pointed at was never written to disk:\n%s", log)
	}
	if !strings.Contains(log, "settings-has-hook-wiring:1") {
		t.Fatalf("installed settings did not carry the hook wiring:\n%s", log)
	}
	if !strings.Contains(log, "hook-token-present:yes") || !strings.Contains(log, "hook-url-present:yes") {
		t.Fatalf("LECTERN_HOOK_TOKEN/LECTERN_HOOK_URL did not reach the process env:\n%s", log)
	}

	// The state transitions below are the actual proof: they can only have
	// happened because the fake agent's two curl calls, using exactly the
	// env lectern handed it, reached this real running server and were
	// authenticated by this exact session's real hook_token.
	row := waitForAgentState(t, r.app.DB, sess.ID, "idle", 20*time.Second)
	if row.StateSource != "hook" {
		t.Fatalf("state_source = %q, want hook", row.StateSource)
	}
	if row.HookSeenAt == nil {
		t.Fatal("hook_seen_at was never set")
	}
}

// fakeInteractiveCodexHookAgent is TestARealClaudeSessionHookEnvAndSettingsReachTheProcess's
// codex twin, but stronger where it can be: rather than reimplementing the
// curl calls inline, it invokes the ACTUAL artifact
// agentevents.CodexHooksInstallCommand wrote
// (~/.lectern/hooks/lectern-codex-hook.py) with real hook fixture JSON on
// stdin, exactly as codex would per its own hooks.json — proving the
// installed script itself works, not a hand-rolled stand-in for it. It also
// checks for --dangerously-bypass-hook-trust in its own argv and inspects
// hooks.json on disk for lectern's marker, mirroring the settings-file
// checks the claude test above does for --settings.
const fakeInteractiveCodexHookAgent = `#!/bin/bash
log="$PWD/session-log.txt"
printf 'argv:%s\n' "$*" >> "$log"
bypass=0
for arg in "$@"; do
  if [ "$arg" = "--dangerously-bypass-hook-trust" ]; then bypass=1; fi
done
printf 'bypass-trust-flag-present:%s\n' "$bypass" >> "$log"

hooks_json="${CODEX_HOME:-$HOME/.codex}/hooks.json"
if [ -f "$hooks_json" ] && grep -q lectern-codex-hook.py "$hooks_json"; then
  printf 'hooks-json-has-marker:1\n' >> "$log"
fi

hook_script="$HOME/.lectern/hooks/lectern-codex-hook.py"
printf 'hook-token-present:%s\n' "${LECTERN_HOOK_TOKEN:+yes}" >> "$log"
printf 'hook-url-present:%s\n' "${LECTERN_HOOK_URL:+yes}" >> "$log"
if [ -n "$LECTERN_HOOK_TOKEN" ] && [ -n "$LECTERN_HOOK_URL" ] && [ -f "$hook_script" ]; then
  printf '%s' '{"session_id":"fake-codex-hook","hook_event_name":"UserPromptSubmit","prompt":"hi"}' \
    | python3 "$hook_script" UserPromptSubmit 8 >> "$log" 2>&1
  printf 'posted:UserPromptSubmit\n' >> "$log"
  printf '%s' '{"session_id":"fake-codex-hook","hook_event_name":"Stop","last_assistant_message":"done","stop_hook_active":false}' \
    | python3 "$hook_script" Stop 8 >> "$log" 2>&1
  printf 'posted:Stop\n' >> "$log"
fi
echo "fake agent ready"
while IFS= read -r line; do
  printf 'typed:%s\n' "$line" >> "$log"
done
`

func TestARealCodexSessionHooksReachTheProcess(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed; this test needs the real thing")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed; this test needs the real thing")
	}
	// Same reasoning as the claude test above: agentevents.CodexHooksInstallCommand
	// writes under ~/.lectern/hooks/ AND merges into ~/.codex/hooks.json on
	// the TARGET, which for a "local" target is this process's own $HOME —
	// redirect it so this test can never touch the operator's real
	// ~/.codex/hooks.json (see docs/agent-events.md's note that this exact
	// machine already has a real, unrelated one installed by another tool).
	t.Setenv("HOME", t.TempDir())

	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.CodexBin, []byte(fakeInteractiveCodexHookAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "codex", "permission_mode": "ask"})

	log := r.waitForLog(r.repo, "posted:Stop", 20*time.Second)
	if !strings.Contains(log, "bypass-trust-flag-present:1") {
		t.Fatalf("--dangerously-bypass-hook-trust did not reach the process argv:\n%s", log)
	}
	if !strings.Contains(log, "hooks-json-has-marker:1") {
		t.Fatalf("hooks.json on disk did not carry lectern's hook entries:\n%s", log)
	}
	if !strings.Contains(log, "hook-token-present:yes") || !strings.Contains(log, "hook-url-present:yes") {
		t.Fatalf("LECTERN_HOOK_TOKEN/LECTERN_HOOK_URL did not reach the process env:\n%s", log)
	}

	// The state transition below is the actual proof, same as the claude
	// test: it can only have happened because the REAL installed hook
	// script's two curl calls, using exactly the env lectern handed the
	// process, reached this real running server and were authenticated by
	// this exact session's real hook_token.
	row := waitForAgentState(t, r.app.DB, sess.ID, "idle", 20*time.Second)
	if row.StateSource != "hook" {
		t.Fatalf("state_source = %q, want hook", row.StateSource)
	}
	if row.HookSeenAt == nil {
		t.Fatal("hook_seen_at was never set")
	}
}
