package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"

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

// inputPlaceholder is the text shown in an empty input box, and its style.
// Scribe's own copy: the rotating tips live in the footer instead. Browsing
// only names the rewind while no turn runs: a message sent mid-turn steers
// that turn (input.go), so the rewind prompt would be false then.
func inputPlaceholder(t *tab, th theme.Theme) (string, lipgloss.Style) {
	if cp := browsedCheckpoint(t); cp != nil && !t.busy {
		return icons.Icon("rewind") + " send a message to rewind to checkpoint #" + itoa(cp.Turn), fg(th.Warning)
	}
	if t.busy {
		return "'/' for commands · or steer by sending a new message", fg(th.TextMuted)
	}
	return "Ask about your code, or type / for commands", fg(th.TextMuted)
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
