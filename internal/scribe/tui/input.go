package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// imageOnlyPrompt is sent when an image is submitted with no text. The API
// rejects an empty text block, so the turn needs some words to go with it.
const imageOnlyPrompt = "Look at the attached image."

// Input limits from the spec.
const (
	maxSteerQueue   = 5
	pasteCollapseAt = 3 // more lines than this (4 or more) collapse to a placeholder
	historyKeep     = 500
	historySearchN  = 50
)

// inputState is one tab's input box: the textarea plus history, stash, paste
// placeholders, history search and the popup selection.
type inputState struct {
	ta      textarea.Model
	history []string          // oldest first
	hidx    int               // -1 = not browsing history; 0 = newest entry
	draft   string            // draft held while browsing history
	stashes []string          // Alt+S drafts, newest last
	pastes  map[string]string // placeholder -> pasted text, expanded on submit
	search  *histSearch
	width   int // textarea width, for counting visual rows
	// probing is true while a clipboard image read is in flight; Enter waits.
	probing bool
	// mounted is set once the stashes for this tab's directory are loaded.
	mounted bool
	acSel   int
	// dismissed hides the popup until the text changes again (Esc).
	dismissed string
	popupOn   bool
	// images are attached to the next message; each shows as an [image N] chip.
	images []inputImage
	imageN int // last chip label handed out; back to 0 when the chips are gone
	clip   clipboardReader
	// clipText reads the clipboard's text when Ctrl+V finds no image on it.
	clipText clipboardTextReader
	cwd      string // working directory the stashes belong to
	vim      vimState
}

// inputImage is one attached image.
type inputImage struct {
	n    int
	part llm.Part
}

// histSearch is the Ctrl+R overlay.
type histSearch struct {
	query   []rune
	matches []string
	sel     int
}

func newInputState() *inputState {
	ta := textarea.New()
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.SetHeight(1)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()
	return &inputState{ta: ta, hidx: -1, pastes: map[string]string{}, clip: systemClipboardImage, clipText: systemClipboardText}
}

// ---------------------------------------------------------------- images

// hasImages reports whether any image is attached.
func (s *inputState) hasImages() bool { return len(s.images) > 0 }

// attach adds an image as the next chip and returns its label number.
func (s *inputState) attach(data []byte, mediaType string) int {
	s.imageN++
	part := llm.Part{Type: llm.PartImage, MediaType: mediaType, Data: data}
	s.images = append(s.images, inputImage{n: s.imageN, part: part})
	return s.imageN
}

// popImage removes the last chip. It reports false when there is none.
func (s *inputState) popImage() bool {
	n := len(s.images)
	if n == 0 {
		return false
	}
	s.images = s.images[:n-1]
	if n == 1 {
		s.imageN = 0
	}
	return true
}

// takeImages hands the attached images to a turn and clears the chips.
func (s *inputState) takeImages() []llm.Part {
	var parts []llm.Part
	for _, im := range s.images {
		parts = append(parts, im.part)
	}
	s.images = nil
	s.imageN = 0
	return parts
}

// chips is the chip row shown in the box's top border, e.g. "[image 1] [image 2]".
func (s *inputState) chips() string {
	out := make([]string, 0, len(s.images))
	for _, im := range s.images {
		out = append(out, fmt.Sprintf("[image %d]", im.n))
	}
	return strings.Join(out, " ")
}

// pasteImage starts a clipboard read off the UI loop. The result comes back as
// an imageProbeMsg; until then the box shows "reading image" and Enter waits. A
// second Ctrl+V during a read does nothing. An image wins; with no image the
// clipboard's text is pasted instead (see finishImageProbe).
func (m *Model) pasteImage(t *tab) tea.Cmd {
	s := t.in
	if s.probing {
		return nil
	}
	s.probing = true
	clip, clipText, id := s.clip, s.clipText, t.id
	return func() tea.Msg {
		data, mt, err := clip()
		if errors.Is(err, errNoClipboardImage) || errors.Is(err, errNoClipboardTool) {
			if text, terr := clipText(); terr == nil {
				err = clipTextFound{text: text}
			}
		}
		return imageProbeMsg{tab: id, data: data, mt: mt, err: err}
	}
}

