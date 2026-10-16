package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// presetHome points the global Scribe home at a temp dir for one test.
func presetHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	return home
}

func writePreset(t *testing.T, home, name, body string) {
	t.Helper()
	dir := filepath.Join(home, "presets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPresetWritesMapEveryFlatField(t *testing.T) {
	yes := true
	p := config.Config{
		Model: "anthropic/claude-sonnet-5", Mode: "plan", Theme: "scribe", Reasoning: "high",
		MaxTokens: 4000, ChatStyle: "bubble", CompactAt: 0.7, SearchURL: "http://s",
		Instructions: []string{"NOTES.md"}, NerdFont: &yes, Vim: &yes,
		Allow: []string{"go test", "make"}, Deny: []string{"rm -rf"},
		Router: config.Router{Explore: "openai/gpt-6", Fallback: []string{"ollama/x"}, MaxAgents: 4},
		ReasoningByProvider: map[string]config.Reasoning{
			"anthropic": {Effort: "medium", Budget: 8000, ServiceTier: "auto"},
		},
		Providers: []config.Provider{{ID: "p"}},
	}
	cur := map[string]any{"allow": []string{"go test"}, "deny": []string{}}
	writes, skipped := presetWrites(p, cur)

	got := map[string]any{}
	for _, w := range writes {
		got[w.key] = w.value
	}
	want := map[string]any{
		"model": "anthropic/claude-sonnet-5", "mode": "plan", "theme": "scribe", "reasoning": "high",
		"max_tokens": 4000, "chat_style": "bubble", "compact_at": 0.7, "search_url": "http://s",
		"instructions": []string{"NOTES.md"}, "nerd_font": true, "vim": true,
		"allow":                                  []string{"go test", "make"}, // added to, not replaced
		"deny":                                   []string{"rm -rf"},
		"router.explore":                         "openai/gpt-6",
		"router.fallback":                        []string{"ollama/x"},
		"router.max_agents":                      4,
		"reasoning_by_provider.anthropic.effort": "medium",
		"reasoning_by_provider.anthropic.budget": 8000,
		"reasoning_by_provider.anthropic.service_tier": "auto",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writes:\n got %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(skipped, []string{"providers"}) {
		t.Fatalf("providers have no flat key and must be reported as skipped, got %v", skipped)
	}
}

func TestPresetWritesSkipUnsetFields(t *testing.T) {
	writes, skipped := presetWrites(config.Config{Mode: "auto"}, nil)
	if len(writes) != 1 || writes[0].key != "mode" || len(skipped) != 0 {
		t.Fatalf("only the mode should be written: %+v skipped %v", writes, skipped)
	}
}

// /preset NAME writes the preset's keys to the scope the preset lives in.
func TestPresetCommandAppliesToItsScope(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "plan", `{"mode":"plan","reasoning_by_provider":{"anthropic":{"effort":"high"}}}`)
	m, fb := newTestModel(t, 100, 40)

	m.runCommand("/preset plan")

	ws := fb.ConfigWrites()
	wantMode := ConfigWrite{Key: "mode", Value: "plan", Project: false}
	wantEffort := ConfigWrite{Key: "reasoning_by_provider.anthropic.effort", Value: "high", Project: false}
	if !containsWrite(ws, wantMode) || !containsWrite(ws, wantEffort) {
		t.Fatalf("expected %+v and %+v in %+v", wantMode, wantEffort, ws)
	}
	advanceChat(m)
	if !strings.Contains(screen(m), "Applied preset plan") {
		t.Fatalf("the result should be reported:\n%s", screen(m))
	}
}

func TestPresetCommandListsPresets(t *testing.T) {
	home := presetHome(t)
	writePreset(t, home, "fast", `{"model":"openai/gpt-6"}`)
	writePreset(t, home, "Broken_Name", `{}`)
	m, fb := newTestModel(t, 100, 40)

	m.runCommand("/preset")
	advanceChat(m)
	out := screen(m)
	if !strings.Contains(out, "fast") || !strings.Contains(out, "global") {
		t.Fatalf("/preset should list the presets:\n%s", out)
	}
	if !strings.Contains(out, "lowercase") {
		t.Fatalf("a file that cannot be used should be reported:\n%s", out)
	}
	if len(fb.ConfigWrites()) != 0 {
		t.Fatalf("listing must write nothing: %+v", fb.ConfigWrites())
	}
}

func TestPresetCommandReportsUnknownName(t *testing.T) {
	presetHome(t)
	m, fb := newTestModel(t, 100, 40)

	m.runCommand("/preset nope")
	advanceChat(m)
	if !strings.Contains(screen(m), `no preset "nope"`) {
		t.Fatalf("an unknown name should say so:\n%s", screen(m))
	}
	if len(fb.ConfigWrites()) != 0 {
		t.Fatalf("an unknown preset must write nothing: %+v", fb.ConfigWrites())
	}
}

// advanceChat finishes the landing transition and lets system rows type out,
// as the screen does before a test can read them.
func advanceChat(m *Model) {
	settle(m)
	for i := 1; i <= 5; i++ {
		m.Update(tickMsg(time.Now().Add(time.Duration(i) * 10 * time.Second)))
	}
}

func containsWrite(ws []ConfigWrite, want ConfigWrite) bool {
	for _, w := range ws {
		if reflect.DeepEqual(w, want) {
			return true
		}
	}
	return false
}
