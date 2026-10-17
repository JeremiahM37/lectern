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

// startShutdown switches to the quit screen and ends every tab, in the order
// the spec gives (06 §8.3): terminals first, then running turns, child
// processes, and last each session's close, which saves it and then stops its
// MCP servers and language servers. See EndSessions.
func (m *Model) startShutdown() tea.Cmd {
	m.phase = phaseShutdown
	var bes []Backend
	for _, t := range m.tabs {
		bes = append(bes, t.be)
	}
	// Terminals and the editor run their own programs, and they must be gone
	// before the backends stop. This runs here, on the UI goroutine, because it
	// changes the model.
	m.closePanes()
	// The resume command is printed once, by SavedBannerLines after the
	// terminal is restored, with the full session id. The quit screen does not
	// show a second, shorter one.
	if id := m.cur().be.SessionID(); id != "" {
		m.resumeLine = "resume: lectern code -s " + id
	}
	m.sd = &shutdownState{start: time.Now(), backends: bes}
	return func() tea.Msg {
		EndSessions(bes, shutdownStopWait)
		return closedMsg{}
	}
}

// shutdownStopWait bounds how long the quit waits for running turns to stop
// before it moves on. Each session's close waits for its own save, so this
// only has to cover an abort taking effect.
const shutdownStopWait = 5 * time.Second

// EndSessions ends every session as the process leaves, in the order the spec
// gives (06 §8.3). The order matters across tabs, not just within one:
//
//  1. Abort every running turn and wait for them all to stop. A turn still
//     running would call a provider or an MCP tool after that server closed.
//  2. Kill the background jobs still running, so no child process outlives
//     its session.
//  3. Close each session. Its close saves the transcript, then stops its MCP
//     servers and language servers. Every save has happened by now, so no
//     tab's MCP shuts down under another tab's unfinished turn.
//
// The Backend interface has no step for the providers or the intelligence
// router, so those close inside Close, not as their own steps here.
func EndSessions(list []Backend, timeout time.Duration) {
	for _, be := range list {
		be.Abort()
	}
	waitIdle(list, timeout)
	for _, be := range list {
		killRunningJobs(be)
	}
	for _, be := range list {
		_ = be.Close()
	}
}

// waitIdle polls until no backend in list is running, or timeout passes.
func waitIdle(list []Backend, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		busy := false
		for _, be := range list {
			if be.Running() {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// killRunningJobs kills every background job of one session that is still
// running. A job that already exited is left alone.
func killRunningJobs(be Backend) {
	for _, j := range be.Jobs() {
		if j.Status == "running" {
			_ = be.KillJob(j.ID)
		}
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
