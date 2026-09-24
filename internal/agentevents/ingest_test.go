package agentevents

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The three JSON bodies below are copied byte-for-byte from the real Claude
// Code 2.1.281 captures in /mnt/bulk/lectern-events-ref/ (per the workstream
// contract's instruction to use them as fixtures rather than invented field
// names), inlined so this package's tests do not depend on a path outside
// the repository.

const fixtureSessionStart = `{"session_id":"65f4714e-3848-434d-b684-9068108338d1","transcript_path":"/home/admin/.claude/projects/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1.jsonl","cwd":"/tmp/claude-1000/sp/probe/work","scratchpad_dir":"/tmp/claude-1000/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1/scratchpad","hook_event_name":"SessionStart","source":"startup","model":"claude-opus-5-5"}`

const fixtureUserPromptSubmit = `{"session_id":"65f4714e-3848-434d-b684-9068108338d1","transcript_path":"/home/admin/.claude/projects/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1.jsonl","cwd":"/tmp/claude-1000/sp/probe/work","scratchpad_dir":"/tmp/claude-1000/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1/scratchpad","prompt_id":"a29c82d0-080b-49a1-bbf0-c51241444213","permission_mode":"auto","hook_event_name":"UserPromptSubmit","prompt":"reply with just: ok"}`

const fixtureStop = `{"session_id":"65f4714e-3848-434d-b684-9068108338d1","transcript_path":"/home/admin/.claude/projects/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1.jsonl","cwd":"/tmp/claude-1000/sp/probe/work","scratchpad_dir":"/tmp/claude-1000/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1/scratchpad","prompt_id":"a29c82d0-080b-49a1-bbf0-c51241444213","permission_mode":"auto","effort":{"level":"medium"},"hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"ok","background_tasks":[],"session_crons":[]}`

const fixtureStatusline = `{"session_id":"65f4714e-3848-434d-b684-9068108338d1","transcript_path":"/home/admin/.claude/projects/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1.jsonl","cwd":"/tmp/claude-1000/sp/probe/work","scratchpad_dir":"/tmp/claude-1000/-tmp-claude-1000-sp-probe-work/65f4714e-3848-434d-b684-9068108338d1/scratchpad","prompt_id":"a29c82d0-080b-49a1-bbf0-c51241444213","effort":{"level":"medium"},"session_name":"Ok","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},"workspace":{"current_dir":"/tmp/claude-1000/sp/probe/work","project_dir":"/tmp/claude-1000/sp/probe/work","added_dirs":[]},"version":"2.1.281","output_style":{"name":"default"},"cost":{"total_cost_usd":0.11431480000000001,"total_duration_ms":29201,"total_api_duration_ms":2155,"total_lines_added":0,"total_lines_removed":0},"context_window":{"total_input_tokens":36451,"total_output_tokens":4,"context_window_size":1000000,"current_usage":{"input_tokens":2,"output_tokens":4,"cache_creation_input_tokens":13590,"cache_read_input_tokens":22859},"used_percentage":4,"remaining_percentage":96},"exceeds_200k_tokens":false,"prompt_cache":{"warm":true,"caching_observed":true,"ttl":"1h","expires_at":1790201983,"requests":1,"misses":0,"expected_rebuilds":0,"hit_ratio":0.6271158541603797,"cache_write_tokens":13590,"miss_recache_tokens":0,"last_miss_at":null,"last_miss_cause":null,"miss_causes":{},"recache_tokens_if_cold":36451},"fast_mode":false,"thinking":{"enabled":true},"rate_limits":{"five_hour":{"used_percentage":4,"resets_at":1790200800},"seven_day":{"used_percentage":72,"resets_at":1790290800}}}`

func newTestSession(t *testing.T) (*store.DB, *Ingester, *store.Session) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "s", Agent: "claude", Workdir: "/w", TmuxSession: "lec-s1"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := NewHookToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update("sessions", sess.ID, map[string]any{"hook_token": token}); err != nil {
		t.Fatal(err)
	}
	sess, err = db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, New(db, bus.New()), sess
}