// finishImageProbe attaches the clipboard image a probe read, or reports why
// there is none. A clipboard with no image is only logged, as the spec has it:
// the error log holds it and nothing pops up.
func (m *Model) finishImageProbe(msg imageProbeMsg) tea.Cmd {
	t := m.tabByID(msg.tab)
	if t == nil {
		return nil
	}
	t.in.probing = false
	if err := msg.err; err != nil {
		var found clipTextFound
		if errors.As(err, &found) {
			m.pasteClipboardText(t, found.text)
			return nil
		}
		if errors.Is(err, errNoClipboardImage) {
			m.logError(t, err.Error())
			return nil
		}
		kind := "err"
		if errors.Is(err, errNoClipboardTool) {
			kind = "info"
		}
		m.setFlash(kind, err.Error())
		return nil
	}
	m.attachFitted(t, msg.data, msg.mt, "Attached image %d from the clipboard", "the clipboard image")
	return nil
}

// pasteClipboardText puts clipboard text into the input the way a bracketed
// paste does: a path to an image attaches the image, and a long paste takes a
// placeholder that expands on submit.
func (m *Model) pasteClipboardText(t *tab, text string) {
	if path, ok := imagePathFromPaste(text); ok {
		m.attachImageFile(t, path)
		return
	}
	t.in.ta.InsertString(t.in.collapsePaste(text))
	t.in.hidx = -1
}

// attachFitted attaches an image, shrinking it first when it is over the size
// limit, and says which happened. okText takes the image number; what names
// the image in an error.
func (m *Model) attachFitted(t *tab, data []byte, mt, okText, what string) {
	f, err := fitImage(data, mt)
	if err != nil {
		m.setFlash("err", "Can't attach "+what+": "+err.Error())
		return
	}
	n := t.in.attach(f.data, f.mediaType)
	if f.resized {
		m.setFlash("ok", fmt.Sprintf("Attached image %d. ", n)+resizeNotice(f)+".")
		return
	}
	m.setFlash("ok", fmt.Sprintf(okText, n))
}

// attachImageFile attaches an image named by a pasted file path.
func (m *Model) attachImageFile(t *tab, path string) {
	data, mt, err := loadImageFile(path)
	if err != nil {
		m.setFlash("err", "Can't attach "+path+": "+err.Error())
		return
	}
	m.attachFitted(t, data, mt, "Attached image %d", path)
}

func (s *inputState) value() string { return s.ta.Value() }

func (s *inputState) empty() bool { return strings.TrimSpace(s.ta.Value()) == "" }

func (s *inputState) clear() {
	s.ta.Reset()
	s.hidx = -1
	s.acSel = 0
	s.pastes = map[string]string{}
}

func (s *inputState) setValue(v string) {
	s.ta.SetValue(v)
	s.acSel = 0
}

// resize sets the textarea's width (the box is two columns of border and two of padding wider).
func (s *inputState) resize(w int) {
	if w < 8 {
		w = 8
	}
	s.width = w
	s.ta.SetWidth(w)
}

// inputHeight is the number of text rows for a terminal rows tall. The box
// grows with its visual lines, from 1 up to 40% of the screen (at least 4
// rows); anything taller scrolls inside the textarea.
func (s *inputState) inputHeight(rows int) int {
	if s.search != nil {
		return 1
	}
	return clamp(s.visualLines(), 1, max(4, rows*2/5))
}

// fitViewport rewinds the textarea to its first row when the whole text fits
// in h rows. bubbles only scrolls to keep the cursor visible and never scrolls
// back, so a box that grew from one row to two while the cursor sat on the
// second row kept showing rows 2 and 3 and hid the first line. Reset rewinds
// the viewport and leaves the cursor at the end, so this only runs when the
// cursor is already there (the case while typing).
func (s *inputState) fitViewport(h int) {
	if s.search != nil || s.visualLines() > h || !s.cursorAtEnd() {
		return
	}
	v := s.ta.Value()
	s.ta.Reset()
	s.ta.InsertString(v)
}

