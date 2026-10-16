package tui

import (
	"context"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// styledFake is the fake backend with reasoning styles on its models, which
// the stock fake leaves out.
type styledFake struct {
	*FakeBackend
	styles map[string]string // provider -> style, applied to every model
}

func (f styledFake) Models(ctx context.Context, provider string) ([]llm.ModelInfo, error) {
	ms, err := f.FakeBackend.Models(ctx, provider)
	for i := range ms {
		ms[i].ReasoningStyle = f.styles[provider]
	}
	return ms, err
}

// newStyledModel is newTestModel with a backend whose models carry styles.
func newStyledModel(t *testing.T, model string, styles map[string]string) (*Model, *FakeBackend) {
	t.Helper()
	fb := NewFakeBackend()
	fb.Delay = 0
	fb.SetModel(model)
	m := NewModel(styledFake{FakeBackend: fb, styles: styles}, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: 100, Height: 40, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m, fb
}

// waitStyle runs the pending style lookup so the Reasoning tab has its answer.
func waitStyle(t *testing.T, m *Model, s *settings) {
	t.Helper()
	s.tab = stReasoning
	// openSettings drops the command the screen returns, so the lookup has not
	// run yet: clear the in-flight mark and start it again, as a user would.
	delete(s.loading, s.be.Model())
	drain(m, s.ensureStyle())
	if _, ok := s.styles[s.be.Model()]; !ok {
		t.Fatal("style lookup did not finish")
	}
}

func rowByLabel(s *settings, label string) (setRow, bool) {
	for _, r := range s.rows() {
		if r.label == label {
			return r, true
		}
	}
	return setRow{}, false
}

// styleFor gives the fake providers the reasoning style of their models: the
// anthropic models take a budget, openai takes effort, ollama has none.
var styleFor = map[string]string{"anthropic": llm.ReasoningBudget, "openai": llm.ReasoningEffort}

func TestReasoningTabShowsOnlyTheStyleControls(t *testing.T) {
	cases := []struct {
		name    string
		model   string // provider/model the fake backend reports
		want    []string
		notWant []string
	}{
		{"budget model gets a budget", "anthropic/claude-sonnet-5", []string{"Budget", "Service tier"}, []string{"Effort"}},
		{"effort model gets effort", "openai/gpt-6", []string{"Effort", "Service tier"}, []string{"Budget"}},
		{"no style gets no controls", "ollama/qwen3.6:35b-a3b", nil, []string{"Budget", "Effort"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newStyledModel(t, c.model, styleFor)
			s := openSettings(t, m)
			waitStyle(t, m, s)
			labels := rowLabels(s)
			for _, w := range c.want {
				if _, ok := rowByLabel(s, w); !ok {
					t.Fatalf("want row %q, rows: %v", w, labels)
				}
			}
			for _, nw := range c.notWant {
				if _, ok := rowByLabel(s, nw); ok {
					t.Fatalf("row %q should not show for %s, rows: %v", nw, c.model, labels)
				}
			}
		})
	}
}

func TestReasoningTabWritesPerProviderKeys(t *testing.T) {
	m, fb := newStyledModel(t, "anthropic/claude-sonnet-5", styleFor)
	s := openSettings(t, m)
	waitStyle(t, m, s)

	selectRow(t, s, "Budget")
	send(m, "enter") // default -> 1024
	if got, want := lastWrite(t, fb), (ConfigWrite{Key: "reasoning_by_provider.anthropic.budget", Value: 1024}); !reflect.DeepEqual(got, want) {
		t.Fatalf("budget write: got %+v want %+v", got, want)
	}

	selectRow(t, s, "Service tier")
	send(m, "enter") // default -> auto
	if got := lastWrite(t, fb); got.Key != "reasoning_by_provider.anthropic.service_tier" || got.Value != "auto" {
		t.Fatalf("service tier write: %+v", got)
	}
}

func TestReasoningEffortCyclesAndClears(t *testing.T) {
	m, fb := newStyledModel(t, "openai/gpt-6", styleFor)
	s := openSettings(t, m)
	waitStyle(t, m, s)
	selectRow(t, s, "Effort")
	send(m, "enter") // default -> low
	if got := lastWrite(t, fb); got.Key != "reasoning_by_provider.openai.effort" || got.Value != "low" {
		t.Fatalf("effort: %+v", got)
	}
	for i := 0; i < 3; i++ { // low -> medium -> high -> default
		send(m, "enter")
	}
	if got := lastWrite(t, fb); got.Value != "" {
		t.Fatalf("cycling past high should clear the effort to default, got %+v", got)
	}
}

func TestReasoningTabStartsLookupWhenOpened(t *testing.T) {
	m, _ := newStyledModel(t, "anthropic/claude-sonnet-5", styleFor)
	s := openSettings(t, m)
	// Until the lookup returns, the tab says so instead of showing controls.
	s.tab = stReasoning
	if _, ok := rowByLabel(s, "Reading controls…"); !ok {
		t.Fatalf("while the lookup runs the tab should say so, rows: %v", rowLabels(s))
	}
	waitStyle(t, m, s)
	if st := s.styles["anthropic/claude-sonnet-5"]; st.style != llm.ReasoningBudget {
		t.Fatalf("style for the fake anthropic model should be budget, got %q", st.style)
	}
}

// A lookup that fails is shown on the tab, not hidden.
func TestReasoningTabShowsLookupError(t *testing.T) {
	m, _ := newStyledModel(t, "nobody/none", styleFor)
	s := openSettings(t, m)
	waitStyle(t, m, s)
	r, ok := rowByLabel(s, "Model details unavailable")
	if !ok || r.note == "" {
		t.Fatalf("a failed lookup should show the error, rows: %v", rowLabels(s))
	}
}
