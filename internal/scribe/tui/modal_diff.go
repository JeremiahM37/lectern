package tui

// Full-screen diff viewer: /diff, /diff <turn>, the git menu's Diff, the
// status list, and the checkpoint browser's diff key.

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(
		&command{Name: "diff", Args: "[turn|path]", Desc: "Show the diff of a turn, or of the working tree", Group: "Git",
			Run: func(m *Model, args string) tea.Cmd {
				if args == "" {
					return m.openWorkingDiff("")
				}
				n, err := strconv.Atoi(args)
				if err != nil {
					return m.openWorkingDiff(args)
				}
				m.openDiffText(fmt.Sprintf("Turn %d diff", n), m.cur().be.TurnDiff(n), "",
					fmt.Sprintf("Turn %d changed no files.", n))
				return nil
			}},
	)
}

// diffFile is one file's part of a unified diff.
type diffFile struct {
	Path       string
	Text       string
	Adds, Dels int
}

// splitDiff cuts a unified diff (git's or a checkpoint's) into one entry per
// file. A new file starts at a "diff --git" line, or at a "---" line that
// follows a hunk and precedes "+++".
func splitDiff(text string) []diffFile {
	lines := strings.Split(text, "\n")
	var files []diffFile
	var block []string
	hasHunk := false
	flush := func() {
		if len(block) == 0 {
			return
		}
		f := diffFile{Text: strings.Join(block, "\n"), Path: pathOfBlock(block)}
		for _, l := range block {
			switch {
			case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++ "):
				f.Adds++
			case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "--- "):
				f.Dels++
			}
		}
		files = append(files, f)
		block, hasHunk = nil, false
	}
	for i, l := range lines {
		split := strings.HasPrefix(l, "diff --git ") || strings.HasPrefix(l, "Binary files ") ||
			(strings.HasPrefix(l, "--- ") && hasHunk && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "))
		if split {
			flush()
		}
		if strings.HasPrefix(l, "@@") {
			hasHunk = true
		}
		block = append(block, l)
	}
	flush()
	return files
}

// pathOfBlock finds the file a block of diff lines is about.
func pathOfBlock(block []string) string {
	for _, l := range block {
		if strings.HasPrefix(l, "+++ ") && !strings.Contains(l, "/dev/null") {
			return strings.TrimPrefix(cutTab(l[4:]), "b/")
		}
	}
	for _, l := range block {
		if strings.HasPrefix(l, "--- ") {
			return strings.TrimPrefix(cutTab(l[4:]), "a/")
		}
		if strings.HasPrefix(l, "Binary files ") {
			if i := strings.Index(l, " and "); i >= 0 {
				return strings.TrimPrefix(strings.TrimSuffix(l[i+5:], " differ"), "b/")
			}
		}
		if strings.HasPrefix(l, "diff --git ") {
			fields := strings.Fields(l)
			return strings.TrimPrefix(fields[len(fields)-1], "b/")
		}
	}
	return "(unknown file)"
}

func cutTab(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		return s[:i]
	}
	return strings.TrimSpace(s)
}

// diffView is the full-screen diff viewer.
type diffView struct {
	m        *Model
	th       theme.Theme
	title    string
	selPath  string
	loading  bool
	files    []diffFile
	raw      string // shown when the text holds no file diff
	emptyMsg string
	cur, off int
	sbs      bool
	cols     int // terminal width as of the last Size call
	lastH    int // body rows as of the last View
	cacheKey string
	cache    []string
}

// newDiffView makes a viewer that is loading until load is called.
func newDiffView(m *Model, title, selPath string) *diffView {
	return &diffView{m: m, th: m.th, title: title, selPath: selPath, loading: true, emptyMsg: "No changes.", cols: m.width}
}

// load fills the viewer and selects selPath when it is in the diff.
func (v *diffView) load(text, empty string) {
	v.loading = false
	v.files = splitDiff(text)
	v.raw = ""
	if len(v.files) == 0 {
		v.raw = strings.TrimSpace(text)
	}
	if empty != "" {
		v.emptyMsg = empty
	}
	v.cur, v.off, v.cacheKey = 0, 0, ""
	for i, f := range v.files {
		if f.Path == v.selPath {
			v.cur = i
			break
		}
	}
}

// openDiffText shows text in a viewer right away.
func (m *Model) openDiffText(title, text, selPath, empty string) {
	v := newDiffView(m, title, selPath)
	v.load(text, empty)
	m.pushModal(v)
}

// wide reports whether the file list fits beside the diff.
func (v *diffView) wide() bool { return v.cols >= 110 }

// step moves to the next (d=1) or previous (d=-1) file.
func (v *diffView) step(d int) {
	if len(v.files) == 0 {
		return
	}
	v.cur = (v.cur + d + len(v.files)) % len(v.files)
	v.off = 0
}