// cursorAtEnd reports whether the cursor is after the last character of the text.
func (s *inputState) cursorAtEnd() bool {
	lines := strings.Split(s.ta.Value(), "\n")
	last := len(lines) - 1
	if s.ta.Line() != last {
		return false
	}
	li := s.ta.LineInfo()
	return li.StartColumn+li.ColumnOffset >= len([]rune(lines[last]))
}

// visualLines counts the rows the text takes after wrapping, at the width the
// textarea wraps to. The textarea wraps on word boundaries, so a plain
// ceil(width / box) under-counts long text and the last row is cut off.
func (s *inputState) visualLines() int {
	w := s.ta.Width()
	if w <= 0 {
		w = s.width
	}
	if w <= 0 {
		return max(1, s.ta.LineCount())
	}
	n := 0
	for _, line := range strings.Split(s.ta.Value(), "\n") {
		n += len(taWrap([]rune(line), w))
	}
	return n
}

// taWrap splits one logical line the way bubbles' textarea does, so the box
// is sized to the rows the textarea draws. It is a copy of that package's
// unexported wrap: words stay whole, and a word wider than the row starts on
// its own row. Keep it in step with the bubbles version in go.mod.
func taWrap(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)
	cells := func(rs []rune) int { return lipgloss.Width(string(rs)) }
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if cells(lines[row])+cells(word)+spaces > width {
				row++
				lines = append(lines, append([]rune{}, word...))
			} else {
				lines[row] = append(lines[row], word...)
			}
			lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
			spaces = 0
			word = nil
		} else {
			lastLen := lipgloss.Width(string(word[len(word)-1]))
			if cells(word)+lastLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}
	if cells(lines[row])+cells(word)+spaces >= width {
		lines = append(lines, append([]rune{}, word...))
	} else {
		lines[row] = append(lines[row], word...)
	}
	return lines
}

// ---------------------------------------------------------------- popups

// slashMatches returns command popup rows for the current "/" text.
func (s *inputState) slashMatches() []*command {
	v := s.ta.Value()
	if !strings.HasPrefix(v, "/") || strings.Contains(v, "\n") {
		return nil
	}
	return rankSlash(strings.TrimPrefix(v, "/"))
}

// mentionToken is the trailing "@..." word, if the text ends in one.
func (s *inputState) mentionToken() (string, bool) {
	v := s.ta.Value()
	i := strings.LastIndexAny(v, " \n\t")
	tok := v[i+1:]
	if strings.HasPrefix(tok, "@") {
		return strings.TrimPrefix(tok, "@"), true
	}
	return "", false
}

// popupActive reports whether a popup should show, and which kind.
func (s *inputState) popupKind() string {
	if s.search != nil || s.hidx >= 0 {
		return ""
	}
	if s.dismissed == s.ta.Value() {
		return ""
	}
	if len(s.slashMatches()) > 0 {
		return "slash"
	}
	if _, ok := s.mentionToken(); ok {
		return "mention"
	}
	return ""
}

