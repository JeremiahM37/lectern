package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// eventBuffer is the size of the channel the TUI reads agent events from.
const eventBuffer = 8192

// TUIHost answers approvals and questions through the TUI. Create it before
// Open, pass it as Options.Host, then give it to NewBackend. Each tab has its
// own TUIHost, so each tab has its own session-level allow list.
type TUIHost struct {
	prompts chan tui.Prompt

	mu      sync.Mutex
	allowed map[string]bool   // Kind+Tool granted AllowSession this session
	emit    func(agent.Event) // set by NewBackend; nil until then
}

// NewTUIHost returns a host with no backend attached yet.
func NewTUIHost() *TUIHost {
	return &TUIHost{prompts: make(chan tui.Prompt, 16), allowed: map[string]bool{}}
}

var _ tools.Host = (*TUIHost)(nil)

func allowKey(ap tools.Approval) string { return ap.Kind + "\x00" + ap.Tool }

// Approve asks the TUI. A grant of AllowSession is remembered for the rest of
// the session for the same kind of action and tool. A cancelled context denies.
func (h *TUIHost) Approve(ctx context.Context, ap tools.Approval) tools.Decision {
	h.mu.Lock()
	granted := h.allowed[allowKey(ap)]
	h.mu.Unlock()
	if granted {
		return tools.AllowSession
	}
	reply := make(chan tui.PromptReply, 1)
	select {
	case h.prompts <- tui.Prompt{Approval: &ap, Reply: reply}:
	case <-ctx.Done():
		return tools.Deny
	}
	select {
	case r := <-reply:
		if r.Decision == tools.AllowSession {
			h.mu.Lock()
			h.allowed[allowKey(ap)] = true
			h.mu.Unlock()
		}
		return r.Decision
	case <-ctx.Done():
		return tools.Deny
	}
}

// Ask puts a question to the user and returns their answer.
func (h *TUIHost) Ask(ctx context.Context, q tools.Question) (string, error) {
	reply := make(chan tui.PromptReply, 1)
	select {
	case h.prompts <- tui.Prompt{Question: &q, Reply: reply}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case r := <-reply:
		return r.Answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Progress streams partial tool output to the event feed.
func (h *TUIHost) Progress(callID, text string) {
	h.mu.Lock()
	emit := h.emit
	h.mu.Unlock()
	if emit != nil {
		emit(agent.Event{Kind: agent.EvToolProgress, CallID: callID, Text: text})
	}
}

func (h *TUIHost) setEmit(f func(agent.Event)) {
	h.mu.Lock()
	h.emit = f
	h.mu.Unlock()
}

// resetSession forgets session-level grants (a new or resumed session).
func (h *TUIHost) resetSession() {
	h.mu.Lock()
	h.allowed = map[string]bool{}
	h.mu.Unlock()
}

// pump carries agent events to the TUI without ever blocking the agent.
// Events go straight into the channel while it has room and nothing is
// backed up. Otherwise they queue, and text or reasoning deltas that arrive
// behind a queued delta of the same kind are merged into it. Nothing is
// dropped, and order is kept.
type pump struct {
	out chan agent.Event

	mu       sync.Mutex
	queue    []agent.Event
	draining bool
}

func newPump() *pump { return &pump{out: make(chan agent.Event, eventBuffer)} }

func (p *pump) push(e agent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		select {
		case p.out <- e:
			return
		default:
		}
	}
	if n := len(p.queue); (e.Kind == agent.EvText || e.Kind == agent.EvReasoning) && n > 0 {
		last := n - 1
		// queue[0] may be in flight to out (drain copies it before sending),
		// so a delta must not be merged into it.
		if !(p.draining && last == 0) && p.queue[last].Kind == e.Kind && p.queue[last].AgentID == e.AgentID {
			p.queue[last].Text += e.Text
			return
		}
	}
	p.queue = append(p.queue, e)
	if !p.draining {
		p.draining = true
		go p.drain()
	}
}

// drain sends queued events in order. The head stays queued until it has been
// delivered, so later pushes cannot overtake it.
func (p *pump) drain() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.draining = false
			p.mu.Unlock()
			return
		}
		e := p.queue[0]
		p.mu.Unlock()
		p.out <- e
		p.mu.Lock()
		p.queue = p.queue[1:]
		p.mu.Unlock()
	}
}

// scribeBackend implements tui.Backend over an App. Methods that replace the
// session (NewSession, Resume, Fork, DeleteSession of the current one) build a
// new App and close the old one.
type scribeBackend struct {
	host   *TUIHost
	events *pump
	opts   Options // options of the first App, reused for new sessions
	root   string

	gen atomic.Uint64 // bumped on every App swap; stale listeners drop events

	mu        sync.Mutex
	app       *App
	turn      bool // a turn goroutine is running (includes the epilogue)
	turnDone  bool // the main agent has emitted turn_done for the current turn
	queued    []queuedTurn
	reasoning string
	closed    bool
	rebuild   bool

	fmu     sync.Mutex
	files   []string
	filesAt time.Time
}

type queuedTurn struct {
	prompt string
	images []llm.Part
}

var _ tui.Backend = (*scribeBackend)(nil)
var _ tui.SessionDeleter = (*scribeBackend)(nil)
var _ tui.SessionRenamer = (*scribeBackend)(nil)

