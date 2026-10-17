package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// Vim keys in the input box (the "vim" setting). The box starts in insert
// mode, so typing works as it always did. Esc goes to normal mode, where the
// keys below edit the text instead of inserting it:
//
//	h j k l   move             w b      words
//	0 $       line start/end   x        delete the character under the cursor
//	dd        delete the line  u        undo the last change
//	i a A I   insert (here, after, at end, at first non-blank)
//
// Enter still sends the message in normal mode, and the Ctrl keys and arrows
// keep their usual meaning. Esc while a turn runs still stops the turn; the
// global key handler takes it before the input sees it.

// vimState is the editing mode of one input box.
type vimState struct {
	normal  bool      // normal mode; false is insert mode
	pending string    // first key of a two-key command ("d" for dd)
	undo    []vimSnap // the text before each change, newest last
}

// vimSnap is the input text and cursor before a change.
type vimSnap struct {
	text     string
	row, col int
}

// vimUndoDepth bounds the undo history of one input box.
const vimUndoDepth = 100

// vimEnabled reports whether the "vim" setting is on for the tab's backend.
func vimEnabled(t *tab) bool {
	if t.be == nil {
		return false
	}
	on, _ := t.be.Config()["vim"].(bool)
	return on
}

// vimCursor is the cursor as (line, column) in runes.
func (s *inputState) vimCursor() (int, int) {
	li := s.ta.LineInfo()
	return s.ta.Line(), li.StartColumn + li.ColumnOffset
}

// vimPlace moves the cursor to (row, col). The textarea moves by wrapped
// visual lines, so it is walked to the row first, then the column is set.
func (s *inputState) vimPlace(row, col int) {
	row = clamp(row, 0, s.ta.LineCount()-1)
	for i := 0; s.ta.Line() > row && i < 100000; i++ {
		s.ta.CursorUp()
	}
	for i := 0; s.ta.Line() < row && i < 100000; i++ {
		s.ta.CursorDown()
	}
	s.ta.SetCursor(col)
}

// vimSetText replaces the input text and puts the cursor at (row, col).
func (s *inputState) vimSetText(text string, row, col int) {
	s.setValue(text)
	s.vimPlace(row, col)
}

// vimSave records the current text so the next change can be undone.
func (s *inputState) vimSave() {
	row, col := s.vimCursor()
	s.vim.undo = append(s.vim.undo, vimSnap{text: s.value(), row: row, col: col})
	if n := len(s.vim.undo); n > vimUndoDepth {
		s.vim.undo = append([]vimSnap(nil), s.vim.undo[n-vimUndoDepth:]...)
	}
}

// vimUndo restores the text before the last change.
func (s *inputState) vimUndo() {
	n := len(s.vim.undo)
	if n == 0 {
		return
	}
	snap := s.vim.undo[n-1]
	s.vim.undo = s.vim.undo[:n-1]
	s.vimSetText(snap.text, snap.row, snap.col)
}

// vimToNormal leaves insert mode. The cursor steps back one place, as in Vim.
func (s *inputState) vimToNormal() {
	s.vim.normal = true
	s.vim.pending = ""
	row, col := s.vimCursor()
	if col > 0 {
		col--
	}
	s.vimPlace(row, col)
}

