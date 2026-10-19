package tui

// Checkpoint browser (/checkpoints), the undo confirm (Ctrl+Z, u in the
// browser), and the background jobs panel (/jobs).

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// confirmUndo is Ctrl+Z: asks before reverting the latest turn.
func (m *Model) confirmUndo() { m.openUndoConfirm(nil) }

// latestActive returns the newest checkpoint that is not undone.
func latestActive(cps []CheckpointInfo) (CheckpointInfo, bool) {
	for i := len(cps) - 1; i >= 0; i-- {
		if !cps[i].Undone {
			return cps[i], true
		}
	}
	return CheckpointInfo{}, false
}

// openUndoConfirm asks before reverting the latest turn, listing its files.
// The outcome is reported in the chat. after, when set, runs once the undo
// has happened (so the browser can refresh).
func (m *Model) openUndoConfirm(after func()) {
	t := m.cur()
	cp, ok := latestActive(t.be.Checkpoints())
	if !ok {
		m.setFlash("info", "Nothing to undo")
		return
	}
	m.pushModal(checkpointConfirm(m.th, fmt.Sprintf("Undo turn %d?", cp.Turn),
		"Restores the files this turn changed to their state before it. /redo brings them back.",
		cp.Files, "Yes, undo", func() tea.Cmd {
			return m.runBusy(fmt.Sprintf("Undoing turn %d…", cp.Turn), func() func() tea.Cmd {
				undone, err := t.be.Undo()
				return func() tea.Cmd {
					if after != nil {
						after()
					}
					if err != nil {
						t.chat.AddSystem("error", "Undo failed: "+err.Error())
						return nil
					}
					t.chat.AddSystem("info", fmt.Sprintf("Undid turn %d: %d files restored.", undone.Turn, len(undone.Files)))
					return nil
				}
			})
		}))
}

// checkpointConfirm asks before reverting files for undo or restore. Both are
// reversible (/redo brings them back), so the frame is not red and the
// buttons say what happens, not "delete". No stays the default answer.
func checkpointConfirm(th theme.Theme, title, intro string, items []string, yesLabel string, onYes func() tea.Cmd) *listConfirm {
	lc := newListConfirm(th, title, intro, items, false, onYes)
	lc.yes = false
	lc.WithLabels(yesLabel, "No")
	return lc
}

