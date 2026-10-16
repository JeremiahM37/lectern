package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// panelBackend serves fixed panel data and records cancellations.
type panelBackend struct {
	*FakeBackend
	model     []agent.Event
	comp      []agent.Event
	subs      []agent.AgentInfo
	cancelled []string
}

func (p *panelBackend) ModelEvents() []agent.Event   { return p.model }
func (p *panelBackend) CompactionLog() []agent.Event { return p.comp }
func (p *panelBackend) Subagents() []agent.AgentInfo { return p.subs }
func (p *panelBackend) CancelSubagent(id string) error {
	for _, s := range p.subs {
		if s.ID == id && (s.Status == "running" || s.Status == "pending") {
			p.cancelled = append(p.cancelled, id)
			return nil
		}
	}
	return errTestNoSub
}

var errTestNoSub = errors.New("no subagent in the newest dispatch")

func stepFinish(model string, step int, in, out int, d time.Duration) agent.Event {
	return agent.Event{Kind: agent.EvStepFinish, Model: model, Step: step,
		Usage: llm.Usage{InputTokens: in, OutputTokens: out}, Duration: d, At: time.Now()}
}

func panelModel(t *testing.T, be *panelBackend) *Model {
	t.Helper()
	be.FakeBackend = NewFakeBackend()
	be.FakeBackend.Delay = 0
	return modalModel(t, 120, 36, be)
}

// Recording starts off; events recorded after e turns it on, and the
// Models tab groups them by model with average and last durations.
func TestModelEventsRecordingAndModelsTab(t *testing.T) {
	be := &panelBackend{model: []agent.Event{stepFinish("ollama/old", 1, 10, 10, time.Second)}}
	m := panelModel(t, be)
	m.runCommand("/model-events")
	if topTitle(m) != "Model events" {
		t.Fatalf("top = %q", topTitle(m))
	}
	mustContain(t, screen(m), "Recording is off.")

	send(m, "e") // on: the calls before this point are not shown
	be.model = append(be.model,
		stepFinish("ollama/qwen", 1, 2000, 100, 800*time.Millisecond),
		stepFinish("ollama/qwen", 2, 3000, 300, 1200*time.Millisecond))
	mustContain(t, screen(m), "ollama/qwen", "1.2 s", "1.0 s")
	if strings.Contains(screen(m), "ollama/old") {
		t.Fatalf("calls before recording started must not show:\n%s", screen(m))
	}
	send(m, "e") // off clears the view
	if strings.Contains(screen(m), "ollama/qwen") {
		t.Fatalf("turning recording off should clear the log:\n%s", screen(m))
	}
}

// Errors and retries are listed with their HTTP status.
func TestModelEventsErrorsTabShowsStatusCodes(t *testing.T) {
	be := &panelBackend{}
	m := panelModel(t, be)
	m.runCommand("/model-events")
	send(m, "e")
	be.model = []agent.Event{
		stepFinish("ollama/qwen", 1, 10, 10, time.Second),
		{Kind: agent.EvRetry, Status: 429, Category: llm.CatRateLimited, Attempt: 1, MaxAttempts: 3, Text: "slow", At: time.Now()},
		{Kind: agent.EvError, Status: 503, Category: llm.CatOverloaded, Text: "down", At: time.Now()},
	}
	send(m, "right")
	send(m, "right")
	mustContain(t, screen(m), "[429]", "[503]", "retry", "error")
}

