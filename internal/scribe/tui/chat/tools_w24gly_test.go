package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/charmbracelet/x/ansi"
)

// cacheGlyphs are the nerd-font codepoints spec 01 §2.9 gives each outcome.
var cacheGlyphs = map[string]string{
	"hit":        "",
	"wait":       "",
	"store":      "",
	"invalidate": "",
}

// dispatchRowWithAgent opens a running dispatch with one subagent row a1 and
// applies info to it. It returns the plain text of the view.
func dispatchRowWithAgent(t *testing.T, w int, info agent.AgentInfo) string {
	t.Helper()
	v, c := newView(t, Options{})
	v.SetSize(w, 60)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "split it", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolStart, CallID: "d1", Tool: "dispatch", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolRun, CallID: "d1", Tool: "dispatch", At: at(c),
		Args: json.RawMessage(`{"tasks":[{"task":"seed","role":"explore"}]}`)})
	info.ID = "a1"
	if info.Role == "" {
		info.Role = "explore"
	}
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d1", At: at(c), Agent: &info})
	return plain(v)
}

// agentRow is the subagent row of a1 in out, or fails the test.
func agentRow(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "a1 explore") {
			return line
		}
	}
	t.Fatalf("no row for a1 in:\n%s", out)
	return ""
}

func TestCacheOutcomeRendersItsGlyphOnTheSubagentRow(t *testing.T) {
	cases := []struct {
		name  string
		info  agent.AgentInfo
		label string
	}{
		{"hit from a subagent", agent.AgentInfo{Cache: "hit", CacheFrom: "a7"}, "from a7"},
		{"hit with no subagent named", agent.AgentInfo{Cache: "hit"}, "from cache"},
		{"wait", agent.AgentInfo{Cache: "wait", CacheFrom: "a2"}, "waiting on a2"},
		{"store", agent.AgentInfo{Cache: "store"}, "cached"},
		{"invalidate", agent.AgentInfo{Cache: "invalidate"}, "updated cache"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.info
			info.Task = "map the sessions store"
			info.Status = "running"
			row := agentRow(t, dispatchRowWithAgent(t, 80, info))
			glyph := cacheGlyphs[info.Cache]
			if !strings.Contains(row, glyph) {
				t.Errorf("row has no %s glyph for %q: %q", info.Cache, info.Cache, row)
			}
			if !strings.Contains(row, tc.label) {
				t.Errorf("row lacks label %q: %q", tc.label, row)
			}
		})
	}
}

// With the dispatch cache off the row carries no cache state, so no glyph and
// no label from it appear on the row.
func TestNoCacheGlyphWhenTheCacheIsOff(t *testing.T) {
	info := agent.AgentInfo{Task: "map the sessions store", Status: "running"}
	row := agentRow(t, dispatchRowWithAgent(t, 80, info))
	for name, glyph := range cacheGlyphs {
		if strings.Contains(row, glyph) {
			t.Errorf("row has the %s glyph with the cache off: %q", name, row)
		}
	}
	for _, label := range []string{"cached", "from cache", "waiting", "updated cache"} {
		if strings.Contains(row, label) {
			t.Errorf("row has label %q with the cache off: %q", label, row)
		}
	}
}

// The cache state must not push a subagent row past the view width, even
// with a long task, a long model and a long error.
func TestCacheStateKeepsTheRowWithinEightyColumns(t *testing.T) {
	for _, cache := range []string{"hit", "wait", "store", "invalidate"} {
		info := agent.AgentInfo{
			Task:      strings.Repeat("read every file in the sessions package ", 3),
			Role:      "explore",
			Status:    "error",
			Model:     "claude-haiku-5-5",
			Step:      12,
			Tokens:    llm.Usage{InputTokens: 15000, OutputTokens: 3000},
			Error:     strings.Repeat("the read timed out after thirty seconds ", 2),
			Cache:     cache,
			CacheFrom: "a10",
		}
		row := agentRow(t, dispatchRowWithAgent(t, 80, info))
		if w := ansi.StringWidth(row); w > 80 {
			t.Errorf("%s row is %d columns, over 80: %q", cache, w, row)
		}
		if !strings.Contains(row, cacheGlyphs[cache]) {
			t.Errorf("%s glyph lost when the row was fitted: %q", cache, row)
		}
	}
}

// A view with no width yet falls back to 80 columns for the cache state.
func TestRowRoomFallsBackToEightyColumns(t *testing.T) {
	v := &View{}
	line := strings.Repeat("x", 30)
	if got := v.rowRoom(line); got != 50 {
		t.Errorf("rowRoom with no view width = %d, want 50", got)
	}
	v.w = 100
	if got := v.rowRoom(line); got != 70 {
		t.Errorf("rowRoom at width 100 = %d, want 70", got)
	}
}

// The glyph table must hold the spec's codepoints, one rune each. Without this
// an empty or wrong value would still pass the row checks below.
func TestCacheGlyphsAreTheSpecCodepoints(t *testing.T) {
	want := map[string]rune{"hit": 0xf0e7, "wait": 0xf017, "store": 0xf0c7, "invalidate": 0xf071}
	for name, r := range want {
		if got := []rune(cacheGlyphs[name]); len(got) != 1 || got[0] != r {
			t.Errorf("%s glyph = %q, want U+%04X", name, cacheGlyphs[name], r)
		}
	}
}