// laterFiles lists the files that active turns after turn changed, sorted.
func laterFiles(cps []CheckpointInfo, turn int) []string {
	seen := map[string]bool{}
	var out []string
	for _, cp := range cps {
		if cp.Turn <= turn || cp.Undone {
			continue
		}
		for _, f := range cp.Files {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}

// cpBrowser lists the turns that changed files, with a preview of the selected one.
type cpBrowser struct {
	m     *Model
	be    Backend
	th    theme.Theme
	cps   []CheckpointInfo
	cur   int
	off   int
	stats map[int][2]int // turn -> added, removed lines
	lastH int
}

// openCheckpoints is /checkpoints.
func (m *Model) openCheckpoints() {
	b := &cpBrowser{m: m, be: m.cur().be, th: m.th, stats: map[int][2]int{}}
	b.reload()
	b.cur = max(0, len(b.cps)-1)
	m.pushModal(b)
}

// reload re-reads the checkpoint list, keeping the cursor in range.
func (b *cpBrowser) reload() {
	b.cps = b.be.Checkpoints()
	b.cur = clamp(b.cur, 0, max(0, len(b.cps)-1))
}

// selected returns the highlighted checkpoint.
func (b *cpBrowser) selected() (CheckpointInfo, bool) {
	if b.cur < 0 || b.cur >= len(b.cps) {
		return CheckpointInfo{}, false
	}
	return b.cps[b.cur], true
}

// stat counts the lines a turn added and removed, cached per turn.
func (b *cpBrowser) stat(turn int) (int, int) {
	if s, ok := b.stats[turn]; ok {
		return s[0], s[1]
	}
	var adds, dels int
	for _, f := range splitDiff(b.be.TurnDiff(turn)) {
		adds += f.Adds
		dels += f.Dels
	}
	b.stats[turn] = [2]int{adds, dels}
	return adds, dels
}

// move steps the cursor, wrapping at both ends.
func (b *cpBrowser) move(d int) {
	if n := len(b.cps); n > 0 {
		b.cur = (b.cur + d + n) % n
	}
}

// restore asks before reverting every later turn's file changes.
func (b *cpBrowser) restore() {
	cp, ok := b.selected()
	if !ok {
		return
	}
	if cp.Undone {
		b.m.setFlash("err", fmt.Sprintf("Turn %d is undone. Redo it first (r).", cp.Turn))
		return
	}
	later := laterFiles(b.cps, cp.Turn)
	intro := fmt.Sprintf("Files go back to how they were at the end of turn %d. File changes made by later turns are reverted.", cp.Turn)
	if len(later) == 0 {
		intro = fmt.Sprintf("No turn after %d changed files, so nothing on disk changes.", cp.Turn)
	} else {
		intro += " These files are reverted:"
	}
	b.m.pushModal(checkpointConfirm(b.th, fmt.Sprintf("Restore to turn %d?", cp.Turn), intro, later, "Yes, restore", func() tea.Cmd {
		return b.m.runBusy(fmt.Sprintf("Restoring to turn %d…", cp.Turn), func() func() tea.Cmd {
			err := b.be.RestoreTo(cp.Turn)
			return func() tea.Cmd {
				if err != nil {
					b.m.setFlash("err", "Restore failed: "+err.Error())
					b.m.showAlert("Restore failed", "Files were not restored to turn "+itoa(cp.Turn)+".\n\n"+err.Error())
					return nil
				}
				b.reload()
				b.m.setFlash("ok", fmt.Sprintf("Restored files to turn %d", cp.Turn))
				return nil
			}
		})
	}))
}

// redo reapplies the most recently undone turn. One redo can re-apply several
// turns (the backend reports only the first), so the flash names every turn
// that flipped from undone to live in the reload, and counts their files once.
// The backend call runs behind a busy modal; the reload and the report run
// when it returns.
func (b *cpBrowser) redo() tea.Cmd {
	b.reload()
	before := undoneTurns(b.cps)
	be := b.be
	return b.m.runBusy("Redoing…", func() func() tea.Cmd {
		_, err := be.Redo()
		return func() tea.Cmd {
			b.reload()
			if err != nil {
				b.m.setFlash("err", "Redo failed: "+err.Error())
				b.m.showAlert("Redo failed", err.Error())
				return nil
			}
			b.reportRedo(before)
			return nil
		}
	})
}

// reportRedo names the turns that flipped from undone to live since before.
func (b *cpBrowser) reportRedo(before map[int]bool) {
	var turns []int
	files := map[string]bool{}
	for _, c := range b.cps {
		if before[c.Turn] && !c.Undone {
			turns = append(turns, c.Turn)
			for _, f := range c.Files {
				files[f] = true
			}
		}
	}
	if len(turns) == 0 {
		b.m.setFlash("info", "Nothing to redo")
		return
	}
	b.m.cur().chat.AddSystem("info", fmt.Sprintf("Redid %s: %s restored.", turnList(turns), filesLabel(len(files))))
	b.m.setFlash("ok", fmt.Sprintf("Redid %s (%s)", turnList(turns), filesLabel(len(files))))
}

// undoneTurns is the set of turns currently marked undone.
func undoneTurns(cps []CheckpointInfo) map[int]bool {
	out := map[int]bool{}
	for _, c := range cps {
		if c.Undone {
			out[c.Turn] = true
		}
	}
	return out
}

// turnList names turns the way a flash reads: "turn 2", "turns 2 and 3",
// "turns 1, 2 and 3". The list is in the order given.
func turnList(turns []int) string {
	if len(turns) == 1 {
		return fmt.Sprintf("turn %d", turns[0])
	}
	parts := make([]string, len(turns))
	for i, t := range turns {
		parts[i] = strconv.Itoa(t)
	}
	last := parts[len(parts)-1]
	return "turns " + strings.Join(parts[:len(parts)-1], ", ") + " and " + last
}

// Update implements Modal.
func (b *cpBrowser) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return b, nil, false
	}
	switch km.String() {
	case "esc", "q":
		return b, nil, true
	case "up", "k":
		b.move(-1)
	case "down", "j":
		b.move(1)
	case "enter":
		b.restore()
	case "d":
		if cp, ok := b.selected(); ok {
			b.m.openDiffText(fmt.Sprintf("Turn %d diff", cp.Turn), b.be.TurnDiff(cp.Turn), "",
				fmt.Sprintf("Turn %d changed no files.", cp.Turn))
		}
	case "u":
		b.m.openUndoConfirm(b.reload)
	case "r":
		return b, b.redo(), false
	}
	return b, nil, false
}

// Title implements Modal.
func (b *cpBrowser) Title() string { return "Checkpoints" }

