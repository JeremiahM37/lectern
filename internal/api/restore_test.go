package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func (h *harness) setRow(id int64, fields map[string]any) {
	h.t.Helper()
	if err := h.App.DB.Update("sessions", id, fields); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) restorable(query string) map[int64]obj {
	h.t.Helper()
	out := map[int64]obj{}
	for _, row := range h.getList("/api/sessions/restorable" + query) {
		out[row.id()] = row
	}
	return out
}

func (h *harness) reopen(id int64, body obj, want int) obj {
	h.t.Helper()
	var out obj
	h.decode("POST", fmt.Sprintf("/api/sessions/%d/reopen", id), body, want, &out)
	return out
}

func TestEndReasonsAreRecorded(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	stopped := h.session(obj{"project_id": pid, "name": "stopped"})
	h.waitSessionStatus(stopped.id(), "running", "waiting", "idle")
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", stopped.id()), nil, 200, nil)
	released := h.post("/api/sessions/adopt", obj{"target_id": h.firstTargetID(), "tmux_session": "legacy-claude", "workdir": "/mock/demo-app"}, 201)
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", released.id()), nil, 200, nil)
	for id, want := range map[int64]string{stopped.id(): sessions.EndStopped, released.id(): sessions.EndReleased} {
		row, err := h.App.DB.Session(id)
		if err != nil {
			t.Fatal(err)
		}
		if row.EndReason != want {
			t.Fatalf("session %d end_reason = %q, want %q", id, row.EndReason, want)
		}
	}
}

func TestRestorableListsEverythingThatCanComeBack(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	now := store.Now()
	mk := func(name string, fields map[string]any) int64 {
		row := h.session(obj{"project_id": pid, "name": name})
		h.setRow(row.id(), fields)
		return row.id()
	}
	exited := mk("exited one", map[string]any{"status": "dead", "ended_at": now - 50, "end_reason": sessions.EndExited, "pane_tail": "done\n> the parser fix is merged\n> \n"})
	archived := mk("archived one", map[string]any{"status": "dead", "ended_at": now - 40, "archived_at": now - 40, "end_reason": sessions.EndStopped, "last_prompt_excerpt": "please  add\nretries"})
	interrupted := mk("interrupted one", map[string]any{"status": sessions.StatusInterrupted, "updated_at": now - 30})
	failed := mk("never started", map[string]any{"status": "dead", "ended_at": now - 20, "end_reason": sessions.EndFailed})
	legacyNoise := mk("legacy failed launch", map[string]any{"status": "dead", "ended_at": now - 10, "pane_hash": "", "pane_tail": ""})
	replaced := mk("replaced", map[string]any{"status": "dead", "ended_at": now - 5, "end_reason": sessions.EndStopped, "pane_hash": "x"})
	successor := h.session(obj{"project_id": pid, "name": "successor"})
	h.setRow(replaced, map[string]any{"reopened_as": successor.id()})
	continuedCID := mk("continued by cid", map[string]any{"status": "dead", "ended_at": now - 4, "end_reason": sessions.EndStopped, "native_recovery_cid": "aaaaaaaa-1111-4111-8111-111111111111"})
	newer := h.session(obj{"project_id": pid, "name": "newer"})
	h.setRow(newer.id(), map[string]any{"resume_id": "aaaaaaaa-1111-4111-8111-111111111111"})

	rows := h.restorable("")
	for _, gone := range []int64{failed, legacyNoise, replaced, continuedCID, successor.id(), newer.id()} {
		if _, ok := rows[gone]; ok {
			t.Fatalf("session %d should not be offered: %#v", gone, rows[gone])
		}
	}
	checks := []struct {
		id                     int64
		reason, action, detail string
	}{
		{exited, "exited", "history", "the parser fix is merged"},
		{archived, "archived", "history", "please add retries"},
		{interrupted, "restart", "relaunch", ""},
	}
	for _, c := range checks {
		row, ok := rows[c.id]
		if !ok {
			t.Fatalf("session %d missing from restorable: %#v", c.id, rows)
		}
		if row.str("reason") != c.reason || row.str("action") != c.action || row.str("preview") != c.detail {
			t.Fatalf("session %d: reason=%q action=%q preview=%q", c.id, row.str("reason"), row.str("action"), row.str("preview"))
		}
		if row.str("reopen_url") != fmt.Sprintf("/api/sessions/%d/reopen", c.id) || row.str("action_label") == "" || row.str("note") == "" {
			t.Fatalf("session %d lacks its action details: %#v", c.id, row)
		}
	}
	if !strings.HasPrefix(rows[archived].str("note"), "Unarchives it.") {
		t.Fatalf("archived note: %q", rows[archived].str("note"))
	}
	list := h.getList("/api/sessions/restorable")
	if list[0].id() != interrupted || list[1].id() != archived || list[2].id() != exited {
		t.Fatalf("restorable order is not newest first: %d %d %d", list[0].id(), list[1].id(), list[2].id())
	}
	if got := h.restorable("?q=PARSER+merged"); len(got) != 1 || got[exited] == nil {
		t.Fatalf("search by last message: %#v", got)
	}
	if got := h.restorable("?all=true"); got[replaced].num("superseded_by") != float64(successor.id()) || got[continuedCID].num("superseded_by") != float64(newer.id()) {
		t.Fatalf("all=true should include continued records with their successor: %#v %#v", got[replaced], got[continuedCID])
	}
	if code := h.status("GET", "/api/sessions/restorable?limit=0", nil); code != 400 {
		t.Fatalf("limit=0: %d", code)
	}
}