// rendered returns the diff of the current file at width, cached.
func (v *diffView) rendered(width int, sbs bool) []string {
	key := fmt.Sprintf("%d/%d/%t", v.cur, width, sbs)
	if key == v.cacheKey {
		return v.cache
	}
	f := v.files[v.cur]
	text := f.Text
	if !strings.Contains(text, "\n+++ ") {
		// git's per-file diffs can lack the ---/+++ header the renderer reads its path from
		text = "--- a/" + f.Path + "\n+++ b/" + f.Path + "\n" + text
	}
	out := render.Diff(text, width, v.th, render.DiffOpts{SideBySide: sbs, LineNumbers: true})
	v.cacheKey, v.cache = key, strings.Split(out, "\n")
	return v.cache
}

// Title implements Modal.
func (v *diffView) Title() string { return v.title }

// Icon implements Iconed.
func (v *diffView) Icon() string { return "diff_add" }

// Footer implements Footered.
func (v *diffView) Footer() string {
	return "j/k scroll · n/p file · PgUp/PgDn page · s side by side · Esc close"
}

// Size implements Sized. The viewer takes the whole screen.
func (v *diffView) Size(cols, rows int) (int, int) {
	v.cols = cols
	return cols - 2, rows - 2
}

// Update implements Modal.
func (v *diffView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	page := max(1, v.lastH-1)
	switch km.String() {
	case "esc", "q":
		return v, nil, true
	case "up", "k":
		v.off--
	case "down", "j":
		v.off++
	case "pgup":
		v.off -= page
	case "pgdown", " ":
		v.off += page
	case "home", "g":
		v.off = 0
	case "end", "G":
		v.off = len(v.cache)
	case "n", "tab":
		v.step(1)
	case "p", "shift+tab":
		v.step(-1)
	case "s":
		if v.wide() {
			v.sbs = !v.sbs
		}
	}
	v.off = clamp(v.off, 0, max(0, len(v.cache)-v.lastH))
	return v, nil, false
}

// View implements Modal.
func (v *diffView) View(w, h int) string {
	th := v.th
	if v.loading {
		return fitJoin([]string{fg(th.TextMuted).Render(spinner() + " Reading the diff…")}, w, h)
	}
	if len(v.files) == 0 {
		msg := v.raw
		if msg == "" {
			msg = v.emptyMsg
		}
		lines := strings.Split(render.Wrap(msg, w), "\n")
		v.lastH = h
		v.off = clamp(v.off, 0, max(0, len(lines)-h))
		end := min(len(lines), v.off+h)
		return fitJoin(lines[v.off:end], w, h)
	}

	listW := 0
	if v.wide() {
		listW = clamp(w/4, 24, 36)
	}
	rightW := w
	if v.wide() {
		rightW = w - listW - 3
	}
	bodyH := max(1, h-1)
	v.lastH = bodyH

	f := v.files[v.cur]
	header := fg(th.TextMuted).Render(fmt.Sprintf("file %d of %d · +%d -%d", v.cur+1, len(v.files), f.Adds, f.Dels))
	if !v.wide() {
		header = fg(th.TextMuted).Render(fmt.Sprintf("file %d of %d · %s · +%d -%d", v.cur+1, len(v.files), f.Path, f.Adds, f.Dels))
	}
	body := v.rendered(rightW, v.sbs && v.wide())
	v.off = clamp(v.off, 0, max(0, len(body)-bodyH))
	end := min(len(body), v.off+bodyH)
	right := fitLines(append([]string{header}, body[v.off:end]...), rightW, h)
	if !v.wide() {
		return strings.Join(right, "\n")
	}
	left := v.fileList(listW, h)
	sep := fg(th.BorderFocused).Render(" │ ")
	out := make([]string, h)
	for i := range out {
		out[i] = left[i] + sep + right[i]
	}
	return strings.Join(out, "\n")
}

// fileList draws the file list for the wide layout, scrolled to the current file.
func (v *diffView) fileList(w, h int) []string {
	th := v.th
	rows := []string{fg(th.TextMuted).Render("Files")}
	avail := h - 1
	start := 0
	if v.cur >= avail {
		start = v.cur - avail + 1
	}
	for i := start; i < len(v.files) && len(rows) < h; i++ {
		f := v.files[i]
		label := fmt.Sprintf("%s  +%d -%d", f.Path, f.Adds, f.Dels)
		if i == v.cur {
			rows = append(rows, fgb(th.BrandSecondary).Render("› ")+fgb(th.TextPrimary).Render(render.Truncate(label, w-2)))
		} else {
			rows = append(rows, "  "+fg(th.TextSecondary).Render(render.Truncate(label, w-2)))
		}
	}
	return fitLines(rows, w, h)
}

// fitJoin pads or cuts lines to exactly w by h and joins them.
func fitJoin(lines []string, w, h int) string {
	return strings.Join(fitLines(lines, w, h), "\n")
}
