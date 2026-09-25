package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeSlackREST answers the two REST calls a Socket Mode session makes:
// apps.connections.open (handing back the test websocket server's URL) and
// chat.postMessage (captured for assertions — the immediate acknowledgement
// and, later, the postback).
type fakeSlackREST struct {
	srv      *httptest.Server
	wsURL    string
	mu       sync.Mutex
	posted   []map[string]any
	botToken string
}

func newFakeSlackREST(t *testing.T, wsURL string) *fakeSlackREST {
	t.Helper()
	f := &fakeSlackREST{wsURL: wsURL, botToken: "xoxb-test"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps.connections.open":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true,"url":"` + f.wsURL + `"}`))
		case "/api/chat.postMessage":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.posted = append(f.posted, body)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlackREST) messages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.posted...)
}

func newSlackManager(t *testing.T, f *fakeSlackREST) (*Manager, *store.Project) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "slack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t1", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "p1", TargetID: target.ID, RepoPath: "/mock/p1"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, nil, nil)
	m.HTTP = f.srv.Client()
	m.HTTP.Transport = rewriteHost{f.srv.URL, http.DefaultTransport}
	return m, project
}

// rewriteHost is defined in linear_test.go and reused here.

func readJSONFrame(ws *testWSServer) (map[string]any, error) {
	op, payload, err := ws.readClientFrame()
	if err != nil {
		return nil, err
	}
	if op != wsOpText {
		return nil, fmt.Errorf("expected a text frame, got opcode %d", op)
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("unreadable frame %q: %w", payload, err)
	}
	return out, nil
}

// runSlackServer drives one server-side exchange in the background and
// reports its outcome (an ack envelope, or an error) on the returned
// channel — never by calling t.Fatal from the goroutine itself, which is
// what caused this suite's real deadlock the first time it was written (see
// the comment on testWSServer in ws_test.go).
func runSlackServer(ws *testWSServer, envelope map[string]any) <-chan struct {
	ack map[string]any
	err error
} {
	ch := make(chan struct {
		ack map[string]any
		err error
	}, 1)
	go func() {
		send := func(ack map[string]any, err error) {
			ch <- struct {
				ack map[string]any
				err error
			}{ack, err}
		}
		if err := ws.accept(); err != nil {
			send(nil, err)
			return
		}
		defer ws.close()
		raw, err := json.Marshal(envelope)
		if err != nil {
			send(nil, err)
			return
		}
		if err := ws.writeServerFrame(wsOpText, raw); err != nil {
			send(nil, err)
			return
		}
		ack, err := readJSONFrame(ws)
		send(ack, err)
	}()
	return ch
}

// TestSlackAppMentionCreatesTaskAndAcknowledges drives one full Socket Mode
// round trip: connect, receive an app_mention events_api envelope, ack it,
// create a task through CreateTask, and post the immediate acknowledgement
// back to the channel via chat.postMessage.
func TestSlackAppMentionCreatesTaskAndAcknowledges(t *testing.T) {
	ws := newTestWSServer(t)
	rest := newFakeSlackREST(t, ws.url())
	m, project := newSlackManager(t, rest)

	var created NewTaskSpec
	m.CreateTask = func(_ *store.Project, spec NewTaskSpec) (*store.Task, error) {
		created = spec
		return &store.Task{ID: 77}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "slack", Enabled: true,
		ConfigJSON:  `{"allowed_users":["U123"]}`,
		SecretsJSON: `{"app_token":"xapp-test","bot_token":"` + rest.botToken + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	envelope := map[string]any{
		"type":        "events_api",
		"envelope_id": "env-1",
		"payload": map[string]any{
			"event_id": "ev-1",
			"event": map[string]any{
				"type": "app_mention", "user": "U123",
				"text": "<@BOT123> please fix the thing", "channel": "C1", "ts": "1000.1",
			},
		},
	}
	serverDone := runSlackServer(ws, envelope)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionErr := make(chan error, 1)
	go func() { sessionErr <- m.slackSession(ctx, src) }()

	select {
	case r := <-serverDone:
		if r.err != nil {
			t.Fatalf("server side of the exchange failed: %v", r.err)
		}
		if r.ack["envelope_id"] != "env-1" {
			t.Fatalf("expected an ack for env-1, got %v", r.ack)
		}
	case err := <-sessionErr:
		t.Fatalf("slackSession exited before completing the exchange: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for the envelope exchange")
	}
	ws.close() // end the session cleanly once the exchange is verified
	select {
	case <-sessionErr:
	case <-time.After(3 * time.Second):
		t.Fatal("slackSession did not exit after its connection closed")
	}

	if !strings.Contains(created.Prompt, "please fix the thing") {
		t.Fatalf("expected the mention text with the bot tag stripped, got %+v", created)
	}

	waitForCondition(t, func() bool { return len(rest.messages()) > 0 })
	msg := rest.messages()[0]
	if msg["channel"] != "C1" || !strings.Contains(msg["text"].(string), "task #77") {
		t.Fatalf("expected an acknowledgement naming the new task, got %+v", msg)
	}

	events, err := m.DB.RecentTriggerEvents(project.ID, 10)
	if err != nil || len(events) != 1 || events[0].Action != "task_created" {
		t.Fatalf("expected one task_created event, got %v %v", events, err)
	}
}

func TestSlackEventRejectsUnlistedUser(t *testing.T) {
	ws := newTestWSServer(t)
	rest := newFakeSlackREST(t, ws.url())
	m, project := newSlackManager(t, rest)
	called := false
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) {
		called = true
		return &store.Task{ID: 1}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "slack", Enabled: true,
		ConfigJSON:  `{"allowed_users":["U999"]}`,
		SecretsJSON: `{"app_token":"xapp-test","bot_token":"` + rest.botToken + `"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	envelope := map[string]any{
		"type": "events_api", "envelope_id": "env-2",
		"payload": map[string]any{
			"event_id": "ev-2",
			"event":    map[string]any{"type": "app_mention", "user": "U123", "text": "hi", "channel": "C1", "ts": "1"},
		},
	}
	serverDone := runSlackServer(ws, envelope)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionErr := make(chan error, 1)
	go func() { sessionErr <- m.slackSession(ctx, src) }()

	select {
	case r := <-serverDone:
		if r.err != nil {
			t.Fatalf("server side of the exchange failed: %v", r.err)
		}
		if r.ack["envelope_id"] != "env-2" {
			t.Fatalf("an unmatched author must still be acked, got %v", r.ack)
		}
	case err := <-sessionErr:
		t.Fatalf("slackSession exited before completing the exchange: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for the envelope exchange")
	}

	waitForCondition(t, func() bool { return len(rest.messages()) > 0 })
	ws.close()
	select {
	case <-sessionErr:
	case <-time.After(3 * time.Second):
		t.Fatal("slackSession did not exit after its connection closed")
	}

	if called {
		t.Fatal("a mention from a user outside the allowlist must not create a task")
	}
	msg := rest.messages()[0]
	if !strings.Contains(msg["text"].(string), "didn't act") {
		t.Fatalf("expected a reply explaining why nothing happened, got %+v", msg)
	}
}

func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
