package console

import (
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// RunScrollback reads a snapshot without sending navigation keys to the agent.
// The attachment keeps collecting output and redraws its live screen on return.
func RunScrollback(text string, in io.Reader, out io.Writer) error {
	m := newScrollback(text, 80, 24)
	_, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

type scrollbackView struct {
	viewport  viewport.Model
	firstSize bool
}

func newScrollback(text string, width, height int) *scrollbackView {
	v := viewport.New(max(1, width), max(1, height-1))
	v.SetContent(strings.TrimRight(text, "\n"))
	v.GotoBottom()
	return &scrollbackView{viewport: v}
}
func (m *scrollbackView) Init() tea.Cmd { return nil }
func (m *scrollbackView) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width, m.viewport.Height = max(1, msg.Width), max(1, msg.Height-1)
		if !m.firstSize {
			m.viewport.GotoBottom()
			m.firstSize = true
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "home", "g":
			m.viewport.GotoTop()
			return m, nil
		case "end", "G":
			m.viewport.GotoBottom()
			return m, nil
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	var command tea.Cmd
	m.viewport, command = m.viewport.Update(message)
	return m, command
}
func (m *scrollbackView) View() string {
	footer := ansi.Truncate("Scrollback · ↑↓ scroll · PgUp/PgDn page · q/Esc return", m.viewport.Width, "")
	return m.viewport.View() + "\n" + footer
}