// NewBackend wraps an opened App for the TUI. h must be the TUIHost that was
// passed as a.opts.Host, or approvals will not reach the TUI.
func NewBackend(a *App, h *TUIHost) tui.Backend {
	if h == nil {
		h = NewTUIHost()
	}
	b := &scribeBackend{
		host:      h,
		events:    newPump(),
		opts:      a.Opts(),
		root:      a.Workdir,
		reasoning: a.Cfg.Reasoning,
	}
	b.attach(a)
	return b
}

// attach makes a the current App and routes its events to the TUI.
func (b *scribeBackend) attach(a *App) {
	g := b.gen.Add(1)
	b.mu.Lock()
	b.app = a
	b.mu.Unlock()
	a.Subscribe(func(e agent.Event) {
		if b.gen.Load() != g {
			return
		}
		if e.Kind == agent.EvTurnDone && e.AgentID == "" {
			// Set before the TUI can see turn_done, so settle() is never behind it.
			b.mu.Lock()
			b.turnDone = true
			b.mu.Unlock()
		}
		b.events.push(e)
	})
	b.host.setEmit(a.emit)
}

func (b *scribeBackend) current() *App {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.app
}

// swap opens a new App with o, makes it current, and closes the old one.
// The caller has checked that no turn is running.
func (b *scribeBackend) swap(o Options) error {
	na, err := Open(context.Background(), o)
	if err != nil {
		return err
	}
	b.mu.Lock()
	old := b.app
	reasoning := b.reasoning
	b.mu.Unlock()
	na.Agent.SetReasoning(reasoning)
	if m := na.Meta.Mode; m != "" {
		na.Agent.SetMode(m)
	}
	b.host.resetSession()
	b.attach(na)
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (b *scribeBackend) busy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.turn
}

var errTurnRunning = errors.New("a turn is running; wait for it or abort it first")

// settle returns once no turn is running, so checkpoints and session state
// are safe to touch. The agent emits turn_done a moment before App.Run closes
// the turn's checkpoints and saves it, so a turn that has reported done is
// waited for briefly. A turn still in progress is an error at once.
func (b *scribeBackend) settle() error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		b.mu.Lock()
		busy, done := b.turn, b.turnDone
		b.mu.Unlock()
		if !busy {
			return nil
		}
		if !done || time.Now().After(deadline) {
			return errTurnRunning
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Events implements tui.Backend.
func (b *scribeBackend) Events() <-chan agent.Event { return b.events.out }

// Prompts implements tui.Backend.
func (b *scribeBackend) Prompts() <-chan tui.Prompt { return b.host.prompts }

// Submit starts a turn. If one is still finishing, the prompt waits for it.
func (b *scribeBackend) Submit(prompt string, images []llm.Part) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if b.turn {
		b.queued = append(b.queued, queuedTurn{prompt, images})
		b.mu.Unlock()
		return
	}
	b.turn, b.turnDone = true, false
	a := b.app
	b.mu.Unlock()
	go b.runTurns(a, prompt, images)
}

func (b *scribeBackend) runTurns(a *App, prompt string, images []llm.Part) {
	for {
		stats, err := a.Run(context.Background(), prompt, images)
		if turnFailed(stats, err) {
			a.emit(agent.Event{Kind: agent.EvError, Text: err.Error()})
		}
		b.mu.Lock()
		if len(b.queued) == 0 || b.app != a || b.closed {
			b.turn = false
			b.queued = nil
			b.mu.Unlock()
			return
		}
		next := b.queued[0]
		b.queued = b.queued[1:]
		b.turnDone = false
		b.mu.Unlock()
		prompt, images = next.prompt, next.images
	}
}

// Steer queues a message for the running turn.
func (b *scribeBackend) Steer(msg string) bool {
	return b.current().Agent.Steer(msg)
}

// Abort stops the running turn and drops prompts queued behind it.
func (b *scribeBackend) Abort() {
	b.mu.Lock()
	b.queued = nil
	a := b.app
	b.mu.Unlock()
	a.Agent.Abort()
}

// Running implements tui.Backend.
func (b *scribeBackend) Running() bool {
	return b.busy() || b.current().Agent.Running()
}

// Workdir implements tui.Backend.
func (b *scribeBackend) Workdir() string { return b.root }

// Version implements tui.Backend.
func (b *scribeBackend) Version() string { return b.opts.Version }

// SessionID implements tui.Backend.
func (b *scribeBackend) SessionID() string { return b.current().Meta.ID }

// SessionTitle implements tui.Backend.
func (b *scribeBackend) SessionTitle() string {
	a := b.current()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Meta.Title
}

// Transcript implements tui.Backend.
func (b *scribeBackend) Transcript() []agent.Event { return b.current().Transcript() }

// Model implements tui.Backend.
func (b *scribeBackend) Model() string { return b.current().Agent.CurrentModel().Ref }

// SetModel implements tui.Backend.
func (b *scribeBackend) SetModel(ref string) error { return b.current().SetModel(ref) }

// Models implements tui.Backend.
func (b *scribeBackend) Models(ctx context.Context, provider string) ([]llm.ModelInfo, error) {
	return b.current().Registry.ListModels(ctx, provider)
}

// localProviders run on this machine and need no key.
var localProviders = map[string]bool{"ollama": true, "lmstudio": true}

// Providers implements tui.Backend.
func (b *scribeBackend) Providers() []tui.ProviderStatus {
	var out []tui.ProviderStatus
	for _, p := range b.current().Registry.List() {
		out = append(out, tui.ProviderStatus{
			ID:           p.ID,
			Name:         p.Name,
			HasKey:       p.HasKey,
			Local:        localProviders[p.ID],
			Icon:         p.ASCIIIcon,
			KeySource:    p.KeySource,
			DefaultModel: p.DefaultModel,
		})
	}
	return out
}

