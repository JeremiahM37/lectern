package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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
}

// NewAgentBus returns an empty bus.
func NewAgentBus() *AgentBus {
	return &AgentBus{owners: map[string][]string{}, reads: map[string]cachedRead{}}
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
	c, ok := b.reads[key]
	if !ok || c.stamp != stamp {
		return "", false
	}
	return c.output, true
}

// StoreRead remembers the output of a read of key at stamp.
func (b *AgentBus) StoreRead(key, stamp, output string) {
	if b == nil || stamp == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads[key] = cachedRead{stamp: stamp, output: output}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
