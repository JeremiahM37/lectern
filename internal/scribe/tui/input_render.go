package tui

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Popup sizing from the spec: a share of the terminal height, within a floor
// and a ceiling. The slash list and the Ctrl+R list differ only in their
// bounds.

// slashPopupRows is how many command rows the slash popup shows.
func slashPopupRows(termRows int) int { return clamp(termRows/4, 4, 8) }

// historyPopupRows is how many matches the Ctrl+R popup shows.
func historyPopupRows(termRows int) int { return clamp(termRows/5, 3, 8) }

// placeholderTipEvery is how long each idle placeholder tip stays up (spec 01
// §4.2 item 4: every 12 s while the input is empty and idle).
const placeholderTipEvery = 12 * time.Second

// idleTips are the rotating placeholder tips for an empty, idle input, in the
// spec's "text · hint" shape. Each key or command was checked against its
// handler; the tests check the commands. The first tip is the copy Scribe
// has always shown, so the first 12 s look the same as before.
var idleTips = []string{
	"Ask about your code, or type / for commands",
	"Search your prompt history · Ctrl+R",
	"Every key and command, from an empty box · ?",
	"Filter every command in the palette · Ctrl+K",
	"Switch models · Ctrl+L",
	"Stash the draft for later, then bring it back · Alt+S / Alt+P",
	"Run a shell command in the project · !command",
	"Mention a file from the project · @path",
	"Add a line without sending · Shift+Enter",
	"Cycle the working mode · Shift+Tab",
	"Hide the model's thinking, or ask for more · /reasoning",
	"Expand or collapse tool output · Ctrl+O",
	"Copy the message the transcript is focused on · Ctrl+Y",
	"Copy the last answer to the clipboard · /copy",
	"Export the session as markdown or JSON · /export",
	"Browse earlier sessions · Ctrl+P",
	"Start a new session · Ctrl+N",
	"Open a new tab, up to five at once · Ctrl+T",
	"Switch between open tabs · Alt+1 to Alt+5",
	"Open the git menu: status, diff, commit · Ctrl+G",
	"Show what a turn changed · /diff",
	"List the turns that changed files · /checkpoints",
	"Step back through checkpoints · Ctrl+B",
	"Undo the last turn and restore its files · Ctrl+Z",
	"Open the error log · Alt+R",
	"Show or hide the checkpoint rail · Alt+C",
	"Shorten a long conversation · /compact",
	"Preview colour themes as you move through them · /theme",
}

// idleTip returns the tip for an idle empty input at the given frame. The
// frame clock moves once per frameGap, so frame × frameGap is the elapsed
// time and the tip changes every placeholderTipEvery of it, with no timer.
func idleTip(frame int) string {
	slot := time.Duration(frame) * frameGap / placeholderTipEvery
	return idleTips[int(slot)%len(idleTips)]
}

// inputPlaceholder is the placeholder at frame 0. Callers that have the
// model should use inputPlaceholderAt so the idle tip rotates.
func inputPlaceholder(t *tab, th theme.Theme) (string, lipgloss.Style) {
	return inputPlaceholderAt(t, th, 0)
}

// inputPlaceholderAt is the text shown in an empty input box, and its style.
// The first match wins, in the spec's order: checkpoint browsing, busy, then
// the rotating idle tip. Browsing only names the rewind while no turn runs: a
// message sent mid-turn steers that turn (input.go), so the rewind prompt
// would be false then. The idle tip is the only one that depends on frame.
func inputPlaceholderAt(t *tab, th theme.Theme, frame int) (string, lipgloss.Style) {
	if cp := browsedCheckpoint(t); cp != nil && !t.busy {
		return fitPlaceholder(t, icons.Icon("rewind")+" send a message to rewind to checkpoint #"+itoa(cp.Turn)), fg(th.Warning)
	}
	if t.busy {
		return fitPlaceholder(t, "'/' for commands · or steer by sending a new message"), fg(th.TextMuted)
	}
	return fitPlaceholder(t, idleTip(frame)), fg(th.TextMuted)
}

// fitPlaceholder cuts text to the width of this tab's input, so the tip is
// never cut at some other width. The width is the textarea's own, which is
// the 60% landing box or the full chat width. One cell is kept for the
// cursor. A cut lands on a word boundary when one fits and ends with an
// ellipsis; text that fits is returned as it is.
func fitPlaceholder(t *tab, text string) string {
	if t.in == nil {
		return text
	}
	room := t.in.ta.Width() - 1
	if room <= 1 || ansi.StringWidth(text) <= room {
		return text
	}
	return truncWord(text, room)
}

// searchPopupLines draws the Ctrl+R list above the box: a header with the
// match count, then a window of matches around the selection. The selected
// row has a "› " marker and the matched text is bold.
func (s *inputState) searchPopupLines(m *Model, w int) []string {
	th := m.th
	ss := s.search
	count := itoa(len(ss.matches)) + " matches"
	if len(ss.matches) == 1 {
		count = "1 match"
	}
	rows := []string{
		fgb(th.Warning).Render("◷ history") + "  " + fg(th.TextMuted).Render(count),
	}
	vis := historyPopupRows(m.height)
	start := 0
	if ss.sel >= vis {
		start = ss.sel - vis + 1
	}
	end := min(len(ss.matches), start+vis)
	query := string(ss.query)
	for i := start; i < end; i++ {
		text := strings.SplitN(ss.matches[i], "\n", 2)[0]
		text = render.Truncate(text, max(1, w-8))
		mark := "  "
		if i == ss.sel {
			mark = fgb(th.BrandSecondary).Render("› ")
		}
		rows = append(rows, " "+mark+highlightMatch(text, query, th))
	}
	return popupBox(rows, w, th.Warning, "↑↓ move · Enter load · Esc cancel", th)
}

// highlightMatch draws text with the first case-insensitive occurrence of
// query in bold brandSecondary and the rest in textPrimary.
func highlightMatch(text, query string, th theme.Theme) string {
	base := fg(th.TextPrimary)
	hi := fgb(th.BrandSecondary)
	tr := []rune(text)
	at := indexFoldRunes(tr, []rune(query))
	if at < 0 {
		return base.Render(text)
	}
	end := at + len([]rune(query))
	return base.Render(string(tr[:at])) + hi.Render(string(tr[at:end])) + base.Render(string(tr[end:]))
}

// indexFoldRunes finds needle in hay, ignoring case, one rune at a time so
// the positions stay valid for the original text. An empty needle is -1.
func indexFoldRunes(hay, needle []rune) int {
	if len(needle) == 0 {
		return -1
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		ok := true
		for j, r := range needle {
			if unicode.ToLower(hay[i+j]) != unicode.ToLower(r) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
