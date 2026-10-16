package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// FakeBackend is a scripted Backend. It streams a realistic turn (reasoning,
// reads, a grep, an edit with a real diff, a shell run behind an approval
// prompt, a subagent dispatch) and keeps just enough state for every screen to
// work. It is used by the tests and by `lectern code --demo`.
type FakeBackend struct {
	mu sync.Mutex

	// Delay is the pause between scripted events (0 for tests).
	Delay time.Duration

	events  chan agent.Event
	prompts chan Prompt
	running bool
	abort   chan struct{}

	transcript []agent.Event
	steerQ     []string
	turns      int

	model     string
	mode      string
	reasoning string
	theme     string
	keys      map[string]string
	sessionID string
	title     string
	workdir   string
	cfg       map[string]any
	toolOn    map[string]bool

	usage     llm.Usage
	cost      float64
	ctxUsed   int
	ctxWindow int

	sessions    []SessionInfo
	checkpoints []CheckpointInfo
	files       []string
}

var _ Backend = (*FakeBackend)(nil)

// NewFakeBackend returns a FakeBackend with a small canned project.
func NewFakeBackend() *FakeBackend {
	f := &FakeBackend{
		Delay:     30 * time.Millisecond,
		events:    make(chan agent.Event, 256),
		prompts:   make(chan Prompt, 8),
		model:     "anthropic/claude-sonnet-5",
		mode:      "default",
		reasoning: "medium",
		theme:     "scribe",
		keys:      map[string]string{"anthropic": "set"},
		sessionID: "7f3c9a21b0e44d6f",
		title:     "Refactor the parser",
		workdir:   "/home/admin/projects/example",
		cfg:       map[string]any{"verbose": false, "show_reasoning": true, "max_steps": 40, "theme": "scribe"},
		toolOn:    map[string]bool{"read": true, "grep": true, "shell": true, "edit_file": true},
		ctxWindow: 200000,
		abort:     make(chan struct{}),
		files:     []string{"internal/parser/parse.go", "internal/parser/parse_test.go", "cmd/app/main.go", "README.md", "go.mod", "internal/lexer/lexer.go"},
		sessions: []SessionInfo{
			{ID: "7f3c9a21b0e44d6f", Title: "Refactor the parser", Model: "anthropic/claude-sonnet-5", Turns: 12, CostUSD: 0.41, Updated: time.Now().Add(-10 * time.Minute), Preview: "the parser drops trailing commas, can you fix it"},
			{ID: "a0b1c2d3e4f56789", Title: "Add Redis caching", Model: "openai/gpt-6", Turns: 4, CostUSD: 0.09, Updated: time.Now().Add(-26 * time.Hour), Preview: "add a cache in front of the user lookup"},
		},
		checkpoints: []CheckpointInfo{
			{Turn: 1, Prompt: "explain the lexer", Created: time.Now().Add(-time.Hour)},
			{Turn: 2, Prompt: "fix trailing commas", Files: []string{"internal/parser/parse.go"}, Created: time.Now().Add(-50 * time.Minute)},
			{Turn: 3, Prompt: "add tests", Files: []string{"internal/parser/parse_test.go"}, Created: time.Now().Add(-40 * time.Minute)},
		},
	}
	return f
}

// emit records an event for the transcript and sends it to the UI.
func (f *FakeBackend) emit(ev agent.Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	f.mu.Lock()
	f.transcript = append(f.transcript, ev)
	f.mu.Unlock()
	f.events <- ev
}

func (f *FakeBackend) pause() {
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
}

// Events implements Backend.
func (f *FakeBackend) Events() <-chan agent.Event { return f.events }

// Prompts implements Backend.
func (f *FakeBackend) Prompts() <-chan Prompt { return f.prompts }

// Submit starts a scripted turn.
func (f *FakeBackend) Submit(prompt string, images []llm.Part) {
	f.mu.Lock()
	f.running = true
	f.turns++
	turn := f.turns
	f.abort = make(chan struct{})
	f.mu.Unlock()
	go f.runTurn(turn, prompt)
}

// Steer queues a message for the running turn.
func (f *FakeBackend) Steer(msg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return false
	}
	f.steerQ = append(f.steerQ, msg)
	return true
}

// Abort stops the running turn.
func (f *FakeBackend) Abort() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		select {
		case <-f.abort:
		default:
			close(f.abort)
		}
	}
}

