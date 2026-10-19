package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// AgentBus is the state the subagents of one dispatch share: the findings
// they have reported, which agent first edited each file, and a cache of
// file reads, so two agents reading the same file do not both pay for it.
// It lives for one dispatch call and is safe for concurrent use.
type AgentBus struct {
	mu       sync.Mutex
	findings []Finding
	bytes    int                 // total text bytes of findings, capped at maxFindingsBytes
	owners   map[string][]string // file -> agents that edited it, first claimant first
	reads    map[string]cachedRead
	// readBytes is the size of reads. readTick orders them by last use, so
	// the least recently used read is the one evicted past maxReadBytes.
	readBytes int
	readTick  uint64
	// results are the read-only tool results (bus_results.go), by tool and args.
	results    map[string]cachedResult
	resultTick uint64
	// resultGen counts invalidations. A result computed before one is not
	// stored after it, so a slow read-only call cannot cache a stale answer.
	resultGen uint64
	now       func() time.Time
	disposed  bool
	// flights are the reads in progress, by key, so a second reader of the
	// same file at the same stamp waits for the first instead of reading again.
	flights map[string]*readFlight
	// observer hears what the cache did for each read (see CacheNote).
	observer func(CacheNote)
	// readWait bounds that wait. Zero means readers never wait.
	readWait time.Duration
	// abortFn cancels the dispatch's subagents; aborted records that a
	// cancel_dispatch call (or the caller) asked for it.
	abortFn func()
	aborted bool
}

// Limits on findings (spec 03 section 3.7): 2 KB each, 128 KB per bus.
const (
	maxFindingBytes  = 2 << 10
	maxFindingsBytes = 128 << 10
)

// Finding is one thing a subagent reported to the others. File and Line are
// optional; when set they anchor the text in the code.
type Finding struct {
	Agent string
	File  string
	Line  int
	Text  string
}

// cachedRead is a read result and the file stamp it was taken at.
type cachedRead struct {
	stamp  string
	output string
	by     string // the subagent whose read this is, "" when unknown
	used   uint64 // readTick at the last store or hit, for eviction
}

// size is what the read costs against maxReadBytes: its output and key.
func (c cachedRead) size() int { return len(c.output) + len(c.stamp) }

// NewAgentBus returns an empty bus.
func NewAgentBus() *AgentBus {
	return &AgentBus{
		owners:   map[string][]string{},
		reads:    map[string]cachedRead{},
		results:  map[string]cachedResult{},
		flights:  map[string]*readFlight{},
		readWait: readWaitTimeout,
		now:      time.Now,
	}
}

// Post records a finding from agent id. Blank text is ignored. A finding
// over the limits is dropped; PostFinding says why.
func (b *AgentBus) Post(id, text string) {
	_ = b.PostFinding(Finding{Agent: id, Text: text})
}

// PostFinding records f. Its text is trimmed; blank text is refused. It
// refuses a finding over maxFindingBytes, and one that would take the bus
// past maxFindingsBytes, with an error that says what to do instead.
func (b *AgentBus) PostFinding(f Finding) error {
	f.Text = strings.TrimSpace(f.Text)
	if b == nil {
		return errors.New("findings are not shared in this session")
	}
	if f.Text == "" {
		return errors.New("the finding is empty: say what you found, with file and line if you have them")
	}
	if len(f.Text) > maxFindingBytes {
		return fmt.Errorf("the finding is %d bytes, over the %d byte limit: shorten it to the point, and post the rest as a second finding", len(f.Text), maxFindingBytes)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bytes+len(f.Text) > maxFindingsBytes {
		return fmt.Errorf("the findings for this dispatch are full (%d bytes): post only what the parent must act on", maxFindingsBytes)
	}
	b.bytes += len(f.Text)
	b.findings = append(b.findings, f)
	return nil
}

// FindingsExcept returns the findings not posted by agent id, oldest first.
func (b *AgentBus) FindingsExcept(id string) []Finding {
	var out []Finding
	for _, f := range b.Findings() {
		if f.Agent != id {
			out = append(out, f)
		}
	}
	return out
}

// Findings returns the findings posted so far, oldest first.
func (b *AgentBus) Findings() []Finding {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Finding(nil), b.findings...)
}

// ClaimEdit records that agent id is editing file. It returns the id of the
// agent that claimed the file first and whether that is a different agent.
// The first claim wins; a conflict does not change the owner.
func (b *AgentBus) ClaimEdit(file, id string) (owner string, conflict bool) {
	if b == nil {
		return id, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.owners[file]
	if len(list) == 0 {
		b.owners[file] = []string{id}
		return id, false
	}
	owner = list[0]
	if !containsString(list, id) {
		b.owners[file] = append(list, id)
		return owner, true
	}
	return owner, owner != id
}

// Conflicts lists the files that more than one agent has edited, as
// "file: agents" messages, sorted. Pass file names to limit the list to those
// files. It is empty when every file has one editor.
func (b *AgentBus) Conflicts(file ...string) []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for f, list := range b.owners {
		if len(list) < 2 || (len(file) > 0 && !containsString(file, f)) {
			continue
		}
		out = append(out, f+": edited by agents "+strings.Join(list, ", "))
	}
	sort.Strings(out)
	return out
}

// CachedRead returns the output of an earlier read of key taken at the same
// stamp. The stamp changes when the file does, so a stale read is a miss.
func (b *AgentBus) CachedRead(key, stamp string) (string, bool) {
	if b == nil || stamp == "" {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out, _, ok := b.lookupLocked(key, stamp)
	return out, ok
}

// StoreRead remembers the output of a read of key at stamp.
func (b *AgentBus) StoreRead(key, stamp, output string) {
	if b == nil || stamp == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.storeReadLocked(key, cachedRead{stamp: stamp, output: output})
}

// Limits on the read cache (spec 03 section 3.7): 50 MB in total, evicting
// the least recently accessed read first.
const maxReadBytes = 50 << 20

// storeReadLocked puts c under key, evicting the least recently used reads
// until the cache fits again. A read bigger than the whole cache is not kept.
// b.mu must be held.
func (b *AgentBus) storeReadLocked(key string, c cachedRead) {
	if b.disposed {
		return
	}
	if old, ok := b.reads[key]; ok {
		b.readBytes -= old.size()
		delete(b.reads, key)
	}
	if c.size() > maxReadBytes {
		return
	}
	b.readTick++
	c.used = b.readTick
	b.reads[key] = c
	b.readBytes += c.size()
	for b.readBytes > maxReadBytes {
		victim, found := "", false
		for k, r := range b.reads {
			if !found || r.used < b.reads[victim].used {
				victim, found = k, true
			}
		}
		if !found {
			break
		}
		b.readBytes -= b.reads[victim].size()
		delete(b.reads, victim)
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SetAbort gives the bus the function that cancels the dispatch's subagents.
func (b *AgentBus) SetAbort(f func()) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.abortFn = f
	b.mu.Unlock()
}

// Abort cancels every subagent of the dispatch. It reports false when the
// dispatch was already cancelled, so the second call says so instead.
func (b *AgentBus) Abort() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	if b.aborted {
		b.mu.Unlock()
		return false
	}
	b.aborted = true
	f := b.abortFn
	b.mu.Unlock()
	if f != nil {
		f()
	}
	return true
}

// Aborted reports whether the dispatch was cancelled through the bus.
func (b *AgentBus) Aborted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.aborted
}
