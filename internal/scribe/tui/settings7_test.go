package tui

import (
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// flat7Backend is a FakeBackend that also keeps the wave 5 flat keys in a
// merged config, as the real backend does, and records each write. Secrets
// are kept apart and never returned, as the real store does.
type flat7Backend struct {
	*FakeBackend
	root    string
	cfg     config.Config
	writes  []flatWrite
	moves   []flatWrite
	secrets map[string]string
}

type flatWrite struct {
	Key     string
	Value   any
	Project bool
}

func newFlat7Backend(t *testing.T) (*Model, *flat7Backend) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, fb := newTestModel(t, 100, 40)
	b := &flat7Backend{FakeBackend: fb, root: t.TempDir(), secrets: map[string]string{}}
	m.tabs[0].be = b
	return m, b
}

func (b *flat7Backend) Workdir() string { return b.root }

func (b *flat7Backend) MergedConfig7() config.Config { return b.cfg }

func (b *flat7Backend) SetField7(key string, value any, project bool) error {
	b.writes = append(b.writes, flatWrite{Key: key, Value: value, Project: project})
	ok, err := b.cfg.SetField(key, value)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	return err
}

func (b *flat7Backend) MoveField7(key string, value any, toProject bool) error {
	b.moves = append(b.moves, flatWrite{Key: key, Value: value, Project: toProject})
	return nil
}

func (b *flat7Backend) SetSecret(id, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("empty key")
	}
	b.secrets[id] = strings.TrimSpace(value)
	return nil
}

func (b *flat7Backend) RemoveSecret(id string) error {
	delete(b.secrets, id)
	return nil
}

func (b *flat7Backend) SecretSource(id string) string {
	if _, ok := b.secrets[id]; ok {
		return "store"
	}
	return ""
}

func (b *flat7Backend) lastWrite(t *testing.T) flatWrite {
	t.Helper()
	if len(b.writes) == 0 {
		t.Fatal("no flat-key write was made")
	}
	return b.writes[len(b.writes)-1]
}

func TestAgentTabTogglesAFeatureAndSavesItsKey(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stAgent
	selectRow(t, s, "De-sloppify after edits")
	send(m, "enter")
	if got := b.lastWrite(t); got.Key != "agent_features.desloppify" || got.Value != true || got.Project {
		t.Fatalf("toggle wrote %+v", got)
	}
	if !b.cfg.AgentFeatures.Enabled("desloppify") {
		t.Fatal("the saved feature should read back on")
	}
	if !strings.Contains(s.note, "next session") {
		t.Fatalf("a feature applies next session; note %q", s.note)
	}
}

func TestToolTimeoutRowTakesMinutes(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stAgent
	selectRow(t, s, "Command timeout")
	send(m, "enter")
	send(m, "ctrl+u")
	typeText(m, "3")
	send(m, "enter")
	if b.cfg.ToolTimeout == nil || *b.cfg.ToolTimeout != 3 {
		t.Fatalf("tool timeout = %v, want 3", b.cfg.ToolTimeout)
	}
	selectRow(t, s, "Command timeout")
	send(m, "enter")
	send(m, "ctrl+u")
	typeText(m, "soon")
	send(m, "enter")
	if !strings.Contains(s.note, "minutes") {
		t.Fatalf("a non-number is refused; note %q", s.note)
	}
}

func TestCompactStrategyCyclesAndDefaultsToV2(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stAgent
	selectRow(t, s, "Compaction strategy")
	send(m, "enter")
	if got := b.lastWrite(t); got.Key != "compact_strategy" || got.Value != "v1" {
		t.Fatalf("first step = %+v", got)
	}
	send(m, "enter")
	send(m, "enter")
	if got := b.lastWrite(t); got.Value != "" || b.cfg.CompactStrategy != "" {
		t.Fatalf("cycling back to v2 should store the default, got %+v", got)
	}
}

func TestDiffStyleAppliesToOpenTabs(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stAppearance
	selectRow(t, s, "Diff style")
	send(m, "enter")
	if got := b.lastWrite(t); got.Key != "diff_style" || got.Value != "sidebyside" {
		t.Fatalf("diff style write = %+v", got)
	}
	if m.tabs[0].copt.DiffStyle != "sidebyside" {
		t.Fatalf("open tab did not take the diff style: %q", m.tabs[0].copt.DiffStyle)
	}
	selectRow(t, s, "Collapse long diffs")
	send(m, "enter")
	if !m.tabs[0].copt.CollapseDiffs {
		t.Fatal("collapse should reach the open tab at once")
	}
	if strings.Contains(s.note, "next session") {
		t.Fatalf("diff settings apply now; note %q", s.note)
	}
}

