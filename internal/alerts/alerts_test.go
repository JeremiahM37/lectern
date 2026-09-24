package alerts

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// testDB mirrors internal/sinks/delivery_test.go's helper: a real sqlite
// temp file, not :memory:, so Notifier's own settings queries behave exactly
// as they do in production.
func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/alerts.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// notifier builds one wired to ntfy (so BuildPayloads always produces
// something for Hook to capture — Notify returns before calling Hook at all
// when no sink is configured) and records every payload it would have sent.
func notifier(t *testing.T, db *store.DB) (*sinks.Notifier, *[]sinks.Payload) {
	t.Helper()
	if err := db.SetSetting("ntfy_server", "https://ntfy.example"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("ntfy_topic", "lec"); err != nil {
		t.Fatal(err)
	}
	var sent []sinks.Payload
	n := &sinks.Notifier{DB: db, BaseURL: "http://deck", Log: slog.Default(),
		Hook: func(p []sinks.Payload) { sent = append(sent, p...) }}
	return n, &sent
}

func newSession(t *testing.T, db *store.DB, name string) *store.Session {
	t.Helper()
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: name, Agent: "claude",
		Workdir: "/x", TmuxSession: "lec-s1"})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func bodies(sent *[]sinks.Payload) []string {
	var out []string
	for _, p := range *sent {
		if msg, ok := p.Body["message"].(string); ok {
			out = append(out, msg)
		}
	}
	return out
}

func TestWorkingTransitionDoesNotAlert(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventUserPromptSubmit, nil, agentevents.StateWorking, true)
	n.Wait()
	if len(*sent) != 0 {
		t.Fatalf("working must not page anyone: %+v", *sent)
	}
}

func TestIdleTransitionAlertsWithExcerpt(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	body, _ := json.Marshal(map[string]string{"last_assistant_message": "Done — tests pass."})
	w.HandleHookEvent(sess, agentevents.EventStop, body, agentevents.StateIdle, true)
	n.Wait()
	got := bodies(sent)
	if len(got) != 1 {
		t.Fatalf("expected exactly one push, got %v", got)
	}
	if want := "demo finished: Done — tests pass."; got[0] != want {
		t.Errorf("body = %q, want %q", got[0], want)
	}
}

func TestUnchangedStateNeverRepeats(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventStop, nil, agentevents.StateIdle, true)
	// A second Stop while nothing else happened — IngestEvent would report
	// changed=false because agent_state did not move, which is exactly what
	// "never repeated for the same state" means in practice: the watcher
	// never has to remember it already alerted, only trust the edge.
	w.HandleHookEvent(sess, agentevents.EventStop, nil, agentevents.StateIdle, false)
	n.Wait()
	if len(*sent) != 1 {
		t.Fatalf("expected exactly one push across two calls, got %d: %+v", len(*sent), *sent)
	}
}

func TestCooldownSuppressesARapidSecondAlert(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	now := time.Now()
	w := &Watcher{DB: db, Notifier: n, Cooldown: time.Minute, Clock: func() time.Time { return now }}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventStop, nil, agentevents.StateIdle, true)
	// A distinct kind, still inside the cooldown window — a storm of
	// different flips must not out-run the per-session cooldown either.
	w.HandleHookEvent(sess, agentevents.EventStopFailure, nil, agentevents.StateError, true)
	n.Wait()
	if len(*sent) != 1 {
		t.Fatalf("cooldown should have suppressed the second alert: %d sent", len(*sent))
	}
	now = now.Add(2 * time.Minute)
	w.HandleHookEvent(sess, agentevents.EventNotification,
		[]byte(`{"notification_type":"idle_prompt"}`), agentevents.StateWaitingInput, true)
	n.Wait()
	if len(*sent) != 2 {
		t.Fatalf("a later alert past the cooldown window should have gone through: %d sent", len(*sent))
	}
}

func TestSuppressedWhenABrowserJustTyped(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	act := NewActivity()
	w := &Watcher{DB: db, Notifier: n, Activity: act}
	sess := newSession(t, db, "demo")
	act.Touch(sess.ID)
	w.HandleHookEvent(sess, agentevents.EventNotification,
		[]byte(`{"notification_type":"idle_prompt"}`), agentevents.StateWaitingInput, true)
	n.Wait()
	if len(*sent) != 0 {
		t.Fatalf("a session someone is actively typing into must not page them: %+v", *sent)
	}
}

func TestActivityExpiresAfterTheSuppressWindow(t *testing.T) {
	act := NewActivity()
	act.seen[1] = time.Now().Add(-time.Hour)
	if act.Recent(1, SuppressWindow) {
		t.Fatal("an hour-old keystroke must not still suppress an alert")
	}
}

func TestPerKindToggleDisablesOnlyThatKind(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	if err := db.SetSetting(KeyIdle, "0"); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventStop, nil, agentevents.StateIdle, true)
	n.Wait()
	if len(*sent) != 0 {
		t.Fatalf("alert_idle=0 must silence the idle alert: %+v", *sent)
	}
	w.HandleHookEvent(sess, agentevents.EventStopFailure, nil, agentevents.StateError, true)
	n.Wait()
	if len(*sent) != 1 {
		t.Fatalf("disabling one kind must not silence another: %+v", *sent)
	}
}

func TestPreCompactAutoAlertsManualDoesNot(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventPreCompact, []byte(`{"trigger":"manual"}`), "", false)
	n.Wait()
	if len(*sent) != 0 {
		t.Fatalf("a manual compact is not a surprise, it must not page: %+v", *sent)
	}
	w.HandleHookEvent(sess, agentevents.EventPreCompact, []byte(`{"trigger":"auto"}`), "", false)
	n.Wait()
	got := bodies(sent)
	if len(got) != 1 || got[0] != "demo is compacting its context" {
		t.Fatalf("auto compact alert: %v", got)
	}
}

// The session-approval flow (internal/broker) already sends a dedicated,
// actionable push for a PermissionRequest hook call. Watcher must not also
// send the generic transition text for the very same event — see the
// deviation documented on HandleHookEvent.
func TestPermissionRequestEventDoesNotDoublePush(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventPermissionRequest,
		[]byte(`{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`),
		agentevents.StateWaitingPermission, true)
	n.Wait()
	if len(*sent) != 0 {
		t.Fatalf("PermissionRequest must defer to the approval push, not double up: %+v", *sent)
	}
}

// Outside "ask" mode, Claude's own Notification(permission_prompt) hook is
// the only signal a waiting_permission transition ever gets, and there is no
// approval push to defer to — it must alert.
func TestNotificationPermissionPromptAlerts(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	body, _ := json.Marshal(map[string]string{
		"notification_type": "permission_prompt",
		"message":           "Claude needs your permission to use Bash",
	})
	w.HandleHookEvent(sess, agentevents.EventNotification, body, agentevents.StateWaitingPermission, true)
	n.Wait()
	got := bodies(sent)
	if len(got) != 1 || got[0] != "demo needs permission: Claude needs your permission to use Bash" {
		t.Fatalf("body: %v", got)
	}
}

func TestWaitingInputAlerts(t *testing.T) {
	db := testDB(t)
	n, sent := notifier(t, db)
	w := &Watcher{DB: db, Notifier: n}
	sess := newSession(t, db, "demo")
	w.HandleHookEvent(sess, agentevents.EventNotification,
		[]byte(`{"notification_type":"idle_prompt"}`), agentevents.StateWaitingInput, true)
	n.Wait()
	got := bodies(sent)
	if len(got) != 1 || got[0] != "demo is waiting for you" {
		t.Fatalf("body: %v", got)
	}
}
