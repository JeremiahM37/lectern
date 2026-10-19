package tui

import (
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// BackgroundError is a fault outside a turn: a server that did not start, a
// hook, the auto-check. The backend queues them; the error log shows each with
// its source.
type BackgroundError struct {
	Source, Text string
	At           time.Time
}

// Error log sources. A row names where its fault came from.
const (
	srcTurn       = "turn"       // the model call or the turn failed
	srcTool       = "tool"       // a tool call returned an error
	srcAutocheck  = "autocheck"  // the post-edit check failed
	srcCompaction = "compaction" // the context could not be compacted
	srcScribe     = "scribe"     // an action in Scribe itself failed
)

// Background faults are polled this often. Memory's stale count is cheap but
// it reads SQLite, so it is refreshed less often.
const (
	bgPollEvery    = 2 * time.Second
	staleRefreshAt = 30 * time.Second
)

// maxErrorRows caps the error log. The oldest rows go first, so a long session
// keeps its recent faults and the errors view stays short enough to scan.
const maxErrorRows = 200

// addError records one fault in the error log. A repeat of a fault already in
// the log (same source, tab and text) does not add a row: the row moves to the
// newest place and counts the repeat, so a tool that fails in a loop cannot
// flood the list.
func (m *Model) addError(tab, source, text string, at time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	for i := len(m.errLog) - 1; i >= 0; i-- {
		e := m.errLog[i]
		if e.Source == source && e.Tab == tab && e.Text == text {
			e.Count++
			e.At = at
			m.errLog = append(append(m.errLog[:i:i], m.errLog[i+1:]...), e)
			return
		}
	}
	m.errLog = append(m.errLog, errEntry{At: at, Tab: tab, Source: source, Text: text, Count: 1})
	m.capErrorLog()
}

// capErrorLog drops the oldest rows once the log is over maxErrorRows.
func (m *Model) capErrorLog() {
	if n := len(m.errLog); n > maxErrorRows {
		m.errLog = append([]errEntry(nil), m.errLog[n-maxErrorRows:]...)
	}
}

// logErrorFrom records a fault from source against tab t and marks the tab
// when it is not the one on screen.
func (m *Model) logErrorFrom(t *tab, source, text string) {
	m.addError(t.name, source, text, time.Now())
	if t != m.cur() {
		t.errFlag = true
	}
}

// toolErrorEntry is the error log text for a failed tool call: the tool name
// and the first line of its output, which is what the model also reads first.
// It returns the source too: an auto-check failure is its own source.
func toolErrorEntry(ev agent.Event) (source, text string) {
	first := ""
	if ev.Result != nil {
		first = strings.TrimSpace(ev.Result.Output)
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
	}
	if len(first) > 160 {
		first = first[:157] + "..."
	}
	if ev.Tool == "auto_check" {
		if first == "" {
			first = "the check failed"
		}
		return srcAutocheck, "auto-check: " + first
	}
	tool := ev.Tool
	if tool == "" {
		tool = "tool"
	}
	if first == "" {
		first = "returned an error with no message"
	}
	return srcTool, tool + ": " + first
}

// pollBackground drains each tab's background faults and, every 30 s,
// refreshes each tab's stale-memory count. It runs from the tick, so it never
// blocks a frame for longer than a store read.
func (m *Model) pollBackground(now time.Time) {
	if now.Sub(m.lastBG) >= bgPollEvery {
		m.lastBG = now
		for _, t := range m.tabs {
			for _, e := range t.be.BackgroundErrors() {
				m.logErrorFrom(t, e.Source, e.Text)
				m.errLog[len(m.errLog)-1].At = e.At
			}
		}
	}
	if now.Sub(m.lastStale) >= staleRefreshAt {
		m.lastStale = now
		for _, t := range m.tabs {
			t.stale = t.be.StaleMemories()
		}
	}
}