func TestBraveKeyIsMaskedAndStoredNotShown(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stTools
	selectRow(t, s, "Brave Search key")
	send(m, "enter")
	top, ok := m.topModal().(*TextPrompt)
	if !ok || !top.masked {
		t.Fatalf("the key field must be a masked prompt, top=%T", m.topModal())
	}
	typeText(m, "BSA-secret-1234")
	send(m, "enter")
	if b.secrets["brave-api-key"] != "BSA-secret-1234" {
		t.Fatalf("key not stored: %v", b.secrets)
	}
	if strings.Contains(s.note, "BSA") || len(b.writes) != 0 {
		t.Fatalf("the key must not appear in the note or in config writes: note=%q writes=%v", s.note, b.writes)
	}
	selectRow(t, s, "Brave Search key")
	if v := s.rows()[s.cur[s.tab]].value; v != "[config]" {
		t.Fatalf("row should show the source, got %q", v)
	}
}

func TestPerModelFallbackAddsAChain(t *testing.T) {
	m, b := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stRouter
	selectRow(t, s, "Per-model fallbacks")
	send(m, "enter")
	typeText(m, "ollama/qwen3.6:35b-a3b")
	send(m, "enter")
	typeText(m, "openai/gpt-6")
	send(m, "enter")
	got := b.lastWrite(t)
	if got.Key != "router.fallback_by_model.ollama/qwen3.6:35b-a3b" || got.Value != "openai/gpt-6" {
		t.Fatalf("per-model chain write = %+v", got)
	}
	if b.cfg.Router.FallbackByModel["ollama/qwen3.6:35b-a3b"][0] != "openai/gpt-6" {
		t.Fatalf("chain not in the merged config: %v", b.cfg.Router.FallbackByModel)
	}
}

func TestRouterSlotsAreRowsAndTheNotesSayWhatIsNotRoutedYet(t *testing.T) {
	m, _ := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stRouter
	labels := strings.Join(rowLabels(s), "|")
	for _, want := range []string{"Default slot", "De-sloppify pass", "Verify review", "Semantic summaries"} {
		if !strings.Contains(labels, want) {
			t.Errorf("Router tab misses %q: %s", want, labels)
		}
	}
	for _, r := range s.rows() {
		if r.label == "Verify review" && !strings.Contains(r.note, "not routed yet") {
			t.Errorf("verify slot note should say it is not routed: %q", r.note)
		}
	}
}

func TestMoveScopeCarriesAFlatKeyThroughMoveField(t *testing.T) {
	m, b := newFlat7Backend(t)
	writeConfigFile(t, config.GlobalPath(), `{"tool_timeout": 4, "agent_features": {"verify_edits": true}}`)
	s := openSettings(t, m)
	s.tab = stAgent
	m.Update(key("right"))
	var moved []string
	for _, mv := range b.moves {
		moved = append(moved, mv.Key)
	}
	if !strings.Contains(strings.Join(moved, ","), "tool_timeout") {
		t.Fatalf("tool_timeout is saved globally and must move to the project; moves=%v", moved)
	}
	if !strings.Contains(strings.Join(moved, ","), "agent_features.verify_edits") {
		t.Fatalf("a nested feature key must move too; moves=%v", moved)
	}
	for _, mv := range b.moves {
		if !mv.Project {
			t.Fatalf("moves go to the project: %+v", mv)
		}
	}
	_ = s
}

func TestHasDottedKeyFindsMapEntriesWithDots(t *testing.T) {
	doc := map[string]any{
		"router": map[string]any{"fallback_by_model": map[string]any{"ollama/qwen3.6:35b-a3b": []any{"x"}}},
	}
	if !hasDottedKey(doc, "router.fallback_by_model.ollama/qwen3.6:35b-a3b") {
		t.Fatal("a dotted model reference inside a map must be found")
	}
	if hasDottedKey(doc, "router.fallback_by_model.other/model") {
		t.Fatal("a missing entry must not be found")
	}
	if !hasDottedKey(map[string]any{"verbose": true}, "verbose") {
		t.Fatal("a top-level key must be found")
	}
}