// NeedsSetup reports whether the first-run wizard should open: no provider has
// a key and no Ollama server answers on its usual address.
func (b *scribeBackend) NeedsSetup() bool {
	// A model the user chose for a local server (ollama/...) is a choice, not
	// a missing setup: if that server is down, the first request says so
	// plainly, which is more useful than a wizard that does not say why it
	// opened.
	if a := b.current(); a.chosenModel() {
		if prov, _ := llm.SplitModelRef(b.Model()); prov == "ollama" {
			return false
		}
	}
	reg := b.current().Registry
	for _, p := range reg.List() {
		if p.HasKey {
			return false
		}
	}
	return !ollamaReachable(reg)
}

// ollamaReachable asks the local Ollama server for its model list with a short
// timeout. The provider's base URL ends in /v1; the native API lives at its root.
func ollamaReachable(reg *registry.Registry) bool {
	info, ok := reg.Lookup("ollama")
	if !ok || info.BaseURL == "" {
		return false
	}
	root := strings.TrimSuffix(strings.TrimRight(info.BaseURL, "/"), "/v1")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// SetKey stores a key. If the current model uses that provider, the model is
// resolved again so the new key applies to the next request.
func (b *scribeBackend) SetKey(provider, key string) error {
	a := b.current()
	if err := a.Registry.SetKey(provider, key); err != nil {
		return err
	}
	if ref := a.Agent.CurrentModel().Ref; strings.HasPrefix(ref, provider+"/") {
		return a.SetModel(ref)
	}
	return nil
}

// Mode implements tui.Backend.
func (b *scribeBackend) Mode() string { return b.current().Agent.Mode() }

// SetMode implements tui.Backend and remembers the mode for this session.
func (b *scribeBackend) SetMode(mode string) {
	a := b.current()
	a.Agent.SetMode(mode)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Meta.Mode = mode
	if !a.opts.Ephemeral {
		_ = a.Store.SaveMeta(&a.Meta)
	}
}

// Reasoning implements tui.Backend.
func (b *scribeBackend) Reasoning() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reasoning
}

// SetReasoning implements tui.Backend.
func (b *scribeBackend) SetReasoning(level string) {
	b.mu.Lock()
	b.reasoning = level
	b.mu.Unlock()
	b.current().Agent.SetReasoning(level)
}

// Usage implements tui.Backend.
func (b *scribeBackend) Usage() (llm.Usage, float64) { return b.current().Agent.Usage() }

// Context implements tui.Backend.
func (b *scribeBackend) Context() (used, window int) { return b.current().Agent.ContextUsed() }

// Compact implements tui.Backend.
func (b *scribeBackend) Compact(ctx context.Context) error {
	if err := b.settle(); err != nil {
		return err
	}
	return b.current().Agent.Compact(ctx, "manual")
}

// NewSession starts an empty session with the current model and mode.
func (b *scribeBackend) NewSession() error {
	if err := b.settle(); err != nil {
		return err
	}
	a := b.current()
	o := b.opts
	o.Resume = ""
	o.Host = b.host
	o.Model = a.Agent.CurrentModel().Ref
	o.Mode = a.Agent.Mode()
	if err := b.swap(o); err != nil {
		return err
	}
	b.events.push(agent.Event{Kind: agent.EvWarning, Text: "Started a new session."})
	return nil
}

// Sessions implements tui.Backend. It lists this project's sessions, newest first.
func (b *scribeBackend) Sessions() []tui.SessionInfo {
	a := b.current()
	metas := a.Store.List()
	out := make([]tui.SessionInfo, 0, len(metas))
	for _, m := range metas {
		out = append(out, tui.SessionInfo{
			ID:      m.ID,
			Title:   m.Title,
			Model:   m.Model,
			Workdir: m.Workdir,
			Updated: m.Updated,
			Turns:   m.Turns,
			CostUSD: m.CostUSD,
			Preview: firstUserText(a.Store, m.ID),
		})
	}
	return out
}

// firstUserText returns the first user message of a session, shortened.
func firstUserText(st *session.Store, id string) string {
	msgs, err := st.LoadMessages(id)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			return truncateRunes(strings.TrimSpace(m.Text()), 300)
		}
	}
	return ""
}

// Resume switches to a saved session (an id or unique prefix).
func (b *scribeBackend) Resume(id string) error {
	if err := b.settle(); err != nil {
		return err
	}
	a := b.current()
	_, meta, err := a.findSession(id)
	if err != nil {
		return err
	}
	if meta.ID == a.Meta.ID {
		return nil
	}
	o := b.opts
	o.Resume = meta.ID
	o.Host = b.host
	o.Model = "" // the session's own model, unless it has none
	o.Mode = ""
	return b.swap(o)
}

// Fork copies the current session at its last message and switches to the copy.
func (b *scribeBackend) Fork() (string, error) {
	if err := b.settle(); err != nil {
		return "", err
	}
	a := b.current()
	if a.opts.Ephemeral {
		return "", errors.New("this session is not saved, so it cannot be forked")
	}
	msgs, err := a.Store.LoadMessages(a.Meta.ID)
	if err != nil {
		return "", err
	}
	child, err := a.Store.Fork(a.Meta.ID, len(msgs))
	if err != nil {
		return "", err
	}
	if err := b.Resume(child); err != nil {
		return child, err
	}
	return child, nil
}

