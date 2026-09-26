package api_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestAnExitedAgentIsNotIdleAndRevivesFresh(t *testing.T) {
	h := newHarness(t)
	row := h.session(obj{"project_id": h.seededProjectID(), "name": "quits"})
	h.waitSessionStatus(row.id(), "running", "waiting", "idle")
	// Old enough for the probe, which leaves just-launched sessions alone.
	h.setRow(row.id(), map[string]any{"created_at": store.Now() - 120})
	ctx := context.Background()
	h.App.Sessions.SetAgentProbeInterval(time.Millisecond)
	h.App.Sessions.Poll(ctx)
	if got := h.sessionByID(row.id()); got["agent_exited_at"] != nil {
		t.Fatalf("a running agent was reported exited: %#v", got["agent_exited_at"])
	}
	h.mock().ExitPaneAgent(row.str("tmux_session"))
	h.waitUntil("the exited agent to be noticed", func() bool {
		h.App.Sessions.Poll(ctx)
		return h.sessionByID(row.id())["agent_exited_at"] != nil
	})
	var next obj
	h.decode("POST", fmt.Sprintf("/api/sessions/%d/revive", row.id()), obj{}, 201, &next)
	if next.id() == row.id() || next.str("name") != "quits" || next["agent_exited_at"] != nil {
		t.Fatalf("revive: %#v", next)
	}
	old, _ := h.App.DB.Session(row.id())
	if old.EndedAt == nil {
		t.Fatal("revive left the old terminal tracked")
	}
	if _, listed := h.restorable("")[row.id()]; listed {
		t.Fatal("a revived session is still offered by Restore")
	}
}

func TestRelaunchNoticeListsAndDismisses(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "came back"})
	b := h.session(obj{"project_id": pid, "name": "long ago"})
	h.setRow(a.id(), map[string]any{"relaunched_at": store.Now() - 30})
	h.setRow(b.id(), map[string]any{"relaunched_at": store.Now() - 30*24*3600})
	rows := h.getList("/api/sessions/relaunched")
	if len(rows) != 1 || rows[0].id() != a.id() {
		t.Fatalf("relaunched: %#v", rows)
	}
	h.post("/api/sessions/relaunched/dismiss", obj{}, 200)
	if rows := h.getList("/api/sessions/relaunched"); len(rows) != 0 {
		t.Fatalf("dismissed notice still lists: %#v", rows)
	}
	// A later restart shows its own sessions again.
	h.setRow(b.id(), map[string]any{"relaunched_at": store.Now() + 1})
	if rows := h.getList("/api/sessions/relaunched"); len(rows) != 1 || rows[0].id() != b.id() {
		t.Fatalf("a later relaunch: %#v", rows)
	}
}

