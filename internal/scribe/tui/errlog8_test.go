package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func toolFail(tool, out string) agent.Event {
	return agent.Event{Kind: agent.EvToolResult, Tool: tool, Result: &tools.Result{IsError: true, Output: out}}
}

func TestFailedToolCallReachesTheErrorLogWithItsSource(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: toolFail("plan", "step 2 has no files\nsee the plan format")})
	if len(m.errLog) != 1 {
		t.Fatalf("errLog = %d entries, want 1", len(m.errLog))
	}
	e := m.errLog[0]
	if e.Source != srcTool || e.Text != "plan: step 2 has no files" {
		t.Fatalf("entry = %+v, want source tool and the first line of the output", e)
	}
	// A successful call is not an error.
	m.onEvent(eventMsg{tab: m.cur().id, ev: agent.Event{Kind: agent.EvToolResult, Tool: "plan", Result: &tools.Result{Output: "ok"}}})
	if len(m.errLog) != 1 {
		t.Fatalf("a successful call logged an error: %+v", m.errLog)
	}
}

func TestRepeatedToolFailureIsOneRowThatCountsTheRepeats(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	for i := 0; i < 3; i++ {
		m.onEvent(eventMsg{tab: m.cur().id, ev: toolFail("edit_file", "no match")})
	}
	m.onEvent(eventMsg{tab: m.cur().id, ev: toolFail("grep", "bad pattern")})
	m.onEvent(eventMsg{tab: m.cur().id, ev: toolFail("edit_file", "no match")})
	if len(m.errLog) != 2 {
		t.Fatalf("errLog = %d rows, want 2 (repeats folded)", len(m.errLog))
	}
	items := m.errorItems()
	if !strings.Contains(items[0].Label, "edit_file: no match") || items[0].Desc != m.cur().name+" ×4" {
		t.Fatalf("newest row = %q / %q, want the repeated edit failure counted 4", items[0].Label, items[0].Desc)
	}
}

func TestErrorSourcesAreMergedAndNamed(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	tb := m.cur()
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvWarning, Text: "compaction failed: model refused the summary"}})
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvError, Text: "rate limited"}})
	m.onEvent(eventMsg{tab: tb.id, ev: toolFail("auto_check", "go vet: unused variable x")})
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvWarning, Text: "a plain notice"}})
	got := map[string]string{}
	for _, e := range m.errLog {
		got[e.Source] = e.Text
	}
	if got[srcCompaction] == "" || got[srcTurn] != "rate limited" || got[srcAutocheck] != "auto-check: go vet: unused variable x" {
		t.Fatalf("sources = %v", got)
	}
	if len(m.errLog) != 3 {
		t.Fatalf("a plain warning must not be an error: %+v", m.errLog)
	}
	if items := m.errorItems(); !strings.Contains(items[0].Label, "autocheck") {
		t.Fatalf("label should name the source: %q", items[0].Label)
	}
}

func TestBackgroundFaultsArePolledIntoTheLog(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.pollBackground(time.Now())
	found := false
	for _, e := range m.errLog {
		if e.Source == "mcp" && strings.Contains(e.Text, "docs did not start") {
			found = true
		}
	}
	if !found {
		t.Fatalf("background fault missing from the log: %+v", m.errLog)
	}
	// The queue is drained: a second poll adds nothing.
	n := len(m.errLog)
	m.pollBackground(time.Now().Add(time.Minute))
	if len(m.errLog) != n {
		t.Fatalf("drained faults were logged again")
	}
}

func TestStaleMemoryHintShowsOnlyWhenThereAreSome(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.cur().stale = 3
	if out := screen(m); !strings.Contains(out, "♻ 3 stale memories — /memory cleanup") {
		t.Fatalf("hint missing from the context bar:\n%s", out)
	}
	m.cur().stale = 1
	if out := screen(m); !strings.Contains(out, "♻ 1 stale memory —") {
		t.Fatalf("singular hint missing:\n%s", out)
	}
	m.cur().stale = 0
	if out := screen(m); strings.Contains(out, "♻") {
		t.Fatalf("no memories stale, yet the hint shows")
	}
}

func TestBrowsingACheckpointShowsItsContextFill(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	tb := m.cur()
	tb.ctxWin, tb.ctxUsed, tb.ctxShown = 200000, 190000, 95 // ctxShown is the eased bar, set to the live value
	tb.cps = []CheckpointInfo{
		{Turn: 1, CtxUsed: 50000, CtxWindow: 200000},
		{Turn: 2, CtxUsed: 120000, CtxWindow: 200000},
		{Turn: 3},
	}
	tb.browse = 0
	out := screen(m)
	if !strings.Contains(out, "◀") || !strings.Contains(out, "~25%") || !strings.Contains(out, "~50k/200k") {
		t.Fatalf("browsed checkpoint 1 should show its fill:\n%s", out)
	}
	if strings.Contains(out, "95%") {
		t.Fatalf("the live fill must not show while browsing:\n%s", out)
	}
	tb.browse = 1
	if out := screen(m); !strings.Contains(out, "~60%") {
		t.Fatalf("checkpoint 2 should show ~60%%:\n%s", out)
	}
	tb.browse = 2
	if out := screen(m); !strings.Contains(out, "context not recorded for this turn") {
		t.Fatalf("a checkpoint with no record should say so:\n%s", out)
	}
	tb.browse = -1
	if out := screen(m); strings.Contains(out, "◀") || !strings.Contains(out, "95%") {
		t.Fatalf("live view should show the live bar again:\n%s", out)
	}
}