// Icon implements Iconed.
func (b *cpBrowser) Icon() string { return "checkpoint" }

// Footer implements Footered.
func (b *cpBrowser) Footer() string {
	return "↑↓ move · Enter restore to turn · d diff · u undo · r redo · Esc close"
}

// Size implements Sized.
func (b *cpBrowser) Size(cols, rows int) (int, int) {
	return min(cols-4, 110), min(rows-4, 26)
}

// View implements Modal.
func (b *cpBrowser) View(w, h int) string {
	th := b.th
	if len(b.cps) == 0 {
		return fitJoin([]string{"", fg(th.TextFaint).Render("  No checkpoints yet. A turn that edits files creates one.")}, w, h)
	}
	preview := w >= 80
	listW := w
	rightW := 0
	if preview {
		listW = w * 5 / 10
		rightW = w - listW - 3
	}

	avail := max(1, h-1)
	if b.cur < b.off {
		b.off = b.cur
	}
	if b.cur >= b.off+avail {
		b.off = b.cur - avail + 1
	}
	latest, hasLatest := latestActive(b.cps)
	rows := []string{fg(th.TextFaint).Render("◆ latest · ● edits · ○ read or undone")}
	for i := b.off; i < len(b.cps) && len(rows) < h; i++ {
		cp := b.cps[i]
		glyph, col := "○", th.TextFaint
		switch {
		case cp.Undone:
		case hasLatest && cp.Turn == latest.Turn:
			glyph, col = "◆", th.Brand
		case len(cp.Files) > 0:
			glyph, col = "●", th.TextMuted
		}
		label := fmt.Sprintf("%s  turn %-4d %-8s %s", glyph, cp.Turn, filesLabel(len(cp.Files)), oneLine(cp.Prompt))
		if cp.Undone {
			label += "  (undone)"
		}
		line := render.Truncate(label, listW-2)
		switch {
		case i == b.cur:
			line = fgb(th.BrandSecondary).Render("› ") + fgb(th.TextPrimary).Render(line)
		case cp.Undone:
			line = "  " + fg(th.TextFaint).Render(line)
		default:
			line = "  " + fg(col).Render(line)
		}
		rows = append(rows, line)
	}
	left := fitLines(rows, listW, h)
	if !preview {
		return strings.Join(left, "\n")
	}
	right := fitLines(b.preview(rightW), rightW, h)
	out := make([]string, h)
	for i := range out {
		out[i] = left[i] + fg(th.BorderFocused).Render(" │ ") + right[i]
	}
	return strings.Join(out, "\n")
}

// filesLabel is "1 file" or "N files".
func filesLabel(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// preview describes the selected checkpoint: its prompt, files and size.
func (b *cpBrowser) preview(w int) []string {
	th := b.th
	cp, ok := b.selected()
	if !ok {
		return nil
	}
	adds, dels := b.stat(cp.Turn)
	lines := []string{
		fgb(th.TextPrimary).Render(fmt.Sprintf("Turn %d · %s", cp.Turn, render.RelTime(cp.Created, time.Now()))),
	}
	if cp.Undone {
		lines = append(lines, fg(th.Warning).Render("Undone. Press r to redo it."))
	}
	lines = append(lines, "")
	prompt := strings.Split(render.Wrap(cp.Prompt, w), "\n")
	for i, l := range prompt {
		if i == 4 {
			lines = append(lines, fg(th.TextFaint).Render("…"))
			break
		}
		lines = append(lines, fg(th.TextMuted).Render(l))
	}
	lines = append(lines, "", fg(th.TextSecondary).Render(fmt.Sprintf("%s · +%d -%d", filesLabel(len(cp.Files)), adds, dels)))
	for i, f := range cp.Files {
		if i == 10 {
			lines = append(lines, fg(th.TextFaint).Render(fmt.Sprintf("  … and %d more", len(cp.Files)-10)))
			break
		}
		lines = append(lines, render.Truncate("  "+f, w))
	}
	return lines
}

// ---------------------------------------------------------------- jobs

func init() {
	overrideCommands(
		&command{Name: "jobs", Desc: "Background shell jobs", Group: "Tabs",
			Run: func(m *Model, _ string) tea.Cmd { m.openJobs(); return nil }},
	)
}

// jobsPanel lists background shell jobs, with output and kill.
type jobsPanel struct {
	m    *Model
	be   Backend
	th   theme.Theme
	jobs []Job
	cur  int
	off  int
}

// openJobs is /jobs.
func (m *Model) openJobs() {
	p := &jobsPanel{m: m, be: m.cur().be, th: m.th}
	p.reload()
	m.pushModal(p)
}

// reload re-reads the jobs (they change while the panel is open).
func (p *jobsPanel) reload() {
	p.jobs = p.be.Jobs()
	p.cur = clamp(p.cur, 0, max(0, len(p.jobs)-1))
}

// running reports whether a job is still going.
func running(j Job) bool { return j.Status == "running" }

// Update implements Modal.
func (p *jobsPanel) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil, false
	}
	n := len(p.jobs)
	switch km.String() {
	case "esc", "q":
		return p, nil, true
	case "up":
		if n > 0 {
			p.cur = (p.cur - 1 + n) % n
		}
	case "down", "j":
		if n > 0 {
			p.cur = (p.cur + 1) % n
		}
	case "enter":
		if n > 0 {
			p.output(p.jobs[p.cur])
		}
	case "k":
		if n > 0 {
			p.kill(p.jobs[p.cur])
		}
	}
	return p, nil, false
}

