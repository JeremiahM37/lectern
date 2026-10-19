package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// w41Tool is a tool that counts its runs and returns a fixed result.
type w41Tool struct {
	name     string
	readOnly bool
	runs     *int
	out      string
	isErr    bool
}

func (t w41Tool) Spec() llm.ToolSpec { return llm.ToolSpec{Name: t.name} }
func (t w41Tool) ReadOnly() bool     { return t.readOnly }
func (t w41Tool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	*t.runs++
	return tools.Result{Output: t.out, IsError: t.isErr}
}

// w41Clock is a bus clock the test moves by hand.
type w41Clock struct{ now time.Time }

func (c *w41Clock) get() time.Time { return c.now }

func newW41Bus(c *w41Clock) *AgentBus {
	b := NewAgentBus()
	b.now = c.get
	return b
}

// A read cache past 50 MB evicts the least recently used read, not the oldest.
func TestBusReadCacheEvictsLeastRecentlyUsedPastTheByteCap(t *testing.T) {
	b := NewAgentBus()
	big := strings.Repeat("x", 20<<20)
	b.StoreRead("a", "1:1", big)
	b.StoreRead("b", "1:1", big)
	if _, ok := b.CachedRead("a", "1:1"); !ok {
		t.Fatal("a was evicted before the cap was reached")
	}
	// a was just used, so b is now the least recently used.
	b.StoreRead("c", "1:1", big)
	if _, ok := b.CachedRead("b", "1:1"); ok {
		t.Fatal("least recently used read b survived past the cap")
	}
	if _, ok := b.CachedRead("a", "1:1"); !ok {
		t.Fatal("recently used read a was evicted")
	}
	if _, ok := b.CachedRead("c", "1:1"); !ok {
		t.Fatal("new read c was not stored")
	}
	if b.readBytes > maxReadBytes {
		t.Fatalf("read cache holds %d bytes, over the %d cap", b.readBytes, maxReadBytes)
	}
}

// A read finished through a ticket is held to the same cap.
func TestBusReadCacheCapAppliesToFinishedReads(t *testing.T) {
	b := NewAgentBus()
	_, _, t1 := b.ReadStart("a1", "one", "1:1")
	t1.Finish(strings.Repeat("y", 30<<20), true)
	_, _, t2 := b.ReadStart("a1", "two", "1:1")
	t2.Finish(strings.Repeat("z", 30<<20), true)
	if _, ok := b.CachedRead("one", "1:1"); ok {
		t.Fatal("the first finished read should have been evicted to make room")
	}
	if _, ok := b.CachedRead("two", "1:1"); !ok {
		t.Fatal("the second finished read is missing")
	}
	// A read bigger than the whole cache is not kept at all.
	b.StoreRead("huge", "1:1", strings.Repeat("h", maxReadBytes+1))
	if _, ok := b.CachedRead("huge", "1:1"); ok {
		t.Fatal("a read bigger than the cache was kept")
	}
}