// The compaction log lists entries newest first, filters, and opens a detail page.
func TestCompactionLogFilterAndDetail(t *testing.T) {
	be := &panelBackend{comp: []agent.Event{
		{Kind: agent.EvCompactStart, Text: "manual", At: time.Now()},
		{Kind: agent.EvCompactDone, Text: "Goal: keep the parser.\nDone: two files.", At: time.Now(),
			Meta: map[string]any{"messages_before": 40, "messages_after": 9}},
		{Kind: agent.EvCompactStart, Text: "auto", At: time.Now()},
	}}
	m := panelModel(t, be)
	m.runCommand("/compact logs")
	if topTitle(m) != "Compaction log" {
		t.Fatalf("/compact logs should open the log, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "3 events", "auto-trigger", "compact")

	send(m, "down") // newest first: the cursor starts on the auto trigger; step to the done entry
	send(m, "enter")
	mustContain(t, screen(m), "Messages: 40 → 9", "── Summary ──", "Goal: keep the parser.")
	send(m, "esc") // back to the list
	mustContain(t, screen(m), "3 events")

	for _, r := range "auto" {
		send(m, string(r))
	}
	if !strings.Contains(screen(m), "1 event") {
		t.Fatalf("filter should leave the auto trigger:\n%s", screen(m))
	}
	typeText(m, "zzz")
	mustContain(t, screen(m), "no matching events")
}

// /compact with no argument still compacts; the log is only for "logs".
func TestCompactWithoutArgumentStillCompacts(t *testing.T) {
	be := &panelBackend{}
	m := panelModel(t, be)
	cmd := m.runCommand("/compact")
	if topTitle(m) == "Compaction log" {
		t.Fatalf("plain /compact must not open the log")
	}
	if cmd == nil {
		t.Fatalf("plain /compact should return a command that compacts")
	}
}

// The subagent panel lists the newest dispatch, and /cancel stops one.
func TestSubagentsPanelAndCancel(t *testing.T) {
	be := &panelBackend{subs: []agent.AgentInfo{
		{ID: "a1", Role: "explore", Status: "done", Model: "ollama/qwen", Step: 3, Tokens: llm.Usage{InputTokens: 900, OutputTokens: 100}},
		{ID: "a2", Role: "code", Status: "running", Model: "ollama/qwen", Step: 1, Last: "read_file x.go"},
	}}
	m := panelModel(t, be)
	m.runCommand("/agents")
	if topTitle(m) != "Subagents" {
		t.Fatalf("top = %q", topTitle(m))
	}
	mustContain(t, screen(m), "a1", "done", "a2", "running", "/cancel <id>")

	m.runCommand("/cancel a2")
	if len(be.cancelled) != 1 || be.cancelled[0] != "a2" {
		t.Fatalf("/cancel a2 should reach the backend, got %v", be.cancelled)
	}
	if got := flashText(m.runCommand("/cancel")); !strings.Contains(got, "Usage: /cancel <id>") {
		t.Fatalf("/cancel with no id should explain its usage, got %q", got)
	}
	if got := flashText(m.runCommand("/cancel a1")); !strings.Contains(got, "Could not cancel a1") {
		t.Fatalf("a finished subagent should be refused, got %q", got)
	}
}

// flashText runs a command that only returns a flash and reads the text.
func flashText(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	if fm, ok := cmd().(flashMsg); ok {
		return fm.text
	}
	return ""
}

// With no dispatch the panel says so, and /dispatch-status opens it too.
func TestSubagentsPanelEmpty(t *testing.T) {
	m := panelModel(t, &panelBackend{})
	m.runCommand("/dispatch-status")
	mustContain(t, screen(m), "No dispatch has run in this session.")
}

// The context bar marks an estimated count with a tilde.
func TestContextBarMarksEstimate(t *testing.T) {
	m := panelModel(t, &panelBackend{})
	tb := m.cur()
	tb.ctxUsed, tb.ctxWin = 84000, 200000
	tb.ctxShown = 42
	tb.ctxEst = true
	_, nums := m.contextParts(tb)
	if got := ansi.Strip(nums); got != "~84k/200k" {
		t.Fatalf("estimated token label = %q, want ~84k/200k", got)
	}
	bar, _ := m.contextParts(tb)
	if !strings.Contains(ansi.Strip(bar), "~42%") {
		t.Fatalf("estimated percent should carry a tilde: %q", ansi.Strip(bar))
	}
	tb.ctxEst = false
	bar, nums = m.contextParts(tb)
	if strings.Contains(ansi.Strip(bar+nums), "~") {
		t.Fatalf("a reported count must not be marked estimated")
	}
}