// popupLines draws the popup above the box. m may be nil when only sizing.
func (s *inputState) popupLines(m *Model, w int) []string {
	if s.search != nil && m != nil {
		return s.searchPopupLines(m, w)
	}
	kind := s.popupKind()
	if kind == "" || m == nil {
		return nil
	}
	th := m.th
	var rows []string
	footer := ""
	switch kind {
	case "slash":
		cmds := s.slashMatches()
		s.acSel = clamp(s.acSel, 0, len(cmds)-1)
		start := 0
		maxRows := slashPopupRows(m.height)
		if s.acSel >= maxRows {
			start = s.acSel - maxRows + 1
		}
		end := min(len(cmds), start+maxRows)
		// Names are padded to the widest visible one, so every description
		// starts in the same column.
		nameW := 0
		for _, c := range cmds[start:end] {
			nameW = max(nameW, ansi.StringWidth("/"+c.Name))
		}
		// popupBox adds one column of gutter and the frame takes two more, so
		// this is the full inner width.
		rowW := w - 3
		for i := start; i < end; i++ {
			rows = append(rows, s.slashRow(th, cmds[i], i == s.acSel, nameW, rowW))
		}
		if len(cmds) > maxRows {
			rows = append(rows, fg(th.TextDim).Render(itoa(s.acSel+1)+"/"+itoa(len(cmds))))
		}
		footer = "↑↓ select · Tab accept · Enter run · Esc dismiss"
	case "mention":
		q, _ := s.mentionToken()
		files := m.cur().be.FileCandidates(q, 8)
		s.acSel = clamp(s.acSel, 0, max(0, len(files)-1))
		if len(files) == 0 {
			rows = append(rows, fg(th.TextFaint).Render("  no matching files"))
		}
		for i, f := range files {
			mark := "  "
			st := fg(th.TextSecondary)
			if i == s.acSel {
				mark = fgb(th.BorderSlash).Render("› ")
				st = fgb(th.TextPrimary)
			}
			rows = append(rows, render.Truncate(mark+st.Render("@"+f), w-6))
		}
		footer = "↑↓ select · Tab/Enter insert · Esc dismiss"
	}
	return popupBox(rows, w, th.BorderSlash, footer, th)
}

// slashRow draws one autocomplete row: marker, the name padded to nameW, then
// the description cut with an ellipsis to fit rowW. The selected row paints
// its highlight background across the whole of rowW.
func (s *inputState) slashRow(th theme.Theme, c *command, sel bool, nameW, rowW int) string {
	bg := lipgloss.NewStyle()
	mark := "  "
	nameCol := fg(th.BorderSlash)
	descCol := fg(th.TextDim)
	if sel {
		bg = lipgloss.NewStyle().Background(th.BgPopupHighlight)
		mark = bg.Foreground(th.BorderSlash).Bold(true).Render("› ")
		nameCol = bg.Foreground(th.Info).Bold(true)
		descCol = bg.Foreground(th.TextSecondary)
	}
	name := "/" + c.Name
	var b strings.Builder
	b.WriteString(mark)
	b.WriteString(nameCol.Render(name))
	b.WriteString(bg.Render(strings.Repeat(" ", nameW-ansi.StringWidth(name)+2)))
	if descW := rowW - (2 + nameW + 2); descW > 0 && c.Desc != "" {
		b.WriteString(descCol.Render(render.Truncate(c.Desc, descW)))
	}
	if used := ansi.StringWidth(b.String()); sel && used < rowW {
		b.WriteString(bg.Render(strings.Repeat(" ", rowW-used)))
	}
	return b.String()
}

// popupBox frames popup rows above the input.
func popupBox(rows []string, w int, col lipgloss.TerminalColor, footer string, th theme.Theme) []string {
	body := make([]string, 0, len(rows))
	for _, r := range rows {
		body = append(body, " "+r)
	}
	return frame("", "", body, footer, w, col, th)
}

// ---------------------------------------------------------------- history

// pushHistory records a submitted prompt (deduplicated, newest last).
func (s *inputState) pushHistory(v string) {
	if strings.TrimSpace(v) == "" {
		return
	}
	out := s.history[:0]
	for _, h := range s.history {
		if h != v {
			out = append(out, h)
		}
	}
	s.history = append(out, v)
	if len(s.history) > historyKeep {
		s.history = s.history[len(s.history)-historyKeep:]
	}
	s.hidx = -1
}

// histPrev steps to an older entry.
func (s *inputState) histPrev() {
	if len(s.history) == 0 {
		return
	}
	if s.hidx == -1 {
		s.draft = s.ta.Value()
	}
	if s.hidx < len(s.history)-1 {
		s.hidx++
	}
	s.setValue(s.history[len(s.history)-1-s.hidx])
}