func TestReopenTracksAReleasedTerminalAgain(t *testing.T) {
	h := newHarness(t)
	adopted := h.post("/api/sessions/adopt", obj{"target_id": h.firstTargetID(), "tmux_session": "legacy-claude", "workdir": "/mock/demo-app"}, 201)
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", adopted.id()), nil, 200, nil)
	if row := h.restorable("")[adopted.id()]; row.str("action") != "track" || row.str("reason") != "released" {
		t.Fatalf("released row: %#v", row)
	}
	out := h.reopen(adopted.id(), obj{}, 201)
	if out.str("action") != "track" || out.sub("session").id() != adopted.id() || out.sub("session")["ended_at"] != nil {
		t.Fatalf("reopen released: %#v", out)
	}
}

func TestReopenShellOpensANewShellInTheSameFolder(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	shell := h.post("/api/shells", obj{"project_id": pid}, 201)
	h.patch(fmt.Sprintf("/api/sessions/%d", shell.id()), obj{"name": "my shell", "group_path": "Work"}, 200)
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", shell.id()), nil, 200, nil)
	if row := h.restorable("")[shell.id()]; row.str("action") != "shell" {
		t.Fatalf("closed shell: %#v", row)
	}
	out := h.reopen(shell.id(), obj{}, 201)
	next := out.sub("session")
	if out.str("action") != "shell" || next.id() == shell.id() || next.str("agent") != "shell" ||
		next.str("workdir") != shell.str("workdir") || next.str("name") != "my shell" || next.str("group_path") != "Work" {
		t.Fatalf("reopened shell: %#v", out)
	}
	if _, ok := h.restorable("")[shell.id()]; ok {
		t.Fatal("a reopened shell is still offered")
	}
}

func TestReopenRelaunchesAnInterruptedSessionInItsOwnRecord(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	row := h.session(obj{"project_id": pid, "name": "lost to reboot"})
	h.waitSessionStatus(row.id(), "running", "waiting", "idle")
	// The restart took the terminal: point the record at a pane that is gone.
	h.setRow(row.id(), map[string]any{"status": sessions.StatusInterrupted, "tmux_session": "gone-after-reboot"})
	if _, err := h.App.DB.InsertWrap(&store.Wrap{SessionID: row.id(), ProjectID: &pid, Summary: "WRAP-MARKER finish the migration"}); err != nil {
		t.Fatal(err)
	}
	before := len(h.mock().CmdLog())
	out := h.reopen(row.id(), obj{}, 201)
	if out.str("action") != "relaunch" || out.sub("session").id() != row.id() {
		t.Fatalf("relaunch: %#v", out)
	}
	fresh, err := h.App.DB.Session(row.id())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.EndedAt != nil || fresh.Status == sessions.StatusInterrupted || fresh.TmuxSession != fmt.Sprintf("lec-s%d", row.id()) {
		t.Fatalf("relaunched row: %#v", fresh)
	}
	launched := false
	for _, cmd := range h.mock().CmdLog()[before:] {
		if strings.HasPrefix(cmd, "tmux new-session") && strings.Contains(cmd, "WRAP-MARKER") {
			launched = true
		}
	}
	if !launched {
		t.Fatalf("relaunch was not primed with the last handoff: %v", h.mock().CmdLog()[before:])
	}
}

func TestReopenRefusesARunningTerminal(t *testing.T) {
	h := newHarness(t)
	row := h.session(obj{"project_id": h.seededProjectID(), "name": "still here"})
	h.waitSessionStatus(row.id(), "running", "waiting", "idle")
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/reopen", row.id()), obj{}); code != 409 {
		t.Fatalf("reopen a live session: %d", code)
	}
	// An interrupted row whose terminal did come back must not be launched twice.
	h.setRow(row.id(), map[string]any{"status": sessions.StatusInterrupted})
	code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/reopen", row.id()), obj{}, nil)
	if code != 409 || !strings.Contains(string(body), "still running") {
		t.Fatalf("reopen over a live terminal: %d %s", code, body)
	}
	if got, _ := h.App.DB.Session(row.id()); got.Status != sessions.StatusInterrupted {
		t.Fatalf("a refused relaunch changed the row: %q", got.Status)
	}
}