// Running implements Backend.
func (f *FakeBackend) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *FakeBackend) runTurn(turn int, prompt string) {
	now := time.Now()
	f.emit(agent.Event{Kind: agent.EvTurnStart, Prompt: prompt, Model: f.Model(), Mode: f.Mode()})
	aborted := func() bool {
		select {
		case <-f.abort:
			return true
		default:
			return false
		}
	}
	stats := &agent.TurnStats{Steps: 2}
	var answer strings.Builder
	var edited []string

	step := func(n int) { f.emit(agent.Event{Kind: agent.EvStepStart, Step: n}) }
	text := func(s string) {
		answer.WriteString(s)
		f.emit(agent.Event{Kind: agent.EvText, Text: s})
		f.pause()
	}
	tool := func(id, name string, args any, output, summary, diff string, files []string, dur time.Duration) {
		raw, _ := json.Marshal(args)
		f.emit(agent.Event{Kind: agent.EvToolStart, CallID: id, Tool: name})
		f.emit(agent.Event{Kind: agent.EvToolArgs, CallID: id, Text: string(raw)})
		f.emit(agent.Event{Kind: agent.EvToolRun, CallID: id, Tool: name, Args: raw})
		f.pause()
		f.emit(agent.Event{Kind: agent.EvToolResult, CallID: id, Tool: name, Duration: dur,
			Result: &tools.Result{Output: output, Summary: summary, Diff: diff, Files: files}})
		stats.ToolCalls = append(stats.ToolCalls, name)
		f.pause()
	}

	step(1)
	for _, chunk := range []string{"The user wants the parser to ", "accept trailing commas. ", "I should read the parser and find where ", "list items are split."} {
		f.emit(agent.Event{Kind: agent.EvReasoning, Text: chunk})
		f.pause()
	}
	text("I'll read the parser first. ")
	tool("c1", "read", map[string]any{"path": "internal/parser/parse.go"},
		"package parser\n\nfunc ParseList(src string) ([]string, error) { ... }\n", "128 lines", "", nil, 14*time.Millisecond)
	tool("c2", "grep", map[string]any{"pattern": "ParseList", "path": "."},
		"internal/parser/parse.go:12: func ParseList\ncmd/app/main.go:31: parser.ParseList(src)\n", "2 matches in 2 files", "", nil, 38*time.Millisecond)
	if aborted() {
		f.finish(turn, stats, &answer, edited, now, true)
		return
	}
	text("The split does not skip an empty last item. I'll fix that. ")

	// approval for a shell command
	reply := make(chan PromptReply, 1)
	f.prompts <- Prompt{
		Approval: &tools.Approval{Tool: "shell", Kind: "shell", Title: "Run shell command", Detail: "go test ./internal/parser/..."},
		Reply:    reply,
	}
	select {
	case r := <-reply:
		if r.Decision == tools.Deny {
			text("Okay, I won't run the tests. ")
		}
	case <-f.abort:
		f.finish(turn, stats, &answer, edited, now, true)
		return
	}

	diff := "--- a/internal/parser/parse.go\n+++ b/internal/parser/parse.go\n@@ -10,7 +10,9 @@ func ParseList(src string) ([]string, error) {\n \tfor _, part := range strings.Split(src, \",\") {\n-\t\titems = append(items, strings.TrimSpace(part))\n+\t\tp := strings.TrimSpace(part)\n+\t\tif p == \"\" {\n+\t\t\tcontinue\n+\t\t}\n+\t\titems = append(items, p)\n \t}\n"
	tool("c3", "edit_file", map[string]any{"path": "internal/parser/parse.go"}, "edited internal/parser/parse.go", "+4 -1", diff,
		[]string{"internal/parser/parse.go"}, 9*time.Millisecond)
	edited = append(edited, "internal/parser/parse.go")

	f.emit(agent.Event{Kind: agent.EvToolStart, CallID: "c4", Tool: "shell"})
	f.emit(agent.Event{Kind: agent.EvToolRun, CallID: "c4", Tool: "shell", Args: json.RawMessage(`{"command":"go test ./internal/parser/..."}`)})
	for _, line := range []string{"ok  \texample/internal/parser\t0.021s", "--- PASS: TestParseTrailingComma (0.00s)", "PASS"} {
		f.emit(agent.Event{Kind: agent.EvToolProgress, CallID: "c4", Text: line})
		f.pause()
	}
	f.emit(agent.Event{Kind: agent.EvToolResult, CallID: "c4", Tool: "shell", Duration: 2100 * time.Millisecond,
		Result: &tools.Result{Output: "ok  example/internal/parser 0.021s", Summary: "exit 0"}})
	stats.ToolCalls = append(stats.ToolCalls, "shell")

	// subagent dispatch
	f.emit(agent.Event{Kind: agent.EvSubagent, Agent: &agent.AgentInfo{ID: "a1", Task: "find other callers of ParseList", Role: "explore", Status: "running", Model: f.Model(), Step: 1}})
	f.emit(agent.Event{Kind: agent.EvToolStart, AgentID: "a1", CallID: "s1", Tool: "grep"})
	f.emit(agent.Event{Kind: agent.EvToolRun, AgentID: "a1", CallID: "s1", Tool: "grep", Args: json.RawMessage(`{"pattern":"ParseList"}`)})
	f.pause()
	f.emit(agent.Event{Kind: agent.EvToolResult, AgentID: "a1", CallID: "s1", Tool: "grep", Result: &tools.Result{Output: "no other callers", Summary: "0 matches"}})
	f.emit(agent.Event{Kind: agent.EvSubagent, Agent: &agent.AgentInfo{ID: "a1", Task: "find other callers of ParseList", Role: "explore", Status: "done", Model: f.Model(), Step: 2, Last: "no other callers"}})

	step(2)
	text("Done. Empty items are now skipped, and the test suite passes.")
	f.emit(agent.Event{Kind: agent.EvStepFinish, Step: 2, Usage: llm.Usage{InputTokens: 9120, OutputTokens: 412}, Total: llm.Usage{InputTokens: 14210, OutputTokens: 1180}, CostUSD: 0.021})
	f.mu.Lock()
	f.ctxUsed = 84000 + turn*4000
	f.usage.Add(llm.Usage{InputTokens: 9120, OutputTokens: 412})
	f.cost += 0.021
	f.mu.Unlock()
	f.emit(agent.Event{Kind: agent.EvContext, ContextUsed: f.ctxUsed, ContextWindow: f.ctxWindow})
	f.checkpoints = append(f.checkpoints, CheckpointInfo{Turn: turn, Prompt: prompt, Files: edited, Created: now})
	f.finish(turn, stats, &answer, edited, now, false)
}

