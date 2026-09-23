package api_test

// Agent hooks (docs/agent-events.md section 2): POST /api/hook/session/{id}/*,
// authenticated only by that session's own hook_token — never the general API
// bearer, and never another session's token. These tests run the real HTTP
// server against real fixture payloads (see agentevents' own package tests
// for the mapping/delta unit coverage); this file is the wire-level contract.

import (
	"fmt"
	"strings"
	"testing"
)

// The UserPromptSubmit and statusline bodies are the same real Claude Code
// 2.1.281 captures used in internal/agentevents' own tests, copied here so
// this package's tests do not reach outside the repository either.
const hookFixtureUserPromptSubmit = `{"session_id":"65f4714e-3848-434d-b684-9068108338d1","hook_event_name":"UserPromptSubmit","prompt":"reply with just: ok"}`

const hookFixtureStatusline = `{"session_id":"65f4714e-3848-434d-b684-9068108338d1","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},"cost":{"total_cost_usd":0.11431480000000001,"total_lines_added":0,"total_lines_removed":0},"context_window":{"total_input_tokens":36451,"total_output_tokens":4,"context_window_size":1000000,"used_percentage":4},"rate_limits":{"five_hour":{"used_percentage":4,"resets_at":1790200800},"seven_day":{"used_percentage":72,"resets_at":1790290800}}}`

// hookToken reaches into the store directly: the API never returns
// hook_token (it is json:"-"), by design — an agent only ever learns it
// through its own LECTERN_HOOK_TOKEN env, never an HTTP response a browser
// could also read.
func hookToken(t *testing.T, h *harness, id int64) string {
	t.Helper()
	row, err := h.App.DB.Session(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.HookToken == "" {
		t.Fatalf("session %d has no hook_token", id)
	}
	return row.HookToken
}

func TestHookSessionEventRequiresThatSessionsOwnToken(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "b", "agent": "claude"})
	tokA := hookToken(t, h, a.id())
	tokB := hookToken(t, h, b.id())
	if tokA == tokB {
		t.Fatal("two sessions must not share a hook token")
	}

	path := fmt.Sprintf("/api/hook/session/%d/UserPromptSubmit", a.id())

	// No Authorization header at all.
	if code := h.status("POST", path, nil); code != 401 {
		t.Fatalf("no token: got %d, want 401", code)
	}
	// Wrong token entirely.
	if code, _ := h.request("POST", path, nil, map[string]string{"Authorization": "Bearer not-a-real-token"}); code != 401 {
		t.Fatalf("wrong token: got %d, want 401", code)
	}
	// Session B's own, valid token presented against session A's endpoint.
	if code, _ := h.request("POST", path, nil, map[string]string{"Authorization": "Bearer " + tokB}); code != 401 {
		t.Fatalf("other session's token: got %d, want 401", code)
	}
	// A's own token must work.
	code, body := h.rawRequest("POST", path, hookFixtureUserPromptSubmit, tokA)
	if code != 200 {
		t.Fatalf("own token: got %d, want 200 (%s)", code, body)
	}
}

func TestHookSessionEventUpdatesStateAndSSEStaysCompatible(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "c", "agent": "claude"})
	tok := hookToken(t, h, sess.id())
	path := fmt.Sprintf("/api/hook/session/%d/UserPromptSubmit", sess.id())

	code, body := h.rawRequest("POST", path, hookFixtureUserPromptSubmit, tok)
	if code != 200 {
		t.Fatalf("got %d: %s", code, body)
	}
	if strings.TrimSpace(string(body)) != "{}" {
		t.Fatalf("hook response body = %q, want {}", body)
	}
	row, err := h.App.DB.Session(sess.id())
	if err != nil {
		t.Fatal(err)
	}
	if row.AgentState != "working" || row.StateSource != "hook" {
		t.Fatalf("state after UserPromptSubmit: agent_state=%q state_source=%q", row.AgentState, row.StateSource)
	}
	if row.HookSeenAt == nil {
		t.Fatal("hook_seen_at not set")
	}
	// The pre-existing status column must keep working for the current UI —
	// docs/agent-events.md section 2's "keep existing status column
	// behaviour working" requirement, exercised here rather than only in
	// internal/agentevents' unit tests, since sessionView is what a real
	// client reads.
	got := h.sessionByID(sess.id())
	if got.str("status") != "running" {
		t.Fatalf("status = %q, want running", got.str("status"))
	}
	if got.str("agent_state") != "working" {
		t.Fatalf("session JSON should expose agent_state: %v", got)
	}
}

