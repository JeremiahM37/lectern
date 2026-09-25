package api_test

// Claim board (docs/claims.md): wire-level tests against the real HTTP
// server, mirroring awareness_test.go's conventions — repo_key is poked
// directly on session/project rows (bypassing internal/awareness's real git
// resolution, which has its own dedicated tests) so these stay focused on
// the claim lifecycle, overlap detection and hook wiring.

import (
	"fmt"
	"strings"
	"testing"
)

func setProjectRepo(t *testing.T, h *harness, projectID int64, repoKey, toplevel string) {
	t.Helper()
	if err := h.App.DB.Update("projects", projectID, map[string]any{
		"repo_key": repoKey, "repo_toplevel": toplevel}); err != nil {
		t.Fatal(err)
	}
}

// ---- lifecycle ---------------------------------------------------------------

func TestCreateClaimBySessionAndList(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "agent-a", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")

	c := h.post("/api/claims", obj{
		"session_id": a.id(), "scope_kind": "paths",
		"paths": []string{"frontend/src/sessions/**"}, "intent": "rename button",
	}, 200)
	if c.str("repo_key") != "1:/repo/.git" {
		t.Fatalf("expected repo_key resolved from the session, got %v", c)
	}
	if c.str("holder") != "agent-a" {
		t.Fatalf("expected holder to default to the session's name, got %v", c)
	}
	if c.id() == 0 {
		t.Fatalf("expected an id, got %v", c)
	}

	list := h.getList("/api/claims?session_id=" + fmt.Sprint(a.id()))
	if len(list) != 1 || list[0].id() != c.id() {
		t.Fatalf("expected the new claim in the session's list, got %v", list)
	}

	byRepo := h.getList("/api/claims?repo_key=1:/repo/.git")
	if len(byRepo) != 1 {
		t.Fatalf("expected the claim to show up by repo_key, got %v", byRepo)
	}
}

func TestCreateClaimRejectsInvalidInput(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")

	if code := h.status("POST", "/api/claims", obj{"session_id": a.id(), "scope_kind": "bogus", "scope": "x"}); code != 422 {
		t.Fatalf("expected 422 for a bad scope_kind, got %d", code)
	}
	if code := h.status("POST", "/api/claims", obj{"scope_kind": "topic", "scope": "x"}); code != 422 {
		t.Fatalf("expected 422 with no identifying field, got %d", code)
	}
}

func TestReleaseClaim(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	c := h.post("/api/claims", obj{"session_id": a.id(), "scope_kind": "topic", "scope": "rename button"}, 200)

	if code := h.status("DELETE", fmt.Sprintf("/api/claims/%d", c.id()), nil); code != 200 {
		t.Fatalf("expected 200 releasing, got %d", code)
	}
	list := h.getList("/api/claims?session_id=" + fmt.Sprint(a.id()))
	if len(list) != 0 {
		t.Fatalf("expected no active claims after release, got %v", list)
	}
	// Releasing again is a no-op, not an error.
	if code := h.status("DELETE", fmt.Sprintf("/api/claims/%d", c.id()), nil); code != 200 {
		t.Fatalf("expected re-releasing to still be 200, got %d", code)
	}
}

func TestExtendClaim(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	c := h.post("/api/claims", obj{"session_id": a.id(), "scope_kind": "topic", "scope": "x", "ttl_minutes": 10}, 200)

	extended := h.post(fmt.Sprintf("/api/claims/%d/extend", c.id()), obj{}, 200)
	if extended.num("expires_at") <= c.num("expires_at") {
		t.Fatalf("expected extend to push expiry forward: %v vs %v", extended.num("expires_at"), c.num("expires_at"))
	}

	h.status("DELETE", fmt.Sprintf("/api/claims/%d", c.id()), nil)
	if code := h.status("POST", fmt.Sprintf("/api/claims/%d/extend", c.id()), obj{}); code != 409 {
		t.Fatalf("expected 409 extending a released claim, got %d", code)
	}
}

// ---- overlap detection: globs -------------------------------------------------