// finish ends the turn: the steering queue is drained into the transcript and EvTurnDone is sent.
func (f *FakeBackend) finish(turn int, stats *agent.TurnStats, answer *strings.Builder, edited []string, started time.Time, aborted bool) {
	f.mu.Lock()
	f.running = false
	steer := f.steerQ
	f.steerQ = nil
	f.mu.Unlock()
	for _, s := range steer {
		f.emit(agent.Event{Kind: agent.EvSteering, Text: s})
	}
	stats.Duration = time.Since(started)
	stats.FilesEdited = edited
	stats.Aborted = aborted
	stats.CostUSD = f.cost
	f.emit(agent.Event{Kind: agent.EvTurnDone, Text: answer.String(), Stats: stats, CostUSD: f.cost})
}

// Model implements Backend.
func (f *FakeBackend) Model() string { f.mu.Lock(); defer f.mu.Unlock(); return f.model }

// SetModel implements Backend. Accepts any "provider/model" in the canned list.
func (f *FakeBackend) SetModel(ref string) error {
	if !strings.Contains(ref, "/") {
		return fmt.Errorf("model must look like provider/model, got %q", ref)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.model = ref
	return nil
}

// Models implements Backend.
func (f *FakeBackend) Models(ctx context.Context, provider string) ([]llm.ModelInfo, error) {
	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	switch provider {
	case "anthropic":
		return []llm.ModelInfo{{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", ContextWindow: 200000}, {ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5", ContextWindow: 200000}}, nil
	case "openai":
		return []llm.ModelInfo{{ID: "gpt-6", Name: "GPT-6", ContextWindow: 400000}}, nil
	case "ollama":
		return []llm.ModelInfo{{ID: "qwen3.6:35b-a3b", Name: "qwen3.6 35B-A3B", ContextWindow: 16384}}, nil
	}
	return nil, errors.New("models unavailable for " + provider)
}

// Providers implements Backend.
func (f *FakeBackend) Providers() []ProviderStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []ProviderStatus{
		{ID: "anthropic", Name: "Anthropic", HasKey: f.keys["anthropic"] != "", Icon: "A"},
		{ID: "openai", Name: "OpenAI", HasKey: f.keys["openai"] != "", Icon: "O"},
		{ID: "ollama", Name: "Ollama", Local: true, HasKey: true, Icon: "◆"},
	}
}

// SetKey implements Backend.
func (f *FakeBackend) SetKey(provider, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("empty key")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[provider] = key
	return nil
}

// Workdir implements Backend.
func (f *FakeBackend) Workdir() string { return f.workdir }

