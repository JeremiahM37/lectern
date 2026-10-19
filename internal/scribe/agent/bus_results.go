package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Limits on the tool-result cache (spec 03 section 3.7): 200 entries, each
// valid for 120 seconds.
const (
	maxResultEntries = 200
	resultTTL        = 120 * time.Second
)

// resultFile is a file a cached result was read from, with the stamp it had
// then. The stamp is checked on every hit, so an edit made outside the bus
// (by the parent between dispatches, say) still makes the result a miss.
type resultFile struct {
	path  string
	stamp string
}

// cachedResult is a read-only tool result and what it depends on. A result
// whose arguments named no regular file is wild: it could depend on any file,
// so any edit clears it, and it is not carried into the next dispatch.
type cachedResult struct {
	result tools.Result
	files  []resultFile
	wild   bool
	at     time.Time
	used   uint64
}

// clock is the bus's time source; tests replace now.
func (b *AgentBus) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// fileStamp is the size and modification time of path, as the read cache
// uses them. ok is false for anything that is not a regular file.
func fileStamp(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()), true
}

// filesUnchanged reports whether every file the result was read from still
// has the stamp it had then.
func (c cachedResult) filesUnchanged() bool {
	for _, f := range c.files {
		if stamp, ok := fileStamp(f.path); !ok || stamp != f.stamp {
			return false
		}
	}
	return true
}

// touches reports whether the result was read from one of paths.
func (c cachedResult) touches(paths []string) bool {
	for _, f := range c.files {
		for _, p := range paths {
			if f.path == p {
				return true
			}
		}
	}
	return false
}

// ToolResult returns the stored result for key when it is still valid: within
// its TTL and with its files unchanged. An expired or stale entry is dropped.
func (b *AgentBus) ToolResult(key string) (tools.Result, bool) {
	if b == nil {
		return tools.Result{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disposed {
		return tools.Result{}, false
	}
	c, ok := b.results[key]
	if !ok {
		return tools.Result{}, false
	}
	if b.clock().Sub(c.at) > resultTTL || !c.filesUnchanged() {
		delete(b.results, key)
		return tools.Result{}, false
	}
	b.resultTick++
	c.used = b.resultTick
	b.results[key] = c
	return c.result, true
}

// storeToolResult keeps res under key, read from files. It is dropped when
// an invalidation ran since gen was read, or when res is an error.
func (b *AgentBus) storeToolResult(key string, res tools.Result, files []resultFile, wild bool, gen uint64) {
	if b == nil || res.IsError {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.resultGen != gen {
		return
	}
	b.storeResultLocked(key, cachedResult{result: res, files: files, wild: wild, at: b.clock()})
}

// storeResultLocked puts c under key. Past maxResultEntries it drops expired
// entries first, then the least recently used. b.mu must be held.
func (b *AgentBus) storeResultLocked(key string, c cachedResult) {
	if b.disposed {
		return
	}
	if b.results == nil {
		b.results = map[string]cachedResult{}
	}
	b.resultTick++
	c.used = b.resultTick
	b.results[key] = c
	if len(b.results) <= maxResultEntries {
		return
	}
	now := b.clock()
	for k, r := range b.results {
		if now.Sub(r.at) > resultTTL {
			delete(b.results, k)
		}
	}
	for len(b.results) > maxResultEntries {
		victim, found := "", false
		for k, r := range b.results {
			if !found || r.used < b.results[victim].used {
				victim, found = k, true
			}
		}
		delete(b.results, victim)
	}
}

// invalidateResults is called for every call that may write. Given the files
// it wrote, it drops the results read from them and every wild result. With no
// files named it drops every result, since nothing says what changed.
func (b *AgentBus) invalidateResults(paths []string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resultGen++
	for k, c := range b.results {
		if len(paths) == 0 || c.wild || c.touches(paths) {
			delete(b.results, k)
		}
	}
}

// resultGeneration is the invalidation count a result must still match when
// it is stored.
func (b *AgentBus) resultGeneration() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resultGen
}

// ResultCacheTool wraps a tool for the subagents of one dispatch. A read-only
// call with the same arguments as an earlier one is answered from the bus while
// the files it read are unchanged. A call that may write clears the results it
// could have changed, before and after it runs. id names the subagent in the
// cache notes.
func ResultCacheTool(inner tools.Tool, bus *AgentBus, id string) tools.Tool {
	return resultCacheTool{Tool: inner, bus: bus, id: id}
}

type resultCacheTool struct {
	tools.Tool
	bus *AgentBus
	id  string
}

// ReadOnlyCall passes the inner tool's answer through, so the mode gate and
// the parallel scheduler treat the wrapped tool as they did before the wrap.
func (t resultCacheTool) ReadOnlyCall(raw json.RawMessage) bool {
	return tools.IsReadOnlyCall(t.Tool, raw)
}

func (t resultCacheTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	if env == nil || t.bus == nil {
		return t.Tool.Run(ctx, env, raw)
	}
	if !tools.IsReadOnlyCall(t.Tool, raw) {
		t.bus.invalidateResults(writtenFiles(env.Workdir, raw))
		res := t.Tool.Run(ctx, env, raw)
		t.bus.invalidateResults(writtenFiles(env.Workdir, raw))
		return res
	}
	key := t.Spec().Name + "\x00" + string(raw)
	if res, ok := t.bus.ToolResult(key); ok {
		t.bus.noteCache(CacheNote{Agent: t.id, Outcome: CacheHit, Tool: true})
		if res.Summary != "" {
			res.Summary += " (cached in this dispatch)"
		} else {
			res.Summary = "cached in this dispatch"
		}
		return res
	}
	gen := t.bus.resultGeneration()
	res := t.Tool.Run(ctx, env, raw)
	files, wild := resultFiles(env.Workdir, raw)
	t.bus.storeToolResult(key, res, files, wild, gen)
	return res
}

// argStrings returns every string in the top level of a call's arguments,
// and the strings in a list argument. Anything else is not a path.
func argStrings(raw json.RawMessage) []string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return nil
	}
	var out []string
	for _, v := range args {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// absArg makes a string argument an absolute path under workdir.
func absArg(workdir, s string) string {
	if filepath.IsAbs(s) {
		return filepath.Clean(s)
	}
	return filepath.Join(workdir, s)
}

// resultFiles lists the regular files a read-only call names, with their
// stamps. wild is true when the call named no file, or named one that is not
// a regular file (a directory, a pattern, a missing path): such a result can
// depend on files the cache cannot see.
func resultFiles(workdir string, raw json.RawMessage) (files []resultFile, wild bool) {
	cands := argStrings(raw)
	if len(cands) == 0 {
		return nil, true
	}
	for _, c := range cands {
		abs := absArg(workdir, c)
		stamp, ok := fileStamp(abs)
		if !ok {
			wild = true
			continue
		}
		files = append(files, resultFile{path: abs, stamp: stamp})
	}
	return files, wild
}

// writtenFiles lists the existing regular files a call that may write names.
// nil means none could be named, so the caller clears every result.
func writtenFiles(workdir string, raw json.RawMessage) []string {
	var out []string
	for _, c := range argStrings(raw) {
		abs := absArg(workdir, c)
		if _, ok := fileStamp(abs); ok {
			out = append(out, abs)
		}
	}
	return out
}