// histNext steps to a newer entry, restoring the draft past the newest.
func (s *inputState) histNext() {
	if s.hidx == -1 {
		return
	}
	s.hidx--
	if s.hidx == -1 {
		s.setValue(s.draft)
		return
	}
	s.setValue(s.history[len(s.history)-1-s.hidx])
}

// openSearch starts Ctrl+R.
func (s *inputState) openSearch() {
	s.search = &histSearch{}
	s.refreshSearch()
}

func (s *inputState) refreshSearch() {
	q := strings.ToLower(string(s.search.query))
	s.search.matches = s.search.matches[:0]
	seen := map[string]bool{}
	for i := len(s.history) - 1; i >= 0 && len(s.history)-i <= historyKeep; i-- {
		h := s.history[i]
		if seen[h] {
			continue
		}
		seen[h] = true
		if q == "" || strings.Contains(strings.ToLower(h), q) {
			s.search.matches = append(s.search.matches, h)
			if len(s.search.matches) >= historySearchN {
				break
			}
		}
	}
	s.search.sel = clamp(s.search.sel, 0, max(0, len(s.search.matches)-1))
}

// ---------------------------------------------------------------- paste

// collapsePaste returns the text to insert for a paste. Four or more lines
// become a placeholder; the full text is kept until submit.
func (s *inputState) collapsePaste(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if pasteLineCount(text) <= pasteCollapseAt {
		return text
	}
	ph := pastePlaceholder(text)
	s.pastes[ph] = text
	return ph
}

