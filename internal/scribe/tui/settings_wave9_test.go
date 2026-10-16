package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// w9Labels lists the labels of rows, for asserting which controls a
// provider and model get.
func w9Labels(rows []setRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.label)
	}
	return out
}

func w9Has(rows []setRow, label string) bool {
	for _, r := range rows {
		if r.label == label {
			return true
		}
	}
	return false
}

func TestPerformanceRowsShowOnlyWhatTheProviderAndModelTake(t *testing.T) {
	s := &settings{}
	c := config.Config{}

	budget := s.performanceRows(c, "anthropic", llm.ReasoningBudget)
	if !w9Has(budget, "Thinking") || w9Has(budget, "Effort") {
		t.Fatalf("budget Claude model: want Thinking and no Effort, got %v", w9Labels(budget))
	}
	adaptive := s.performanceRows(c, "anthropic", llm.ReasoningAdaptive)
	if !w9Has(adaptive, "Effort") {
		t.Fatalf("adaptive Claude model: want Effort, got %v", w9Labels(adaptive))
	}

	oa := s.performanceRows(c, "openai", llm.ReasoningEffort)
	if w9Has(oa, "Thinking") || w9Has(oa, "Effort") || w9Has(oa, "Cache lifetime") {
		t.Fatalf("OpenAI: only One tool at a time applies, got %v", w9Labels(oa))
	}
	if !w9Has(oa, "One tool at a time") {
		t.Fatalf("OpenAI should keep One tool at a time, got %v", w9Labels(oa))
	}

	ds := s.performanceRows(c, "deepseek", llm.ReasoningEffort)
	if len(ds) != 0 {
		t.Fatalf("DeepSeek gets no Claude-only or shared performance rows, got %v", w9Labels(ds))
	}

	for _, rows := range [][]setRow{budget, adaptive, oa} {
		for _, gone := range []string{"Speed tier", "Stream tool arguments", "Send reasoning back"} {
			if w9Has(rows, gone) {
				t.Fatalf("%q has no reader in any adapter and must not be shown", gone)
			}
		}
	}
}

func TestWatchdogPickerSavesAPresetAndStaysSeparateFromTimeouts(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/timeouts")
	if topTitle(m) != "Command Timeout" {
		t.Fatalf("/timeouts should be the command timeout picker, top=%q", topTitle(m))
	}
	m.closeModal(m.topModal())

	m.runCommand("/watchdog")
	if topTitle(m) != "Stream Watchdog" {
		t.Fatalf("/watchdog should open the stream watchdog, top=%q", topTitle(m))
	}
	send(m, "enter") // First token
	if topTitle(m) != "First token" {
		t.Fatalf("the first threshold should open its presets, top=%q", topTitle(m))
	}
	send(m, "enter") // 60 seconds
	if got := b.lastWrite(t); got.Key != "watchdog.first_token" || got.Value != "60" {
		t.Fatalf("preset wrote %+v", got)
	}
	if b.cfg.WatchdogTimeouts.FirstToken != 60 {
		t.Fatalf("saved first token = %d, want 60", b.cfg.WatchdogTimeouts.FirstToken)
	}
	if b.cfg.ToolTimeout != nil {
		t.Fatal("the stream watchdog must not touch the command timeout")
	}
}

func TestWatchdogCustomValueOutOfRangeIsRefused(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/watchdog")
	send(m, "enter") // First token
	// "Custom…" is the last row; the cursor wraps, so one up reaches it.
	send(m, "up")
	send(m, "enter")
	prompt, ok := m.topModal().(*TextPrompt)
	if !ok {
		t.Fatalf("Custom should open a value prompt, top=%q", topTitle(m))
	}
	prompt.WithValue("5")
	send(m, "enter")
	if b.cfg.WatchdogTimeouts.FirstToken != 0 {
		t.Fatalf("a 5 second first token must not be saved, got %d", b.cfg.WatchdogTimeouts.FirstToken)
	}
}

func TestDiffStyleDirectArgumentFollowsTheScopeWord(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/diff-style compact global")
	if got := b.lastWrite(t); got.Key != "diff_style" || got.Value != "compact" || got.Project {
		t.Fatalf("explicit global wrote %+v", got)
	}
	m.runCommand("/diff-style sidebyside project")
	if got := b.lastWrite(t); got.Value != "sidebyside" || !got.Project {
		t.Fatalf("explicit project wrote %+v", got)
	}
	m.runCommand("/diff-style --project default")
	if got := b.lastWrite(t); got.Value != "" || !got.Project {
		t.Fatalf("--project default should clear in the project, wrote %+v", got)
	}
}

