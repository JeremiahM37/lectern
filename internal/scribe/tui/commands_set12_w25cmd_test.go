package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func TestThinkingShowsDefaultAndChoices(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := flashOf(t, thinkingCommand(m, ""))
	if f.kind != "info" || !strings.Contains(f.text, "Thinking: default") || !strings.Contains(f.text, "default, off, adaptive, enabled") {
		t.Fatalf("status should show default and the choices, got %q (%s)", f.text, f.kind)
	}
}

func TestThinkingSetsAndDefaultClears(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, thinkingCommand(m, "ADAPTIVE"))
	if fb.flat.Performance.Thinking != "adaptive" || f.kind != "ok" {
		t.Fatalf("/thinking ADAPTIVE should save adaptive and confirm, got %q (%s)", fb.flat.Performance.Thinking, f.kind)
	}
	if !strings.Contains(f.text, "next session") {
		t.Fatalf("confirmation should say when it applies, got %q", f.text)
	}
	thinkingCommand(m, "off")
	if fb.flat.Performance.Thinking != "off" {
		t.Fatalf("/thinking off should store off, got %q", fb.flat.Performance.Thinking)
	}
	thinkingCommand(m, "default")
	if fb.flat.Performance.Thinking != "" {
		t.Fatalf("/thinking default should clear the value, got %q", fb.flat.Performance.Thinking)
	}
}

func TestThinkingRejectsUnknownAndListsValid(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.flat.Performance.Thinking = "off"
	f := flashOf(t, thinkingCommand(m, "auto"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: default, off, adaptive, enabled") {
		t.Fatalf("a bad thinking value should list the choices, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.Performance.Thinking != "off" {
		t.Fatalf("a rejected value must not be written, now %q", fb.flat.Performance.Thinking)
	}
}

func TestEffortShowsOffAndChoices(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, effortCommand(m, ""))
	if !strings.Contains(f.text, "Effort: off") || !strings.Contains(f.text, "off, low, medium, high, xhigh, max") {
		t.Fatalf("status should show off and the choices, got %q", f.text)
	}
	fb.flat.Performance.Effort = "high"
	f = flashOf(t, effortCommand(m, ""))
	if !strings.Contains(f.text, "Effort: high") {
		t.Fatalf("status should show high, got %q", f.text)
	}
}

func TestEffortSetsAndOffClears(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, effortCommand(m, "XHigh"))
	if fb.flat.Performance.Effort != "xhigh" || f.kind != "ok" {
		t.Fatalf("/effort XHigh should save xhigh, got %q (%s)", fb.flat.Performance.Effort, f.kind)
	}
	effortCommand(m, "off")
	if fb.flat.Performance.Effort != "" {
		t.Fatalf("/effort off should clear the value, got %q", fb.flat.Performance.Effort)
	}
}

func TestEffortRejectsUnknownAndListsValid(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, effortCommand(m, "minimal"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: off, low, medium, high, xhigh, max") {
		t.Fatalf("a bad effort should list the choices, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.Performance.Effort != "" {
		t.Fatalf("a rejected effort must not be written, now %q", fb.flat.Performance.Effort)
	}
}

func TestToolStreamDefaultsOnAndTogglesTheFlag(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, toolStreamCommand(m, ""))
	if !strings.Contains(f.text, "Tool streaming: on") {
		t.Fatalf("an unset flag should read as on, got %q", f.text)
	}
	f = flashOf(t, toolStreamCommand(m, "off"))
	if p := fb.flat.Performance.ToolStreaming; p == nil || *p || f.kind != "ok" {
		t.Fatalf("/toolstream off should store false, got %v (%s)", p, f.kind)
	}
	toolStreamCommand(m, "ON")
	if p := fb.flat.Performance.ToolStreaming; p == nil || !*p {
		t.Fatal("/toolstream ON should store true")
	}
}

func TestToolStreamRejectsOtherWords(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, toolStreamCommand(m, "maybe"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: on, off") {
		t.Fatalf("a bad value should list on and off, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.Performance.ToolStreaming != nil {
		t.Fatal("a rejected value must not be written")
	}
}

func TestSeqToolsWritesOneToolAtATime(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, seqToolsCommand(m, ""))
	if !strings.Contains(f.text, "One tool at a time: off") {
		t.Fatalf("default should be off, got %q", f.text)
	}
	f = flashOf(t, seqToolsCommand(m, "on"))
	if p := fb.flat.Performance.DisableParallelToolUse; p == nil || !*p || f.kind != "ok" {
		t.Fatalf("/seqtools on should set disable_parallel_tool_use, got %v (%s)", p, f.kind)
	}
	if !strings.Contains(f.text, "One tool at a time set to on") {
		t.Fatalf("confirmation should name the setting, got %q", f.text)
	}
	seqToolsCommand(m, "off")
	if p := fb.flat.Performance.DisableParallelToolUse; p == nil || *p {
		t.Fatal("/seqtools off should store false")
	}
}

func TestSeqToolsRejectsOtherWords(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, seqToolsCommand(m, "sequential"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: on, off") {
		t.Fatalf("a bad value should list on and off, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.Performance.DisableParallelToolUse != nil {
		t.Fatal("a rejected value must not be written")
	}
}

func TestPerfShowsTheEffectiveSettingsAndChangesNothing(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.flat.Performance = config.Performance{Thinking: "adaptive", Effort: "high", Speed: "fast", CacheTTL: "1h"}
	off := false
	fb.flat.Performance.SendReasoning = &off
	f := flashOf(t, perfCommand(m, ""))
	if f.kind != "info" {
		t.Fatalf("/perf should be information, got %q (%s)", f.text, f.kind)
	}
	// The values are in the panel, one per line, not in the flash line.
	text := strings.Join(perfLines(fb.flat.Performance), "\n")
	for _, want := range []string{"thinking", "adaptive", "effort", "high", "speed", "fast", "cache lifetime", "1h", "tool streaming", "on", "send reasoning", "off", "one tool at a time"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/perf panel should contain %q, got:\n%s", want, text)
		}
	}
	if fb.flat.Performance.Thinking != "adaptive" || fb.flat.Performance.Effort != "high" {
		t.Fatal("/perf must not change any setting")
	}
}

func TestPerfRejectsAnArgument(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := flashOf(t, perfCommand(m, "fast"))
	if f.kind != "err" || !strings.Contains(f.text, "takes no argument") {
		t.Fatalf("/perf with an argument should say it takes none, got %q (%s)", f.text, f.kind)
	}
}

// The spec names these, but nothing in config or llm.Request backs them, so
// they must stay unregistered rather than store a value nothing reads.
// (/pruning is backed by context_management.pruning_target since wave 33.)
func TestUnbackedPerfCommandsAreNotRegistered(t *testing.T) {
	for _, name := range []string{"codeexec", "computeruse", "cleartools", "clearthinking"} {
		if c, _ := findCommand("/" + name); c != nil {
			t.Errorf("/%s should not be registered: it has no setting behind it", name)
		}
	}
}
