package api_test

// Session checks: GET/POST /api/sessions/{id}/checks and the project
// settings "auto-detected check command" preview. See internal/checks and
// docs/agent-events.md section 4.

import (
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func TestSessionCheckRunNowRecordsAndSurfacesOnSession(t *testing.T) {
	h := newHarness(t)
	p := h.project("checked", obj{"verify_cmd": "mockverify-pass"})
	sess := h.session(obj{"project_id": p.id()})

	code, _ := h.request("POST", fmt.Sprintf("/api/sessions/%d/checks", sess.id()), nil, nil)
	if code != 202 {
		t.Fatalf("run-now: got %d, want 202", code)
	}

	var rows []obj
	h.waitUntil("the check to finish", func() bool {
		rows = h.getList(fmt.Sprintf("/api/sessions/%d/checks", sess.id()))
		return len(rows) == 1 && rows[0].str("status") != "running"
	})
	if rows[0].str("command") != "mockverify-pass" || rows[0].str("status") != "passed" {
		t.Fatalf("check row: %v", rows[0])
	}
	if rows[0].str("output_tail") == "" {
		t.Errorf("expected the mock's stdout to be captured, got empty output_tail")
	}

	got := h.sessionByID(sess.id())
	last := got.sub("last_check")
	if last.str("status") != "passed" || last.str("command") != "mockverify-pass" {
		t.Fatalf("session's last_check summary: %v", last)
	}
}

func TestSessionCheckRunNowSkippedWithoutCommand(t *testing.T) {
	h := newHarness(t)
	p := h.project("unchecked", nil)
	sess := h.session(obj{"project_id": p.id()})

	h.post(fmt.Sprintf("/api/sessions/%d/checks", sess.id()), nil, 202)

	var rows []obj
	h.waitUntil("a skipped row to appear", func() bool {
		rows = h.getList(fmt.Sprintf("/api/sessions/%d/checks", sess.id()))
		return len(rows) == 1
	})
	if rows[0].str("status") != "skipped" {
		t.Fatalf("expected a skipped row when no check command is configured, got %v", rows[0])
	}
}

func TestSessionCheckListEmptyBeforeAnyRun(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID()})
	rows := h.getList(fmt.Sprintf("/api/sessions/%d/checks", sess.id()))
	if len(rows) != 0 {
		t.Fatalf("expected no checks before any trigger, got %v", rows)
	}
	if got := h.sessionByID(sess.id())["last_check"]; got != nil {
		t.Fatalf("expected no last_check before any trigger, got %v", got)
	}
}

// Running a check now is an operator action, like deciding an approval — a
// local (loopback, no tailscale identity) caller in tailscale mode is not a
// human and must be refused. See TestApprovalDecisionNeedsHumanInTailscaleMode.
func TestSessionCheckRunNowNeedsHumanInTailscaleMode(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth = "tailscale"; c.TailscaleUsers = "nobody@example.com" })
	sess := h.session(obj{"project_id": h.seededProjectID()})
	code, body := h.request("POST", fmt.Sprintf("/api/sessions/%d/checks", sess.id()), nil, nil)
	if code != 403 {
		t.Fatalf("a local principal running a check in tailscale mode: got %d, want 403 — %s", code, body)
	}
}

func TestProjectCheckCommandPreviewConfiguredVsAuto(t *testing.T) {
	h := newHarness(t)
	configured := h.project("configured", obj{"verify_cmd": "go test ./..."})
	got := h.get(fmt.Sprintf("/api/projects/%d/check-command", configured.id()))
	if got.str("command") != "go test ./..." || got.str("source") != "configured" {
		t.Fatalf("configured preview: %v", got)
	}

	bare := h.project("bare", nil)
	got = h.get(fmt.Sprintf("/api/projects/%d/check-command", bare.id()))
	if got.str("source") != "none" || got.str("command") != "" {
		t.Fatalf("expected no auto-detect on a bare mock repo, got %v", got)
	}
}