// Version implements Backend.
func (f *FakeBackend) Version() string { return "0.9.0" }

// SessionID implements Backend.
func (f *FakeBackend) SessionID() string { f.mu.Lock(); defer f.mu.Unlock(); return f.sessionID }

// SessionTitle implements Backend.
func (f *FakeBackend) SessionTitle() string { f.mu.Lock(); defer f.mu.Unlock(); return f.title }

// Transcript implements Backend.
func (f *FakeBackend) Transcript() []agent.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Event(nil), f.transcript...)
}

// Mode implements Backend.
func (f *FakeBackend) Mode() string { f.mu.Lock(); defer f.mu.Unlock(); return f.mode }

// SetMode implements Backend.
func (f *FakeBackend) SetMode(mode string) { f.mu.Lock(); defer f.mu.Unlock(); f.mode = mode }

// Reasoning implements Backend.
func (f *FakeBackend) Reasoning() string { f.mu.Lock(); defer f.mu.Unlock(); return f.reasoning }

// SetReasoning implements Backend.
func (f *FakeBackend) SetReasoning(level string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasoning = level
}

// Usage implements Backend.
func (f *FakeBackend) Usage() (llm.Usage, float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage, f.cost
}

// Context implements Backend.
func (f *FakeBackend) Context() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxUsed, f.ctxWindow
}

// Compact implements Backend.
func (f *FakeBackend) Compact(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxUsed /= 3
	return nil
}

// NewSession implements Backend.
func (f *FakeBackend) NewSession() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionID = fmt.Sprintf("%016x", time.Now().UnixNano())
	f.title = "New session"
	f.transcript = nil
	f.turns = 0
	f.checkpoints = nil
	f.usage = llm.Usage{}
	f.cost = 0
	f.ctxUsed = 0
	return nil
}

// Sessions implements Backend.
func (f *FakeBackend) Sessions() []SessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SessionInfo(nil), f.sessions...)
}

// Resume implements Backend.
func (f *FakeBackend) Resume(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == id || strings.HasPrefix(s.ID, id) {
			f.sessionID = s.ID
			f.title = s.Title
			f.model = s.Model
			return nil
		}
	}
	return fmt.Errorf("no session %q", id)
}

// Fork implements Backend.
func (f *FakeBackend) Fork() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionID = f.sessionID + "-fork"
	return f.sessionID, nil
}

// Rename implements Backend.
func (f *FakeBackend) Rename(title string) error {
	if strings.TrimSpace(title) == "" {
		return errors.New("empty title")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.title = title
	return nil
}

// RenameSession implements SessionRenamer.
func (f *FakeBackend) RenameSession(id, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.sessions {
		if f.sessions[i].ID == id {
			f.sessions[i].Title = title
			return nil
		}
	}
	return fmt.Errorf("no session %q", id)
}

// DeleteSession implements SessionDeleter.
func (f *FakeBackend) DeleteSession(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.sessions {
		if f.sessions[i].ID == id {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("no session %q", id)
}

// Export implements Backend.
func (f *FakeBackend) Export(format string) (string, error) {
	if format != "markdown" && format != "json" {
		return "", fmt.Errorf("unknown format %q", format)
	}
	return "scribe-export." + map[string]string{"markdown": "md", "json": "json"}[format], nil
}

// Checkpoints implements Backend.
func (f *FakeBackend) Checkpoints() []CheckpointInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CheckpointInfo(nil), f.checkpoints...)
}

// Undo implements Backend.
func (f *FakeBackend) Undo() (CheckpointInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.checkpoints) - 1; i >= 0; i-- {
		if !f.checkpoints[i].Undone {
			f.checkpoints[i].Undone = true
			return f.checkpoints[i], nil
		}
	}
	return CheckpointInfo{}, errors.New("nothing to undo")
}

// Redo implements Backend.
func (f *FakeBackend) Redo() (CheckpointInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.checkpoints {
		if f.checkpoints[i].Undone {
			f.checkpoints[i].Undone = false
			return f.checkpoints[i], nil
		}
	}
	return CheckpointInfo{}, errors.New("nothing to redo")
}

// RestoreTo implements Backend.
func (f *FakeBackend) RestoreTo(turn int) error { return nil }

// TurnDiff implements Backend.
func (f *FakeBackend) TurnDiff(turn int) string {
	return fmt.Sprintf("turn %d changed %d files", turn, len(f.Checkpoints()))
}