// TestALostAdoptedSessionResumesItsLikelyConversation uses the isolated real
// target: the read-only history reader lists the folder's conversations.
func TestALostAdoptedSessionResumesItsLikelyConversation(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Mock = false
		c.SessionPoll = time.Hour
	})
	near := "77777777-7777-4777-8777-777777777777"
	row := nativeRecentSession(t, h, near, "")
	var launch sessions.LaunchConfiguration
	home := ""
	if cfg, err := h.App.Sessions.SessionLaunchConfiguration(row); err == nil {
		launch = *cfg
		home = launch.Spec.Env["CLAUDE_CONFIG_DIR"]
	}
	far := "88888888-8888-4888-8888-888888888888"
	writeNativeLifecycleHistory(t, "claude", home, row.Workdir, far)
	lost := store.Now() - 600
	farPath := filepath.Join(home, "projects", claudeProjectSlug(row.Workdir), far+".jsonl")
	nearPath := filepath.Join(home, "projects", claudeProjectSlug(row.Workdir), near+".jsonl")
	if err := os.Chtimes(farPath, time.Unix(int64(lost-86400), 0), time.Unix(int64(lost-86400), 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(nearPath, time.Unix(int64(lost-20), 0), time.Unix(int64(lost-20), 0)); err != nil {
		t.Fatal(err)
	}
	// An adopted session: nothing was ever bound to it.
	h.setRow(row.ID, map[string]any{"origin": "discovered", "resume_id": "", "native_recovery_cid": "",
		"last_activity_at": lost, "ended_at": lost + 60, "end_reason": sessions.EndExited})
	if plan := h.restorable("")[row.ID]; plan.str("action") != "history" {
		t.Fatalf("before matching: %#v", plan)
	}
	if got := h.App.Sessions.MatchLostAdopted(context.Background(), row.ID); got != near {
		t.Fatalf("match: got %q want %q", got, near)
	}
	plan := h.restorable("")[row.ID]
	if plan.str("action") != "resume" || plan["likely_match"] != true {
		t.Fatalf("after matching: %#v", plan)
	}
	out := h.reopen(row.ID, obj{}, 201)
	if out.sub("session").str("resume_id") != near {
		t.Fatalf("resumed the wrong conversation: %#v", out)
	}

	// Two conversations near the loss: no guess, the picker stays.
	other := nativeRecentSession(t, h, "99999999-9999-4999-8999-999999999999", "")
	cfg, _ := h.App.Sessions.SessionLaunchConfiguration(other)
	otherHome := cfg.Spec.Env["CLAUDE_CONFIG_DIR"]
	second := "aaaaaaaa-9999-4999-8999-999999999999"
	writeNativeLifecycleHistory(t, "claude", otherHome, other.Workdir, second)
	for _, cid := range []string{"99999999-9999-4999-8999-999999999999", second} {
		p := filepath.Join(otherHome, "projects", claudeProjectSlug(other.Workdir), cid+".jsonl")
		if err := os.Chtimes(p, time.Unix(int64(lost-10), 0), time.Unix(int64(lost-10), 0)); err != nil {
			t.Fatal(err)
		}
	}
	h.setRow(other.ID, map[string]any{"origin": "discovered", "resume_id": "", "native_recovery_cid": "",
		"last_activity_at": lost, "ended_at": lost + 60, "end_reason": sessions.EndExited})
	if got := h.App.Sessions.MatchLostAdopted(context.Background(), other.ID); got != "" {
		t.Fatalf("an ambiguous folder was matched to %q", got)
	}
	if out := h.reopen(other.ID, obj{}, 409); out["needs_history"] != true {
		t.Fatalf("ambiguous: %#v", out)
	}
}

func TestRevivingAnAdoptedExitedAgentLeavesTheShellOpen(t *testing.T) {
	h := newHarness(t)
	row := h.post("/api/sessions/adopt", obj{"target_id": h.firstTargetID(), "tmux_session": "legacy-claude", "workdir": "/mock/demo-app"}, 201)
	h.setRow(row.id(), map[string]any{"created_at": store.Now() - 120, "status": "idle"})
	h.App.Sessions.SetAgentProbeInterval(time.Millisecond)
	h.mock().ExitPaneAgent("legacy-claude")
	ctx := context.Background()
	h.waitUntil("the adopted agent's exit to be noticed", func() bool {
		h.App.Sessions.Poll(ctx)
		return h.sessionByID(row.id())["agent_exited_at"] != nil
	})
	var next obj
	h.decode("POST", fmt.Sprintf("/api/sessions/%d/revive", row.id()), obj{}, 201, &next)
	old, _ := h.App.DB.Session(row.id())
	if old.EndReason != sessions.EndReleased {
		t.Fatalf("the operator's shell should be released, not closed: %q", old.EndReason)
	}
	for _, cmd := range h.mock().CmdLog() {
		if strings.Contains(cmd, "kill-session") && strings.Contains(cmd, "legacy-claude") {
			t.Fatalf("revive closed the adopted terminal: %s", cmd)
		}
	}
	if next.id() == row.id() || next.str("agent") != "claude" {
		t.Fatalf("revive: %#v", next)
	}
}