// Rename retitles the current session.
func (b *scribeBackend) Rename(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("the title is empty")
	}
	a := b.current()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Meta.Title = title
	if !a.opts.Ephemeral {
		return a.Store.SaveMeta(&a.Meta)
	}
	return nil
}

// RenameSession retitles any session in this project.
func (b *scribeBackend) RenameSession(id, title string) error {
	a := b.current()
	if id == a.Meta.ID {
		return b.Rename(title)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("the title is empty")
	}
	m, err := a.Store.Open(id)
	if err != nil {
		return err
	}
	m.Title = title
	return a.Store.SaveMeta(&m)
}

// DeleteSession removes a saved session. Deleting the current one first
// starts a new session, so the TUI is never left on a deleted id.
func (b *scribeBackend) DeleteSession(id string) error {
	a := b.current()
	if id == a.Meta.ID {
		if err := b.NewSession(); err != nil {
			return err
		}
	}
	return b.current().Store.Delete(id)
}

// History returns recent prompts in this folder, oldest first, for the input's Up-arrow history.
func (b *scribeBackend) History() []string {
	entries, err := session.Recent(b.root, 500)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i].Text)
	}
	return out
}

// Checkpoints implements tui.Backend.
func (b *scribeBackend) Checkpoints() []tui.CheckpointInfo {
	a := b.current()
	if a.Checks == nil {
		return nil
	}
	return checkpointInfos(a.Checks.List())
}

func checkpointInfos(cps []session.Checkpoint) []tui.CheckpointInfo {
	out := make([]tui.CheckpointInfo, 0, len(cps))
	for _, c := range cps {
		out = append(out, tui.CheckpointInfo{Turn: c.Turn, Prompt: c.Prompt, Files: c.Files, Created: c.Created, Undone: c.Undone})
	}
	return out
}

// conflictError explains a refused undo or restore in terms the user can act on.
func conflictError(err error) error {
	var ce *session.ConflictError
	if errors.As(err, &ce) {
		return fmt.Errorf("these files changed after the turn wrote them, so nothing was reverted: %s. Revert or keep those edits by hand first", strings.Join(ce.Files, ", "))
	}
	return err
}

// Undo reverts the latest turn that changed files.
func (b *scribeBackend) Undo() (tui.CheckpointInfo, error) {
	if err := b.settle(); err != nil {
		return tui.CheckpointInfo{}, err
	}
	a := b.current()
	if a.Checks == nil {
		return tui.CheckpointInfo{}, errors.New("checkpoints are off for this session")
	}
	turns, err := a.Checks.Undo(false)
	if err != nil {
		return tui.CheckpointInfo{}, conflictError(err)
	}
	return b.checkpointFor(turns), nil
}

// Redo reapplies the most recently undone turn.
func (b *scribeBackend) Redo() (tui.CheckpointInfo, error) {
	if err := b.settle(); err != nil {
		return tui.CheckpointInfo{}, err
	}
	a := b.current()
	if a.Checks == nil {
		return tui.CheckpointInfo{}, errors.New("checkpoints are off for this session")
	}
	turns, err := a.Checks.Redo(false)
	if err != nil {
		return tui.CheckpointInfo{}, conflictError(err)
	}
	return b.checkpointFor(turns), nil
}

// checkpointFor finds the checkpoint info for the first turn in turns.
func (b *scribeBackend) checkpointFor(turns []int) tui.CheckpointInfo {
	if len(turns) == 0 {
		return tui.CheckpointInfo{}
	}
	for _, c := range b.Checkpoints() {
		if c.Turn == turns[0] {
			return c
		}
	}
	return tui.CheckpointInfo{Turn: turns[0]}
}

// RestoreTo reverts every turn after turn.
func (b *scribeBackend) RestoreTo(turn int) error {
	if err := b.settle(); err != nil {
		return err
	}
	a := b.current()
	if a.Checks == nil {
		return errors.New("checkpoints are off for this session")
	}
	_, err := a.Checks.RestoreTo(turn, false)
	return conflictError(err)
}

// TurnDiff returns the diff a turn made, or a one-line reason it cannot.
func (b *scribeBackend) TurnDiff(turn int) string {
	a := b.current()
	if a.Checks == nil {
		return "checkpoints are off for this session"
	}
	d, err := a.Checks.Diff(turn)
	if err != nil {
		return "no diff for turn " + strconv.Itoa(turn) + ": " + err.Error()
	}
	return d
}

// Memories implements tui.Backend. An empty query lists the most recent memories.
func (b *scribeBackend) Memories(query string) []tui.MemoryItem {
	a := b.current()
	if a.Memory == nil {
		return nil
	}
	q := strings.TrimSpace(query)
	var out []tui.MemoryItem
	for _, db := range a.Memory.DBs() {
		if q != "" {
			hits, err := db.Search(q, 50)
			if err != nil {
				continue
			}
			for _, h := range hits {
				out = append(out, memoryItem(h.Record))
			}
			continue
		}
		recs, err := db.List(memory.ListOptions{Limit: 200})
		if err != nil {
			continue
		}
		for _, r := range recs {
			out = append(out, memoryItem(r))
		}
	}
	return out
}

func memoryItem(r memory.Record) tui.MemoryItem {
	return tui.MemoryItem{ID: r.ID, Scope: r.Scope, Category: r.Category, Title: r.Title, Body: r.Body, Pinned: r.Pinned, Updated: r.Updated}
}

