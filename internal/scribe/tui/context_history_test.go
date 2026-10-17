package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// historyFixture is a session with two main-agent models, one dispatch that
// spent subagent tokens, one skipped and one real compaction, plus subagent
// step events that must not count as main-agent calls.
func historyFixture() []agent.Event {
	step := func(model string, in, out, read, write int) agent.Event {
		return agent.Event{Kind: agent.EvStepFinish, Model: model,
			Usage: llm.Usage{InputTokens: in, OutputTokens: out, CacheReadTokens: read, CacheWriteTokens: write}}
	}
	return []agent.Event{
		step("anthropic/sonnet", 1000, 200, 3000, 500),
		step("anthropic/sonnet", 500, 100, 1500, 0),
		step("openai/gpt-x", 800, 50, 0, 0),
		{Kind: agent.EvStepFinish, AgentID: "a1", Model: "anthropic/haiku",
			Usage: llm.Usage{InputTokens: 9999}},
		{Kind: agent.EvCompactDone, Meta: map[string]any{"skipped": true, "messages_before": 4, "messages_after": 4}},
		{Kind: agent.EvCompactDone, Meta: map[string]any{"model": "anthropic/sonnet", "messages_before": 84, "messages_after": 12}},
		{Kind: agent.EvToolResult, Tool: "dispatch", Result: &tools.Result{Meta: map[string]any{
			"subagent_usage":    llm.Usage{InputTokens: 4000, OutputTokens: 1000},
			"subagent_cost_usd": 0.021,
		}}},
	}
}

func TestContextPanelShowsHistory(t *testing.T) {
	m, fb := newTestModel(t, 80, 40)
	fb.mu.Lock()
	fb.transcript = historyFixture()
	fb.usage = llm.Usage{InputTokens: 2300, OutputTokens: 350, CacheReadTokens: 4500, CacheWriteTokens: 500}
	fb.cost = 0.1234
	fb.mu.Unlock()

	lines := contextLines(m.cur())
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"  Cache write    500",
		"  Cache hit      62% of input read from cache",
		"  Subagents      1 dispatch, 5.0k tokens, $0.0210 (in the totals above)",
		"  Compactions    1 (last: 84 to 12 messages)",
		"By model (main agent)",
		"  anthropic/sonnet",
		"  openai/gpt-x",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("panel lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "anthropic/haiku") {
		t.Fatalf("a subagent's model call was counted as a main-agent call:\n%s", text)
	}
	// Busiest model first: sonnet has the most tokens.
	if i, j := strings.Index(text, "anthropic/sonnet"), strings.Index(text, "openai/gpt-x"); i < 0 || j < 0 || i > j {
		t.Fatalf("models are not busiest first:\n%s", text)
	}
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > 80 {
			t.Fatalf("line is %d columns, wider than 80: %q", n, l)
		}
	}
}

// After a resume the transcript comes back from JSON, so Meta numbers are
// float64 and the usage is a plain map. The fold must read the same figures.
func TestFoldHistoryReadsResumedTranscript(t *testing.T) {
	raw, err := json.Marshal(historyFixture())
	if err != nil {
		t.Fatal(err)
	}
	var back []agent.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	live, resumed := foldHistory(historyFixture()), foldHistory(back)
	if resumed.Subagents != 1 || resumed.SubagentUsage != live.SubagentUsage || resumed.SubagentCost != live.SubagentCost {
		t.Fatalf("subagent spend differs after resume: live %+v resumed %+v", live, resumed)
	}
	if resumed.Compactions != 1 || resumed.Last != live.Last {
		t.Fatalf("compactions differ after resume: live %+v resumed %+v", live, resumed)
	}
	if len(resumed.Models) != 2 || resumed.Models[0].Calls != live.Models[0].Calls || resumed.Models[0].Usage != live.Models[0].Usage {
		t.Fatalf("models differ after resume: %+v", resumed.Models)
	}
}

