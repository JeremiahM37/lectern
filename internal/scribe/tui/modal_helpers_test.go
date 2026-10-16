package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// recBackend wraps the fake backend with data and recorded calls for the modal tests.
type recBackend struct {
	*FakeBackend
	mu        sync.Mutex
	mems      []MemoryItem
	saved     []MemoryItem
	deleted   []string
	restarted []string
	rebuilds  int
	servers   []ServerStatus
	stats     MapStats
	tools     []ToolInfo
}

func newRecBackend() *recBackend {
	fb := NewFakeBackend()
	fb.Delay = 0
	return &recBackend{FakeBackend: fb}
}

func (b *recBackend) Memories(string) []MemoryItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]MemoryItem(nil), b.mems...)
}

func (b *recBackend) SaveMemory(it MemoryItem) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saved = append(b.saved, it)
	if it.ID == "" {
		it.ID = fmt.Sprintf("new%d", len(b.saved))
		if it.Scope == "" {
			it.Scope = "project"
		}
		b.mems = append(b.mems, it)
		return nil
	}
	for i := range b.mems {
		if b.mems[i].ID == it.ID {
			b.mems[i].Title, b.mems[i].Body, b.mems[i].Category, b.mems[i].Pinned = it.Title, it.Body, it.Category, it.Pinned
		}
	}
	return nil
}

func (b *recBackend) DeleteMemory(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, id)
	for i := range b.mems {
		if b.mems[i].ID == id {
			b.mems = append(b.mems[:i], b.mems[i+1:]...)
			break
		}
	}
	return nil
}

func (b *recBackend) Servers() []ServerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ServerStatus(nil), b.servers...)
}

func (b *recBackend) RestartServer(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.restarted = append(b.restarted, name)
	return nil
}

func (b *recBackend) MapStats() MapStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *recBackend) RebuildMap() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rebuilds++
}

// SetToolEnabled updates the fixture list when one is set, else the fake.
func (b *recBackend) SetToolEnabled(name string, on bool) {
	if b.tools == nil {
		b.FakeBackend.SetToolEnabled(name, on)
		return
	}
	for i := range b.tools {
		if b.tools[i].Name == name {
			b.tools[i].Enabled = on
		}
	}
}

func (b *recBackend) Tools() []ToolInfo {
	if b.tools != nil {
		return b.tools
	}
	return b.FakeBackend.Tools()
}

// plainBackend hides the optional SkillCatalog methods.
type plainBackend struct{ Backend }

// modalModel returns a chat-phase model driven by be at w x h.
func modalModel(t *testing.T, w, h int, be Backend) *Model {
	t.Helper()
	m, _ := newTestModel(t, w, h)
	m.cur().be = be
	m.phase = phaseChat
	return m
}

// checkWidths asserts the whole screen is exactly w x h cells.
func checkWidths(t *testing.T, m *Model, w, h int) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != h {
		t.Fatalf("at %dx%d want %d lines, got %d", w, h, h, len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("at %dx%d line %d is %d wide: %q", w, h, i, got, ansi.Strip(l))
		}
	}
}

// topTitle is the title of the modal on top of the stack, or "".
func topTitle(m *Model) string {
	if md := m.topModal(); md != nil {
		return md.Title()
	}
	return ""
}

// mustContain fails when the screen does not contain every part.
func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Fatalf("screen should contain %q:\n%s", p, s)
		}
	}
}

// mustNotContain fails when the screen contains any part.
func mustNotContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(s, p) {
			t.Fatalf("screen should not contain %q:\n%s", p, s)
		}
	}
}

// stamp is a fixed reference time for memory ages in tests.
var stamp = time.Now().Add(-3 * time.Hour)
