package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Outcomes of a read that goes through the dispatch cache. They are what the
// subagent's row shows (spec 01 §2.9): a read served from an earlier one, a
// read held up behind another subagent's, a read kept for the others, and a
// read that replaced an entry for a file which had changed.
const (
	CacheHit        = "hit"
	CacheWait       = "wait"
	CacheStore      = "store"
	CacheInvalidate = "invalidate"
)

// readWaitTimeout is how long a reader waits on another subagent's read of
// the same file (spec 03 §3.7). After it, the reader reads the file itself.
const readWaitTimeout = 30 * time.Second

// CacheNote says what the cache did for one subagent's read. From is the
// subagent whose read supplied the entry (hit) or is being waited on (wait).
// It is empty when no subagent is named, for example an entry stored before
// the reader's id was known.
//
// Tool is set for a read-only tool result (bus_results.go), so the tally can
// count it apart from file reads.
type CacheNote struct {
	Agent   string
	Outcome string
	From    string
	Tool    bool
}

// readFlight is a read of one file that is running now, so a second reader
// can wait for it rather than start its own.
type readFlight struct {
	stamp  string
	by     string
	done   chan struct{}
	closed bool // done is closed; it is closed once, by whoever ends the flight
}

// releaseFlightLocked wakes the waiters of f. b.mu must be held, so a flight
// released by Dispose is not closed a second time by its ticket's Finish.
func releaseFlightLocked(f *readFlight) {
	if !f.closed {
		f.closed = true
		close(f.done)
	}
}

// ReadTicket is a reader's claim on a read it has to run itself. Finish keeps
// the result for the other subagents, or only releases the waiters when the
// read failed. A nil ticket does nothing.
type ReadTicket struct {
	bus   *AgentBus
	key   string
	stamp string
	agent string
	f     *readFlight
	once  sync.Once
}

// SetCacheObserver sets the function that hears each CacheNote. It is called
// without the bus lock held, so it may call back into the bus.
func (b *AgentBus) SetCacheObserver(f func(CacheNote)) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.observer = f
	b.mu.Unlock()
}

// noteCache passes n to the observer, if one is set.
func (b *AgentBus) noteCache(n CacheNote) {
	b.mu.Lock()
	f := b.observer
	b.mu.Unlock()
	if f != nil {
		f(n)
	}
}

// lookupLocked is the cached read of key at stamp, and the subagent that
// stored it. b.mu must be held.
func (b *AgentBus) lookupLocked(key, stamp string) (out, by string, ok bool) {
	c, found := b.reads[key]
	if !found || c.stamp != stamp {
		return "", "", false
	}
	b.readTick++
	c.used = b.readTick
	b.reads[key] = c
	return c.output, c.by, true
}

// ReadStart is the cache step of a read by subagent id. A stored read at the
// same stamp is returned as a hit. A read of that file at that stamp that is
// running now is waited for, up to readWait, and then served as a hit. Any
// other read gets a ticket and runs; its caller reports the result through
// Finish. With no bus or no stamp nothing is cached, and the ticket is nil.
func (b *AgentBus) ReadStart(id, key, stamp string) (string, bool, *ReadTicket) {
	if b == nil || stamp == "" {
		return "", false, nil
	}
	b.mu.Lock()
	if b.disposed {
		b.mu.Unlock()
		return "", false, nil
	}
	if out, by, ok := b.lookupLocked(key, stamp); ok {
		b.mu.Unlock()
		b.noteCache(CacheNote{Agent: id, Outcome: CacheHit, From: by})
		return out, true, nil
	}
	if f := b.flights[key]; f != nil && f.stamp == stamp && b.readWait > 0 {
		b.mu.Unlock()
		b.noteCache(CacheNote{Agent: id, Outcome: CacheWait, From: f.by})
		timer := time.NewTimer(b.readWait)
		select {
		case <-f.done:
		case <-timer.C:
		}
		timer.Stop()
		b.mu.Lock()
		if out, by, ok := b.lookupLocked(key, stamp); ok {
			b.mu.Unlock()
			b.noteCache(CacheNote{Agent: id, Outcome: CacheHit, From: by})
			return out, true, nil
		}
	}
	f := &readFlight{stamp: stamp, by: id, done: make(chan struct{})}
	b.flights[key] = f
	b.mu.Unlock()
	return "", false, &ReadTicket{bus: b, key: key, stamp: stamp, agent: id, f: f}
}

// Finish ends the read the ticket stands for. A successful read is stored for
// the others, and the note is store, or invalidate when it replaces an entry
// for an older stamp. A failed read is not stored. Waiters are released either
// way. Only the first call has an effect.
func (t *ReadTicket) Finish(output string, ok bool) {
	if t == nil || t.bus == nil {
		return
	}
	t.once.Do(func() { t.bus.finishRead(t, output, ok) })
}

func (b *AgentBus) finishRead(t *ReadTicket, output string, ok bool) {
	b.mu.Lock()
	if b.flights[t.key] == t.f {
		delete(b.flights, t.key)
	}
	outcome := ""
	if ok {
		old, existed := b.reads[t.key]
		b.storeReadLocked(t.key, cachedRead{stamp: t.stamp, output: output, by: t.agent})
		outcome = CacheStore
		if existed && old.stamp != t.stamp {
			outcome = CacheInvalidate
		}
	}
	releaseFlightLocked(t.f)
	b.mu.Unlock()
	if outcome != "" {
		b.noteCache(CacheNote{Agent: t.agent, Outcome: outcome})
	}
}

// ReadCacheTool wraps a read tool so that a repeated read of an unchanged
// file is served from the bus, by subagent id. The stamp is the file's size
// and modification time, so an edit makes the next read a miss. Reads with a
// path list, line range, symbol or force, and reads with no plain path, go
// straight to the tool. A cache hit still marks the file read for the edit
// guard, because the agent did see it.
func ReadCacheTool(inner tools.Tool, bus *AgentBus, id string) tools.Tool {
	return readCacheTool{Tool: inner, bus: bus, id: id}
}

type readCacheTool struct {
	tools.Tool
	bus *AgentBus
	id  string
}

func (t readCacheTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var args map[string]json.RawMessage
	if env == nil || json.Unmarshal(raw, &args) != nil {
		return t.Tool.Run(ctx, env, raw)
	}
	for _, k := range []string{"paths", "lines", "symbol", "force"} {
		if _, set := args[k]; set {
			return t.Tool.Run(ctx, env, raw)
		}
	}
	var path string
	if json.Unmarshal(args["path"], &path) != nil || path == "" {
		return t.Tool.Run(ctx, env, raw)
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(env.Workdir, path)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return t.Tool.Run(ctx, env, raw)
	}
	stamp := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	key := abs + "\x00" + string(raw)
	out, hit, ticket := t.bus.ReadStart(t.id, key, stamp)
	if hit {
		if env.Files != nil {
			env.Files.MarkRead(abs)
		}
		return tools.Result{Output: out, Summary: "read (cached in this dispatch)"}
	}
	// The ticket must end even if the read panics, or its waiters would sit
	// out the whole wait. A panic counts as a failed read.
	var res tools.Result
	ok := false
	defer func() { ticket.Finish(res.Output, ok && !res.IsError) }()
	res = t.Tool.Run(ctx, env, raw)
	ok = true
	return res
}
