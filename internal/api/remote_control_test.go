package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func lastLaunch(h *harness) string {
	h.t.Helper()
	last := ""
	for _, c := range h.mock().CmdLog() {
		if strings.HasPrefix(c, "tmux new-session") {
			last = c
		}
	}
	return last
}

// A claude session started with remote_control:true is launched with the
// named Remote Control flag, recorded on its row, and picks up the claude.ai
// link from its screen.
func TestRemoteControlAtLaunch(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "phone build", "agent": "claude", "remote_control": true, "prime": "start"})
	if cmd := h.launchCmd(); !strings.Contains(cmd, "--remote-control=phone build") {
		t.Fatalf("launch has no named Remote Control flag: %s", cmd)
	}
	if sess["remote_control"] != true {
		t.Fatalf("row not marked: %v", sess)
	}
	h.waitUntil("the claude.ai link", func() bool {
		return strings.HasPrefix(h.sessionByID(sess.id()).str("remote_control_url"), "https://claude.ai/code/session_")
	})
	if got := h.sessionByID(sess.id()); got["remote_control_available"] != nil {
		t.Fatalf("a session that has it is not offered it again: %v", got)
	}
}

func TestRemoteControlRefusedForOtherAgents(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	var out obj
	h.decode("POST", "/api/sessions", obj{"project_id": pid, "agent": "codex", "remote_control": true}, 409, &out)
	if !strings.Contains(out.str("detail"), "claude") {
		t.Fatalf("refusal does not say why: %v", out)
	}
	plain := h.session(obj{"project_id": pid, "agent": "codex"})
	avail := h.sessionByID(plain.id()).sub("remote_control_available")
	if avail["available"] == true || avail.str("reason") == "" {
		t.Fatalf("codex must report Remote Control unavailable with a reason: %v", avail)
	}
}

// Turning it on later types /remote-control into the idle session, marks the
// row and its launch configuration, so a relaunch of the record keeps it.
func TestRemoteControlEnableExistingSessionAndKeepOnRelaunch(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	row := h.session(obj{"project_id": pid, "name": "later", "agent": "claude", "remote_control": false})
	h.waitSessionStatus(row.id(), "waiting", "idle")
	if avail := h.sessionByID(row.id()).sub("remote_control_available"); avail["available"] != true {
		t.Fatalf("an idle claude session should offer Remote Control: %v", avail)
	}
	out := h.post(fmt.Sprintf("/api/sessions/%d/remote-control", row.id()), obj{}, 200)
	if out["remote_control"] != true || !strings.HasPrefix(out.str("remote_control_url"), "https://claude.ai/code/session_") {
		t.Fatalf("enable: %v", out)
	}
	if !strings.Contains(h.mock().PaneText(fmt.Sprintf("lec-s%d", row.id())), "❯ /remote-control later") {
		t.Fatal("the named command was not typed into the session")
	}
	// Again is a no-op, not a second command into the pane.
	h.post(fmt.Sprintf("/api/sessions/%d/remote-control", row.id()), obj{}, 200)

	h.setRow(row.id(), map[string]any{"status": sessions.StatusInterrupted, "tmux_session": "gone-after-reboot"})
	before := len(h.mock().CmdLog())
	h.reopen(row.id(), obj{}, 201)
	relaunch := ""
	for _, c := range h.mock().CmdLog()[before:] {
		if strings.HasPrefix(c, "tmux new-session") {
			relaunch = c
		}
	}
	if !strings.Contains(relaunch, "--remote-control=later") {
		t.Fatalf("a relaunch dropped Remote Control: %s", relaunch)
	}
}

// Claude Code's one-time consent question is never answered for the person:
// it is closed and the call says how to accept it.
func TestRemoteControlConsentIsNotAnswered(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	row := h.session(obj{"project_id": pid, "name": "needs consent", "agent": "claude", "remote_control": false})
	h.mock().RemoteControlConsent = true
	h.waitSessionStatus(row.id(), "waiting", "idle")
	var out obj
	h.decode("POST", fmt.Sprintf("/api/sessions/%d/remote-control", row.id()), obj{}, 409, &out)
	if out.str("code") != "remote_control_consent" || !strings.Contains(out.str("detail"), "/remote-control") {
		t.Fatalf("consent refusal: %v", out)
	}
	if pane := h.mock().PaneText(fmt.Sprintf("lec-s%d", row.id())); strings.Contains(pane, "Enable Remote Control") {
		t.Fatalf("the question was left open on screen:\n%s", pane)
	}
	if got := h.sessionByID(row.id()); got["remote_control"] == true {
		t.Fatal("a session whose consent was not given must not be marked")
	}
}