// vimKey handles a key in normal mode. handled=false lets the key take its
// usual path: Enter sends, Ctrl keys and arrows keep working.
func (m *Model) vimKey(t *tab, km tea.KeyMsg) (tea.Cmd, bool) {
	s := t.in
	if km.Paste {
		// pasted text is text: it goes in as it would in insert mode
		s.vim.normal = false
		return nil, false
	}
	if km.Type == tea.KeyRunes && !km.Alt && len(km.Runes) > 1 {
		// A burst of keys in one read (key repeat, fast typing) is one
		// command per key; once a key enters insert mode the rest is text.
		for i, r := range km.Runes {
			if !s.vim.normal {
				s.ta.InsertString(string(km.Runes[i:]))
				break
			}
			m.vimKey(t, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		return nil, true
	}
	if km.Type != tea.KeyRunes || km.Alt || len(km.Runes) != 1 {
		switch km.String() {
		case "esc":
			s.vim.pending = ""
			return nil, true
		case "backspace", "left":
			s.vimMove(0, -1)
			return nil, true
		case "right":
			s.vimMove(0, 1)
			return nil, true
		case "up":
			s.vimMove(-1, 0)
			return nil, true
		case "down":
			s.vimMove(1, 0)
			return nil, true
		case "enter":
			s.vim.normal = false
			return nil, false
		}
		return nil, false
	}
	r := km.Runes[0]
	if s.vim.pending != "" {
		// the second key of dd; any other key cancels the command
		pending := s.vim.pending
		s.vim.pending = ""
		if pending == "d" && r == 'd' {
			s.vimDeleteLine()
		}
		return nil, true
	}
	if r == '?' && s.empty() {
		return nil, false // the help key works on an empty box in any mode
	}
	lines := strings.Split(s.value(), "\n")
	row, col := s.vimCursor()
	row = clamp(row, 0, len(lines)-1)
	line := []rune(lines[row])
	switch r {
	case 'h':
		s.vimMove(0, -1)
	case 'l':
		s.vimMove(0, 1)
	case 'j':
		s.vimMove(1, 0)
	case 'k':
		s.vimMove(-1, 0)
	case '0':
		s.vimPlace(row, 0)
	case '$':
		s.vimPlace(row, len(line)-1)
	case 'w':
		nr, nc := vimPosOf(lines, vimNextWord(vimJoin(lines), vimOffsetOf(lines, row, col)))
		s.vimPlace(nr, nc)
	case 'b':
		nr, nc := vimPosOf(lines, vimPrevWord(vimJoin(lines), vimOffsetOf(lines, row, col)))
		s.vimPlace(nr, nc)
	case 'x':
		if col >= len(line) {
			col = len(line) - 1
		}
		if col >= 0 {
			s.vimSave()
			nl := append(append([]rune{}, line[:col]...), line[col+1:]...)
			lines[row] = string(nl)
			s.vimSetText(strings.Join(lines, "\n"), row, clamp(col, 0, max(0, len(nl)-1)))
		}
	case 'd':
		s.vim.pending = "d"
	case 'u':
		s.vimUndo()
	case 'i':
		s.vimSave()
		s.vim.normal = false
	case 'a':
		s.vimSave()
		s.vim.normal = false
		s.vimPlace(row, min(col+1, len(line)))
	case 'A':
		s.vimSave()
		s.vim.normal = false
		s.vimPlace(row, len(line))
	case 'I':
		s.vimSave()
		s.vim.normal = false
		s.vimPlace(row, vimFirstNonBlank(line))
	default:
		// every other letter or symbol is a command we do not have; it must
		// not be typed into the text
	}
	return nil, true
}

// vimMove moves the cursor by dr lines and dc columns, staying on the text.
// In normal mode the cursor sits on a character, never past the last one.
func (s *inputState) vimMove(dr, dc int) {
	lines := strings.Split(s.value(), "\n")
	row, col := s.vimCursor()
	row = clamp(row+dr, 0, len(lines)-1)
	col = clamp(col+dc, 0, max(0, len([]rune(lines[row]))-1))
	s.vimPlace(row, col)
}

// vimDeleteLine is dd: the cursor's line goes, the cursor lands on the line
// that moves up into its place.
func (s *inputState) vimDeleteLine() {
	s.vimSave()
	lines := strings.Split(s.value(), "\n")
	row, _ := s.vimCursor()
	row = clamp(row, 0, len(lines)-1)
	if len(lines) == 1 {
		s.vimSetText("", 0, 0)
		return
	}
	lines = append(lines[:row], lines[row+1:]...)
	row = min(row, len(lines)-1)
	s.vimSetText(strings.Join(lines, "\n"), row, vimFirstNonBlank([]rune(lines[row])))
}

// vimFirstNonBlank is the column of the first non-space rune, or 0.
func vimFirstNonBlank(line []rune) int {
	for i, r := range line {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return 0
}

// vimJoin is the whole text as runes, lines separated by newlines.
func vimJoin(lines []string) []rune { return []rune(strings.Join(lines, "\n")) }

// vimOffsetOf is the rune offset of (row, col) in the joined text.
func vimOffsetOf(lines []string, row, col int) int {
	off := 0
	for i := 0; i < row && i < len(lines); i++ {
		off += len([]rune(lines[i])) + 1
	}
	return off + col
}

// vimPosOf is the (row, col) of a rune offset in the joined text.
func vimPosOf(lines []string, off int) (int, int) {
	for i, l := range lines {
		n := len([]rune(l))
		if off <= n || i == len(lines)-1 {
			return i, clamp(off, 0, n)
		}
		off -= n + 1
	}
	return 0, 0
}

// vimClass groups runes for word motions: 0 blank, 1 word, 2 punctuation.
func vimClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
		return 1
	}
	return 2
}

// vimNextWord is the start of the next word (w). It stops at the last rune
// when there is no further word.
func vimNextWord(text []rune, i int) int {
	n := len(text)
	if n == 0 {
		return 0
	}
	i = clamp(i, 0, n-1)
	c := vimClass(text[i])
	j := i
	if c != 0 {
		for j < n && vimClass(text[j]) == c {
			j++
		}
	}
	for j < n && vimClass(text[j]) == 0 {
		j++
	}
	if j >= n {
		return n - 1
	}
	return j
}

// vimPrevWord is the start of the previous word (b).
func vimPrevWord(text []rune, i int) int {
	if i <= 0 || len(text) == 0 {
		return 0
	}
	j := min(i, len(text)) - 1
	for j > 0 && vimClass(text[j]) == 0 {
		j--
	}
	c := vimClass(text[j])
	for j > 0 && vimClass(text[j-1]) == c {
		j--
	}
	return j
}

// vimModeLabel is the footer label while vim keys are on.
func vimModeLabel(s *inputState) string {
	if s.vim.normal {
		return "-- NORMAL --  i insert · hjkl w b 0 $ · x dd u"
	}
	return "-- INSERT --  Esc normal"
}
