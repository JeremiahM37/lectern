package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/limits"
)

// Accounts and the swap policy end to end on the mock target
// (docs/accounts.md). The real CLIs and a real terminal swap are covered by
// internal/sessions' account swap integration test.

func TestAccountsRegisterWithoutExposingDirectories(t *testing.T) {
	h := newHarness(t)
	h.post("/api/accounts", obj{"agent": "aider", "label": "x"}, 422)
	h.post("/api/accounts", obj{"agent": "claude", "label": ""}, 422)
	h.post("/api/accounts", obj{"agent": "claude", "label": "x", "dir": "relative/path"}, 422)
	work := h.post("/api/accounts", obj{"agent": "claude", "label": "Work"}, 201)
	h.post("/api/accounts", obj{"agent": "claude", "label": "work"}, 409)
	h.post("/api/accounts", obj{"agent": "claude", "label": "Mine", "dir": "/srv/claude-mine"}, 201)
	if !h.cmdLogHas("chmod 700") {
		t.Fatal("the account directory was not made private")
	}
	list := h.getList("/api/accounts")
	if len(list) != 3 {
		t.Fatalf("%d accounts, want Default + 2: %v", len(list), list)
	}
	if list[0].str("label") != "Default" || list[0]["default"] != true {
		t.Fatalf("the CLI's own login was not registered first: %v", list[0])
	}
	for _, row := range list {
		for _, key := range []string{"dir", "Dir"} {
			if _, ok := row[key]; ok {
				t.Fatalf("an account directory reached the API: %v", row)
			}
		}
		if row["signed_in"] != true {
			t.Fatalf("signed_in: %v", row)
		}
	}
	h.status("DELETE", fmt.Sprintf("/api/accounts/%d", work.id()), nil)
	if n := len(h.getList("/api/accounts")); n != 2 {
		t.Fatalf("%d accounts after remove", n)
	}
}

func TestAccountSignInOpensATerminalWithTheAccountDirectory(t *testing.T) {
	h := newHarness(t)
	acct := h.post("/api/accounts", obj{"agent": "codex", "label": "second"}, 201)
	sess := h.post(fmt.Sprintf("/api/accounts/%d/login", acct.id()), obj{}, 201)
	if sess.str("agent") != "shell" || !strings.HasPrefix(sess.str("name"), "Sign in") {
		t.Fatalf("sign-in session: %v", sess)
	}
	if !h.cmdLogHas("CODEX_HOME=/mock/home/.lectern/accounts/codex/second") || !h.cmdLogHas("codex login") {
		t.Fatal("the sign-in terminal does not run codex login with the account's CODEX_HOME")
	}
}

// projectTarget is the target a project's work runs on.
func (h *harness) projectTarget(pid int64) int64 {
	h.t.Helper()
	p, err := h.App.DB.Project(pid)
	if err != nil {
		h.t.Fatal(err)
	}
	return p.TargetID
}

