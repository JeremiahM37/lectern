package decisionlog

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeClock lets a test place each hook at an exact moment.
type fakeClock struct{ t float64 }

func newTestRecorder(t *testing.T) (*Recorder, *store.DB, *fakeClock) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := &fakeClock{t: 1000}
	r := New(db)
	r.now = func() float64 { return clock.t }
	return r, db, clock
}

func at(clock *fakeClock, t float64) { clock.t = t }

var testSession = &store.Session{ID: 7, Agent: "claude"}

func fire(r *Recorder, event, body string) {
	r.HandleHookEvent(testSession, event, []byte(body), "", false)
}

func rows(t *testing.T, r *Recorder, db *store.DB) []*store.Decision {
	t.Helper()
	r.Wait()
	out, err := db.Decisions(0, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func only(t *testing.T, got []*store.Decision) *store.Decision {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("want exactly one decision, got %d: %+v", len(got), got)
	}
	return got[0]
}

func floatOf(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

// A Stop followed by a prompt is a reply: its latency is the time the agent's
// turn waited for a person, and only the prompt's length is kept.
func TestStopThenPromptRecordsReplyLatency(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 100)
	fire(r, agentevents.EventStop, `{"last_assistant_message":"done"}`)
	at(clock, 107)
	fire(r, agentevents.EventUserPromptSubmit, `{"prompt":"ship it please"}`)

	d := only(t, rows(t, r, db))
	if d.Kind != KindTurnResponse || d.Decision != "reply" || d.Via != ViaTerminal {
		t.Fatalf("reply row: %+v", d)
	}
	if floatOf(d.LatencyS) != 7 || floatOf(d.WaitedSince) != 100 {
		t.Fatalf("latency %v waited %v, want 7 and 100", floatOf(d.LatencyS), floatOf(d.WaitedSince))
	}
	if d.Agent != "claude" || d.SessionID == nil || *d.SessionID != 7 {
		t.Fatalf("session attribution: %+v", d)
	}
	if got := string(d.Detail); got != `{"prompt_chars":14}` {
		t.Fatalf("detail: %s", got)
	}
	if strings.Contains(string(d.Detail), "ship") {
		t.Fatal("prompt text reached the log")
	}
}

// A prompt that arrives while the agent is still working interrupts it. No
// Stop came first, so there was no wait to measure.
func TestPromptWhileWorkingIsAnInterrupt(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 10)
	fire(r, agentevents.EventUserPromptSubmit, `{"prompt":"go"}`)
	at(clock, 20)
	fire(r, agentevents.EventUserPromptSubmit, `{"prompt":"wait, stop"}`)

	d := only(t, rows(t, r, db))
	if d.Kind != KindInterrupt || d.Decision != "interrupt" {
		t.Fatalf("interrupt row: %+v", d)
	}
	if d.LatencyS != nil || d.WaitedSince != nil {
		t.Fatalf("an interrupt has no wait: latency=%v waited=%v", d.LatencyS, d.WaitedSince)
	}
}

// The first prompt of a session is neither a reply nor an interrupt.
func TestFirstPromptRecordsNothing(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 10)
	fire(r, agentevents.EventUserPromptSubmit, `{"prompt":"hello"}`)
	if got := rows(t, r, db); len(got) != 0 {
		t.Fatalf("a first prompt recorded %+v", got)
	}
}

// A permission request answered by a PostToolUse for the same tool is allowed,
// and the latency runs from the request.
func TestPermissionThenPostToolUseIsAllowed(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 5)
	fire(r, agentevents.EventPermissionRequest, `{"tool_name":"Bash","tool_input":{"command":"rm -rf x"}}`)
	at(clock, 9)
	fire(r, agentevents.EventPostToolUse, `{"tool_name":"Bash"}`)

	d := only(t, rows(t, r, db))
	if d.Kind != KindPermissionPrompt || d.Decision != "allowed" || d.Via != ViaInferred {
		t.Fatalf("allowed row: %+v", d)
	}
	if floatOf(d.LatencyS) != 4 || floatOf(d.WaitedSince) != 5 {
		t.Fatalf("latency %v waited %v, want 4 and 5", floatOf(d.LatencyS), floatOf(d.WaitedSince))
	}
	if strings.Contains(string(d.Detail), "rm -rf") {
		t.Fatal("command text reached the log")
	}
}

// A PostToolUse for some other tool does not answer the request.
func TestPostToolUseForAnotherToolLeavesRequestOpen(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 5)
	fire(r, agentevents.EventPermissionRequest, `{"tool_name":"Bash"}`)
	at(clock, 6)
	fire(r, agentevents.EventPostToolUse, `{"tool_name":"Read"}`)
	if got := rows(t, r, db); len(got) != 0 {
		t.Fatalf("an unrelated tool resolved the request: %+v", got)
	}
	at(clock, 8)
	fire(r, agentevents.EventStop, `{}`)
	d := only(t, rows(t, r, db))
	if d.Decision != "denied_or_interrupted" {
		t.Fatalf("the open request should close at the Stop: %+v", d)
	}
}