// SaveMemory creates a memory (m.ID empty) or updates one. Scope defaults to project.
func (b *scribeBackend) SaveMemory(m tui.MemoryItem) error {
	a := b.current()
	if a.Memory == nil {
		return errors.New("memory is off for this session")
	}
	scope := m.Scope
	if scope == "" {
		scope = memory.ScopeProject
	}
	db, err := a.Memory.DB(scope)
	if err != nil {
		return err
	}
	if m.ID == "" {
		rec, _, err := db.Add(memory.Record{Scope: scope, Category: m.Category, Title: m.Title, Body: m.Body, Pinned: m.Pinned})
		if err != nil {
			return err
		}
		if m.Pinned {
			return db.Pin(rec.ID, true)
		}
		return nil
	}
	rec, err := db.Get(m.ID)
	if err != nil {
		return err
	}
	rec.Title, rec.Body = m.Title, m.Body
	if m.Category != "" {
		rec.Category = m.Category
	}
	if err := db.Update(rec); err != nil {
		return err
	}
	return db.Pin(rec.ID, m.Pinned)
}

// DeleteMemory removes a memory from whichever store holds it.
func (b *scribeBackend) DeleteMemory(id string) error {
	a := b.current()
	if a.Memory == nil {
		return errors.New("memory is off for this session")
	}
	for _, db := range a.Memory.DBs() {
		if _, err := db.Get(id); err == nil {
			return db.Delete(id)
		}
	}
	return fmt.Errorf("no memory %q", id)
}

// gitRun runs git in dir and returns its combined output.
func gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := osexec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// Git implements tui.Backend.
func (b *scribeBackend) Git() tui.GitStatus {
	wd := b.current().Workdir
	branch, dirty, ahead, behind, err := exec.Status(wd)
	if err != nil {
		return tui.GitStatus{}
	}
	changed, _ := exec.ChangedFiles(wd)
	return tui.GitStatus{Branch: branch, Dirty: dirty, Ahead: ahead, Behind: behind, Changed: changed}
}

// maxDiffChars caps diffs shown in the UI and sent to a model.
const maxDiffChars = 20000

// GitDiff returns the unstaged and the staged diff.
func (b *scribeBackend) GitDiff() string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	wd := b.current().Workdir
	unstaged, _ := gitRun(ctx, wd, "diff", "--no-color")
	staged, _ := gitRun(ctx, wd, "diff", "--cached", "--no-color")
	switch {
	case staged == "":
		return unstaged
	case unstaged == "":
		return staged
	}
	return staged + "\n" + unstaged
}

// GitCommit stages every change (git add -A) and commits with message.
func (b *scribeBackend) GitCommit(message string) error {
	return b.GitCommitWith(message, true)
}

const commitSystem = `You write git commit messages. Given a diff, reply with a single conventional commit subject line (type(scope): summary, under 72 characters, imperative mood), then a blank line and a short body of at most five bullet points only if the change needs explaining. Plain text only: no code fences, no preamble, no quotes.`

// SuggestCommitMessage asks the title model (else the current model) for a commit message.
func (b *scribeBackend) SuggestCommitMessage(ctx context.Context) (string, error) {
	a := b.current()
	wd := a.Workdir
	staged, _ := gitRun(ctx, wd, "diff", "--cached", "--no-color")
	unstaged, _ := gitRun(ctx, wd, "diff", "--no-color")
	diff := strings.TrimSpace(staged + "\n" + unstaged)
	if diff == "" {
		return "", errors.New("there are no changes to describe")
	}
	diff = truncateRunes(diff, maxDiffChars)

	m := a.Agent.CurrentModel()
	if ref := a.Cfg.Router.Title; ref != "" {
		if tm, err := a.resolve(ref); err == nil {
			m = tm
		}
	}
	req := llm.Request{
		Model:     m.ID,
		System:    commitSystem,
		Messages:  []llm.Message{llm.UserText("Diff:\n\n" + diff)},
		MaxTokens: 300,
	}
	text, err := agent.Collect(ctx, m.Provider, req)
	if err != nil {
		return "", err
	}
	return cleanCommitText(text), nil
}

