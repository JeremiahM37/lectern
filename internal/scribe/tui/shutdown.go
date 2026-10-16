package tui

import (
	"context"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// Stopper is implemented by backends that end their session when the process
// is leaving, whether the user quit or a signal arrived. app.scribeBackend
// implements it.
type Stopper interface {
	// Shutdown aborts a running turn, waits up to timeout for it to save, and
	// closes the session.
	Shutdown(timeout time.Duration)
}

// SavedBanner is the line printed once the terminal is restored, so the user
// can find the session again. The id is the full session id, so `-s` matches
// it exactly.
func SavedBanner(id string) string {
	return "Session saved: " + id + " — resume with: lectern code -s " + id
}

// WithTermSignals returns a context that is cancelled when the terminal is
// asked to stop (SIGTERM) or goes away (SIGHUP). Cancelling it makes bubbletea
// kill the program, which restores the terminal before Run returns; the caller
// then ends the sessions and prints SavedBanner.
func WithTermSignals(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGHUP)
}

// shutdownState drives the quit screen: saving, closing servers, done, resume hint.
type shutdownState struct {
	start    time.Time
	step     int       // 0 saving, 1 closing servers, 2 done, 3 resume hint shown
	doneAt   time.Time // when the backends finished closing
	resume   string
	backends []Backend
}

// startShutdown switches to the quit screen and closes every tab's backend.
func (m *Model) startShutdown() tea.Cmd {
	m.phase = phaseShutdown
	var bes []Backend
	for _, t := range m.tabs {
		bes = append(bes, t.be)
	}
	resume := ""
	if id := m.cur().be.SessionID(); id != "" {
		resume = "lectern code --resume " + short(id)
		m.resumeLine = "resume: " + resume
	}
	m.sd = &shutdownState{start: time.Now(), resume: resume, backends: bes}
	return func() tea.Msg {
		for _, be := range bes {
			_ = be.Close()
		}
		return closedMsg{}
	}
}

// finishShutdown is called when every backend has closed.
func (m *Model) finishShutdown() tea.Cmd {
	if m.sd == nil {
		return nil
	}
	m.sd.step = 2
	m.sd.doneAt = time.Now()
	return nil
}

// tickShutdown advances the quit screen and quits when it is done.
func (m *Model) tickShutdown(now time.Time) tea.Cmd {
	sd := m.sd
	if sd.step == 0 && now.Sub(sd.start) >= 250*time.Millisecond {
		sd.step = 1
	}
	if sd.step == 2 && now.Sub(sd.doneAt) >= 400*time.Millisecond {
		sd.step = 3
	}
	if sd.step == 3 && now.Sub(sd.doneAt) >= 1400*time.Millisecond {
		return tea.Quit
	}
	return nil
}

// viewShutdown is the quit screen: wordmark, the steps and the resume hint.
func (m *Model) viewShutdown() string {
	th := m.th
	sd := m.sd
	var lines []string
	if m.width >= 36 {
		for _, l := range wordmarkRows() {
			lines = append(lines, lipgloss.PlaceHorizontal(m.width, lipgloss.Center, fg(th.Brand).Render(l)))
		}
	} else {
		lines = append(lines, lipgloss.PlaceHorizontal(m.width, lipgloss.Center, fgb(th.Brand).Render("SCRIBE")))
	}
	lines = append(lines, "")
	steps := []struct {
		text string
		at   int
	}{
		{"Saving session", 0},
		{"Closing language servers", 1},
		{"Done", 2},
	}
	for _, s := range steps {
		mark := fg(th.TextDim).Render("·")
		col := th.TextDim
		switch {
		case sd.step > s.at:
			mark = fg(th.Success).Render("✓")
			col = th.TextMuted
		case sd.step == s.at:
			mark = fg(th.Brand).Render(render.Frame(render.Braille, m.frame/3))
			col = th.TextPrimary
		}
		row := mark + " " + fg(col).Render(s.text)
		lines = append(lines, lipgloss.PlaceHorizontal(m.width, lipgloss.Center, row))
	}
	if sd.step >= 3 && sd.resume != "" {
		lines = append(lines, "", lipgloss.PlaceHorizontal(m.width, lipgloss.Center,
			fg(th.TextSecondary).Render("resume: ")+fgb(th.BrandAlt).Render(sd.resume)))
	}
	top := (m.height - len(lines)) / 2
	out := make([]string, 0, m.height)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	return strings.Join(fitLines(out, m.width, m.height), "\n")
}
