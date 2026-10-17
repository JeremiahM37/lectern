package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// healthFake is the fake backend for the Health Check: a FakeBackend that also
// answers HealthChecks with fixed rows and counts how often it was asked.
type healthFake struct {
	*FakeBackend
	rows  []HealthBackend
	calls *int
}

func (f healthFake) HealthChecks() []HealthBackend {
	if f.calls != nil {
		*f.calls++
	}
	return f.rows
}

// checklistFake is a backend with only the older Diagnose checklist.
type checklistFake struct{ *FakeBackend }

func (checklistFake) Diagnose() []DiagnoseCheck {
	return []DiagnoseCheck{{Area: "Git", Name: "repository", State: "ok"}}
}

func sampleHealth() []HealthBackend {
	return []HealthBackend{
		{Name: "Model providers", Probes: []HealthProbe{
			{Name: "anthropic", State: "pass", Reason: "host accepts connections", Ms: 41},
			{Name: "openai", State: "fail", Reason: "not reachable: timeout", Fix: "check the network", Ms: -1},
		}},
		{Name: "Git", Probes: []HealthProbe{
			{Name: "repository", State: "warn", Reason: "branch main, uncommitted changes", Fix: "commit or stash", Ms: -1},
		}},
	}
}

// The popup shows each backend, each probe with its state, latency and reason,
// and the fix under a warn or fail.
func TestHealthPopupShowsProbeRows(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: sampleHealth()}
	out := p.View(120, 30)
	for _, want := range []string{
		"Model providers", "anthropic", "pass", "41ms", "host accepts connections",
		"openai", "fail", "not reachable: timeout", "→ check the network",
		"Git", "repository", "warn", "branch main, uncommitted changes", "→ commit or stash",
		"1 pass", "1 warn", "1 fail",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("health popup is missing %q:\n%s", want, out)
		}
	}
}

// A pass row has no fix line, even when a fix is set.
func TestHealthPopupHidesFixOnPass(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: []HealthBackend{{Name: "Disk", Probes: []HealthProbe{
		{Name: "project filesystem", State: "pass", Reason: "40 GB free", Fix: "not shown", Ms: -1},
	}}}}
	if out := p.View(120, 30); strings.Contains(out, "not shown") {
		t.Fatalf("a pass row showed its fix:\n%s", out)
	}
}

// A probe with no timing has no latency column, so the row is not padded with
// a made-up number.
func TestHealthMsIsBlankWhenUntimed(t *testing.T) {
	if got := healthMs(-1); got != "" {
		t.Fatalf("untimed probe shows %q", got)
	}
	if got := healthMs(7); got != "7ms" {
		t.Fatalf("timed probe shows %q, want 7ms", got)
	}
}

// Only Esc closes the popup. Other keys are swallowed, and the popup stays open.
func TestHealthPopupEscClosesOtherKeysStay(t *testing.T) {
	p := &healthPopup{th: theme.Default(), backends: sampleHealth()}
	if _, _, closed := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}); closed {
		t.Fatal("a stray key closed the Health Check")
	}
	if _, _, closed := p.Update(tea.KeyMsg{Type: tea.KeyEsc}); !closed {
		t.Fatal("Esc did not close the Health Check")
	}
}

// r starts a run even while one is in flight, and only the newest run's
// results land. An older run that finishes late is dropped.
func TestHealthPopupRerunDropsStaleResults(t *testing.T) {
	p := &healthPopup{th: theme.Default(), loading: true}
	_, cmd, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil || !p.loading || p.gen != 1 {
		t.Fatalf("r did not start a run: loading=%v gen=%d cmd=%v", p.loading, p.gen, cmd)
	}
	p.Update(healthDone{gen: 0, backends: sampleHealth()})
	if p.backends != nil || !p.loading {
		t.Fatalf("a stale run overwrote the popup: %+v", p.backends)
	}
	p.Update(healthDone{gen: 1, backends: sampleHealth()[:1]})
	if p.loading || len(p.backends) != 1 {
		t.Fatalf("the newest run did not land: loading=%v backends=%d", p.loading, len(p.backends))
	}
}

// The first run runs the backend's probes once, off the UI goroutine, and
// hands the rows back as a message.
func TestHealthPopupRunCallsBackendOnce(t *testing.T) {
	calls := 0
	be := healthFake{FakeBackend: NewFakeBackend(), rows: sampleHealth(), calls: &calls}
	p := &healthPopup{th: theme.Default(), be: be, loading: true}
	msg, ok := p.run()().(healthDone)
	if !ok || calls != 1 || len(msg.backends) != 2 {
		t.Fatalf("run gave %T, %d calls, %d backends", msg, calls, len(msg.backends))
	}
}

// While the first run is pending the popup says it is running, not empty.
func TestHealthPopupShowsRunningBeforeFirstResult(t *testing.T) {
	p := &healthPopup{th: theme.Default(), loading: true}
	if out := p.View(100, 20); !strings.Contains(out, "Running probes") {
		t.Fatalf("pending popup does not say it is running:\n%s", out)
	}
}

// The badge says how a backend stands at a glance.
func TestHealthBadgeStates(t *testing.T) {
	th := theme.Default()
	cases := []struct {
		name   string
		probes []HealthProbe
		want   string
	}{
		{"no probes yet", nil, "◌"},
		{"all fail", []HealthProbe{{State: "fail"}, {State: "fail"}}, "✗"},
		{"some pass, one fail", []HealthProbe{{State: "pass"}, {State: "fail"}}, "◐"},
		{"warn only", []HealthProbe{{State: "warn"}}, "◐"},
		{"all pass", []HealthProbe{{State: "pass"}, {State: "pass"}}, "●"},
		{"nothing applies", []HealthProbe{{State: "info"}}, "○"},
	}
	for _, c := range cases {
		got, _ := healthBadge(th, HealthBackend{Name: "x", Probes: c.probes})
		if got != c.want {
			t.Errorf("%s: badge %q, want %q", c.name, got, c.want)
		}
	}
}

// /diagnose opens the Health Check when the backend has one, and the older
// checklist otherwise.
func TestDiagnoseCommandOpensHealthCheck(t *testing.T) {
	m, fb := newTestModel(t, 120, 40)
	calls := 0
	m.cur().be = healthFake{FakeBackend: fb, rows: sampleHealth(), calls: &calls}
	feed(m, m.runCommand("/diagnose"))
	top, ok := m.modals[len(m.modals)-1].(*healthPopup)
	if !ok {
		t.Fatalf("/diagnose opened %T, want the Health Check", m.modals[len(m.modals)-1])
	}
	if calls != 1 || len(top.backends) != 2 {
		t.Fatalf("Health Check ran %d times with %d backends", calls, len(top.backends))
	}
}

// A backend without HealthChecks keeps the old checklist popup.
func TestDiagnoseCommandKeepsChecklistWithoutHealthCheck(t *testing.T) {
	m, fb := newTestModel(t, 120, 40)
	m.cur().be = checklistFake{FakeBackend: fb}
	m.runCommand("/diagnose")
	if _, ok := m.modals[len(m.modals)-1].(*diagnosePopup); !ok {
		t.Fatalf("/diagnose without HealthChecks opened %T", m.modals[len(m.modals)-1])
	}
}