// pasteLineCount counts the lines a paste occupies. A single trailing newline
// ends the last line rather than starting an empty one, so "a\nb\n" is 2 lines.
func pasteLineCount(text string) int {
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

// pastePlaceholder is the inline token for a long paste: the first line,
// trimmed and cut to 30 characters with "..." when cut, then the line count.
func pastePlaceholder(text string) string {
	first := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if r := []rune(first); len(r) > 30 {
		first = string(r[:30]) + "..."
	}
	return fmt.Sprintf("<pasted (%s +%d lines)>", first, pasteLineCount(text))
}

// expand replaces paste placeholders with the text they stand for. A token the
// user edited no longer matches, so it is sent as typed.
func (s *inputState) expand(v string) string {
	for ph, text := range s.pastes {
		if strings.Contains(v, ph) {
			v = strings.ReplaceAll(v, ph, text)
		}
	}
	return v
}

// ---------------------------------------------------------------- keys

// handleInputKey routes a key to the input box. Global keys were already offered.
func (m *Model) handleInputKey(km tea.KeyMsg) tea.Cmd {
	t := m.cur()
	s := t.in
	key := km.String()
	if s.search != nil {
		return m.searchKey(t, km)
	}
	if s.probing && key == "enter" {
		// the image read decides what the message carries; Enter waits for it
		return nil
	}
	if kind := s.popupKind(); kind != "" {
		switch key {
		case "up", "down":
			n := 0
			if kind == "slash" {
				n = len(s.slashMatches())
			} else {
				n = len(t.be.FileCandidates(mentionQuery(s), 8))
			}
			if n > 0 {
				step := 1
				if key == "up" {
					step = -1
				}
				s.acSel = (s.acSel + step + n) % n
			}
			return nil
		case "tab", "right":
			if kind == "slash" {
				if c := s.slashMatches(); len(c) > 0 {
					s.setValue("/" + c[clamp(s.acSel, 0, len(c)-1)].Name + " ")
				}
			} else {
				m.acceptMention(t)
			}
			return nil
		case "enter":
			if kind == "slash" {
				c := s.slashMatches()
				if len(c) == 0 {
					break
				}
				pick := c[clamp(s.acSel, 0, len(c)-1)]
				// Required arguments (<...>) are filled in and wait for the user;
				// optional ones ([...]) do not hold the command back, so Enter on
				// /mode runs it at once. Tab still completes the name for typing
				// an argument.
				if strings.HasPrefix(pick.Args, "<") && !strings.Contains(s.value(), " ") {
					s.setValue("/" + pick.Name + " ")
					return nil
				}
				return m.submit("/" + pick.Name)
			}
			m.acceptMention(t)
			return nil
		case "esc":
			s.dismissed = s.value()
			return nil
		}
	}
	// Vim keys (the "vim" setting). Off, the state is dropped so turning it
	// back on starts in insert mode.
	if !vimEnabled(t) {
		s.vim = vimState{}
	} else if s.vim.normal {
		if cmd, handled := m.vimKey(t, km); handled {
			return cmd
		}
	} else if key == "esc" {
		s.vimToNormal()
		return nil
	}
	switch key {
	case "enter":
		return m.submit(s.value())
	case "shift+enter", "alt+enter", "ctrl+j":
		s.ta.InsertString("\n")
		return nil
	case "alt+s":
		if !s.empty() {
			s.pushStash(s.expand(s.value()))
			s.clear()
			m.saveStashes(t)
			m.setFlash("ok", "Draft stashed (Alt+P to pop)")
		}
		return nil
	case "alt+p":
		if n := len(s.stashes); n > 0 && s.empty() {
			s.setValue(s.stashes[n-1])
			s.stashes = s.stashes[:n-1]
			m.saveStashes(t)
		}
		return nil
	case "ctrl+r":
		s.openSearch()
		return nil
	case "ctrl+v", "alt+v":
		return m.pasteImage(t)
	case "backspace":
		// on an empty input, Backspace takes off the last image chip first
		if s.empty() && s.popImage() {
			return nil
		}
	case "?":
		if s.empty() {
			m.openHelp()
			return nil
		}
	case "up":
		if s.ta.Line() == 0 && len(s.history) > 0 {
			s.histPrev()
			return nil
		}
	case "down":
		if s.ta.Line() >= s.ta.LineCount()-1 && s.hidx >= 0 {
			s.histNext()
			return nil
		}
	case "ctrl+u":
		// delete the current line
		v := s.value()
		lines := strings.Split(v, "\n")
		if len(lines) > 0 {
			li := s.ta.Line()
			if li < len(lines) {
				lines = append(lines[:li], lines[li+1:]...)
				s.setValue(strings.Join(lines, "\n"))
			}
		}
		return nil
	}
	if km.Paste {
		if path, ok := imagePathFromPaste(string(km.Runes)); ok {
			m.attachImageFile(t, path)
			return nil
		}
		text := s.collapsePaste(string(km.Runes))
		s.ta.InsertString(text)
		s.hidx = -1
		return nil
	}
	s.hidx = -1
	before := s.value()
	var cmd tea.Cmd
	s.ta, cmd = s.ta.Update(km)
	if s.value() != before {
		s.acSel = 0
	}
	return cmd
}

// mentionQuery returns the text after the trailing "@", or "".
func mentionQuery(s *inputState) string {
	q, _ := s.mentionToken()
	return q
}

// acceptMention replaces the trailing "@partial" with the selected file.
func (m *Model) acceptMention(t *tab) {
	s := t.in
	files := t.be.FileCandidates(mentionQuery(s), 8)
	if len(files) == 0 {
		return
	}
	pick := files[clamp(s.acSel, 0, len(files)-1)]
	_ = session.RecordPick(session.PickFile, pick) // frecency ranks files by use
	v := s.value()
	i := strings.LastIndexAny(v, " \n\t")
	s.setValue(v[:i+1] + "@" + pick + " ")
}

// searchKey handles keys while Ctrl+R is open.
func (m *Model) searchKey(t *tab, km tea.KeyMsg) tea.Cmd {
	s := t.in
	ss := s.search
	switch km.String() {
	case "esc", "ctrl+r":
		s.search = nil
		return nil
	case "enter":
		if len(ss.matches) > 0 {
			s.setValue(ss.matches[ss.sel])
		}
		s.search = nil
		return nil
	case "up":
		if n := len(ss.matches); n > 0 {
			ss.sel = (ss.sel - 1 + n) % n
		}
	case "down":
		if n := len(ss.matches); n > 0 {
			ss.sel = (ss.sel + 1) % n
		}
	case "backspace":
		if r := ss.query; len(r) > 0 {
			ss.query = r[:len(r)-1]
			ss.sel = 0
			s.refreshSearch()
		}
	default:
		if txt := printable(km); txt != "" {
			ss.query = append(ss.query, []rune(txt)...)
			ss.sel = 0
			s.refreshSearch()
		}
	}
	return nil
}

// ---------------------------------------------------------------- submit

// submit runs whatever the input holds: a slash command, a shell command, a
// steering message (while busy), or a new turn.
func (m *Model) submit(raw string) tea.Cmd {
	t := m.cur()
	s := t.in
	if strings.TrimSpace(raw) == "" {
		// an image on its own is a message; it waits for the turn to end
		if !s.hasImages() {
			return nil
		}
		if t.busy {
			m.setFlash("info", "Type a message to send the image with it")
			return nil
		}
	}
	text := strings.TrimSpace(s.expand(raw))
	if !secretLine(text) {
		s.pushHistory(raw)
	}
	s.clear()
	s.dismissed = ""
	if strings.HasPrefix(text, "/") {
		if c, _ := findCommand(text); c != nil {
			bumpFrecency(c.Name)
		}
		return m.runCommand(text)
	}
	if strings.HasPrefix(text, "!") {
		return m.runShell(t, strings.TrimSpace(text[1:]))
	}
	if !t.busy && t.browse >= 0 && t.browse < len(t.cps)-1 {
		if br, ok := t.be.(CheckpointBrancher); ok {
			return m.confirmBranch(t, br, raw, text)
		}
	}
	if t.busy {
		m.enterChat()
		if len(t.steerQ) >= maxSteerQueue {
			m.setFlash("err", "steering queue is full (5); wait for the next step")
			s.setValue(raw)
			return nil
		}
		steered := false
		if is, ok := t.be.(ImageSteerer); ok && s.hasImages() {
			steered = is.SteerWithImages(text, s.takeImages())
		} else {
			steered = t.be.Steer(text)
			if steered && s.hasImages() {
				m.setFlash("info", "Attached images go with your next message, after this turn")
			}
		}
		if steered {
			t.steerQ = append(t.steerQ, text)
			return nil
		}
		// the turn ended while we were typing: send it as a new turn
	}
	if text == "" {
		text = imageOnlyPrompt
	}
	return m.startTurnWith(t, text, s.takeImages())
}

// startTurn sends a prompt as a new turn.
func (m *Model) startTurn(t *tab, text string) tea.Cmd {
	return m.startTurnWith(t, text, nil)
}

// startTurnWith sends a prompt and its images as a new turn.
func (m *Model) startTurnWith(t *tab, text string, images []llm.Part) tea.Cmd {
	now := time.Now()
	m.enterChat()
	t.chat.AddUser(text, len(images), now, false)
	t.chat.AttachImages(images)
	t.busy = true
	t.since = now
	t.be.Submit(text, images)
	return nil
}

// runShell runs a "!command" and prints its output as a system message.
func (m *Model) runShell(t *tab, command string) tea.Cmd {
	if command == "" {
		return nil
	}
	t.chat.AddSystem("shell", "$ "+command)
	be := t.be
	id := t.id
	return func() tea.Msg {
		out, code, err := be.Shell(shellCtx(), command)
		text := strings.TrimRight(out, "\n")
		if err != nil {
			text = strings.TrimSpace(text + "\n" + err.Error())
		}
		if code != 0 {
			text += fmt.Sprintf("\n(exit %d)", code)
		}
		return shellMsg{tab: id, text: text}
	}
}

// shellMsg carries the output of a "!command".
type shellMsg struct {
	tab  int
	text string
}

// ---------------------------------------------------------------- render

// boxLines draws the input box (or the Ctrl+R overlay) at width w.
func (m *Model) boxLines(t *tab, w int, modeCol lipgloss.TerminalColor) []string {
	th := m.th
	s := t.in
	s.ta.FocusedStyle.Prompt = lipgloss.NewStyle().Bold(true).Foreground(modeCol)
	ph, phStyle := inputPlaceholderAt(t, th, m.frame)
	// The textarea cuts a long placeholder at its width without a mark; cut
	// it here with an ellipsis instead (one cell is kept for the cursor).
	if room := s.ta.Width() - 1; room > 1 && ansi.StringWidth(ph) > room {
		ph = ansi.Truncate(ph, room, "…")
	}
	s.ta.Placeholder, s.ta.FocusedStyle.Placeholder = ph, phStyle
	h := s.inputHeight(m.height)
	s.ta.SetHeight(h)
	s.fitViewport(h)
	var body []string
	border := th.Border
	footer := ""
	// chips sit in the top border, so the box keeps its height
	title := ""
	switch {
	case s.search != nil:
		border = th.Warning
		q := string(s.search.query)
		row := fg(th.Warning).Bold(true).Render("search: ") + fg(th.TextPrimary).Render(q) + fg(th.Warning).Render("▌")
		body = append(body, " "+row)
		footer = "type to filter · Enter load · Esc cancel"
	default:
		if s.popupKind() == "slash" {
			border = th.BorderSlash
		} else if t.busy {
			border = th.BrandDim
		} else {
			border = th.BorderFocused
		}
		for _, l := range strings.Split(s.ta.View(), "\n") {
			body = append(body, " "+l)
		}
		if strings.Contains(s.value(), "\n") {
			footer = "Shift+⏎ newline · ^U del line"
		}
		if vimEnabled(t) {
			footer = vimModeLabel(s)
		}
		if s.probing {
			// the right-hand slot of the box while the clipboard is read
			footer = "reading image…"
		}
		title = s.chips()
	}
	return frame(title, "", body, footer, w, border, th)
}

// shellCtx is the context for "!command" runs.
func shellCtx() context.Context { return context.Background() }

// secretLine reports whether a submitted line carries a secret, such as
// "/remote token set <token>" or "/keys <provider> <key>". Such lines are
// kept out of the Up-arrow history so a token cannot be recalled or shown
// again later.
func secretLine(text string) bool {
	f := strings.Fields(strings.ToLower(text))
	if len(f) >= 3 && (f[0] == "/remote" || f[0] == "/hearth") && f[1] == "token" && f[2] == "set" {
		return true
	}
	if len(f) >= 2 && (f[0] == "/keys" || f[0] == "/api-keys") {
		return true
	}
	return redact.Default().Redact(text) != text
}

// ImageSteerer is a backend whose running turn can take images with a
// steering message, so a pasted screenshot reaches the model mid-turn.
type ImageSteerer interface {
	SteerWithImages(msg string, images []llm.Part) bool
}

// confirmBranch handles a message sent while an earlier checkpoint is shown:
// sending from there drops the later turns, so it asks first. Yes reverts the
// later turns' files, drops their messages and sends; No puts the text back.
func (m *Model) confirmBranch(t *tab, br CheckpointBrancher, raw, text string) tea.Cmd {
	cp := t.cps[t.browse]
	images := t.in.takeImages()
	msg := fmt.Sprintf("You are viewing turn %d. Sending from here drops everything after it: the later turns' file changes are reverted and their messages are removed for good.", cp.Turn)
	c := NewConfirm(m.th, "Send from an earlier turn?", msg, false, func() tea.Cmd {
		if _, err := br.BranchFrom(cp.Turn); err != nil {
			t.in.setValue(raw)
			return cmdErrText("could not go back to turn " + itoa(cp.Turn) + ": " + err.Error())
		}
		m.replayTranscript(t)
		t.browse = -1
		m.syncRail(t)
		if text == "" {
			text = imageOnlyPrompt
		}
		return m.startTurnWith(t, text, images)
	}).WithLabels("Send and drop later turns", "Cancel")
	c.onNo = func() { t.in.setValue(raw) }
	m.pushModal(c)
	return nil
}