func TestHookSessionStatuslineBooksUsage(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "d", "agent": "claude"})
	tok := hookToken(t, h, sess.id())
	path := fmt.Sprintf("/api/hook/session/%d/statusline", sess.id())

	code, body := h.rawRequest("POST", path, hookFixtureStatusline, tok)
	if code != 200 {
		t.Fatalf("got %d: %s", code, body)
	}
	got := h.sessionByID(sess.id())
	if got.str("model") != "claude-opus-5-5" {
		t.Fatalf("model = %v", got["model"])
	}
	if int64(got.num("context_pct")) != 4 {
		t.Fatalf("context_pct = %v", got["context_pct"])
	}
	if got.num("cost_usd") < 0.114 || got.num("cost_usd") > 0.115 {
		t.Fatalf("cost_usd = %v", got["cost_usd"])
	}

	// Wrong token must not be able to pollute another session's usage even
	// via the statusline route.
	other := h.session(obj{"project_id": pid, "name": "e", "agent": "claude"})
	if code, _ := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/statusline", other.id()),
		hookFixtureStatusline, "wrong-token"); code != 401 {
		t.Fatalf("wrong token on statusline: got %d, want 401", code)
	}
	untouched, err := h.App.DB.Session(other.id())
	if err != nil {
		t.Fatal(err)
	}
	if untouched.CostUSD != nil {
		t.Fatalf("statusline with wrong token must not have written usage: %v", *untouched.CostUSD)
	}
}

// TestHookInstallGatedOnBuiltinSpec covers manager.go's spec.Builtin gate: an
// operator can name a custom agent "claude" (overriding the built-in), and
// that program is not necessarily anything --settings means something to —
// e2e/test_session_continuity.py does exactly this with a scripted python
// stand-in. The mock executor's catch-all always answers an unrecognized
// command with empty stdout (see internal/executor/mock.go), so it cannot
// prove --settings actually reaches argv — that is what
// TestARealClaudeSessionHookEnvAndSettingsReachTheProcess proves, for real,
// with a real target. What the mock CAN prove, and what this checks, is
// whether the hooks installer command was attempted AT ALL: it must run for
// the built-in spec and must not run for a same-named custom one.
func TestHookInstallGatedOnBuiltinSpec(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	h.session(obj{"project_id": pid, "name": "builtin", "agent": "claude"})
	if !installCommandWasRun(h) {
		t.Fatal("the built-in claude spec should have attempted the hooks install")
	}

	h.decode("PUT", "/api/agents", []obj{{
		"name": "claude", "command": "continuity-agent.py", "model_flag": "--model", "prompt_arg": true,
	}}, 200, nil)
	before := len(h.mock().CmdLog())
	h.session(obj{"project_id": pid, "name": "overridden", "agent": "claude"})
	for _, cmd := range h.mock().CmdLog()[before:] {
		if strings.Contains(cmd, "ADKHOOKINSTALL") {
			t.Fatal("a custom agent merely named \"claude\" must not get the hooks installer run against it")
		}
	}
}

// installCommandWasRun looks for the claude settings installer's heredoc
// marker (ADKHOOKINSTALL, from agentevents.ClaudeSettingsInstallCommand)
// anywhere in the mock executor's command log.
func installCommandWasRun(h *harness) bool {
	h.t.Helper()
	for _, cmd := range h.mock().CmdLog() {
		if strings.Contains(cmd, "ADKHOOKINSTALL") {
			return true
		}
	}
	return false
}

func TestHookSessionEventUnknownNameIsAcceptedAndIgnored(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "f", "agent": "claude"})
	tok := hookToken(t, h, sess.id())
	code, body := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/SomeFutureHook", sess.id()), `{}`, tok)
	if code != 200 || strings.TrimSpace(string(body)) != "{}" {
		t.Fatalf("unknown event: %d %s", code, body)
	}
	row, err := h.App.DB.Session(sess.id())
	if err != nil {
		t.Fatal(err)
	}
	if row.AgentState != "" {
		t.Fatalf("unknown event must not set a state: %q", row.AgentState)
	}
}
