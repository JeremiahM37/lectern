package broker

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type pushLog struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (l *pushLog) add(raw []byte) {
	var m map[string]any
	json.Unmarshal(raw, &m)
	l.mu.Lock()
	l.msgs = append(l.msgs, m)
	l.mu.Unlock()
}

func (l *pushLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []string{}
	for _, m := range l.msgs {
		k, _ := m["kind"].(string)
		out = append(out, k)
	}
	return out
}

func newBroker(t *testing.T) (*Broker, *store.DB, int64, *pushLog) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, _ := db.InsertTarget(&store.Target{Name: "t", Kind: "local"})
	project, _ := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/tmp"})
	task, _ := db.InsertTask(&store.Task{ProjectID: project.ID, Title: "t", Status: "running"})
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	log := &pushLog{}
	n := &sinks.Notifier{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PushHook: log.add}
	return New(db, bus.New(), n, time.Minute), db, att.ID, log
}

// An approval decided anywhere withdraws its notification from every device.
func TestDecisionDismissesTheNotificationEverywhere(t *testing.T) {
	br, _, attempt, log := newBroker(t)
	id, err := br.Create(attempt, "Bash", map[string]any{"command": "ls"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if br.Decide(id, "approved", "", "user") == nil {
		t.Fatal("decision refused")
	}
	if got := log.kinds(); len(got) != 2 || got[0] != "approval" || got[1] != "dismiss" {
		t.Fatalf("pushes: %v", got)
	}
	last := log.msgs[1]
	if last["tag"] != sinks.ApprovalTag(id) || last["title"] != "Approved on another device" {
		t.Fatalf("dismissal: %v", last)
	}
	// A second decision is a conflict and withdraws nothing more.
	br.Decide(id, "denied", "", "user")
	if len(log.kinds()) != 2 {
		t.Fatalf("a refused decision sent a push: %v", log.kinds())
	}
}

// A policy auto-approval never notified anyone, so nothing is withdrawn.
func TestPolicyDecisionSendsNothing(t *testing.T) {
	br, _, attempt, log := newBroker(t)
	id, _ := br.Create(attempt, "Bash", map[string]any{"command": "ls"}, true)
	br.Decide(id, "approved", "matched always-allow rule", "policy")
	if got := log.kinds(); len(got) != 0 {
		t.Fatalf("pushes: %v", got)
	}
}

func TestExpiryWithdrawsWithItsOwnWords(t *testing.T) {
	br, _, attempt, log := newBroker(t)
	id, _ := br.Create(attempt, "Bash", map[string]any{"command": "ls"}, false)
	br.ExpireForAttempt(attempt)
	msgs := log.msgs
	if len(msgs) != 2 || msgs[1]["title"] != "Approval expired" || msgs[1]["tag"] != sinks.ApprovalTag(id) {
		t.Fatalf("pushes: %v", msgs)
	}
}
