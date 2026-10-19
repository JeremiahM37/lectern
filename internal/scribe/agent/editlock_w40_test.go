package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// stubEdit is an edit tool that records each call and, when gate is set,
// blocks until the gate is closed, so a test can hold the lock open.
type stubEdit struct {
	name  string
	gate  chan struct{}
	calls chan string
}

func (s *stubEdit) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: s.name, Description: "stub", Schema: tools.Schema(map[string]any{})}
}
func (s *stubEdit) ReadOnly() bool { return false }
func (s *stubEdit) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	s.calls <- editPath(raw)
	if s.gate != nil {
		<-s.gate
	}
	return tools.Result{Output: "edited " + editPath(raw), Summary: "edited"}
}

func editCall(path string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"path": path})
	return b
}

func TestEditLockQueuesInArrivalOrder(t *testing.T) {
	l := newEditLocks(nil)
	ctx := context.Background()
	relA, err := l.acquire(ctx, "/w/x.go", "a1")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 2)
	for _, id := range []string{"a2", "a3"} {
		go func() {
			rel, err := l.acquire(ctx, "/w/x.go", id)
			if err != nil {
				t.Error(err)
				return
			}
			got <- id
			rel()
		}()
		time.Sleep(20 * time.Millisecond) // a2 queues before a3
	}
	relA()
	if first := <-got; first != "a2" {
		t.Fatalf("first waiter = %s, want a2 (FIFO)", first)
	}
	if second := <-got; second != "a3" {
		t.Fatalf("second waiter = %s, want a3", second)
	}
}

