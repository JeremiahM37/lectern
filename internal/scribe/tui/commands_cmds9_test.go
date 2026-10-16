package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// claimBackend is a FakeBackend with a claim table, a hook switch and a
// session privacy list, so the wave 9 commands can run against it.
type claimBackend struct {
	*FakeBackend
	claims   map[string]string // path -> holder label; "" label means this tab
	events   map[string]bool   // hook event -> off
	privacy  []string
	storage  map[string]int64
	cleaned  []string
	undoLeft int
}

func newClaimBackend() *claimBackend {
	return &claimBackend{FakeBackend: NewFakeBackend(), claims: map[string]string{}, events: map[string]bool{"PreToolUse": false}, storage: map[string]int64{"cache": 2048}}
}

func (c *claimBackend) Claims() []ClaimRow {
	var out []ClaimRow
	for p, l := range c.claims {
		out = append(out, ClaimRow{Path: p, Label: l, Age: time.Second, Mine: l == ""})
	}
	return out
}

func (c *claimBackend) ForceClaim(path string) (string, error) {
	prev := c.claims[path]
	c.claims[path] = ""
	return prev, nil
}

func (c *claimBackend) ReleaseClaim(path string) (bool, error) {
	if l, ok := c.claims[path]; ok && l == "" {
		delete(c.claims, path)
		return true, nil
	}
	return false, nil
}

func (c *claimBackend) ReleaseAllClaims() int {
	n := 0
	for p, l := range c.claims {
		if l == "" {
			delete(c.claims, p)
			n++
		}
	}
	return n
}

func (c *claimBackend) HookEvents() []HookEvent {
	return []HookEvent{{Event: "PreToolUse", Hooks: 2, Off: c.events["PreToolUse"]}}
}

func (c *claimBackend) SetHookEventOff(event string, off bool) error {
	c.events[event] = off
	return nil
}

func (c *claimBackend) SessionForbidden() []string { return c.privacy }

func (c *claimBackend) AddSessionForbidden(pat string) bool {
	for _, p := range c.privacy {
		if p == pat {
			return false
		}
	}
	c.privacy = append(c.privacy, pat)
	return true
}

func (c *claimBackend) RemoveSessionForbidden(pat string) bool {
	for i, p := range c.privacy {
		if p == pat {
			c.privacy = append(c.privacy[:i], c.privacy[i+1:]...)
			return true
		}
	}
	return false
}

func (c *claimBackend) StorageReport() []StorageItem {
	return []StorageItem{{Key: "cache", Label: "Code map cache", Path: "/x/cache", Bytes: c.storage["cache"], Files: 3}}
}

func (c *claimBackend) CleanStorage(keys []string) (int64, error) {
	c.cleaned = append(c.cleaned, keys...)
	return c.storage["cache"], nil
}

// feed delivers a command's result to the model, as the event loop would.
func feed(m *Model, cmd tea.Cmd) {
	if cmd != nil {
		m.Update(cmd())
	}
}

// lastChat returns the text of the newest transcript row.
func lastChat(m *Model) string {
	rows := m.cur().chat.Export()
	if len(rows) == 0 {
		return ""
	}
	return rows[len(rows)-1].Text
}

func TestCommandErrorIsShownInChatWithWrapper(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.tabs[0].be = NewFakeBackend() // no FileClaimer
	cmd := m.runCommand("/claim force a.go")
	if cmd == nil {
		t.Fatal("a command error should come back as a command")
	}
	feed(m, cmd)
	if got := lastChat(m); !strings.HasPrefix(got, "Command error: ") {
		t.Fatalf("chat row = %q, want the Command error: wrapper", got)
	}
}

func TestClaimForceAndRelease(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cb := newClaimBackend()
	cb.claims["pkg/x.go"] = "Scribe session other"
	m.tabs[0].be = cb
	feed(m, m.runCommand("/claim force pkg/x.go"))
	if cb.claims["pkg/x.go"] != "" {
		t.Fatalf("force should take the file for this tab, got %q", cb.claims["pkg/x.go"])
	}
	if !strings.Contains(lastChat(m), "Took pkg/x.go from Scribe session other") {
		t.Fatalf("force should say whom it took the file from, got %q", lastChat(m))
	}
	feed(m, m.runCommand("/claim release-all"))
	if len(cb.claims) != 0 {
		t.Fatalf("release-all should drop this tab's claims, left %v", cb.claims)
	}
}

func TestClaimUsageForUnknownSubcommand(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.tabs[0].be = newClaimBackend()
	feed(m, m.runCommand("/claim frobnicate"))
	if !strings.Contains(m.flash, "Usage: /claim") {
		t.Fatalf("expected a usage error, got %q", m.flash)
	}
}

func TestHooksOffAndOnForSession(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cb := newClaimBackend()
	m.tabs[0].be = cb
	m.runCommand("/hooks off PreToolUse")
	if !cb.events["PreToolUse"] {
		t.Fatal("/hooks off should switch the event off")
	}
	m.runCommand("/hooks on PreToolUse")
	if cb.events["PreToolUse"] {
		t.Fatal("/hooks on should switch the event back on")
	}
}

