package api_test

// Cross-agent awareness (docs/agent-events.md "Cross-agent awareness"):
// wire-level tests against the real HTTP server. repo_key/repo_toplevel are
// poked directly on the session rows (as hookToken() already does for
// hook_token) rather than waited on asynchronously — internal/awareness's
// own real-git test covers actual resolution; this file is the hook
// wiring, dedup, settings toggle and peers-endpoint contract.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func setSessionRepo(t *testing.T, h *harness, id int64, repoKey, toplevel, workdir string) {
	t.Helper()
	fields := map[string]any{"repo_key": repoKey, "repo_toplevel": toplevel}
	if workdir != "" {
		fields["workdir"] = workdir
	}
	if err := h.App.DB.Update("sessions", id, fields); err != nil {
		t.Fatal(err)
	}
}

func postToolUseBody(toolName, filePath string) string {
	return fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":%q,"tool_input":{"file_path":%q}}`, toolName, filePath)
}

func preToolUseBody(toolName, filePath string) string {
	return fmt.Sprintf(`{"hook_event_name":"PreToolUse","tool_name":%q,"tool_input":{"file_path":%q}}`, toolName, filePath)
}

func hookAdditionalContext(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var parsed struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("could not parse hook response %s: %v", body, err)
	}
	return parsed.HookSpecificOutput.AdditionalContext, parsed.HookSpecificOutput.AdditionalContext != ""
}

func TestSessionPeersEndpointEmptyWithNoAwareness(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "solo", "agent": "claude"})
	got := h.get(fmt.Sprintf("/api/sessions/%d/peers", sess.id()))
	if len(got.list("peers")) != 0 {
		t.Fatalf("expected no peers, got %v", got)
	}
	if len(got.list("self_files")) != 0 {
		t.Fatalf("expected no self_files, got %v", got)
	}
}

func TestAwarenessBriefingOnSessionStartAndDedup(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "agent-a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "agent-b", "agent": "codex"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo", "/repo-wt")

	// A edits a file (PostToolUse) — recorded against A's repo.
	tokA := hookToken(t, h, a.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Edit", "/repo/session_card.tsx"), tokA)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}

	// B's SessionStart should be briefed about A.
	tokB := hookToken(t, h, b.id())
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/SessionStart", b.id()), `{}`, tokB)
	if code != 200 {
		t.Fatalf("SessionStart: got %d: %s", code, resp)
	}
	text, ok := hookAdditionalContext(t, resp)
	if !ok {
		t.Fatalf("expected a briefing, got %s", resp)
	}
	if !strings.Contains(text, "agent-a") || !strings.Contains(text, "session_card.tsx") {
		t.Fatalf("briefing should name the peer and the file: %s", text)
	}

	// Same peer state again via UserPromptSubmit: deduplicated to {}.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/UserPromptSubmit", b.id()),
		`{"hook_event_name":"UserPromptSubmit","prompt":"continue"}`, tokB)
	if code != 200 {
		t.Fatalf("UserPromptSubmit: got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected the unchanged briefing to be deduplicated, got %s", resp)
	}

	// A new edit from A changes the peer summary, so B's next prompt is
	// briefed again even though the TTL has not elapsed.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Write", "/repo/new_file.go"), tokA)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/UserPromptSubmit", b.id()),
		`{"hook_event_name":"UserPromptSubmit","prompt":"continue"}`, tokB)
	if code != 200 {
		t.Fatalf("UserPromptSubmit: got %d: %s", code, resp)
	}
	text, ok = hookAdditionalContext(t, resp)
	if !ok || !strings.Contains(text, "new_file.go") {
		t.Fatalf("expected a refreshed briefing mentioning the new file, got %s", resp)
	}
}

func TestAwarenessNoBriefingWithoutPeers(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "alone", "agent": "claude"})
	setSessionRepo(t, h, sess.id(), "1:/repo/.git", "/repo", "/repo")
	tok := hookToken(t, h, sess.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/SessionStart", sess.id()), `{}`, tok)
	if code != 200 {
		t.Fatalf("got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected {} with no peers, got %s", resp)
	}
}

func TestAwarenessEditWarningSameDirVsWorktree(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "writer", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "other-worktree", "agent": "codex"})
	c := h.session(obj{"project_id": pid, "name": "same-dir", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo", "/repo-wt")
	setSessionRepo(t, h, c.id(), "1:/repo/.git", "/repo", "/repo")

	tokA := hookToken(t, h, a.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Edit", "/repo/shared.go"), tokA)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}

	// C shares A's exact workdir and checks FIRST, before anyone else's
	// PreToolUse can record a more recent intent-edit on this same file
	// (PreToolUse itself records intent — see the next check — so ordering
	// here matters to isolate what each check is exercising).
	tokC := hookToken(t, h, c.id())
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", c.id()),
		preToolUseBody("Edit", "/repo/shared.go"), tokC)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	text, ok := hookAdditionalContext(t, resp)
	if !ok || !strings.Contains(text, "SAME working directory") {
		t.Fatalf("expected same-directory wording, got %s", resp)
	}

	// A separate file, edited only by A: B (a different worktree) gets the
	// weaker merge-conflict-risk wording.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Edit", "/repo/shared2.go"), tokA)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}
	tokB := hookToken(t, h, b.id())
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", b.id()),
		preToolUseBody("Edit", "/repo/shared2.go"), tokB)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	text, ok = hookAdditionalContext(t, resp)
	if !ok || !strings.Contains(text, "separate worktree") {
		t.Fatalf("expected separate-worktree wording, got %s", resp)
	}

	// An unrelated file must not warn.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", b.id()),
		preToolUseBody("Edit", "/repo/unrelated.go"), tokB)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected {} for an untouched file, got %s", resp)
	}
}

func TestAwarenessSettingsToggleOff(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "b", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo", "/repo")

	if code, resp := h.request("PUT", "/api/settings",
		obj{"awareness_briefing": "0", "awareness_edit_warning": "0"}, nil); code != 200 {
		t.Fatalf("PUT settings: got %d: %s", code, resp)
	}

	tokA := hookToken(t, h, a.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Edit", "/repo/shared.go"), tokA)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}

	tokB := hookToken(t, h, b.id())
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", b.id()),
		preToolUseBody("Edit", "/repo/shared.go"), tokB)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected edit warning suppressed by setting, got %s", resp)
	}

	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/SessionStart", b.id()), `{}`, tokB)
	if code != 200 {
		t.Fatalf("SessionStart: got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected briefing suppressed by setting, got %s", resp)
	}
}

func TestSessionPeersEndpointShape(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "b", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo", "/repo")

	if err := h.App.DB.UpsertSessionFileEdit(a.id(), "1:/repo/.git", "shared.go", store.Now()); err != nil {
		t.Fatal(err)
	}

	got := h.get(fmt.Sprintf("/api/sessions/%d/peers", b.id()))
	peers := got.list("peers")
	if len(peers) != 1 {
		t.Fatalf("expected one peer, got %v", got)
	}
	if peers[0].str("name") != "a" {
		t.Fatalf("expected peer named a, got %v", peers[0])
	}
	files := peers[0].list("files")
	if len(files) != 1 || files[0].str("rel_path") != "shared.go" {
		t.Fatalf("expected shared.go in peer's files, got %v", peers[0])
	}
}
