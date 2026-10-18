package tui

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"sync"
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
		recordShutdownErrors(EndSessions(bes, shutdownStopWait))
		return closedMsg{}
	}
}

// shutdownStopWait bounds how long the quit waits for running turns to stop
// before it moves on. Each session's close waits for its own save, so this
// only has to cover an abort taking effect.
const shutdownStopWait = 5 * time.Second

// MCPStopper stops a session's MCP servers as one shutdown step.
type MCPStopper interface{ StopMCP() error }

// LSPStopper stops a session's language servers as one shutdown step.
type LSPStopper interface{ StopLSP() error }

// serverStopWait bounds each server stop step.
const serverStopWait = 3 * time.Second

// ProviderCloser is implemented by backends that can release the connections
// their LLM providers hold open. It is the "deactivate provider" step of 06
// §8.3, and runs after every turn has stopped, so no request is cut short.
type ProviderCloser interface {
	CloseProviders() error
}

// ProcessGroupKiller is implemented by backends that own child process groups
// (background jobs). It is the last step of 06 §8.3, after the sessions are
// closed, and it kills whatever a session's close left running.
type ProcessGroupKiller interface {
	KillProcessGroups() error
}

// Step bounds. Each step runs in its own goroutine and is abandoned after its
// bound, so a stuck provider or child process cannot hold the exit. These are
// variables so tests can shorten them.
var (
	providerCloseWait = 2 * time.Second
	jobKillWait       = 5 * time.Second
	sessionCloseWait  = 10 * time.Second
	processGroupWait  = 5 * time.Second
)

// shutdownErrs keeps what the quit path could not do cleanly, for the caller
// to print once the terminal is restored. The quit screen is still drawing,
// so nothing is written during the shutdown itself.
var shutdownErrs struct {
	sync.Mutex
	list []error
}

// ShutdownErrors returns the failed or timed-out shutdown steps from the last
// quit, oldest first. Each step is best-effort, so a failure never stops the
// exit; it is only reported here.
func ShutdownErrors() []error {
	shutdownErrs.Lock()
	defer shutdownErrs.Unlock()
	return append([]error(nil), shutdownErrs.list...)
}

// recordShutdownErrors adds the failures of one EndSessions call.
func recordShutdownErrors(errs []error) {
	if len(errs) == 0 {
		return
	}
	shutdownErrs.Lock()
	defer shutdownErrs.Unlock()
	shutdownErrs.list = append(shutdownErrs.list, errs...)
}

// EndSessions ends every session as the process leaves, in the order the spec
// gives (06 §8.3). The order matters across tabs, not just within one:
//
//  1. Abort every running turn and wait for them all to stop. A turn still
//     running would call a provider or an MCP tool after that server closed.
//  2. Close the idle provider connections ("deactivate provider"). No turn is
//     running any more, so nothing is cut short.
//     The intelligence-router step has no counterpart in Scribe: there is no
//     router object to dispose. A dispatch's subagents run under the turn's
//     context, so step 1 stops them too.
//  3. Kill the background jobs still running, so no child process outlives
//     its session.
//  4. Close each session. Its close saves the transcript, then stops its MCP
//     servers and language servers. Every save has happened by now, so no
//     tab's MCP shuts down under another tab's unfinished turn.
//  5. Kill the process groups that step 4 left behind.
//
// Every step is bounded and best-effort. A step that fails or overruns is
// returned in the error list, and the next step still runs.
func EndSessions(list []Backend, timeout time.Duration) []error {
	var errs []error
	note := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, be := range list {
		be.Abort()
	}
	if !waitIdle(list, timeout) {
		note(fmt.Errorf("stop running turns: still running after %s", timeout))
	}
	for _, be := range list {
		if p, ok := be.(ProviderCloser); ok {
			note(boundedStep("close providers", providerCloseWait, p.CloseProviders))
		}
	}
	// MCP servers and language servers stop as their own steps (spec 06
	// §8.3), before each session closes; Close stopping them again is a
	// no-op.
	for _, be := range list {
		if s, ok := be.(MCPStopper); ok {
			note(boundedStep("stop MCP servers", serverStopWait, s.StopMCP))
		}
	}
	for _, be := range list {
		if s, ok := be.(LSPStopper); ok {
			note(boundedStep("stop language servers", serverStopWait, s.StopLSP))
		}
	}
	for _, be := range list {
		note(boundedStep("kill background jobs", jobKillWait, func() error {
			killRunningJobs(be)
			return nil
		}))
	}
	for _, be := range list {
		note(boundedStep("close session", sessionCloseWait, be.Close))
	}
	for _, be := range list {
		if k, ok := be.(ProcessGroupKiller); ok {
			note(boundedStep("kill process groups", processGroupWait, k.KillProcessGroups))
		}
	}
	return errs
}

// boundedStep runs fn and waits at most wait for it. A step that overruns is
// abandoned: its goroutine finishes on its own, and the exit does not wait for
// it. A panic is returned as an error, so one bad step does not stop the rest.
func boundedStep(name string, wait time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- fn()
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("%s: no answer after %s, abandoned", name, wait)
	}
}

// waitIdle polls until no backend in list is running, or timeout passes. It
// reports whether every backend went idle in time.
func waitIdle(list []Backend, timeout time.Duration) bool {
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
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
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