func TestRestartPlanKeepsTheSession(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fb := NewFakeBackend()
	m.tabs[0].be = fb
	pendingRestart = nil
	defer func() { pendingRestart = nil }()
	m.runCommand("/restart")
	if pendingRestart == nil {
		t.Fatal("/restart should plan a re-exec")
	}
	want := []string{"code", "--resume", fb.SessionID()}
	if strings.Join(pendingRestart.args, " ") != strings.Join(want, " ") {
		t.Fatalf("restart args = %v, want %v", pendingRestart.args, want)
	}
}

func TestPrivacySessionScope(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cb := newClaimBackend()
	m.tabs[0].be = cb
	m.runCommand("/privacy session secrets/*")
	if len(cb.privacy) != 1 || cb.privacy[0] != "secrets/*" {
		t.Fatalf("session pattern not added: %v", cb.privacy)
	}
	m.runCommand("/privacy session remove secrets/*")
	if len(cb.privacy) != 0 {
		t.Fatalf("session remove failed: %v", cb.privacy)
	}
}

func TestStorageReportAndCleanNeedsConfirm(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cb := newClaimBackend()
	m.tabs[0].be = cb
	m.runCommand("/storage clean cache")
	if len(cb.cleaned) != 0 {
		t.Fatal("cleanup must wait for the confirm")
	}
	if len(m.modals) == 0 {
		t.Fatal("/storage clean should open a confirm")
	}
}

func TestCheckpointLiveAndJumpAndUndoCount(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fb := NewFakeBackend()
	m.tabs[0].be = fb
	feed(m, m.runCommand("/checkpoint 99"))
	if !strings.Contains(lastChat(m), "No checkpoint for turn 99") {
		t.Fatalf("missing turn should be an error, got %q", lastChat(m))
	}
	m.runCommand("/checkpoint live")
	if m.cur().browse != -1 {
		t.Fatal("/checkpoint live should leave browsing")
	}
	feed(m, m.runCommand("/undo 0"))
	if !strings.Contains(m.flash, "Usage: /undo") {
		t.Fatalf("/undo 0 should be refused, got %q", m.flash)
	}
}

func TestPrefixMatcherChainRunsAfterNames(t *testing.T) {
	saved := commandTable
	defer func() { commandTable = saved }()
	commandTable = append([]*command(nil), commandTable...)
	commandTable = append(commandTable, &command{Name: "zzmatch", Group: "System",
		Match: func(line string) (string, bool) {
			if strings.HasPrefix(line, "zz do ") {
				return strings.TrimPrefix(line, "zz do "), true
			}
			return "", false
		}})
	c, args := findCommand("/zz do thing")
	if c == nil || c.Name != "zzmatch" || args != "thing" {
		t.Fatalf("matcher did not resolve: %v %q", c, args)
	}
	if c, _ := findCommand("/model"); c == nil || c.Name != "model" {
		t.Fatal("exact names must still win over matchers")
	}
}

func TestModelEventsAndAliasesResolve(t *testing.T) {
	for line, want := range map[string]string{
		"/model events":    "model events",
		"/compaction":      "compaction",
		"/compact-v2-logs": "compaction",
		"/edit a.go:3":     "edit",
		"/open a.go":       "open",
		"/files":           "files",
		"/editor-settings": "editor-settings",
	} {
		c, _ := findCommand(line)
		if c == nil || c.Name != want {
			t.Fatalf("%q resolved to %v, want %s", line, c, want)
		}
	}
}

// keep the import used even when the file grows
var _ = tea.Quit

// Output a command prints into the chat must not stay hidden behind the
// landing page.
func TestCommandOutputLeavesTheLandingPage(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseLanding
	registerCommands(&command{Name: "zz-print", Desc: "test", Run: func(m *Model, _ string) tea.Cmd {
		m.cur().chat.AddSystem("info", "printed")
		return nil
	}})
	m.runCommand("/zz-print")
	if m.phase == phaseLanding {
		t.Fatal("still on the landing page after a command printed into the chat")
	}
	m2, _ := newTestModel(t, 100, 30)
	m2.phase = phaseLanding
	m2.runCommand("/help")
	if m2.phase != phaseLanding && m2.cur().chat.Len() == 0 {
		t.Fatal("a command that printed nothing should not leave the landing page")
	}
}

// Three quick Ctrl+C presses quit even while dialogs keep the earlier presses
// busy.
func TestThreeQuickCtrlCAlwaysQuit(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseChat
	m.openInfo("One", "", []string{"a"})
	m.openInfo("Two", "", []string{"b"})
	m.openInfo("Three", "", []string{"c"})
	for i := 0; i < 3; i++ {
		m.handleCtrlC()
	}
	if m.phase != phaseShutdown {
		t.Fatalf("phase = %v after three quick Ctrl+C", m.phase)
	}
	m2, _ := newTestModel(t, 100, 30)
	m2.phase = phaseChat
	m2.openInfo("Open", "", []string{"a"})
	m2.handleCtrlC()
	m2.ctrlCAt = m2.ctrlCAt.Add(-2 * ctrlCBurst)
	m2.handleCtrlC()
	if m2.ctrlCs != 1 {
		t.Fatalf("slow presses must not count as a burst: count %d", m2.ctrlCs)
	}
}