func TestEditLockDifferentPathsDoNotWait(t *testing.T) {
	l := newEditLocks(nil)
	ctx := context.Background()
	if _, err := l.acquire(ctx, "/w/x.go", "a1"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		rel, err := l.acquire(ctx, "/w/y.go", "a2")
		if err == nil {
			rel()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a2 waited for a lock on another path")
	}
}

func TestEditLockForceReleasesAStuckHolder(t *testing.T) {
	var reported []string
	l := newEditLocks(func(msg string) { reported = append(reported, msg) })
	l.maxHold = 100 * time.Millisecond
	ctx := context.Background()
	relStuck, err := l.acquire(ctx, "/w/x.go", "a1")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rel, err := l.acquire(ctx, "/w/x.go", "a2")
	if err != nil {
		t.Fatalf("a2 was not granted the lock: %v", err)
	}
	if waited := time.Since(start); waited < 80*time.Millisecond {
		t.Fatalf("a2 got the lock after %s, before the hold limit", waited)
	}
	if len(reported) != 1 || !strings.Contains(reported[0], "a1") || !strings.Contains(reported[0], "force released") {
		t.Fatalf("forced release was not reported as an error: %v", reported)
	}
	if got := l.forcedReleases(); len(got) != 1 {
		t.Fatalf("forcedReleases = %v, want one entry", got)
	}
	// The stuck agent finally finishing must not free a2's lock.
	relStuck()
	third := make(chan struct{})
	go func() {
		r3, err := l.acquire(ctx, "/w/x.go", "a3")
		if err == nil {
			r3()
		}
		close(third)
	}()
	select {
	case <-third:
		t.Fatal("a3 got the lock while a2 still held it")
	case <-time.After(20 * time.Millisecond):
	}
	rel()
	<-third
}

func TestEditLockWaiterThatGivesUpDoesNotBlockOthers(t *testing.T) {
	l := newEditLocks(nil)
	bg := context.Background()
	relA, _ := l.acquire(bg, "/w/x.go", "a1")
	ctx, cancel := context.WithCancel(bg)
	errB := make(chan error, 1)
	go func() {
		_, err := l.acquire(ctx, "/w/x.go", "a2")
		errB <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errB; err == nil {
		t.Fatal("a2 should have given up when its context ended")
	}
	relA()
	rel, err := l.acquire(bg, "/w/x.go", "a3")
	if err != nil {
		t.Fatal(err)
	}
	rel()
}

func TestEditLockIsReentrantForTheHolder(t *testing.T) {
	l := newEditLocks(nil)
	ctx := context.Background()
	rel1, _ := l.acquire(ctx, "/w/x.go", "a1")
	rel2, err := l.acquire(ctx, "/w/x.go", "a1")
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	// One release left: the path is still held by a1.
	_, holder := l.peerState("/w/x.go", "a2")
	if holder != "a1" {
		t.Fatalf("holder after one release = %q, want a1", holder)
	}
	rel1()
	if _, holder := l.peerState("/w/x.go", "a2"); holder != "" {
		t.Fatalf("holder after both releases = %q, want none", holder)
	}
}

func TestGuardedEditToolTakesTheLockAndRecordsTheEditor(t *testing.T) {
	dir := t.TempDir()
	locks := newEditLocks(nil)
	bus := NewAgentBus()
	gate := make(chan struct{})
	calls := make(chan string, 4)
	stub := &stubEdit{name: "edit_file", gate: gate, calls: calls}

	regA := tools.NewRegistry()
	regA.Register(stub)
	guardEdits(regA, "code", "a1", bus, locks)
	regB := tools.NewRegistry()
	regB.Register(&stubEdit{name: "edit_file", calls: calls})
	guardEdits(regB, "code", "a2", bus, locks)

	env := &tools.Env{Workdir: dir, CallID: "c"}
	abs := filepath.Join(dir, "pkg", "x.go")
	doneA := make(chan tools.Result, 1)
	go func() {
		t1, _ := regA.Get("edit_file")
		doneA <- t1.Run(context.Background(), env, editCall("pkg/x.go"))
	}()
	<-calls // a1 is inside its edit and holds the lock

	doneB := make(chan tools.Result, 1)
	go func() {
		t2, _ := regB.Get("edit_file")
		doneB <- t2.Run(context.Background(), env, editCall("pkg/x.go"))
	}()
	select {
	case <-calls:
		t.Fatal("a2 edited the file while a1 held its lock")
	case <-time.After(30 * time.Millisecond):
	}
	close(gate)
	if r := <-doneA; r.IsError {
		t.Fatalf("a1 edit failed: %s", r.Output)
	}
	<-calls // a2 runs once a1 releases
	if r := <-doneB; r.IsError {
		t.Fatalf("a2 edit failed: %s", r.Output)
	}

	editors, _ := locks.peerState(abs, "a3")
	if strings.Join(editors, ",") != "a1,a2" {
		t.Fatalf("editors = %v, want a1 then a2", editors)
	}
	// The bus claim is made too: a2 was the second claimant, so it conflicts.
	if owner, conflict := bus.ClaimEdit(abs, "a3"); owner != "a1" || !conflict {
		t.Fatalf("bus owner = %q conflict=%v, want a1 and a conflict", owner, conflict)
	}
}

func TestCheckEditConflictsReportsPeers(t *testing.T) {
	dir := t.TempDir()
	locks := newEditLocks(nil)
	env := &tools.Env{Workdir: dir}
	check := func(id string) string {
		r := checkEditConflictsTool{id: id, locks: locks}
		res := r.Run(context.Background(), env, editCall("x.go"))
		if res.IsError {
			t.Fatalf("check failed: %s", res.Output)
		}
		return res.Output
	}
	if out := check("a2"); !strings.Contains(out, "No other agent") {
		t.Fatalf("untouched file: %q", out)
	}
	locks.recordEditor(filepath.Join(dir, "x.go"), "a1")
	if out := check("a2"); !strings.Contains(out, "Agent a1 already edited x.go") {
		t.Fatalf("after a1's edit: %q", out)
	}
	if out := check("a1"); !strings.Contains(out, "No other agent") {
		t.Fatalf("an agent's own edit is not a conflict: %q", out)
	}
	rel, _ := locks.acquire(context.Background(), filepath.Join(dir, "x.go"), "a1")
	if out := check("a2"); !strings.Contains(out, "being edited by agent a1 right now") {
		t.Fatalf("while a1 holds the lock: %q", out)
	}
	rel()
}

func TestCheckEditConflictsNeedsAPath(t *testing.T) {
	r := checkEditConflictsTool{id: "a1", locks: newEditLocks(nil)}
	if res := r.Run(context.Background(), &tools.Env{Workdir: t.TempDir()}, json.RawMessage(`{}`)); !res.IsError {
		t.Fatal("a call without a path should be an error")
	}
}

func TestGuardEditsRegistersCheckOnlyForCodeAgents(t *testing.T) {
	locks := newEditLocks(nil)
	bus := NewAgentBus()
	reg := tools.NewRegistry()
	reg.Register(&stubEdit{name: "edit_file", calls: make(chan string, 1)})
	guardEdits(reg, "explore", "a1", bus, locks)
	if _, ok := reg.Get("check_edit_conflicts"); ok {
		t.Fatal("an explore agent got check_edit_conflicts")
	}
	guardEdits(reg, "code", "a2", bus, locks)
	if _, ok := reg.Get("check_edit_conflicts"); !ok {
		t.Fatal("a code agent did not get check_edit_conflicts")
	}
}

// offeredTools is the tool names a request offered the model.
func offeredTools(req recordedReq) []string {
	var out []string
	for _, t := range req.body.Tools {
		out = append(out, t.Function.Name)
	}
	return out
}

// A code subagent is offered check_edit_conflicts and its write goes through
// the lock; an explore subagent of the same dispatch is not offered it.
func TestDispatchCodeAgentGetsEditGuards(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.Contains(openingOf(req), "CODE-LOCK") {
			return write(req)
		}
		return turn(sseTurn{text: []string{"looked"}, usage: usage})
	})

	res := h.run(`{"tasks":[{"task":"CODE-LOCK add the notes file","role":"code","files":["notes.txt"]},{"task":"EXPLORE-LOCK look around","role":"explore"}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed: %s", res.Output)
	}
	if got, _ := os.ReadFile(filepath.Join(h.dir, "notes.txt")); string(got) != "hello\n" {
		t.Fatalf("the locked write did not land; content = %q", got)
	}
	if len(res.Files) != 1 || res.Files[0] != "notes.txt" {
		t.Fatalf("Files = %v", res.Files)
	}
	if strings.Contains(res.Output, "## Edit locks") {
		t.Fatalf("no lock was forced, but the report has an edit lock section:\n%s", res.Output)
	}
	sawCode, sawExplore := false, false
	for _, req := range h.f.requests() {
		names := offeredTools(req)
		switch {
		case strings.Contains(openingOf(req), "CODE-LOCK"):
			sawCode = true
			if !slices.Contains(names, "check_edit_conflicts") {
				t.Fatalf("code agent was not offered check_edit_conflicts: %v", names)
			}
		case strings.Contains(openingOf(req), "EXPLORE-LOCK"):
			sawExplore = true
			if slices.Contains(names, "check_edit_conflicts") {
				t.Fatalf("explore agent was offered check_edit_conflicts: %v", names)
			}
		}
	}
	if !sawCode || !sawExplore {
		t.Fatalf("missing requests: code=%v explore=%v", sawCode, sawExplore)
	}
}