func TestReopenWithoutABoundConversationAsksForHistory(t *testing.T) {
	h := newHarness(t)
	row := h.session(obj{"project_id": h.seededProjectID(), "name": "unbound"})
	h.setRow(row.id(), map[string]any{"status": "dead", "ended_at": store.Now(), "end_reason": sessions.EndExited, "archived_at": store.Now()})
	out := h.reopen(row.id(), obj{}, 409)
	if out["needs_history"] != true {
		t.Fatalf("expected needs_history: %#v", out)
	}
	if got, _ := h.App.DB.Session(row.id()); got.ArchivedAt == nil {
		t.Fatal("asking for history must not unarchive the record")
	}
}

func TestReopenElsewhereContinuesFromTheLastHandoff(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	row := h.session(obj{"project_id": pid, "name": "claude work"})
	h.waitSessionStatus(row.id(), "running", "waiting", "idle")
	h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", row.id()), nil, 200, nil)
	wrapID, err := h.App.DB.InsertWrap(&store.Wrap{SessionID: row.id(), ProjectID: &pid, Summary: "HANDOFF-MARKER next: ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if plan := h.restorable("")[row.id()]; plan.str("action") != "handoff" {
		t.Fatalf("closed row with a wrap: %#v", plan)
	}
	before := len(h.mock().CmdLog())
	out := h.reopen(row.id(), obj{"agent": "codex"}, 201)
	next := out.sub("session")
	if out.str("action") != "continue" || next.str("agent") != "codex" || next.id() == row.id() || !strings.Contains(out.str("message"), "last handoff") {
		t.Fatalf("continue in codex: %#v", out)
	}
	primed := false
	for _, cmd := range h.mock().CmdLog()[before:] {
		if strings.HasPrefix(cmd, "tmux new-session") && strings.Contains(cmd, "HANDOFF-MARKER") && strings.Contains(cmd, "wrote this handoff") {
			primed = true
		}
	}
	if !primed {
		t.Fatalf("codex was not primed with the handoff: %v", h.mock().CmdLog()[before:])
	}
	wraps, _ := h.App.DB.SessionWraps(row.id())
	if len(wraps) != 1 || wraps[0].ID != wrapID || wraps[0].NextSessionID == nil || *wraps[0].NextSessionID != next.id() {
		t.Fatalf("the handoff was not linked to its successor: %#v", wraps)
	}
	if view := h.sessionByID(next.id()); view.num("predecessor_id") != float64(row.id()) {
		t.Fatalf("successor does not point back: %#v", view["predecessor_id"])
	}
	if _, ok := h.restorable("")[row.id()]; ok {
		t.Fatal("a continued record is still offered")
	}
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/reopen", row.id()), obj{"agent": "nope"}); code != 422 {
		t.Fatalf("unknown agent: %d", code)
	}
}

func TestReopenFailureKeepsTheRecordArchived(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	row := h.session(obj{"project_id": pid, "name": "archived gemini"})
	h.setRow(row.id(), map[string]any{"agent": "gemini", "status": "dead", "ended_at": store.Now(), "archived_at": store.Now(), "end_reason": sessions.EndStopped, "launch_config_json": ""})
	if plan := h.restorable("")[row.id()]; plan.str("action") != "fresh" {
		t.Fatalf("archived gemini: %#v", plan)
	}
	// A project on another target makes the launch fail after unarchiving.
	other, err := h.App.DB.InsertTarget(&store.Target{Name: "elsewhere", Kind: "local", Status: "online", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.setRow(row.id(), map[string]any{"target_id": other.ID})
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/reopen", row.id()), obj{}); code != 409 {
		t.Fatalf("failing reopen: %d", code)
	}
	if got, _ := h.App.DB.Session(row.id()); got.ArchivedAt == nil {
		t.Fatal("a failed reopen left the record unarchived")
	}
}

// TestReopenResumesTheBoundConversationUnderItsOwnName uses the isolated real
// target, where the read-only history reader validates the conversation.
func TestReopenResumesTheBoundConversationUnderItsOwnName(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Mock = false
		c.SessionPoll = time.Hour
	})
	cid := "66666666-6666-4666-8666-666666666666"
	row := nativeRecentSession(t, h, cid, "")
	h.setRow(row.ID, map[string]any{"end_reason": sessions.EndStopped})
	if plan := h.restorable("")[row.ID]; plan.str("action") != "resume" {
		t.Fatalf("bound row: %#v", plan)
	}
	out := h.reopen(row.ID, obj{}, 201)
	next := out.sub("session")
	if out.str("action") != "resume" || next.str("resume_id") != cid || next.str("name") != row.Name {
		t.Fatalf("reopen bound conversation: %#v", out)
	}
	if _, ok := h.restorable("")[row.ID]; ok {
		t.Fatal("the resumed record is still offered")
	}
}
