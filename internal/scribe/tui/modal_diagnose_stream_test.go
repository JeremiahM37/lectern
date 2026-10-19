package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// streamFake streams its rows one at a time, the way a language server probe
// finishes. release lets a test hold the last row back.
type streamFake struct {
	release chan struct{}
}

func (f streamFake) HealthChecks() []HealthBackend { return nil }

func (f streamFake) HealthChecksStream(report func([]HealthBackend)) []HealthBackend {
	first := []HealthBackend{{Name: "Git", Probes: []HealthProbe{{Name: "repository", State: "pass", Ms: -1}}}}
	report(first)
	if f.release != nil {
		<-f.release
	}
	out := append(first, HealthBackend{Name: "LSP go (tier 1)", Probes: []HealthProbe{{Name: "documentSymbol", State: "pass", Ms: 12}}})
	return out
}

// drive runs the popup's commands the way the event loop does, until the run
// ends or the step budget is spent.
func drive(p *healthPopup, cmd tea.Cmd, steps int) {
	for i := 0; i < steps && cmd != nil; i++ {
		msg := cmd()
		var next tea.Cmd
		_, next, _ = p.Update(msg)
		if !p.loading {
			return
		}
		cmd = next
	}
}

// A streaming run fills the popup in while it runs and ends with the final rows.
func TestHealthPopupStreamsPartialThenFinalRows(t *testing.T) {
	p := &healthPopup{th: theme.Default(), be: streamFake{}, loading: true}
	drive(p, p.run(), 200)
	if p.loading {
		t.Fatal("run did not end")
	}
	if len(p.backends) != 2 || p.backends[1].Name != "LSP go (tier 1)" || p.err != "" {
		t.Fatalf("final rows = %+v, err %q", p.backends, p.err)
	}
}

// A run that outlives the 90 s budget ends with the spec's error, and keeps the
// rows it had.
func TestHealthPopupTimesOutWithSpecError(t *testing.T) {
	old := healthPopupTimeout
	healthPopupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { healthPopupTimeout = old })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	p := &healthPopup{th: theme.Default(), be: streamFake{release: release}, loading: true}
	drive(p, p.run(), 400)
	if p.loading || p.err != "Health check timed out" {
		t.Fatalf("loading %v, err %q; want the timeout error", p.loading, p.err)
	}
	if len(p.backends) != 1 || p.backends[0].Name != "Git" {
		t.Fatalf("partial rows lost: %+v", p.backends)
	}
	if !strings.Contains(p.View(100, 40), "Health check timed out") {
		t.Fatal("the timeout error is not shown")
	}
}

// Re-running clears the old error and a stale poll from the old run is ignored.
func TestHealthPopupRerunClearsErrorAndIgnoresStalePoll(t *testing.T) {
	p := &healthPopup{th: theme.Default(), be: streamFake{}, err: "old", gen: 3}
	if _, _, handled := p.Update(healthPoll{gen: 2}); handled {
		t.Fatal("a poll should not close the popup")
	}
	if p.live != nil {
		t.Fatal("a stale poll must not start reading a run")
	}
	_, _, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if p.err != "" || !p.loading || p.gen != 4 {
		t.Fatalf("re-run state: err %q loading %v gen %d", p.err, p.loading, p.gen)
	}
}

// healthLive keeps the first final answer: a late finish after a timeout is
// ignored, and so is a partial that arrives after the end.
func TestHealthLiveFinishesOnce(t *testing.T) {
	l := &healthLive{}
	l.set([]HealthBackend{{Name: "A"}})
	l.finish([]HealthBackend{{Name: "B"}}, "")
	l.finish([]HealthBackend{{Name: "C"}}, "late")
	l.set([]HealthBackend{{Name: "D"}})
	bs, done, errText := l.snapshot()
	if !done || errText != "" || len(bs) != 1 || bs[0].Name != "B" {
		t.Fatalf("snapshot = %+v, %v, %q", bs, done, errText)
	}
}
