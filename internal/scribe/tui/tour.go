package tui

// The first-launch welcome tour: eleven short pages on what Lectern does. It
// opens once, on the first launch, and /tour opens it again. It is a modal, but
// it never holds the input: Esc skips it, and any other key closes it and is
// handled as it would be without the tour, so the first letter typed is not
// lost.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// tourPage is one page of the tour: a heading and a few short lines.
type tourPage struct {
	heading string
	lines   []string
}

// tourPages are the eleven pages, in reading order. Each line is kept under
// tourLineWidth cells so the page fits the tour box at 80 columns.
var tourPages = []tourPage{
	{"Welcome to Lectern", []string{
		"Lectern is a coding agent for your terminal.",
		"It reads your code, edits files and runs commands.",
		"It asks before anything risky, and records what it changed.",
	}},
	{"Talking to Lectern", []string{
		"Type a message and press Enter to send it.",
		"Press Enter while Lectern works to queue a steering message.",
		"Shift+Enter or Ctrl+J adds a line without sending.",
		"Type / to list every command, or ? for the key reference.",
	}},
	{"Tools and approvals", []string{
		"Tools let Lectern read, search, edit and run commands.",
		"A risky tool call stops and waits for your answer in a prompt.",
		"Answer the prompt to approve it or to deny it.",
	}},
	{"Codex with your ChatGPT plan", []string{
		"/codex signs you in with your ChatGPT subscription.",
		"Run it with no argument to see the sign-in status.",
		"It can also sign you out, or switch to another account.",
	}},
	{"Switching models", []string{
		"Ctrl+L opens the model picker.",
		"/model provider/model switches straight to one model.",
		"/keys adds or changes the API key for a provider.",
	}},
	{"Checkpoints", []string{
		"Every turn that changes files leaves a checkpoint.",
		"Ctrl+B steps back to an earlier checkpoint, Ctrl+F steps forward.",
		"Ctrl+F past the last checkpoint returns to the live session.",
		"/checkpoints lists them. Ctrl+Z undoes the last turn and its files.",
	}},
	{"Status and diagnosis", []string{
		"/status shows the model, mode, context, git, servers and jobs.",
		"/diagnose checks providers, language and MCP servers, git and disk.",
		"Run either one when something looks wrong.",
	}},
	{"Finding things fast", []string{
		"Ctrl+K opens the command palette. Type to filter it.",
		"Ctrl+R searches your prompt history as you type.",
		"Ctrl+P opens earlier sessions, and Ctrl+N starts a new one.",
	}},
	{"Plans and subagents", []string{
		"Ctrl+D or Shift+Tab cycles the mode. Plan mode only researches.",
		"/plan <task> has Lectern research and plan the task first.",
		"Subagents take focused jobs in parallel.",
		"A long session compacts its history. /compact does it now.",
	}},
	{"Settings", []string{
		"/settings views and changes the settings.",
		"/mode shows or switches the working mode.",
		"/scope moves one setting to the project or the global config.",
		"/theme previews and changes the colours.",
	}},
	{"The checkpoint rail", []string{
		"Alt+C shows or hides the checkpoint rail.",
		"Up and Down move along it, Enter restores a checkpoint.",
		"Esc leaves the rail. /tour opens this tour again.",
	}},
}

// tourLineWidth is the widest line a tour page may show, in cells.
const tourLineWidth = 70

func init() {
	registerCommands(
		&command{Name: "tour", Desc: "Open the welcome tour again, page by page", Group: "Help",
			Run: func(m *Model, args string) tea.Cmd { m.openTour(); return nil }},
	)
}

// tourStateFile is the file in the Lectern home that records the tour.
const tourStateFile = "tour.json"

// tourState is what the tour records. TourSeen is the "tour_seen" flag.
type tourState struct {
	TourSeen bool `json:"tour_seen"`
}

func tourStatePath() string { return filepath.Join(config.Home(), tourStateFile) }