// cleanCommitText drops code fences and surrounding whitespace a model adds anyway.
func cleanCommitText(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		keep = append(keep, strings.TrimRight(line, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// Servers implements tui.Backend. Language servers are not listed yet: the
// lsp manager does not expose its sessions.
func (b *scribeBackend) Servers() []tui.ServerStatus {
	a := b.current()
	if a.ext.mcp == nil {
		return nil
	}
	var out []tui.ServerStatus
	for _, s := range a.ext.mcp.Status() {
		out = append(out, tui.ServerStatus{Name: s.Name, Kind: "mcp", State: string(s.State), Error: s.Error, Tools: len(s.ToolNames)})
	}
	return out
}

// RestartServer restarts an MCP server.
func (b *scribeBackend) RestartServer(name string) error {
	a := b.current()
	if a.ext.mcp == nil {
		return errors.New("no MCP servers are configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return a.ext.mcp.Restart(ctx, name)
}

// MapStats implements tui.Backend.
func (b *scribeBackend) MapStats() tui.MapStats {
	a := b.current()
	if a.ext.cmap == nil {
		return tui.MapStats{}
	}
	st := a.ext.cmap.Stats()
	b.mu.Lock()
	rebuilding := b.rebuild
	b.mu.Unlock()
	return tui.MapStats{
		Files:     st.Files,
		Symbols:   st.Symbols,
		Edges:     st.Edges,
		Languages: st.Languages,
		Building:  rebuilding || !isClosed(a.ext.mapOK),
	}
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// RebuildMap rebuilds the code map in the background.
func (b *scribeBackend) RebuildMap() {
	a := b.current()
	if a.ext.cmap == nil {
		return
	}
	b.mu.Lock()
	if b.rebuild {
		b.mu.Unlock()
		return
	}
	b.rebuild = true
	b.mu.Unlock()
	go func() {
		defer func() {
			b.mu.Lock()
			b.rebuild = false
			b.mu.Unlock()
		}()
		if a.ext.cmap.Build(context.Background(), nil) == nil {
			a.mu.Lock()
			a.codeMap = a.ext.cmap.Render(repomap.Budget(0), nil)
			a.mu.Unlock()
		}
	}()
}

// Jobs implements tui.Backend. Each job carries its last few output lines.
func (b *scribeBackend) Jobs() []tui.Job {
	a := b.current()
	if a.ext.jobs == nil {
		return nil
	}
	var out []tui.Job
	for _, ji := range a.ext.jobs.List() {
		j := tui.Job{ID: ji.ID, Command: ji.Command, Status: ji.Status, Started: ji.Start, ExitCode: ji.ExitCode}
		if job, ok := a.ext.jobs.Get(ji.ID); ok {
			lines, _, _, _, _ := job.Read(0)
			if len(lines) > 5 {
				lines = lines[len(lines)-5:]
			}
			j.Tail = lines
		}
		out = append(out, j)
	}
	return out
}

// KillJob stops a background job.
func (b *scribeBackend) KillJob(id string) error {
	a := b.current()
	if a.ext.jobs == nil {
		return errors.New("no background jobs")
	}
	return a.ext.jobs.Kill(id)
}

// Skills implements tui.Backend.
func (b *scribeBackend) Skills() []string {
	a := b.current()
	if a.Skills == nil {
		return nil
	}
	out := make([]string, 0, len(a.Skills.Skills))
	for _, s := range a.Skills.Skills {
		out = append(out, s.Name)
	}
	return out
}

// Tools implements tui.Backend. MCP tools are named mcp__<server>__<tool>.
func (b *scribeBackend) Tools() []tui.ToolInfo {
	a := b.current()
	a.mu.Lock()
	disabled := map[string]bool{}
	for _, n := range a.Cfg.DisabledTools {
		disabled[n] = true
	}
	a.mu.Unlock()
	var out []tui.ToolInfo
	for _, n := range a.Tools.SortedNames() {
		t, ok := a.Tools.Get(n)
		if !ok {
			continue
		}
		info := tui.ToolInfo{
			Name:        n,
			Description: t.Spec().Description,
			Enabled:     !disabled[n],
			ReadOnly:    t.ReadOnly(),
			Source:      "builtin",
		}
		if parts := strings.SplitN(n, "__", 3); len(parts) == 3 && parts[0] == "mcp" {
			info.Source = "mcp:" + parts[1]
		}
		out = append(out, info)
	}
	return out
}

// SetToolEnabled turns a tool on or off for this project and for the running session.
func (b *scribeBackend) SetToolEnabled(name string, on bool) {
	a := b.current()
	path := config.ProjectPath(a.Workdir)
	raw, err := loadConfigFile(path)
	if err != nil {
		return
	}
	raw.DisabledTools = withoutName(raw.DisabledTools, name)
	if !on {
		raw.DisabledTools = append(raw.DisabledTools, name)
	}
	if err := config.SaveProject(a.Workdir, raw); err != nil {
		return
	}
	merged, err := config.Load(a.Workdir)
	if err != nil {
		return
	}
	a.mu.Lock()
	a.Cfg.DisabledTools = merged.DisabledTools
	a.mu.Unlock()
	a.Agent.SetActive(a.activeTools())
}

func withoutName(list []string, name string) []string {
	out := list[:0:0]
	for _, n := range list {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

// Config implements tui.Backend. It is the merged settings as a flat map.
func (b *scribeBackend) Config() map[string]any {
	a := b.current()
	a.mu.Lock()
	c := a.Cfg
	a.mu.Unlock()
	chat := c.ChatStyle
	if chat == "" {
		chat = "accent"
	}
	compactAt := c.CompactAt
	if compactAt == 0 {
		compactAt = 0.8
	}
	return map[string]any{
		"allow":             c.Allow,
		"deny":              c.Deny,
		"model":             a.Agent.CurrentModel().Ref,
		"mode":              a.Agent.Mode(),
		"theme":             c.Theme,
		"reasoning":         b.Reasoning(),
		"max_tokens":        c.MaxTokens,
		"nerd_font":         config.Bool(c.NerdFont, false),
		"show_reasoning":    config.Bool(c.ShowReasoning, true),
		"verbose":           config.Bool(c.Verbose, false),
		"chat_style":        chat,
		"vim":               config.Bool(c.Vim, false),
		"repo_map":          config.Bool(c.RepoMap, true),
		"auto_compact":      config.Bool(c.AutoCompact, true),
		"compact_at":        compactAt,
		"memory":            config.Bool(c.Memory, true),
		"checkpoints":       config.Bool(c.Checkpoints, true),
		"search_url":        c.SearchURL,
		"instructions":      c.Instructions,
		"router.explore":    c.Router.Explore,
		"router.code":       c.Router.Code,
		"router.web_search": c.Router.WebSearch,
		"router.compact":    c.Router.Compact,
		"router.title":      c.Router.Title,
		"router.fallback":   c.Router.Fallback,
		"router.max_agents": c.Router.MaxAgents,
	}
}

// SetConfig writes one setting to the global or the project config file and
// applies it now where that is possible. Other settings apply to the next session.
func (b *scribeBackend) SetConfig(key string, value any, project bool) error {
	a := b.current()
	path := config.GlobalPath()
	if project {
		path = config.ProjectPath(a.Workdir)
	}
	raw, err := loadConfigFile(path)
	if err != nil {
		return err
	}
	if key == "provider.add" {
		if err := b.checkProviderAdd(value); err != nil {
			return err
		}
	}
	if err := setConfigField(&raw, key, value); err != nil {
		return err
	}
	if project {
		err = config.SaveProject(a.Workdir, raw)
	} else {
		err = config.SaveGlobal(raw)
	}
	if err != nil {
		return err
	}
	merged, err := config.Load(a.Workdir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Cfg = merged
	if key == "provider.add" {
		// The registry is built once from the config; rebuild it so the new
		// provider takes keys and models straight away.
		a.Registry = registry.New(merged)
	}
	a.mu.Unlock()
	switch key {
	case "model":
		return b.SetModel(toString(value))
	case "mode":
		b.SetMode(fmt.Sprint(value))
	case "reasoning":
		b.SetReasoning(fmt.Sprint(value))
	}
	return nil
}

// Theme implements tui.Backend.
func (b *scribeBackend) Theme() string {
	a := b.current()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Cfg.Theme
}

// SetTheme saves the theme to the global config.
func (b *scribeBackend) SetTheme(name string) {
	a := b.current()
	raw, err := loadConfigFile(config.GlobalPath())
	if err != nil {
		return
	}
	raw.Theme = name
	if err := config.SaveGlobal(raw); err != nil {
		return
	}
	a.mu.Lock()
	a.Cfg.Theme = name
	a.mu.Unlock()
}

// Shell runs command with bash in the workdir, for the user's `!command`.
// Output is capped; a non-zero exit is not an error, the code is returned.
func (b *scribeBackend) Shell(ctx context.Context, command string) (string, int, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", 0, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(ctx, "bash", "-c", command) // not a login shell: keep the inherited PATH
	cmd.Dir = b.current().Workdir
	cmd.Env = exec.ScrubEnv(os.Environ()) // same credential scrubbing as the shell tool
	cmd.WaitDelay = 2 * time.Second
	var buf limitBuffer
	buf.max = 1 << 20
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := capOutput(buf.String(), maxDiffChars)
	if err == nil {
		return out, 0, nil
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return out, ee.ExitCode(), nil
	}
	if ctx.Err() != nil {
		return out, -1, fmt.Errorf("timed out after 120s")
	}
	return out, -1, err
}

// limitBuffer keeps the first max bytes written and discards the rest.
type limitBuffer struct {
	bytes.Buffer
	max int
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		if len(p) > room {
			l.Buffer.Write(p[:room])
		} else {
			l.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// capOutput keeps the head and tail of s when it is longer than n bytes.
func capOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := n / 2
	return s[:half] + "\n… output truncated …\n" + s[len(s)-half:]
}

// truncateRunes shortens s to at most n runes, adding an ellipsis when cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

const fileListTTL = 10 * time.Second

// maxWalkFiles bounds the fallback walk for folders that are not git repositories.
const maxWalkFiles = 50000

// FileCandidates implements tui.Backend. It fuzzy-matches workdir-relative paths.
func (b *scribeBackend) FileCandidates(query string, limit int) []string {
	if limit <= 0 {
		limit = 20
	}
	files := b.fileList()
	q := strings.ToLower(strings.TrimSpace(query))
	type hit struct {
		path  string
		score int
	}
	var hits []hit
	for _, f := range files {
		if s, ok := fuzzyPath(q, f); ok {
			hits = append(hits, hit{f, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if len(hits[i].path) != len(hits[j].path) {
			return len(hits[i].path) < len(hits[j].path)
		}
		return hits[i].path < hits[j].path
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.path)
	}
	return out
}

// fuzzyPath scores path against q (lowercase). Every query byte must appear in
// order. Matches at the start of the basename, after a separator or in a run
// score higher, and long paths score lower.
func fuzzyPath(q, path string) (int, bool) {
	if q == "" {
		return 0, true
	}
	p := strings.ToLower(path)
	score := 0
	if base := p[strings.LastIndexByte(p, '/')+1:]; strings.Contains(base, q) {
		score += 100
	}
	qi, prev := 0, -2
	for i := 0; i < len(p) && qi < len(q); i++ {
		if p[i] != q[qi] {
			continue
		}
		s := 1
		if i == prev+1 {
			s += 5
		}
		if i == 0 || strings.IndexByte("/_-. ", p[i-1]) >= 0 {
			s += 4
		}
		score += s
		prev = i
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	return score - len(p)/10, true
}

// fileList returns the project's files (tracked and untracked, minus ignored),
// cached for a few seconds.
func (b *scribeBackend) fileList() []string {
	b.fmu.Lock()
	defer b.fmu.Unlock()
	if b.files != nil && time.Since(b.filesAt) < fileListTTL {
		return b.files
	}
	wd := b.current().Workdir
	var files []string
	if out, err := osexec.Command("git", "-C", wd, "ls-files", "-co", "--exclude-standard", "-z").Output(); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	} else {
		files = walkProject(wd)
	}
	sort.Strings(files)
	b.files, b.filesAt = files, time.Now()
	return files
}

// walkProject lists files under root, skipping VCS and dependency folders.
func walkProject(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxWalkFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return files
}

// Close ends the backend and its App.
func (b *scribeBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.queued = nil
	a := b.app
	b.mu.Unlock()
	return a.Close()
}

// loadConfigFile reads one config file without merging the global config in.
// A missing file is an empty config.
func loadConfigFile(path string) (config.Config, error) {
	var c config.Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// setConfigField sets one flat setting key on c. Values may come from the
// settings screen as strings, so each is parsed for its field's type.
func setConfigField(c *config.Config, key string, v any) error {
	switch key {
	case "model":
		c.Model = toString(v)
	case "mode":
		s := toString(v)
		if !knownMode(s) {
			return fmt.Errorf("unknown mode %q (use one of: %s)", s, strings.Join(agent.Modes, ", "))
		}
		c.Mode = s
	case "theme":
		c.Theme = toString(v)
	case "reasoning":
		s := toString(v)
		switch s {
		case "", "low", "medium", "high":
		default:
			return fmt.Errorf("reasoning must be empty, low, medium or high")
		}
		c.Reasoning = s
	case "max_tokens":
		n, err := toInt(v)
		if err != nil {
			return err
		}
		c.MaxTokens = n
	case "compact_at":
		f, err := toFloat(v)
		if err != nil {
			return err
		}
		c.CompactAt = f
	case "chat_style":
		s := toString(v)
		if s != "accent" && s != "bubble" {
			return fmt.Errorf("chat_style must be accent or bubble")
		}
		c.ChatStyle = s
	case "search_url":
		c.SearchURL = toString(v)
	case "allow":
		c.Allow = toStrings(v)
	case "deny":
		c.Deny = toStrings(v)
	case "provider.add":
		p, err := providerFromSetting(v)
		if err != nil {
			return err
		}
		upsertProvider(c, p)
	case "instructions":
		c.Instructions = toStrings(v)
	case "router.explore":
		c.Router.Explore = toString(v)
	case "router.code":
		c.Router.Code = toString(v)
	case "router.web_search":
		c.Router.WebSearch = toString(v)
	case "router.compact":
		c.Router.Compact = toString(v)
	case "router.title":
		c.Router.Title = toString(v)
	case "router.fallback":
		c.Router.Fallback = toStrings(v)
	case "router.max_agents":
		n, err := toInt(v)
		if err != nil {
			return err
		}
		c.Router.MaxAgents = n
	default:
		if ok, err := c.SetReasoningField(key, v); ok {
			return err
		}
		ptr := boolField(c, key)
		if ptr == nil {
			return fmt.Errorf("unknown setting %q", key)
		}
		bv, err := toBool(v)
		if err != nil {
			return err
		}
		*ptr = &bv
	}
	return nil
}

// providerFromSetting reads a provider from the flat settings value: a map with
// id, base_url and optionally name, api_key_env.
func providerFromSetting(v any) (config.Provider, error) {
	var p config.Provider
	m, ok := v.(map[string]any)
	if !ok {
		return p, fmt.Errorf("provider.add expects id, base_url and api_key_env")
	}
	p.ID = toString(m["id"])
	p.Name = toString(m["name"])
	p.BaseURL = toString(m["base_url"])
	p.APIKeyEnv = toString(m["api_key_env"])
	if p.ID == "" || strings.ContainsAny(p.ID, " /\\") {
		return p, fmt.Errorf("provider id must be a short name without spaces or slashes")
	}
	if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
		return p, fmt.Errorf("base URL must start with http:// or https://")
	}
	return p, nil
}

// upsertProvider replaces the provider with the same id, or appends it.
func upsertProvider(c *config.Config, p config.Provider) {
	for i := range c.Providers {
		if c.Providers[i].ID == p.ID {
			c.Providers[i] = p
			return
		}
	}
	c.Providers = append(c.Providers, p)
}

// checkProviderAdd refuses an id that belongs to a built-in provider.
func (b *scribeBackend) checkProviderAdd(v any) error {
	p, err := providerFromSetting(v)
	if err != nil {
		return err
	}
	if info, ok := b.current().Registry.Lookup(p.ID); ok && !info.Custom {
		return fmt.Errorf("%q is a built-in provider; choose another id", p.ID)
	}
	return nil
}

// boolField returns the pointer for a bool setting, or nil for other keys.
func boolField(c *config.Config, key string) **bool {
	switch key {
	case "nerd_font":
		return &c.NerdFont
	case "show_reasoning":
		return &c.ShowReasoning
	case "verbose":
		return &c.Verbose
	case "vim":
		return &c.Vim
	case "repo_map":
		return &c.RepoMap
	case "auto_compact":
		return &c.AutoCompact
	case "memory":
		return &c.Memory
	case "checkpoints":
		return &c.Checkpoints
	}
	return nil
}

func knownMode(m string) bool {
	for _, k := range agent.Modes {
		if k == m {
			return true
		}
	}
	return false
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "on", "yes", "1":
			return true, nil
		case "false", "off", "no", "0":
			return false, nil
		}
	}
	return false, fmt.Errorf("expected true or false, got %v", v)
}

func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case float64:
		return int(x), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("expected a whole number, got %v", v)
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("expected a number, got %v", v)
}

// toStrings accepts a list or a comma-separated string.
func toStrings(v any) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	switch x := v.(type) {
	case []string:
		for _, s := range x {
			add(s)
		}
	case []any:
		for _, s := range x {
			add(fmt.Sprint(s))
		}
	case string:
		for _, s := range strings.Split(x, ",") {
			add(s)
		}
	}
	return out
}
