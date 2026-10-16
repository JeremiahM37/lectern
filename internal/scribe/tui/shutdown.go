package tui

import (
	"context"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Quit screen timing (spec 1, ShutdownSplash), measured from the moment the
// backends finish closing: phase 3 shows the resume hint 350 ms later, and the
// program exits 1000 ms after that.
const (
	shutdownHintAfter = 350 * time.Millisecond
	shutdownQuitAfter = 1350 * time.Millisecond
	shutdownAbortAt   = 250 * time.Millisecond // phase 1: running turns are stopped
	shutdownFade      = 3200 * time.Millisecond
)

// Stopper is implemented by backends that end their session when the process
// is leaving, whether the user quit or a signal arrived. app.scribeBackend
// implements it.
type Stopper interface {
	// Shutdown aborts a running turn, waits up to timeout for it to save, and
	// closes the session.
	Shutdown(timeout time.Duration)
}

// EmergencySaver is implemented by backends that can write their session to
// disk at once, without aborting or waiting for a running turn. It is the
// last resort after a crash (spec 05 §2.2): a panic must not lose the
// conversation that a signal would have saved.
type EmergencySaver interface {
	EmergencySave()
}

// EmergencySaveAll calls EmergencySave on every backend that has it. A save
// that panics is contained, so the others still run.
func EmergencySaveAll(list []Backend) {
	for _, be := range list {
		s, ok := be.(EmergencySaver)
		if !ok {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			s.EmergencySave()
		}()
	}
}

// SavedBanner is the line printed once the terminal is restored, so the user
// can find the session again. The id is the full session id, so `-s` matches
// it exactly.
func SavedBanner(id string) string {
	return "Session saved: " + id + " — resume with: lectern code -s " + id
}

// SavedBannerLines is what the caller prints for one session once the
// terminal is restored: SavedBanner, plus a line when the newest turn never
// finished (it was aborted, or the process stopped during it), saying how to
// finish it. events is the session transcript, as Backend.Transcript returns it.
func SavedBannerLines(id string, events []agent.Event) []string {
	lines := []string{SavedBanner(id)}
	if LastTurnInterrupted(events) {
		lines = append(lines, "The last turn was interrupted. Finish it with: lectern code -c, "+
			"or /session continue inside the session.")
	}
	return lines
}

// LastTurnInterrupted reports whether the newest turn in events did not end
// normally: its turn_done says it was aborted, or it has a turn_start with no
// turn_done after it (the process was stopped mid-turn). An empty transcript
// is not interrupted.
func LastTurnInterrupted(events []agent.Event) bool {
	open, aborted := false, false
	for _, e := range events {
		switch e.Kind {
		case agent.EvTurnStart:
			open, aborted = true, false
		case agent.EvTurnDone:
			open = false
			aborted = e.Stats != nil && e.Stats.Aborted
		}
	}
	return open || aborted
}

// WithTermSignals returns a context that is cancelled when the terminal is
// asked to stop (SIGINT, SIGTERM) or goes away (SIGHUP). In raw mode Ctrl+C is
// a key, not a signal, so SIGINT here comes from outside (kill -INT). Cancelling
// the context makes bubbletea kill the program, which restores the terminal
// before Run returns; the caller then ends the sessions and prints SavedBanner.
func WithTermSignals(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
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
	if sd.step == 0 && now.Sub(sd.start) >= shutdownAbortAt {
		sd.step = 1
	}
	if sd.step == 2 && now.Sub(sd.doneAt) >= shutdownHintAfter {
		sd.step = 3
	}
	if sd.step == 3 && now.Sub(sd.doneAt) >= shutdownQuitAfter {
		return tea.Quit
	}
	return nil
}

// shutdownFadeColor is the wordmark colour at time since the quit screen
// opened. It walks amber, warning, brandAlt, brand and brandDim over 3.2 s,
// so the screen cools as it closes.
func shutdownFadeColor(th theme.Theme, since time.Duration) lipgloss.TerminalColor {
	stops := []lipgloss.TerminalColor{th.Amber, th.Warning, th.BrandAlt, th.Brand, th.BrandDim}
	p := clampF(float64(since)/float64(shutdownFade), 0, 1) * float64(len(stops)-1)
	i := int(p)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	return theme.Lerp(stops[i], stops[i+1], p-float64(i))
}

// shutdownRow is one line of the quit screen's progress list.
type shutdownRow struct {
	text string
	// done and active are the steps at which the row is ticked and spun.
	done, active int
}

// shutdownRows is the four progress steps. The last two steps follow the
// backend closing, so they are ticked together when the backends report done.
var shutdownRows = []shutdownRow{
	{"Stopping running turns", 1, 0},
	{"Saving session", 2, 1},
	{"Closing language servers", 2, 1},
	{"Done", 3, 2},
}

// viewShutdown is the quit screen: wordmark, the steps and the resume hint.
func (m *Model) viewShutdown() string {
	th := m.th
	sd := m.sd
	var lines []string
	mark := shutdownFadeColor(th, time.Since(sd.start))
	if m.width >= landingWordmarkCols {
		for _, l := range wordmarkRows() {
			lines = append(lines, lipgloss.PlaceHorizontal(m.width, lipgloss.Center, fgb(mark).Render(l)))
		}
	} else {
		lines = append(lines, lipgloss.PlaceHorizontal(m.width, lipgloss.Center, fgb(mark).Render("SCRIBE")))
	}
	lines = append(lines, "")
	// the steps are one block: its widest row is centred and the rows stay
	// left-aligned to each other, so the marks line up
	var steps []string
	block := 0
	for _, s := range shutdownRows {
		glyph := fg(th.TextDim).Render("·")
		col := th.TextDim
		switch {
		case sd.step >= s.done:
			glyph = fg(th.Success).Render("✓")
			col = th.TextMuted
		case sd.step == s.active:
			glyph = fg(th.Brand).Render(render.Frame(render.Braille, m.frame/3))
			col = th.TextPrimary
		}
		row := glyph + " " + fg(col).Render(s.text)
		steps = append(steps, row)
		block = max(block, ansi.StringWidth(row))
	}
	pad := strings.Repeat(" ", max(0, (m.width-block)/2))
	for _, row := range steps {
		lines = append(lines, pad+row)
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
