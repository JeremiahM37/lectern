package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// noteLog collects the cache notes a bus reports, in order.
type noteLog struct {
	mu    sync.Mutex
	notes []CacheNote
	ch    chan CacheNote
}

func newNoteLog(b *AgentBus) *noteLog {
	l := &noteLog{ch: make(chan CacheNote, 16)}
	b.SetCacheObserver(func(n CacheNote) {
		l.mu.Lock()
		l.notes = append(l.notes, n)
		l.mu.Unlock()
		l.ch <- n
	})
	return l
}

func (l *noteLog) all() []CacheNote {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]CacheNote(nil), l.notes...)
}

func (l *noteLog) waitFor(t *testing.T, outcome string) CacheNote {
	t.Helper()
	select {
	case n := <-l.ch:
		if n.Outcome != outcome {
			t.Fatalf("note %+v, want outcome %s", n, outcome)
		}
		return n
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s note", outcome)
	}
	return CacheNote{}
}

// A first read is stored, a repeat at the same stamp is a hit that names the
// reader who stored it, and a changed stamp replaces the entry as invalidate.
func TestCacheNotesStoreHitAndInvalidate(t *testing.T) {
	b := NewAgentBus()
	log := newNoteLog(b)

	_, hit, ticket := b.ReadStart("a1", "f", "10:1")
	if hit || ticket == nil {
		t.Fatalf("first read: hit=%v ticket=%v, want a miss with a ticket", hit, ticket)
	}
	ticket.Finish("v1", true)
	log.waitFor(t, CacheStore)

	out, hit, ticket := b.ReadStart("a2", "f", "10:1")
	if !hit || out != "v1" || ticket != nil {
		t.Fatalf("repeat read = %q hit=%v ticket=%v, want v1 hit", out, hit, ticket)
	}
	if n := log.waitFor(t, CacheHit); n.Agent != "a2" || n.From != "a1" {
		t.Fatalf("hit note = %+v, want reader a2 served from a1", n)
	}

	_, hit, ticket = b.ReadStart("a2", "f", "12:2")
	if hit || ticket == nil {
		t.Fatal("a changed file must miss the cache")
	}
	ticket.Finish("v2", true)
	if n := log.waitFor(t, CacheInvalidate); n.Agent != "a2" {
		t.Fatalf("invalidate note = %+v, want reader a2", n)
	}
	if out, hit, _ := b.ReadStart("a3", "f", "12:2"); !hit || out != "v2" {
		t.Fatalf("after invalidate = %q hit=%v, want v2", out, hit)
	}
}