func TestIngestEventFixturesDriveState(t *testing.T) {
	db, in, sess := newTestSession(t)

	// SessionStart is accepted but not mapped: hook_seen_at moves, agent_state
	// does not.
	state, changed, err := in.IngestEvent(sess, EventSessionStart, []byte(fixtureSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	if state != "" || changed {
		t.Fatalf("SessionStart should not set a state: state=%q changed=%v", state, changed)
	}
	row, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.HookSeenAt == nil {
		t.Fatal("hook_seen_at not set by SessionStart")
	}
	if row.AgentState != "" {
		t.Fatalf("agent_state should still be empty, got %q", row.AgentState)
	}

	// UserPromptSubmit -> working, and status follows along for the old UI.
	state, changed, err = in.IngestEvent(row, EventUserPromptSubmit, []byte(fixtureUserPromptSubmit))
	if err != nil {
		t.Fatal(err)
	}
	if state != StateWorking || !changed {
		t.Fatalf("UserPromptSubmit: state=%q changed=%v", state, changed)
	}
	row, _ = db.Session(sess.ID)
	if row.AgentState != StateWorking || row.StateSource != SourceHook || row.Status != "running" {
		t.Fatalf("after UserPromptSubmit: %+v", row)
	}

	// Re-sending the same event must not re-publish (changed=false) even
	// though the row is touched again.
	_, changed, err = in.IngestEvent(row, EventUserPromptSubmit, []byte(fixtureUserPromptSubmit))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("repeated identical state must not report changed")
	}

	// Stop -> idle.
	state, changed, err = in.IngestEvent(row, EventStop, []byte(fixtureStop))
	if err != nil {
		t.Fatal(err)
	}
	if state != StateIdle || !changed {
		t.Fatalf("Stop: state=%q changed=%v", state, changed)
	}
	row, _ = db.Session(sess.ID)
	if row.AgentState != StateIdle || row.Status != "idle" {
		t.Fatalf("after Stop: %+v", row)
	}
}

func TestIngestEventPermissionRequestWaitsPermission(t *testing.T) {
	_, in, sess := newTestSession(t)
	state, changed, err := in.IngestEvent(sess, EventPermissionRequest, []byte(`{"tool_name":"Bash"}`))
	if err != nil {
		t.Fatal(err)
	}
	if state != StateWaitingPermission || !changed {
		t.Fatalf("PermissionRequest: state=%q changed=%v", state, changed)
	}
}

func TestIngestEventNotificationBranchesOnType(t *testing.T) {
	_, in, sess := newTestSession(t)
	state, _, err := in.IngestEvent(sess, EventNotification, []byte(`{"notification_type":"permission_prompt"}`))
	if err != nil || state != StateWaitingPermission {
		t.Fatalf("permission_prompt: state=%q err=%v", state, err)
	}
	sess, _ = in.DB.Session(sess.ID)
	state, _, err = in.IngestEvent(sess, EventNotification, []byte(`{"notification_type":"idle_prompt"}`))
	if err != nil || state != StateWaitingInput {
		t.Fatalf("idle_prompt: state=%q err=%v", state, err)
	}
}

func TestIngestEventUnknownIsAcceptedAndIgnored(t *testing.T) {
	db, in, sess := newTestSession(t)
	if _, _, err := in.IngestEvent(sess, "SomeFutureHook", []byte(`{"anything":"goes"}`)); err != nil {
		t.Fatal(err)
	}
	row, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.HookSeenAt == nil {
		t.Fatal("hook_seen_at should still move for an unknown event")
	}
	if row.AgentState != "" {
		t.Fatalf("unknown event must not invent a state: %q", row.AgentState)
	}
}

func TestIngestStatuslineFixtureUpdatesSessionAndBooksUsage(t *testing.T) {
	db, in, sess := newTestSession(t)
	if err := in.IngestStatusline(sess, []byte(fixtureStatusline)); err != nil {
		t.Fatal(err)
	}
	row, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Model != "claude-opus-5-5" {
		t.Errorf("model = %q", row.Model)
	}
	if row.ContextUsedPct == nil || *row.ContextUsedPct != 4 {
		t.Errorf("context_used_pct = %v", row.ContextUsedPct)
	}
	if row.ContextTokens == nil || *row.ContextTokens != 36451 {
		t.Errorf("context_tokens = %v", row.ContextTokens)
	}
	if row.ContextSize == nil || *row.ContextSize != 1000000 {
		t.Errorf("context_size = %v", row.ContextSize)
	}
	if row.CostUSD == nil || *row.CostUSD < 0.114 || *row.CostUSD > 0.115 {
		t.Errorf("cost_usd = %v", row.CostUSD)
	}
	if row.Rate5hPct == nil || *row.Rate5hPct != 4 || row.Rate7dPct == nil || *row.Rate7dPct != 72 {
		t.Errorf("rate limits = 5h:%v 7d:%v", row.Rate5hPct, row.Rate7dPct)
	}
	if row.UsageAt == nil {
		t.Fatal("usage_at not set")
	}

	// First sample: the whole cost is new, so the FULL amount is booked —
	// there is nothing to double-count yet.
	inputBooked, outputBooked, err := db.UsageDailySessionTotals(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inputBooked != 36451 || outputBooked != 4 {
		t.Fatalf("first-sample usage_daily tokens = in:%d out:%d", inputBooked, outputBooked)
	}

	// Second, IDENTICAL sample (Claude Code's statusline fires on every
	// render tick, often with nothing changed): must add nothing more.
	if err := in.IngestStatusline(row, []byte(fixtureStatusline)); err != nil {
		t.Fatal(err)
	}
	inputBooked2, outputBooked2, err := db.UsageDailySessionTotals(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inputBooked2 != inputBooked || outputBooked2 != outputBooked {
		t.Fatalf("repeated identical statusline double-counted: in:%d->%d out:%d->%d",
			inputBooked, inputBooked2, outputBooked, outputBooked2)
	}
	var costCents int
	if err := db.QueryRow(`SELECT CAST(ROUND(SUM(cost_usd)*100) AS INTEGER) FROM usage_daily WHERE session_id=?`, sess.ID).Scan(&costCents); err != nil {
		t.Fatal(err)
	}
	if costCents != 11 { // 0.1143 rounds to 11 cents, booked exactly once
		t.Fatalf("cost booked twice: %d cents", costCents)
	}
}

func TestIngestStatuslineMalformedBodyDegradesSilently(t *testing.T) {
	db, in, sess := newTestSession(t)
	if err := in.IngestStatusline(sess, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	row, err := db.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.HookSeenAt == nil {
		t.Fatal("hook_seen_at should still move on a malformed statusline body")
	}
	if row.CostUSD != nil {
		t.Fatalf("malformed body must not fabricate usage: cost_usd=%v", *row.CostUSD)
	}
}

func TestValidTokenConstantTimeAndRejectsEmpty(t *testing.T) {
	if ValidToken("", "") {
		t.Fatal("two empty strings must not authenticate")
	}
	if ValidToken("secret", "") {
		t.Fatal("empty supplied token must not authenticate")
	}
	if ValidToken("", "secret") {
		t.Fatal("empty session token must not authenticate")
	}
	if !ValidToken("secret", "secret") {
		t.Fatal("matching tokens must authenticate")
	}
	if ValidToken("secret", "other") {
		t.Fatal("mismatched tokens must not authenticate")
	}
}