func TestLimitedTaskSwapsToTheNextAccountAndResumes(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.post("/api/accounts", obj{"agent": "claude", "label": "spare", "target_id": h.projectTarget(pid)}, 201)
	h.request2("PUT", "/api/limits/policy", obj{"project_id": pid, "policy": obj{"mode": "swap", "then": "wait"}}, 200)
	task := h.task(pid, "limited", "Refactor the parser [mock:limit]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("the continuation attempt", func() bool { return len(h.attempts(task.id())) == 2 })
	atts := h.attempts(task.id())
	first, next := atts[0], atts[1]
	if first.N > next.N {
		first, next = next, first
	}
	if next.AccountID == nil || next.NotBefore != nil || next.ResumeSession == "" ||
		next.WorktreePath != first.WorktreePath || next.Agent != first.Agent {
		t.Fatalf("continuation: %+v", next)
	}
	spare, _ := h.App.DB.Account(*next.AccountID)
	if spare == nil || spare.Label != "spare" {
		t.Fatalf("continuation runs under %+v, want spare", spare)
	}
	hold, _ := h.App.DB.LatestLimitHoldForTask(task.id())
	if hold.State != limits.StateSwapped || hold.AccountTo == nil || *hold.AccountTo != spare.ID {
		t.Fatalf("hold: %+v", hold)
	}
	// The default login is remembered as limited; the continuation runs with
	// spare's config directory and resumes the same conversation.
	dflt, _ := h.App.DB.DefaultAccountFor(spare.TargetID, "claude")
	if dflt.LimitedAt == nil {
		t.Fatal("the account that hit the limit was not recorded")
	}
	h.waitStatus(task.id(), "review")
	if !h.cmdLogHas("CLAUDE_CONFIG_DIR=/mock/home/.lectern/accounts/claude/spare") ||
		!h.cmdLogHas("--resume "+first.SessionID) {
		t.Fatal("the continuation did not resume under spare's CLAUDE_CONFIG_DIR")
	}
}

func TestLimitedTaskWithEveryAccountLimitedFallsBack(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	spare := h.post("/api/accounts", obj{"agent": "claude", "label": "spare", "target_id": h.projectTarget(pid)}, 201)
	until := float64(4102444800) // far in the future
	h.App.DB.MarkAccountLimited(int64(spare.id()), 1, &until)
	h.request2("PUT", "/api/limits/policy", obj{"project_id": pid, "policy": obj{"mode": "swap", "then": "wait"}}, 200)
	task := h.task(pid, "limited", "Refactor the parser [mock:limit]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("the continuation attempt", func() bool { return len(h.attempts(task.id())) == 2 })
	// The second attempt can start a moment before the hold is marked requeued.
	h.waitUntil("the hold to be requeued", func() bool {
		hold, _ := h.App.DB.LatestLimitHoldForTask(task.id())
		return hold != nil && hold.State == limits.StateRequeued
	})
	hold, _ := h.App.DB.LatestLimitHoldForTask(task.id())
	if hold.State != limits.StateRequeued || hold.Policy != limits.ModeWait {
		t.Fatalf("fallback hold: %+v", hold)
	}
}

func TestSessionCardShowsItsAccountOnlyWithSeveralLogins(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "name": "acct", "agent": "claude"})
	if got := h.sessionByID(sess.id()).str("account"); got != "" {
		t.Fatalf("account shown with one login: %q", got)
	}
	h.post("/api/accounts", obj{"agent": "claude", "label": "spare", "target_id": h.projectTarget(h.seededProjectID())}, 201)
	if got := h.sessionByID(sess.id()).str("account"); got != "Default" {
		t.Fatalf("account label %q, want Default", got)
	}
}

func TestSwapPolicyIsAcceptedAndValidated(t *testing.T) {
	h := newHarness(t)
	h.request2("PUT", "/api/limits/policy", obj{"policy": obj{"mode": "swap", "then": "handoff"}}, 422)
	got := h.request2("PUT", "/api/limits/policy", obj{"policy": obj{"mode": "swap", "then": "handoff", "fallback_agent": "codex"}}, 200)
	if got.sub("effective").str("mode") != "swap" || got.sub("effective").str("then") != "handoff" {
		t.Fatalf("policy: %v", got)
	}
}

// Codex reports its windows in the rollout; they show on the account the
// session ran under, and a full one keeps that account out of the rotation.
func TestCodexRateWindowsShowOnTheAccount(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.post("/api/accounts", obj{"agent": "codex", "label": "spare", "target_id": h.projectTarget(pid)}, 201)
	sess := h.session(obj{"project_id": pid, "name": "codex live", "agent": "codex"})
	row, _ := h.App.DB.Session(sess.id())
	in := agentevents.New(h.App.DB, h.App.Bus)
	if err := in.IngestCodexUsage(row, &agentevents.CodexUsage{RateLimits: []agentevents.CodexRateWindow{
		{UsedPercent: 100, WindowMinutes: 300, ResetsAt: 4102444800},
		{UsedPercent: 41.6, WindowMinutes: 10080, ResetsAt: 4102444800},
	}}); err != nil {
		t.Fatal(err)
	}
	var dflt obj
	for _, a := range h.getList("/api/accounts") {
		if a["default"] == true {
			dflt = a
		}
	}
	usage := dflt.sub("usage")
	if usage.num("rate_5h_pct") != 100 || usage.num("rate_7d_pct") != 42 || dflt.num("blocked_until") != 4102444800 {
		t.Fatalf("default codex account: %v", dflt)
	}
}