func TestRemoteControlRefusedWhileBusy(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	row := h.session(obj{"project_id": pid, "agent": "claude", "remote_control": false})
	h.waitSessionStatus(row.id(), "waiting", "idle")
	if err := h.App.DB.Update("sessions", row.id(), map[string]any{"status": sessions.StatusRunning, "updated_at": store.Now()}); err != nil {
		t.Fatal(err)
	}
	var out obj
	h.decode("POST", fmt.Sprintf("/api/sessions/%d/remote-control", row.id()), obj{}, 409, &out)
	if out.str("code") != "busy" {
		t.Fatalf("busy refusal: %v", out)
	}
}

// New claude sessions open in the Claude app unless the setting is off or the
// launch opts out; a session that cannot use it starts without it.
func TestRemoteControlByDefault(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	on := h.session(obj{"project_id": pid, "name": "default on", "agent": "claude"})
	if cmd := h.launchCmd(); !strings.Contains(cmd, "--remote-control=default on") || on["remote_control"] != true {
		t.Fatalf("a new claude session did not get Remote Control by default: %s %v", cmd, on)
	}
	out := h.session(obj{"project_id": pid, "name": "opted out", "agent": "claude", "remote_control": false})
	if cmd := lastLaunch(h); strings.Contains(cmd, "--remote-control") || out["remote_control"] == true {
		t.Fatalf("remote_control:false did not opt out: %s", cmd)
	}
	codex := h.session(obj{"project_id": pid, "agent": "codex"})
	if codex["remote_control"] == true {
		t.Fatalf("codex cannot use it and must start without it: %v", codex)
	}
	h.decode("PUT", "/api/settings", obj{"remote_control_default": "0"}, 200, nil)
	off := h.session(obj{"project_id": pid, "name": "setting off", "agent": "claude"})
	if cmd := lastLaunch(h); strings.Contains(cmd, "--remote-control") || off["remote_control"] == true {
		t.Fatalf("the setting off still turned it on: %s", cmd)
	}
}

// "Turn it on everywhere" turns it on in idle claude sessions, lists the
// ones that cannot use it with the reason, and turns busy ones on once they
// wait for you.
func TestRemoteControlForEveryRunningSession(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.decode("PUT", "/api/settings", obj{"remote_control_default": "0"}, 200, nil)
	idle := h.session(obj{"project_id": pid, "name": "idle one", "agent": "claude"})
	busy := h.session(obj{"project_id": pid, "name": "busy one", "agent": "claude"})
	codex := h.session(obj{"project_id": pid, "agent": "codex"})
	h.waitSessionStatus(idle.id(), "waiting", "idle")
	h.waitSessionStatus(busy.id(), "waiting", "idle")
	if err := h.App.DB.Update("sessions", busy.id(), map[string]any{"status": sessions.StatusRunning, "updated_at": store.Now()}); err != nil {
		t.Fatal(err)
	}
	res := h.post("/api/remote-control/all", obj{}, 200)
	got := map[int64]string{}
	for _, row := range res["sessions"].([]any) {
		r := obj(row.(map[string]any))
		got[r.id()] = r.str("status")
		if r.id() == codex.id() && r.str("reason") == "" {
			t.Fatalf("a skipped session needs its reason: %v", r)
		}
	}
	if got[idle.id()] != "on" || got[busy.id()] != "pending" || got[codex.id()] != "skipped" {
		t.Fatalf("outcomes: %v", got)
	}
	if err := h.App.DB.Update("sessions", busy.id(), map[string]any{"status": "waiting", "updated_at": store.Now()}); err != nil {
		t.Fatal(err)
	}
	h.App.Sessions.RetryRemoteControl(t.Context())
	if h.sessionByID(busy.id())["remote_control"] != true {
		t.Fatal("the busy session was not turned on once it was waiting")
	}
	again := h.post("/api/remote-control/all", obj{}, 200)
	for _, row := range again["sessions"].([]any) {
		r := obj(row.(map[string]any))
		if r.id() == idle.id() && r.str("status") != "already" {
			t.Fatalf("a second run must not type the command again: %v", r)
		}
	}
}