// A Stop before any PostToolUse means the request was denied or cut short.
func TestPermissionThenStopIsDeniedOrInterrupted(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 5)
	fire(r, agentevents.EventPermissionRequest, `{"tool_name":"Bash"}`)
	at(clock, 8)
	fire(r, agentevents.EventStop, `{}`)

	got := rows(t, r, db)
	var perm *store.Decision
	for _, d := range got {
		if d.Kind == KindPermissionPrompt {
			perm = d
		}
	}
	if perm == nil || perm.Decision != "denied_or_interrupted" || floatOf(perm.LatencyS) != 3 {
		t.Fatalf("permission outcome: %+v", got)
	}
}

// A new prompt also cuts an open permission request short.
func TestPermissionThenPromptIsDeniedOrInterrupted(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 5)
	fire(r, agentevents.EventPermissionRequest, `{"tool_name":"Bash"}`)
	at(clock, 6)
	fire(r, agentevents.EventUserPromptSubmit, `{"prompt":"never mind"}`)

	var outcomes []string
	for _, d := range rows(t, r, db) {
		if d.Kind == KindPermissionPrompt {
			outcomes = append(outcomes, d.Decision)
		}
	}
	if len(outcomes) != 1 || outcomes[0] != "denied_or_interrupted" {
		t.Fatalf("permission outcomes: %v", outcomes)
	}
}

// Review actions take their latency from the session's last Stop, and are
// null when there has been none.
func TestReviewLatencyFromLastStop(t *testing.T) {
	r, db, clock := newTestRecorder(t)
	at(clock, 50)
	r.RecordReview(7, "claude", KindReviewCommit, "done", map[string]any{"files": 2})
	at(clock, 60)
	fire(r, agentevents.EventStop, `{}`)
	at(clock, 75)
	r.RecordReview(7, "claude", KindReviewCommentsSent, "sent", map[string]any{"count": 3})

	got := rows(t, r, db)
	var noStop, afterStop *store.Decision
	for _, d := range got {
		switch d.Kind {
		case KindReviewCommit:
			noStop = d
		case KindReviewCommentsSent:
			afterStop = d
		}
	}
	if noStop == nil || noStop.LatencyS != nil {
		t.Fatalf("a review before any Stop should have null latency: %+v", noStop)
	}
	if afterStop == nil || floatOf(afterStop.LatencyS) != 15 || floatOf(afterStop.WaitedSince) != 60 {
		t.Fatalf("review after a Stop: %+v", afterStop)
	}
}

// Another event type that carries no decision is ignored.
func TestUnrelatedEventsRecordNothing(t *testing.T) {
	r, db, _ := newTestRecorder(t)
	fire(r, agentevents.EventPreCompact, `{}`)
	if got := rows(t, r, db); len(got) != 0 {
		t.Fatalf("unrelated event recorded %+v", got)
	}
}

// The off switch: with LECTERN_DECISION_LOG=0 nothing at all is written.
func TestOffSwitchWritesNothing(t *testing.T) {
	t.Setenv(EnvVar, "0")
	r, db, clock := newTestRecorder(t)
	at(clock, 1)
	fire(r, agentevents.EventStop, `{}`)
	fire(r, agentevents.EventUserPromptSubmit, `{"prompt":"x"}`)
	r.RecordReview(7, "claude", KindReviewCommit, "done", nil)
	if got := rows(t, r, db); len(got) != 0 {
		t.Fatalf("disabled log wrote %+v", got)
	}
}

// An approval that stood is recorded with its own timing and its via mapped
// from decided_by. The broker callback feeds this function.
func TestRecordApprovalMapsViaAndLatency(t *testing.T) {
	_, db, _ := newTestRecorder(t)
	decided := 130.0
	row := &store.Approval{ToolName: "Bash", Status: "denied", DecidedBy: "user",
		CreatedAt: 100, DecidedAt: &decided, SessionID: 0}
	if err := RecordApproval(db, row); err != nil {
		t.Fatal(err)
	}
	stale := &store.Approval{ToolName: "Bash", Status: "stale", DecidedBy: "witness",
		CreatedAt: 200, DecidedAt: f(202)}
	if err := RecordApproval(db, stale); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Decisions(0, KindApproval, 0)
	if len(got) != 2 {
		t.Fatalf("approvals recorded: %d", len(got))
	}
	byDecision := map[string]*store.Decision{}
	for _, d := range got {
		byDecision[d.Decision] = d
	}
	if d := byDecision["denied"]; d == nil || d.Via != ViaWeb || floatOf(d.LatencyS) != 30 {
		t.Fatalf("denied: %+v", d)
	}
	if d := byDecision["stale"]; d == nil || d.Via != ViaWitness || floatOf(d.LatencyS) != 2 {
		t.Fatalf("stale: %+v", d)
	}
}

// An approval with no decided_at has not stood, so nothing is recorded.
func TestRecordApprovalIgnoresUndecided(t *testing.T) {
	_, db, _ := newTestRecorder(t)
	if err := RecordApproval(db, &store.Approval{Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Decisions(0, "", 0); len(got) != 0 {
		t.Fatalf("undecided approval recorded: %+v", got)
	}
}
