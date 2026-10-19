package tui

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
)

// w41Home points the frecency and error stores at a temp dir for one test.
func w41Home(t *testing.T) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
}

// Ctrl+R hits used before come first, in frecency order; unpicked hits keep
// newest-first order behind them.
func TestW41SearchRanksPickedHistoryFirst(t *testing.T) {
	w41Home(t)
	s := newInputState()
	s.history = []string{"alpha one", "beta two", "alpha three"}
	s.openSearch()
	s.search.query = []rune("alpha")
	s.refreshSearch()
	if got := s.search.matches; len(got) != 2 || got[0] != "alpha three" || got[1] != "alpha one" {
		t.Fatalf("unranked search = %q, want newest first", got)
	}
	if err := session.RecordPick(session.PickHistory, "alpha one"); err != nil {
		t.Fatal(err)
	}
	s.openSearch()
	s.search.query = []rune("alpha")
	s.refreshSearch()
	if got := s.search.matches; len(got) != 2 || got[0] != "alpha one" || got[1] != "alpha three" {
		t.Fatalf("ranked search = %q, want the picked line first", got)
	}
}

// Enter on a Ctrl+R hit records the pick, so the next search ranks it.
func TestW41SearchEnterRecordsPick(t *testing.T) {
	w41Home(t)
	m := &Model{}
	tb := &tab{in: newInputState()}
	tb.in.history = []string{"deploy the thing", "unrelated"}
	tb.in.openSearch()
	tb.in.search.query = []rune("deploy")
	tb.in.refreshSearch()
	m.searchKey(tb, tea.KeyMsg{Type: tea.KeyEnter})
	if tb.in.search != nil {
		t.Fatal("enter should close the search")
	}
	keys, err := session.TopPicks(session.PickHistory, 0)
	if err != nil || len(keys) != 1 || keys[0] != "deploy the thing" {
		t.Fatalf("recorded picks = %q, %v", keys, err)
	}
}

// The errors view keeps the newest maxErrorRows rows and drops the oldest.
func TestW41ErrorLogCapDropsOldest(t *testing.T) {
	m := &Model{}
	for i := 0; i < maxErrorRows+5; i++ {
		m.addError("main", srcTool, fmt.Sprintf("fault %d", i), time.Unix(int64(i), 0))
	}
	if len(m.errLog) != maxErrorRows {
		t.Fatalf("rows = %d, want %d", len(m.errLog), maxErrorRows)
	}
	if first := m.errLog[0].Text; first != "fault 5" {
		t.Fatalf("oldest kept = %q, want fault 5", first)
	}
	if last := m.errLog[len(m.errLog)-1].Text; last != fmt.Sprintf("fault %d", maxErrorRows+4) {
		t.Fatalf("newest kept = %q", last)
	}
}

// originBackend is a fake backend that reports the folder its session was
// saved in.
type originBackend struct {
	*FakeBackend
	saved string
}

func (o originBackend) SessionWorkdir() string { return o.saved }

// A session opened from another folder gets the foreign header; one from the
// current folder keeps whatever header the transcript already has.
func TestW41ForeignSessionHeader(t *testing.T) {
	be := originBackend{FakeBackend: NewFakeBackend(), saved: "/elsewhere/project"}
	tb := &tab{be: be, chat: chat.New(chat.Options{})}
	applyForeignHeader(tb)
	if h := tb.chat.SessionHeader(); h != "from another project: /elsewhere/project" {
		t.Fatalf("foreign header = %q", h)
	}

	same := originBackend{FakeBackend: NewFakeBackend(), saved: NewFakeBackend().Workdir()}
	tc := &tab{be: same, chat: chat.New(chat.Options{})}
	tc.chat.SetSessionHeader("resumed after compaction")
	applyForeignHeader(tc)
	if h := tc.chat.SessionHeader(); h != "resumed after compaction" {
		t.Fatalf("same-folder header = %q, want the compaction header kept", h)
	}
}

// chatBudgetBackend records what the per-chat budget row saves.
type chatBudgetBackend struct {
	*FakeBackend
	limit, saved int
	calls        int
}

func (c *chatBudgetBackend) ChatBudget() ChatBudget {
	return ChatBudget{Today: 1000, Limit: c.limit}
}

func (c *chatBudgetBackend) SetChatBudget(n int) error {
	c.calls++
	c.saved = n
	c.limit = n
	return nil
}

// The per-chat budget row shows the limit and saves typed values through
// SetChatBudget; a bad value saves nothing.
func TestW41ChatBudgetRow(t *testing.T) {
	be := &chatBudgetBackend{FakeBackend: NewFakeBackend(), limit: 2000}
	s := &settings{be: be}
	row, ok := s.chatBudgetRow()
	if !ok || row.label != "This chat per day" || row.value != "2.0k" {
		t.Fatalf("row = %+v ok=%v", row, ok)
	}

	s.setChatBudget("500k")
	if be.calls != 1 || be.saved != 500000 || s.noteKind != "ok" {
		t.Fatalf("500k: calls=%d saved=%d note=%q/%s", be.calls, be.saved, s.note, s.noteKind)
	}
	s.setChatBudget("off")
	if be.saved != 0 {
		t.Fatalf("off saved %d, want 0", be.saved)
	}
	s.setChatBudget("lots")
	if be.calls != 2 || s.noteKind != "err" {
		t.Fatalf("bad value: calls=%d note=%q/%s", be.calls, s.note, s.noteKind)
	}
}

// A backend without a per-chat budget gets no row.
func TestW41ChatBudgetRowAbsent(t *testing.T) {
	s := &settings{be: NewFakeBackend()}
	if _, ok := s.chatBudgetRow(); ok {
		t.Fatal("row shown for a backend with no per-chat budget")
	}
}