func TestCacheHitPercent(t *testing.T) {
	cases := []struct {
		name string
		u    llm.Usage
		pct  int
		ok   bool
	}{
		{"all input from cache", llm.Usage{CacheReadTokens: 900}, 100, true},
		{"read against uncached and written", llm.Usage{InputTokens: 100, CacheWriteTokens: 100, CacheReadTokens: 200}, 50, true},
		{"only cache writes", llm.Usage{CacheWriteTokens: 40}, 0, true},
		{"no input at all", llm.Usage{OutputTokens: 9}, 0, false},
	}
	for _, c := range cases {
		pct, ok := cacheHitPercent(c.u)
		if pct != c.pct || ok != c.ok {
			t.Errorf("%s: got %d,%v want %d,%v", c.name, pct, ok, c.pct, c.ok)
		}
	}
}

func TestModelTableCapsRowsAndSumsTheRest(t *testing.T) {
	var models []modelUsage
	for i := 0; i < maxModelRows+2; i++ {
		models = append(models, modelUsage{Ref: fmt.Sprintf("p/model-%d", i), Calls: 1, Usage: llm.Usage{InputTokens: 100}})
	}
	lines := modelTableLines(models)
	if len(lines) != 1+maxModelRows+1 {
		t.Fatalf("want header, %d rows and an overflow row, got %d lines:\n%s", maxModelRows, len(lines), strings.Join(lines, "\n"))
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "+2 more models") || !strings.Contains(last, "    2") {
		t.Fatalf("overflow row should sum the two hidden models: %q", last)
	}
}

func TestModelTableTruncatesLongNames(t *testing.T) {
	long := "provider/" + strings.Repeat("x", 60)
	lines := modelTableLines([]modelUsage{{Ref: long, Calls: 3, Usage: llm.Usage{InputTokens: 10}}})
	if !strings.Contains(lines[1], "…") || strings.Contains(lines[1], long) {
		t.Fatalf("a long model ref should be cut with an ellipsis: %q", lines[1])
	}
	if n := utf8.RuneCountInString(lines[1]); n > 80 {
		t.Fatalf("row is %d columns: %q", n, lines[1])
	}
}

func TestHistoryLinesWhenNothingRan(t *testing.T) {
	m, fb := newTestModel(t, 80, 40)
	fb.mu.Lock()
	fb.transcript = nil
	fb.usage = llm.Usage{}
	fb.mu.Unlock()

	text := strings.Join(contextLines(m.cur()), "\n")
	for _, want := range []string{
		"  Cache hit      n/a (no input yet)",
		"  Subagents      none this session",
		"  Compactions    none this session",
		"  No model calls in this session's transcript yet.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("panel lacks %q:\n%s", want, text)
		}
	}
}

func TestSubagentLineOmitsCostForLocalModels(t *testing.T) {
	h := contextHistory{Subagents: 2, SubagentUsage: llm.Usage{InputTokens: 900}, SubagentCost: 0.5}
	if got := subagentLine(h, true); strings.Contains(got, "$") {
		t.Fatalf("a local model has no cost to show: %q", got)
	}
	if got := subagentLine(h, false); !strings.Contains(got, "2 dispatches, 900 tokens, $0.5000") {
		t.Fatalf("remote model line: %q", got)
	}
}

// Tool runs and steps without a model name must not create model rows.
func TestFoldIgnoresNonModelEvents(t *testing.T) {
	h := foldHistory([]agent.Event{
		{Kind: agent.EvToolRun, Tool: "read"},
		{Kind: agent.EvStepFinish, Model: ""},
	})
	if len(h.Models) != 0 || h.Compactions != 0 || h.Subagents != 0 {
		t.Fatalf("nothing here should count: %+v", h)
	}
}

func TestModelTableShowsCostPerModel(t *testing.T) {
	h := foldHistory([]agent.Event{
		{Kind: agent.EvStepFinish, Model: "anthropic/claude-sonnet-5-5", StepCostUSD: 0.10},
		{Kind: agent.EvStepFinish, Model: "anthropic/claude-sonnet-5-5", StepCostUSD: 0.02},
		{Kind: agent.EvStepFinish, Model: "ollama/qwen3.6:35b-a3b"},
	})
	out := strings.Join(modelTableLines(h.Models), "\n")
	if !strings.Contains(out, "$0.12") {
		t.Fatalf("sonnet row should cost $0.12:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "qwen") && !strings.HasSuffix(strings.TrimSpace(line), "-") {
			t.Fatalf("a free local model should show -: %q", line)
		}
		if n := len([]rune(line)); n > 80 {
			t.Fatalf("line is %d columns: %q", n, line)
		}
	}
}