// output opens the job's recent output.
func (p *jobsPanel) output(j Job) {
	started := "—"
	if !j.Started.IsZero() {
		started = j.Started.Format("15:04:05")
	}
	lines := []string{
		"Command  " + j.Command,
		"Status   " + statusWord(j),
		"Started  " + started,
		"",
	}
	if len(j.Tail) == 0 {
		lines = append(lines, "No output yet.")
	} else {
		lines = append(lines, j.Tail...)
	}
	p.m.openInfo("Job "+j.ID, "terminal", lines)
}

// kill stops a running job after asking.
func (p *jobsPanel) kill(j Job) {
	if !running(j) {
		p.m.setFlash("info", "Job "+j.ID+" already finished.")
		return
	}
	p.m.pushModal(NewConfirm(p.th, "Kill job "+j.ID+"?", "Stops the running command: "+j.Command, true, func() tea.Cmd {
		if err := p.be.KillJob(j.ID); err != nil {
			p.m.failAlert("Could not stop the job: " + err.Error())
		} else {
			p.m.setFlash("ok", "Stopped job "+j.ID)
		}
		p.reload()
		return nil
	}))
}

// statusWord is the plain status text: running, exit 0, or exit N.
func statusWord(j Job) string {
	switch {
	case running(j):
		return "● running"
	case j.ExitCode == 0:
		return "✓ exit 0"
	}
	return fmt.Sprintf("✗ exit %d", j.ExitCode)
}

// Title implements Modal.
func (p *jobsPanel) Title() string { return "Background jobs" }

// Icon implements Iconed.
func (p *jobsPanel) Icon() string { return "terminal" }

// Footer implements Footered.
func (p *jobsPanel) Footer() string { return "↑↓ move · Enter output · k kill · Esc close" }

// Size implements Sized.
func (p *jobsPanel) Size(cols, rows int) (int, int) {
	return min(cols-4, 100), min(rows-4, 22)
}

// View implements Modal.
func (p *jobsPanel) View(w, h int) string {
	p.reload()
	th := p.th
	if len(p.jobs) == 0 {
		return fitJoin([]string{"", fg(th.TextFaint).Render("  No background jobs in this session.")}, w, h)
	}
	vis := max(1, h/2)
	if p.cur < p.off {
		p.off = p.cur
	}
	if p.cur >= p.off+vis {
		p.off = p.cur - vis + 1
	}
	var rows []string
	for i := p.off; i < len(p.jobs) && len(rows) < h-1; i++ {
		j := p.jobs[i]
		col := th.Info
		switch {
		case running(j):
		case j.ExitCode == 0:
			col = th.Success
		default:
			col = th.Error
		}
		mark := "  "
		if i == p.cur {
			mark = fgb(th.BrandSecondary).Render("› ")
		}
		age := ""
		if !j.Started.IsZero() {
			age = render.Elapsed(time.Since(j.Started))
		}
		left := mark + fg(col).Render(statusWord(j)) + "  " + fgb(th.TextPrimary).Render(j.Command)
		rows = append(rows, spaceBetween(render.Truncate(left, w-len(age)-2), fg(th.TextDim).Render(age), w))
		last := "    no output yet"
		if n := len(j.Tail); n > 0 {
			last = "    " + strings.TrimSpace(j.Tail[n-1])
		}
		rows = append(rows, fg(th.TextFaint).Render(render.Truncate(last, w)))
	}
	return strings.Join(fitLines(rows, w, h), "\n")
}