func TestClaimsEditWarningOverlap(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "codex-session", "agent": "codex"})
	b := h.session(obj{"project_id": pid, "name": "other", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo-wt", "/repo-wt")
	// This test is isolating CLAIMS overlap specifically; awareness's own
	// (separate) same-file edit-warning would otherwise also fire once B's
	// PreToolUse below records an intent-edit on the shared file, and again
	// when A later touches it — turn it off so only claims_edit_warning text
	// is under test here.
	if code, resp := h.request("PUT", "/api/settings", obj{"awareness_edit_warning": "0"}, nil); code != 200 {
		t.Fatalf("PUT settings: got %d: %s", code, resp)
	}

	h.post("/api/claims", obj{"session_id": a.id(), "scope_kind": "paths",
		"paths": []string{"frontend/src/sessions/**"}, "intent": "rename button"}, 200)

	tokB := hookToken(t, h, b.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", b.id()),
		preToolUseBody("Edit", "/repo-wt/frontend/src/sessions/SessionCard.tsx"), tokB)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	text, ok := hookAdditionalContext(t, resp)
	if !ok {
		t.Fatalf("expected an overlap warning, got %s", resp)
	}
	for _, want := range []string{fmt.Sprintf("session #%d", a.id()), "codex", "frontend/src/sessions/**", "rename button"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected warning to mention %q, got %q", want, text)
		}
	}

	// An unrelated file gets no warning.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", b.id()),
		preToolUseBody("Edit", "/repo-wt/frontend/src/board/Board.tsx"), tokB)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected {} for an unrelated file, got %s", resp)
	}

	// The claim holder's own session never warns about itself.
	tokA := hookToken(t, h, a.id())
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", a.id()),
		preToolUseBody("Edit", "/repo/frontend/src/sessions/SessionCard.tsx"), tokA)
	if code != 200 {
		t.Fatalf("PreToolUse: got %d: %s", code, resp)
	}
	if strings.TrimSpace(string(resp)) != "{}" {
		t.Fatalf("expected no self-overlap, got %s", resp)
	}
}

// ---- briefing ------------------------------------------------------------

func TestClaimsBriefingOnSessionStart(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "agent-a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "agent-b", "agent": "codex"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo", "/repo-wt")

	h.post("/api/claims", obj{"session_id": a.id(), "scope_kind": "topic", "scope": "rename button in session card",
		"intent": "rename button in session card"}, 200)

	tokB := hookToken(t, h, b.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/SessionStart", b.id()), `{}`, tokB)
	if code != 200 {
		t.Fatalf("SessionStart: got %d: %s", code, resp)
	}
	text, ok := hookAdditionalContext(t, resp)
	if !ok {
		t.Fatalf("expected claims briefing text, got %s", resp)
	}
	for _, want := range []string{fmt.Sprintf("session #%d", a.id()), "claude", "rename button"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected briefing to mention %q, got %q", want, text)
		}
	}
}

// ---- settings toggle -----------------------------------------------------

func TestClaimsSettingsToggleOff(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "b", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	setSessionRepo(t, h, b.id(), "1:/repo/.git", "/repo", "/repo")

	h.post("/api/claims", obj{"session_id": a.id(), "scope_kind": "paths",
		"paths": []string{"shared.go"}}, 200)

	// awareness_briefing is also turned off here so its own (unrelated) peer
	// briefing does not confuse this test about which feature suppressed what
	// — the two sessions above share a repo, so awareness would otherwise
	// brief them about each other regardless of the claims_* settings below.
	if code, resp := h.request("PUT", "/api/settings",
		obj{"claims_briefing": "0", "claims_edit_warning": "0", "claims_auto_paths": "0",
			"awareness_briefing": "0"}, nil); code != 200 {
		t.Fatalf("PUT settings: got %d: %s", code, resp)
	}

	tokB := hookToken(t, h, b.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PreToolUse", b.id()),
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

	// claims_auto_paths off: PostToolUse must not create an implicit claim.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", b.id()),
		postToolUseBody("Edit", "/repo/new.go"), tokB)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}
	list := h.getList("/api/claims?session_id=" + fmt.Sprint(b.id()))
	for _, c := range list {
		if c["auto"] == true {
			t.Fatalf("expected no automatic claim while claims_auto_paths is off, got %v", list)
		}
	}
}

// ---- automatic claims ------------------------------------------------------