// A reader that arrives while another subagent is reading the same file at
// the same stamp waits for that read, and is served its result.
func TestCacheWaitServesTheRunningRead(t *testing.T) {
	b := NewAgentBus()
	log := newNoteLog(b)
	_, _, owner := b.ReadStart("a1", "f", "10:1")

	type got struct {
		out string
		hit bool
	}
	done := make(chan got, 1)
	go func() {
		out, hit, _ := b.ReadStart("a2", "f", "10:1")
		done <- got{out, hit}
	}()

	if n := log.waitFor(t, CacheWait); n.Agent != "a2" || n.From != "a1" {
		t.Fatalf("wait note = %+v, want a2 waiting on a1", n)
	}
	owner.Finish("contents", true)
	log.waitFor(t, CacheStore)

	select {
	case g := <-done:
		if !g.hit || g.out != "contents" {
			t.Fatalf("waiter got %q hit=%v, want the owner's result", g.out, g.hit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter was not released")
	}
	if n := log.waitFor(t, CacheHit); n.From != "a1" {
		t.Fatalf("hit after wait = %+v, want from a1", n)
	}
}

// A read that fails is not stored, and its waiters are released to read the
// file themselves instead of being served nothing.
func TestCacheFailedReadIsNotStoredAndReleasesWaiters(t *testing.T) {
	b := NewAgentBus()
	log := newNoteLog(b)
	_, _, owner := b.ReadStart("a1", "f", "10:1")

	done := make(chan bool, 1)
	go func() {
		_, hit, ticket := b.ReadStart("a2", "f", "10:1")
		ticket.Finish("own read", true)
		done <- hit
	}()
	log.waitFor(t, CacheWait)
	owner.Finish("error text", false)

	select {
	case hit := <-done:
		if hit {
			t.Fatal("waiter was served a failed read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter was not released by a failed read")
	}
	for _, n := range log.all() {
		if n.Outcome == CacheStore && n.Agent == "a1" {
			t.Fatalf("failed read produced a store note: %+v", n)
		}
	}
}

// A waiter whose owner never finishes gives up after readWait and reads the
// file itself, so a stuck read cannot hold the dispatch.
func TestCacheWaitGivesUpAfterTheTimeout(t *testing.T) {
	b := NewAgentBus()
	b.readWait = 20 * time.Millisecond
	log := newNoteLog(b)
	b.ReadStart("a1", "f", "10:1") // never finished

	_, hit, ticket := b.ReadStart("a2", "f", "10:1")
	if hit || ticket == nil {
		t.Fatal("a timed-out wait must fall through to a read of its own")
	}
	log.waitFor(t, CacheWait)
	ticket.Finish("own", true)
	log.waitFor(t, CacheStore)
}

// With no bus, or no stamp to compare, nothing is cached and nothing is
// reported. This is the state when the dispatch_cache feature is off.
func TestCacheSilentWithoutBusOrStamp(t *testing.T) {
	var nilBus *AgentBus
	if out, hit, ticket := nilBus.ReadStart("a1", "f", "10:1"); hit || out != "" || ticket != nil {
		t.Fatal("a nil bus cached a read")
	}
	nilBus.SetCacheObserver(func(CacheNote) { t.Fatal("note from a nil bus") })
	ticket := (*ReadTicket)(nil)
	ticket.Finish("x", true) // must not panic

	b := NewAgentBus()
	calls := 0
	b.SetCacheObserver(func(CacheNote) { calls++ })
	_, hit, ticket := b.ReadStart("a1", "f", "")
	if hit || ticket != nil {
		t.Fatal("a read with no stamp was cached")
	}
	ticket.Finish("x", true)
	if calls != 0 {
		t.Fatalf("%d notes for reads that were never cached", calls)
	}
}

// Finish has an effect once; a second call does not store or note again.
func TestCacheFinishOnlyOnce(t *testing.T) {
	b := NewAgentBus()
	log := newNoteLog(b)
	_, _, ticket := b.ReadStart("a1", "f", "10:1")
	ticket.Finish("v1", true)
	ticket.Finish("other", true)
	log.waitFor(t, CacheStore)
	if out, _, _ := b.ReadStart("a2", "f", "10:1"); out != "v1" {
		t.Fatalf("stored output = %q, want the first finish", out)
	}
	if n := len(log.all()); n != 2 { // one store, one hit
		t.Fatalf("%d notes, want 2", n)
	}
}

// countRead is an inner read tool that counts its runs and can panic.
type countRead struct {
	runs  *int
	panic bool
}

func (countRead) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "read", Description: "read", Schema: tools.Schema(map[string]any{"path": tools.Str("p")}, "path")}
}
func (countRead) ReadOnly() bool { return true }
func (c countRead) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	*c.runs++
	if c.panic {
		panic("read exploded")
	}
	return tools.Result{Output: "contents"}
}

// marks counts the reads the cache hit marks, the way the edit guard does.
type marks struct{ read int }

func (m *marks) BeforeWrite(string)         {}
func (m *marks) MarkRead(string)            { m.read++ }
func (m *marks) StaleSinceRead(string) bool { return false }
func (m *marks) WasRead(string) bool        { return false }

// ReadCacheTool serves the second read from the bus, marks the file read on
// that hit, and names the subagent in its notes.
func TestReadCacheToolNamesTheSubagent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewAgentBus()
	log := newNoteLog(b)
	runs := 0
	tool := ReadCacheTool(countRead{runs: &runs}, b, "a7")
	m := &marks{}
	env := &tools.Env{Workdir: dir, Files: m}
	args := json.RawMessage(`{"path":"a.go"}`)

	first := tool.Run(context.Background(), env, args)
	second := tool.Run(context.Background(), env, args)
	if runs != 1 || first.Output != second.Output || m.read != 1 {
		t.Fatalf("runs=%d marks=%d outputs %q/%q", runs, m.read, first.Output, second.Output)
	}
	if n := log.waitFor(t, CacheStore); n.Agent != "a7" {
		t.Fatalf("store note = %+v, want a7", n)
	}
	if n := log.waitFor(t, CacheHit); n.Agent != "a7" || n.From != "a7" {
		t.Fatalf("hit note = %+v, want a7 from a7", n)
	}
}

// A read that panics must not leave its waiters parked, and must not store
// an empty result as if the read had succeeded.
func TestReadCacheToolPanicReleasesTheTicket(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewAgentBus()
	log := newNoteLog(b)
	runs := 0
	tool := ReadCacheTool(countRead{runs: &runs, panic: true}, b, "a1")
	env := &tools.Env{Workdir: dir, Files: &marks{}}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		tool.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	}()

	for _, n := range log.all() {
		if n.Outcome == CacheStore {
			t.Fatalf("a panicked read was stored: %+v", n)
		}
	}
	if _, hit, ticket := b.ReadStart("a2", "x", "1:1"); hit || ticket == nil {
		t.Fatal("unrelated read affected by the panic")
	}
}