// tourSeen reports whether the tour has already been shown on this machine.
// A missing or unreadable file counts as not seen.
func tourSeen() bool {
	b, err := os.ReadFile(tourStatePath())
	if err != nil {
		return false
	}
	var st tourState
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	return st.TourSeen
}

// markTourSeen records tour_seen in the Lectern home.
func markTourSeen() error {
	if err := os.MkdirAll(filepath.Dir(tourStatePath()), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(tourState{TourSeen: true})
	if err != nil {
		return err
	}
	return os.WriteFile(tourStatePath(), b, 0o600)
}

// maybeStartTour opens the tour on the first launch: once the landing page is
// showing and the tour has never been seen. A session that opens with a
// transcript is not a first launch, so it gets no tour.
func (m *Model) maybeStartTour() {
	// A prompt given on the command line is already work in progress: the
	// tour would hold it back (a modal stops the startup prompt), so it waits
	// for a later launch.
	if m.phase != phaseLanding || tourSeen() || strings.TrimSpace(m.opts.Prompt) != "" {
		return
	}
	m.openTour()
}

// openTour shows the tour from page one. It records tour_seen as soon as it
// opens, so the tour is shown once even if the process ends before a key is
// pressed.
func (m *Model) openTour() {
	_ = markTourSeen()
	m.pushModal(&tourModal{m: m})
}

// tourModal is the tour box.
type tourModal struct {
	m    *Model
	page int
}

// Title implements Modal.
func (t *tourModal) Title() string { return "Lectern tour" }

// Footer implements Footered. The last page's Enter closes the tour, so the
// hint says so.
func (t *tourModal) Footer() string {
	if t.page == len(tourPages)-1 {
		return "←/→ page · Enter done · Esc skip"
	}
	return "←/→ page · Enter next · Esc skip"
}

// Size implements Sized: a fixed box, narrower than the terminal on a phone.
func (t *tourModal) Size(cols, rows int) (int, int) {
	return clamp(min(cols-4, 78), 30, cols-2), clamp(min(rows-4, 14), 8, rows-2)
}

// Update implements Modal. The tour's own keys page through it; Esc closes it;
// any other key closes it and is then handled by the Model as usual.
func (t *tourModal) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return t, nil, false
	}
	n := len(tourPages)
	switch km.String() {
	case "right":
		t.page = (t.page + 1) % n
		return t, nil, false
	case "left":
		t.page = (t.page - 1 + n) % n
		return t, nil, false
	case "enter":
		// A message already typed is sent, not spent on a page: the tour must
		// never swallow the first prompt of a new install.
		if !t.m.cur().in.empty() {
			t.m.closeModal(t)
			cmd := t.m.onKey(km)
			return t, cmd, true
		}
		if t.page == n-1 {
			return t, nil, true
		}
		t.page++
		return t, nil, false
	case "esc":
		return t, nil, true
	}
	// Any other key: close the tour first, so the Model handles the key with
	// the tour gone, then report done (closing again is a no-op).
	t.m.closeModal(t)
	cmd := t.m.onKey(km)
	return t, cmd, true
}

// View implements Modal: the page heading, its lines, and the step rail.
func (t *tourModal) View(w, h int) string {
	th := t.m.th
	page := tourPages[t.page]
	lines := []string{
		fgb(th.Brand).Render(page.heading),
		"",
	}
	for _, l := range page.lines {
		lines = append(lines, fg(th.TextSecondary).Render(truncWord(l, min(w, tourLineWidth))))
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	var dots []string
	for i := range tourPages {
		if i == t.page {
			dots = append(dots, fgb(th.Brand).Render("●"))
		} else {
			dots = append(dots, fg(th.TextFaint).Render("○"))
		}
	}
	rail := strings.Join(dots, " ") + fg(th.TextMuted).Render("  "+itoa(t.page+1)+" of "+itoa(len(tourPages)))
	lines = append(lines, rail)
	return strings.Join(lines, "\n")
}