func TestClaimsAutoPathClaimOnFirstEdit(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	tok := hookToken(t, h, a.id())

	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Edit", "/repo/frontend/src/sessions/SessionCard.tsx"), tok)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}
	list := h.getList("/api/claims?session_id=" + fmt.Sprint(a.id()))
	if len(list) != 1 {
		t.Fatalf("expected exactly one automatic claim after the first edit, got %v", list)
	}
	if list[0]["auto"] != true || list[0].str("scope_kind") != "paths" {
		t.Fatalf("expected an automatic paths claim, got %v", list[0])
	}

	// A second edit, in a DIFFERENT directory, must not create a second
	// automatic claim — "first PostToolUse edit" is a one-time signal.
	code, resp = h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", a.id()),
		postToolUseBody("Edit", "/repo/frontend/src/board/Board.tsx"), tok)
	if code != 200 {
		t.Fatalf("PostToolUse: got %d: %s", code, resp)
	}
	list = h.getList("/api/claims?session_id=" + fmt.Sprint(a.id()))
	if len(list) != 1 {
		t.Fatalf("expected the automatic claim to stay singular, got %v", list)
	}
}

// ---- auto-release ----------------------------------------------------------

func TestClaimsAutoReleaseOnSessionEnd(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	setSessionRepo(t, h, a.id(), "1:/repo/.git", "/repo", "/repo")
	h.post("/api/claims", obj{"session_id": a.id(), "scope_kind": "topic", "scope": "x"}, 200)

	tok := hookToken(t, h, a.id())
	code, resp := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/SessionEnd", a.id()), `{}`, tok)
	if code != 200 {
		t.Fatalf("SessionEnd: got %d: %s", code, resp)
	}
	list := h.getList("/api/claims?session_id=" + fmt.Sprint(a.id()))
	if len(list) != 0 {
		t.Fatalf("expected claims released immediately on SessionEnd, got %v", list)
	}
}

func TestClaimsAutoReleaseOnAttemptFinish(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	setProjectRepo(t, h, pid, "1:/repo/.git", "/repo")

	res := h.run(pid, "auto-claimed task", "echo hi", obj{})
	att := res.sub("attempt")
	if att.id() == 0 {
		t.Fatalf("expected the finished task to report its attempt, got %v", res)
	}
	// h.run already waited for the task to leave running/queued, so the
	// scheduler's periodic Claim board sweep (internal/scheduler.Scheduler.
	// Claims, docs/claims.md) has had ticks to release the attempt's claim.
	h.waitUntil("the finished attempt's task claim to be released", func() bool {
		return len(h.getList("/api/claims?attempt_id="+fmt.Sprint(att.id()))) == 0
	})
}

func TestClaimsTaskAutoClaimExistsWhileRunning(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	setProjectRepo(t, h, pid, "1:/repo/.git", "/repo")

	task := h.task(pid, "in-flight task", "echo hi", obj{})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("the task's auto claim to appear", func() bool {
		list := h.getList("/api/claims?repo_key=1:/repo/.git")
		for _, c := range list {
			if c.str("scope_kind") == "task" && c.str("scope") == fmt.Sprint(task.id()) {
				return true
			}
		}
		return false
	})
}

// ---- topic overlap (new-session/new-task launch check) ----------------------

func TestClaimsTopicOverlapEndpoint(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	setProjectRepo(t, h, pid, "1:/repo/.git", "/repo")

	h.post("/api/claims", obj{"project_id": pid, "scope_kind": "topic",
		"scope": "rename button in session card"}, 200)

	overlaps := h.getList(fmt.Sprintf("/api/claims/topic-overlap?project_id=%d&text=%s",
		pid, "please+rename+the+button+in+the+session+card"))
	if len(overlaps) != 1 {
		t.Fatalf("expected one overlapping topic claim, got %v", overlaps)
	}

	none := h.getList(fmt.Sprintf("/api/claims/topic-overlap?project_id=%d&text=%s",
		pid, "upgrade+the+docker+base+image"))
	if len(none) != 0 {
		t.Fatalf("expected no overlap for an unrelated prompt, got %v", none)
	}
}

// ---- store-level sanity (queried through the API's own filters) -------------

func TestClaimsListEmptyWithNoClaims(t *testing.T) {
	h := newHarness(t)
	list := h.getList("/api/claims")
	if len(list) != 0 {
		t.Fatalf("expected no claims on a fresh board, got %v", list)
	}
}