// A repeated call of a read-only tool is answered from the bus, and an edit to
// the file it read makes the next call a miss.
func TestResultCacheServesRepeatAndFollowsFileStamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewAgentBus()
	log := newNoteLog(b)
	runs := 0
	tool := ResultCacheTool(w41Tool{name: "grep", readOnly: true, runs: &runs, out: "1 match"}, b, "s1")
	env := &tools.Env{Workdir: dir}
	args := json.RawMessage(`{"path":"a.go","pattern":"package"}`)

	first := tool.Run(context.Background(), env, args)
	second := tool.Run(context.Background(), env, args)
	if runs != 1 || first.Output != second.Output || !strings.Contains(second.Summary, "cached") {
		t.Fatalf("runs=%d second=%+v", runs, second)
	}
	if n := log.waitFor(t, CacheHit); !n.Tool || n.Agent != "s1" {
		t.Fatalf("hit note = %+v, want a tool hit for s1", n)
	}

	if err := os.WriteFile(path, []byte("package a\n// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool.Run(context.Background(), env, args)
	if runs != 2 {
		t.Fatalf("an edited file still served from the cache: runs=%d", runs)
	}
}

// A call that may write clears the results it could have changed: every wild
// result, and the results read from the file it named. A result read from an
// unrelated file survives.
func TestResultCacheWriteClearsWildAndNamedResults(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := NewAgentBus()
	env := &tools.Env{Workdir: dir}
	runsA, runsWild, runsEdit := 0, 0, 0
	readA := ResultCacheTool(w41Tool{name: "read_lines", readOnly: true, runs: &runsA, out: "a"}, b, "s")
	wild := ResultCacheTool(w41Tool{name: "git_log", readOnly: true, runs: &runsWild, out: "log"}, b, "s")
	edit := ResultCacheTool(w41Tool{name: "edit_file", readOnly: false, runs: &runsEdit, out: "ok"}, b, "s")
	argsA := json.RawMessage(`{"path":"a.go"}`)

	readA.Run(context.Background(), env, argsA)
	wild.Run(context.Background(), env, json.RawMessage(`{}`))
	edit.Run(context.Background(), env, json.RawMessage(`{"path":"b.go","old":"x","new":"y"}`))

	readA.Run(context.Background(), env, argsA)
	wild.Run(context.Background(), env, json.RawMessage(`{}`))
	if runsA != 1 {
		t.Fatalf("a result from an unrelated file was cleared: runsA=%d", runsA)
	}
	if runsWild != 2 {
		t.Fatalf("a wild result survived a write: runsWild=%d", runsWild)
	}
	if runsEdit != 1 {
		t.Fatalf("the write ran %d times, want 1", runsEdit)
	}
}

// A write whose target cannot be named (a new file, a shell command) clears
// every result.
func TestResultCacheUnnamedWriteClearsEverything(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewAgentBus()
	env := &tools.Env{Workdir: dir}
	runs, runsW := 0, 0
	read := ResultCacheTool(w41Tool{name: "read_lines", readOnly: true, runs: &runs, out: "a"}, b, "s")
	write := ResultCacheTool(w41Tool{name: "write_file", readOnly: false, runs: &runsW, out: "ok"}, b, "s")
	read.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	write.Run(context.Background(), env, json.RawMessage(`{"path":"new.go","content":"x"}`))
	read.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	if runs != 2 {
		t.Fatalf("a result survived a write to a new file: runs=%d", runs)
	}
}

// A failed call is never stored, so the next call runs again.
func TestResultCacheDoesNotStoreErrors(t *testing.T) {
	b := NewAgentBus()
	env := &tools.Env{Workdir: t.TempDir()}
	runs := 0
	tool := ResultCacheTool(w41Tool{name: "grep", readOnly: true, runs: &runs, out: "boom", isErr: true}, b, "s")
	tool.Run(context.Background(), env, json.RawMessage(`{"pattern":"x"}`))
	tool.Run(context.Background(), env, json.RawMessage(`{"pattern":"x"}`))
	if runs != 2 {
		t.Fatalf("an error result was served from the cache: runs=%d", runs)
	}
}

// A result computed before an invalidation is not stored after it.
func TestResultCacheDropsResultOverlappingAnInvalidation(t *testing.T) {
	b := NewAgentBus()
	gen := b.resultGeneration()
	b.invalidateResults(nil)
	b.storeToolResult("k", tools.Result{Output: "stale"}, nil, true, gen)
	if _, ok := b.ToolResult("k"); ok {
		t.Fatal("a result from before an invalidation was stored")
	}
}

// Results expire after 120 seconds, and past 200 entries the least recently
// used one goes.
func TestResultCacheTTLAndEntryLimit(t *testing.T) {
	c := &w41Clock{now: time.Unix(1000, 0)}
	b := newW41Bus(c)
	b.storeToolResult("old", tools.Result{Output: "x"}, nil, true, b.resultGeneration())
	c.now = c.now.Add(resultTTL + time.Second)
	if _, ok := b.ToolResult("old"); ok {
		t.Fatal("a result past its TTL was served")
	}

	for i := 0; i < maxResultEntries; i++ {
		b.storeToolResult(fmt.Sprintf("k%d", i), tools.Result{Output: "v"}, nil, true, b.resultGeneration())
	}
	// Reading k0 makes it recently used, so k1 is the one that goes.
	if _, ok := b.ToolResult("k0"); !ok {
		t.Fatal("k0 is missing before the limit")
	}
	b.storeToolResult("extra", tools.Result{Output: "v"}, nil, true, b.resultGeneration())
	if n := len(b.results); n != maxResultEntries {
		t.Fatalf("the cache holds %d entries, want %d", n, maxResultEntries)
	}
	if _, ok := b.ToolResult("k1"); ok {
		t.Fatal("the least recently used entry k1 survived the limit")
	}
	if _, ok := b.ToolResult("k0"); !ok {
		t.Fatal("the recently used entry k0 was evicted")
	}
	if _, ok := b.ToolResult("extra"); !ok {
		t.Fatal("the new entry was not stored")
	}
}

// Export keeps finished reads and file-backed results, drops the subagent
// names and wild results, and a seeded bus serves them until a file changes.
func TestExportSeedCarriesCachesIntoTheNextBus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := NewAgentBus()
	first.StoreRead("read-key", "5:5", "package a")
	runs := 0
	tool := ResultCacheTool(w41Tool{name: "grep", readOnly: true, runs: &runs, out: "m"}, first, "s9")
	env := &tools.Env{Workdir: dir}
	tool.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	tool.Run(context.Background(), env, json.RawMessage(`{"pattern":"any"}`))

	snap := first.ExportCaches()
	if snap == nil || len(snap.reads) != 1 || len(snap.results) != 1 {
		t.Fatalf("export = %+v, want one read and one file result", snap)
	}

	next := NewAgentBus()
	next.SeedCaches(snap)
	if out, ok := next.CachedRead("read-key", "5:5"); !ok || out != "package a" {
		t.Fatal("the seeded read is missing")
	}
	if n := next.reads["read-key"].by; n != "" {
		t.Fatalf("the seeded read still names subagent %q", n)
	}
	seeded := 0
	again := ResultCacheTool(w41Tool{name: "grep", readOnly: true, runs: &seeded, out: "m"}, next, "s10")
	again.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	if seeded != 0 {
		t.Fatalf("the seeded file result was not served: runs=%d", seeded)
	}

	if err := os.WriteFile(path, []byte("package a // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	if seeded != 1 {
		t.Fatalf("a seeded result survived an edit made between dispatches: runs=%d", seeded)
	}
}

// Dispose drops every cache, releases a reader waiting on a read that never
// finishes, and makes later use inert.
func TestDisposeEmptiesCachesAndReleasesWaiters(t *testing.T) {
	b := NewAgentBus()
	b.readWait = time.Minute
	b.StoreRead("k", "1:1", "v")
	_, _, owner := b.ReadStart("a1", "pending", "1:1")

	waited := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		b.ReadStart("a2", "pending", "1:1")
		waited <- time.Since(start)
	}()
	time.Sleep(20 * time.Millisecond)
	b.Dispose()
	select {
	case d := <-waited:
		if d > 10*time.Second {
			t.Fatalf("the waiter sat out %v after dispose", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispose did not release the waiter")
	}

	owner.Finish("late", true)
	if _, ok := b.CachedRead("k", "1:1"); ok {
		t.Fatal("a read survived dispose")
	}
	if _, _, ticket := b.ReadStart("a3", "new", "1:1"); ticket != nil {
		t.Fatal("a disposed bus handed out a read ticket")
	}
	b.StoreRead("k", "1:1", "again")
	if _, ok := b.CachedRead("k", "1:1"); ok {
		t.Fatal("a disposed bus stored a read")
	}
	if b.ExportCaches() != nil {
		t.Fatal("a disposed bus exported caches")
	}
}

// The wrapper keeps the inner tool's read-only answer per call, so the mode
// gate still sees a write as a write.
func TestResultCacheToolKeepsPerCallReadOnlyAnswer(t *testing.T) {
	b := NewAgentBus()
	runs := 0
	inner := gitLike{w41Tool: w41Tool{name: "git", readOnly: false, runs: &runs}}
	tool := ResultCacheTool(inner, b, "s")
	if !tools.IsReadOnlyCall(tool, json.RawMessage(`{"op":"status"}`)) {
		t.Fatal("a read-only git call is reported as a write")
	}
	if tools.IsReadOnlyCall(tool, json.RawMessage(`{"op":"commit"}`)) {
		t.Fatal("a git commit is reported as read-only")
	}
}

type gitLike struct{ w41Tool }

func (g gitLike) ReadOnlyCall(raw json.RawMessage) bool {
	return strings.Contains(string(raw), "status")
}