func TestInstructionTogglingKeepsAtLeastOne(t *testing.T) {
	next, err := nextInstructions(nil, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if listIndex(next, "codex") >= 0 {
		t.Fatalf("codex was on in the defaults and should now be off: %v", next)
	}
	if _, err := nextInstructions([]string{"claude"}, "claude"); err == nil {
		t.Fatal("the last instruction file cannot be turned off")
	}
	added, err := nextInstructions([]string{"claude"}, "aider")
	if err != nil || len(added) != 2 {
		t.Fatalf("adding a source = %v, %v", added, err)
	}
}

func TestToolsTabListsHooksReadOnly(t *testing.T) {
	m, b := newFlat7Backend(t)
	b.cfg.Hooks = []config.Hook{{Event: "PreToolUse", Matcher: "Bash", Command: "echo ok", If: "Bash(git commit*)", Once: true}}
	s := openSettings(t, m)
	s.tab = stTools
	found := false
	for _, r := range s.rows() {
		if r.label == "PreToolUse" {
			found = true
			if r.act != nil {
				t.Fatal("a hook row is read-only")
			}
			if !strings.Contains(r.note, "if Bash(git commit*)") || !strings.Contains(r.note, "once") {
				t.Fatalf("hook note misses its flags: %q", r.note)
			}
		}
	}
	if !found {
		t.Fatalf("the hook is not listed: %v", rowLabels(s))
	}
}

func TestEditorAccessRowIsSaved(t *testing.T) {
	m, _ := newFlat7Backend(t)
	s := openSettings(t, m)
	s.tab = stTools
	selectRow(t, s, "Agent editor access")
	defer nvim.SetAccess(nvim.AccessOn)
	if row := s.rows()[s.cur[s.tab]]; row.act != nil {
		row.act()
	}
	c, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if c.EditorAccess == "" {
		t.Fatal("changing editor access in settings did not save it")
	}
}

// ---------------------------------------------------------------- commands

func TestDiffStyleCommandDirectArgumentSavesGlobally(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/diff-style compact")
	if got := b.lastWrite(t); got.Key != "diff_style" || got.Value != "compact" || got.Project {
		t.Fatalf("/diff-style compact wrote %+v", got)
	}
}

func TestDiffStylePickerTabTogglesCollapse(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/diff-style")
	if topTitle(m) != "Diff Style" {
		t.Fatalf("/diff-style should open the picker, top=%q", topTitle(m))
	}
	send(m, "tab")
	if got := b.lastWrite(t); got.Key != "collapse_diffs" || got.Value != true {
		t.Fatalf("tab should toggle collapse: %+v", got)
	}
}

func TestChatStyleDirectArgumentSavesToProject(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/chat-style bubble")
	w := fb.ConfigWrites()
	if len(w) == 0 || w[len(w)-1].Key != "chat_style" || w[len(w)-1].Value != "bubble" || !w[len(w)-1].Project {
		t.Fatalf("/chat-style bubble should save to the project: %v", w)
	}
	m.runCommand("/chat-style sideways")
	if got := m.cur().be.(*FakeBackend).ConfigWrites(); len(got) != len(w) {
		t.Fatal("a bad style must not write")
	}
}

func TestVimHintsArgumentSetsTheVimKey(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/vim-hints on")
	w := fb.ConfigWrites()
	if len(w) == 0 || w[len(w)-1].Key != "vim" || w[len(w)-1].Value != true {
		t.Fatalf("/vim-hints on = %v", w)
	}
}

func TestPrivacyAddAndRemoveWriteTheProjectList(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/privacy add secrets/**")
	if got := b.lastWrite(t); got.Key != "forbidden" || !got.Project {
		t.Fatalf("add wrote %+v", got)
	}
	if len(b.cfg.Forbidden) != 1 || b.cfg.Forbidden[0] != "secrets/**" {
		t.Fatalf("the saved list should be the merged config now: %v", b.cfg.Forbidden)
	}
	b.cfg.Forbidden = []string{"secrets/**", ".env"}
	m.runCommand("/privacy remove .env")
	if got := b.lastWrite(t); strings.Join(got.Value.([]string), ",") != "secrets/**" {
		t.Fatalf("remove should keep the others: %+v", got)
	}
	m.runCommand("/privacy")
	if topTitle(m) != "Forbidden Paths" {
		t.Fatalf("/privacy should list the patterns, top=%q", topTitle(m))
	}
}

func TestTimeoutsPickerPresetSavesMinutes(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/timeouts")
	if topTitle(m) != "Command Timeout" {
		t.Fatalf("/timeouts should open its picker, top=%q", topTitle(m))
	}
	send(m, "enter") // first preset: 30 seconds
	if b.cfg.ToolTimeout == nil || *b.cfg.ToolTimeout != 0.5 {
		t.Fatalf("preset not saved: %v", b.cfg.ToolTimeout)
	}
}

func TestAgentFeaturesPickerStaysOpenAndToggles(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/agent-features")
	if topTitle(m) != "Agent Features" {
		t.Fatalf("top=%q", topTitle(m))
	}
	send(m, "enter")
	if !b.cfg.AgentFeatures.Enabled("desloppify") {
		t.Fatal("the first feature should toggle on")
	}
	if topTitle(m) != "Agent Features" {
		t.Fatal("the picker should stay open")
	}
}

func TestHooksCommandShowsNoneOrTheList(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/hooks")
	if topTitle(m) != "Hooks" {
		t.Fatalf("top=%q", topTitle(m))
	}
	m.closeModal(m.topModal())
	b.cfg.Hooks = []config.Hook{{Event: "Stop", Command: "true"}}
	m.runCommand("/hooks")
	if !strings.Contains(screen(m), "Stop") {
		t.Fatalf("the hook event should be listed:\n%s", screen(m))
	}
}

func TestCompactSettingsOpensTheStrategyPicker(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.runCommand("/compact settings")
	if topTitle(m) != "Compaction" {
		t.Fatalf("top=%q", topTitle(m))
	}
	send(m, "down")
	send(m, "enter")
	if got := b.lastWrite(t); got.Key != "compact_strategy" || got.Value != "v1" {
		t.Fatalf("picked %+v", got)
	}
}

func TestInstructionsCommandOpensTheToggles(t *testing.T) {
	m, _ := newFlat7Backend(t)
	m.runCommand("/instructions")
	if topTitle(m) != "Instruction Files" {
		t.Fatalf("top=%q", topTitle(m))
	}
}

// ---------------------------------------------------------------- picker folding

// foldPicker is a two-group picker for the folding and key tests.
func foldPicker(t *testing.T) *Picker {
	t.Helper()
	p := NewPicker(theme.Default(), "Fold", "x")
	p.Collapsible = true
	p.Items = []PickItem{
		{Value: "a1", Label: "a1", Group: "Alpha"},
		{Value: "a2", Label: "a2", Group: "Alpha"},
		{Value: "b1", Label: "b1", Group: "Beta"},
	}
	p.refilter()
	return p
}

func TestPickerFoldsAGroupWithHAndUnfoldsWithL(t *testing.T) {
	p := foldPicker(t)
	if len(p.visible) != 3 {
		t.Fatalf("all rows visible at first, got %d", len(p.visible))
	}
	p.Update(key("h"))
	if !p.folded["Alpha"] || len(p.visible) != 2 {
		t.Fatalf("h folds the cursor group: folded=%v visible=%d", p.folded, len(p.visible))
	}
	if sel, _ := p.Selected(); sel.Group != "Alpha" {
		t.Fatalf("the folded row keeps the cursor: %+v", sel)
	}
	p.Update(key("l"))
	if p.folded["Alpha"] || len(p.visible) != 3 {
		t.Fatal("l unfolds the group")
	}
}

func TestPickerEnterOnFoldedRowUnfoldsInsteadOfSelecting(t *testing.T) {
	p := foldPicker(t)
	selected := ""
	p.OnSelect = func(it PickItem) tea.Cmd { selected = it.Value; return nil }
	p.SetFolded("Alpha", true)
	p.Update(key("enter"))
	if selected != "" {
		t.Fatalf("enter on a folded row must not select %q", selected)
	}
	if p.folded["Alpha"] {
		t.Fatal("enter should unfold the group")
	}
	p.Update(key("enter"))
	if selected != "a1" {
		t.Fatalf("enter on an open row selects it, got %q", selected)
	}
}

func TestPickerSpaceTogglesAndFilterShowsEverything(t *testing.T) {
	p := foldPicker(t)
	p.Update(key(" "))
	if !p.folded["Alpha"] {
		t.Fatal("space folds the cursor group")
	}
	p.query = "a2"
	p.refilter()
	if len(p.visible) != 1 || p.Items[p.visible[0]].Value != "a2" {
		t.Fatalf("a filter shows the matching row even in a folded group, got %v", p.visible)
	}
}

func TestPickerCtrlJAndCtrlKMoveTheCursor(t *testing.T) {
	p := foldPicker(t)
	p.Update(key("ctrl+j"))
	if p.cursor != 1 {
		t.Fatalf("ctrl+j moves down, cursor=%d", p.cursor)
	}
	p.Update(key("ctrl+k"))
	if p.cursor != 0 {
		t.Fatalf("ctrl+k moves up, cursor=%d", p.cursor)
	}
}

func TestPickerFoldedGroupRendersOneRow(t *testing.T) {
	p := foldPicker(t)
	p.SetFolded("Alpha", true)
	out := ansi.Strip(p.View(60, 10))
	if !strings.Contains(out, "▸ Alpha") {
		t.Fatalf("a folded group shows a closed caret:\n%s", out)
	}
	if strings.Contains(out, "a2") {
		t.Fatalf("a folded group hides its other rows:\n%s", out)
	}
	if !strings.Contains(out, "b1") {
		t.Fatalf("other groups still show:\n%s", out)
	}
}

// TestNewTabUsesTheSavedDiffStyle: a saved diff style applies to a tab from
// the start, not only after it is changed in /settings.
func TestNewTabUsesTheSavedDiffStyle(t *testing.T) {
	m, b := newFlat7Backend(t)
	b.cfg = config.Config{DiffStyle: chat.DiffSideBySide}
	tb := m.addTab(b, "two")
	if tb.copt.DiffStyle != chat.DiffSideBySide {
		t.Fatalf("new tab diff style = %q, want %q", tb.copt.DiffStyle, chat.DiffSideBySide)
	}
}