// Memories implements Backend.
func (f *FakeBackend) Memories(query string) []MemoryItem {
	return []MemoryItem{{ID: "m1", Scope: "project", Category: "convention", Title: "Tests live next to the code", Body: "Keep _test.go files beside the package they test.", Pinned: true}}
}

// SaveMemory implements Backend.
func (f *FakeBackend) SaveMemory(m MemoryItem) error { return nil }

// DeleteMemory implements Backend.
func (f *FakeBackend) DeleteMemory(id string) error { return nil }

// Git implements Backend.
func (f *FakeBackend) Git() GitStatus {
	return GitStatus{Branch: "feature/parser", Dirty: true, Ahead: 1, Changed: []string{"internal/parser/parse.go"}}
}

// GitDiff implements Backend.
func (f *FakeBackend) GitDiff() string {
	return "diff --git a/internal/parser/parse.go b/internal/parser/parse.go\n@@ -10,7 +10,9 @@\n-\t\titems = append(items, strings.TrimSpace(part))\n+\t\tp := strings.TrimSpace(part)\n"
}

// GitCommit implements Backend.
func (f *FakeBackend) GitCommit(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("empty message")
	}
	return nil
}

// SuggestCommitMessage implements Backend.
func (f *FakeBackend) SuggestCommitMessage(ctx context.Context) (string, error) {
	return "parser: skip empty list items", nil
}

// Servers implements Backend.
func (f *FakeBackend) Servers() []ServerStatus {
	return []ServerStatus{
		{Name: "gopls", Kind: "lsp", State: "ready", Tools: 3},
		{Name: "github", Kind: "mcp", State: "ready", Tools: 12},
		{Name: "postgres", Kind: "mcp", State: "failed", Error: "connection refused"},
	}
}

// RestartServer implements Backend.
func (f *FakeBackend) RestartServer(name string) error { return nil }

// MapStats implements Backend.
func (f *FakeBackend) MapStats() MapStats {
	return MapStats{Files: 214, Symbols: 3920, Edges: 11840, Languages: map[string]int{"go": 180, "ts": 34}}
}

// RebuildMap implements Backend.
func (f *FakeBackend) RebuildMap() {}

// Jobs implements Backend.
func (f *FakeBackend) Jobs() []Job {
	return []Job{{ID: "j1", Command: "go test -count=1 ./...", Status: "running", Started: time.Now().Add(-40 * time.Second), Tail: []string{"ok  example/internal/lexer"}}}
}

// KillJob implements Backend.
func (f *FakeBackend) KillJob(id string) error { return nil }

// Skills implements Backend.
func (f *FakeBackend) Skills() []string {
	return []string{"release-notes", "db-migrations", "k8s-debug"}
}

// Tools implements Backend.
func (f *FakeBackend) Tools() []ToolInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []ToolInfo{
		{Name: "read", Description: "read a file", Enabled: f.toolOn["read"], ReadOnly: true, Source: "builtin"},
		{Name: "grep", Description: "search file contents", Enabled: f.toolOn["grep"], ReadOnly: true, Source: "builtin"},
		{Name: "edit_file", Description: "replace text in a file", Enabled: f.toolOn["edit_file"], Source: "builtin"},
		{Name: "shell", Description: "run a shell command", Enabled: f.toolOn["shell"], Source: "builtin"},
	}
}

// SetToolEnabled implements Backend.
func (f *FakeBackend) SetToolEnabled(name string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toolOn[name] = on
}

// Config implements Backend.
func (f *FakeBackend) Config() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]any, len(f.cfg))
	for k, v := range f.cfg {
		out[k] = v
	}
	return out
}

// SetConfig implements Backend.
func (f *FakeBackend) SetConfig(key string, value any, project bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg[key] = value
	return nil
}

// Theme implements Backend.
func (f *FakeBackend) Theme() string { f.mu.Lock(); defer f.mu.Unlock(); return f.theme }

// SetTheme implements Backend.
func (f *FakeBackend) SetTheme(name string) { f.mu.Lock(); defer f.mu.Unlock(); f.theme = name }

// Shell implements Backend. It does not run anything; it echoes the command.
func (f *FakeBackend) Shell(ctx context.Context, command string) (string, int, error) {
	return "(fake shell) " + command, 0, nil
}

// FileCandidates implements Backend.
func (f *FakeBackend) FileCandidates(query string, limit int) []string {
	var out []string
	q := strings.ToLower(query)
	for _, p := range f.files {
		if q == "" || strings.Contains(strings.ToLower(p), q) {
			out = append(out, p)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// Close implements Backend.
func (f *FakeBackend) Close() error {
	f.Abort()
	return nil
}