func TestDiffStyleDirectArgumentDefaultsToWhereTheKeyLives(t *testing.T) {
	m, b := newFlat7Backend(t)
	// Nothing in the project file: a plain value goes global.
	m.runCommand("/diff-style compact")
	if got := b.lastWrite(t); got.Project {
		t.Fatalf("no project value: should save global, wrote %+v", got)
	}
	// The project file sets diff_style: a plain value stays in the project.
	if err := config.SaveProject(b.root, config.Config{DiffStyle: "compact"}); err != nil {
		t.Fatal(err)
	}
	m.runCommand("/diff-style sidebyside")
	if got := b.lastWrite(t); !got.Project || got.Value != "sidebyside" {
		t.Fatalf("the key lives in the project: should save there, wrote %+v", got)
	}
}

func TestDiffStyleUsageWhenOnlyAScopeWord(t *testing.T) {
	m, b := newFlat7Backend(t)
	before := len(b.writes)
	m.runCommand("/diff-style project")
	if len(b.writes) != before {
		t.Fatal("a scope word with no style must not write")
	}
}

func TestVimHintsDirectArgumentScope(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/vim-hints on project")
	w := fb.ConfigWrites()
	if len(w) == 0 || w[len(w)-1].Key != "vim" || w[len(w)-1].Value != true || !w[len(w)-1].Project {
		t.Fatalf("/vim-hints on project = %v", w)
	}
	m.runCommand("/vim-hints off")
	w = fb.ConfigWrites()
	if w[len(w)-1].Value != false || w[len(w)-1].Project {
		t.Fatalf("/vim-hints off (no scope, nothing in project) = %v", w[len(w)-1])
	}
}

func TestSplitScopeArg(t *testing.T) {
	cases := []struct{ in, value, scope string }{
		{"compact", "compact", ""},
		{"compact project", "compact", "project"},
		{"project compact", "compact", "project"},
		{"on --global", "on", "global"},
		{"project", "", "project"},
		{"sidebyside", "sidebyside", ""},
	}
	for _, c := range cases {
		v, s := splitScopeArg(c.in)
		if v != c.value || s != c.scope {
			t.Errorf("splitScopeArg(%q) = %q, %q; want %q, %q", c.in, v, s, c.value, c.scope)
		}
	}
}

func TestEditorIntegrationRowsSaveEachToggle(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := &settings{m: m, be: b}
	rows := s.editorIntegrationRows(config.Config{})
	if len(rows) != 1+len(config.EditorIntegrationKeys) {
		t.Fatalf("want a heading and %d toggles, got %d rows", len(config.EditorIntegrationKeys), len(rows))
	}
	if !rows[1].on || rows[1].key != "editor_integration.diagnostics" {
		t.Fatalf("first toggle: %+v", rows[1])
	}
	rows[1].act()
	if got := b.lastWrite(t); got.Key != "editor_integration.diagnostics" || got.Value != false {
		t.Fatalf("toggle wrote %+v", got)
	}
	if b.cfg.EditorIntegrationOn("diagnostics") {
		t.Fatal("the saved toggle should be off in the merged config")
	}
}

func TestContextDispatchAndSystemTabs(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	m.runCommand("/context dispatch")
	if topTitle(m) != "Context" {
		t.Fatalf("/context dispatch should open the context panel, top=%q", topTitle(m))
	}
	m.closeModal(m.topModal())
	m.runCommand("/context system")
	if topTitle(m) != "Context" {
		t.Fatalf("/context system should open the context panel, top=%q", topTitle(m))
	}
	sys := strings.Join(contextTabLines(m.cur(), "system"), "\n")
	for _, want := range []string{"System prompt", "Tool definitions", "Instruction files loaded", "scribe"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("System tab lacks %q:\n%s", want, sys)
		}
	}
	disp := strings.Join(contextTabLines(m.cur(), "dispatch"), "\n")
	if !strings.Contains(disp, "No subagents") {
		t.Fatalf("an idle session has no dispatch:\n%s", disp)
	}
}

func TestContextTabWordOnlyMatchesTheFirstWord(t *testing.T) {
	if contextTabWord("dispatch") != "dispatch" || contextTabWord("System now") != "system" {
		t.Fatal("first word should pick the tab")
	}
	if contextTabWord("clear memory") != "" || contextTabWord("") != "" {
		t.Fatal("other words must not pick a tab")
	}
}
