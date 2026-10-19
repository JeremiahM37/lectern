package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// tagline is the landing page line under the wordmark.
const tagline = "Knows your code. Writes it carefully."

// wordmarkGlyphs are the block letters of LECTERN, six rows tall, drawn with
// box characters only. Each letter is padded to its own width in
// wordmarkRows, so the rows of a letter always line up.
var wordmarkGlyphs = map[rune][6]string{
	'L': {"██╗     ", "██║     ", "██║     ", "██║     ", "███████╗", "╚══════╝"},
	'E': {"███████╗", "██╔════╝", "█████╗  ", "██╔══╝  ", "███████╗", "╚══════╝"},
	'C': {" ██████╗", "██╔════╝", "██║     ", "██║     ", "╚██████╗", " ╚═════╝"},
	'T': {"████████╗", "╚══██╔══╝", "   ██║   ", "   ██║   ", "   ██║   ", "   ╚═╝   "},
	'R': {"██████╗ ", "██╔══██╗", "██████╔╝", "██╔══██╗", "██║  ██║", "╚═╝  ╚═╝"},
	'N': {"███╗   ██╗", "████╗  ██║", "██╔██╗ ██║", "██║╚██╗██║", "██║ ╚████║", "╚═╝  ╚═══╝"},
}

// wordmarkGap is the space between two letters of the wordmark.
const wordmarkGap = "  "

// wordmarkRows returns the six rows of the LECTERN block wordmark. Its widest
// row is wordmarkWidth cells, so it fits 80 columns.
func wordmarkRows() []string {
	out := make([]string, 6)
	for i, r := range "LECTERN" {
		g := wordmarkGlyphs[r]
		for y := range out {
			if i > 0 {
				out[y] += wordmarkGap
			}
			out[y] += g[y]
		}
	}
	return out
}

// wordmarkWidth is the width in cells of the wordmark. The letters are
// single-width box characters, so a rune count is the cell count.
func wordmarkWidth() int {
	return lipgloss.Width(wordmarkRows()[0])
}

// mascotBase is the quill dipped into an inkwell. Every row is a single-cell
// glyph so the mascot lines up in any terminal.
var mascotBase = []string{
	"        ╱│",
	"      ╱╱ │",
	"    ╱╱  │",
	"  ✒╱   │",
	" ╭─┴─╮",
	" │ ~ │",
	" ╰───╯",
}

// mascotFrames are the four animation frames: the ink in the well shifts and
// a drop gathers under it, falls, and is gone. At 400 ms a frame it loops
// every 1.6 s.
var mascotFrames = func() [][]string {
	drips := []struct{ well, bottom string }{
		{"│ ~ │", " ╰───╯"},
		{"│ ~ │", " ╰─·─╯"},
		{"│ ~ │", " ╰─┊─╯"},
		{"│ ≈ │", " ╰─ ─╯"},
	}
	out := make([][]string, len(drips))
	for i, d := range drips {
		f := append([]string{}, mascotBase...)
		f[5] = " " + d.well
		f[6] = d.bottom
		out[i] = f
	}
	return out
}()

// mascotTick is how many 50 ms ticks a mascot frame lasts (400 ms).
const mascotTick = 8

// landingState is the animation state of the landing page.
type landingState struct {
	start time.Time
}

func newLandingState() landingState { return landingState{start: time.Now()} }

// Landing page gates: the spacer rows go under 18 rows. The block wordmark
// needs the terminal to be at least landingWordmarkRows tall and
// landingWordmarkCols wide (wordmarkWidth, so it fits); narrower or shorter,
// the page shows the small logo. (Empryo's drifting rune field is left out on
// purpose: it is that product's look, and on Scribe's page it read as noise
// around the mascot.)
const (
	landingSpacerRows   = 18
	landingWordmarkRows = 30
	landingWordmarkCols = 71
)

// statusBlock is the landing page's health line: a red LSP or MCP mark when a
// server failed, then the number of MCP servers that are ready. It is empty
// when there is nothing to say.
func (m *Model) statusBlock() string {
	th := m.th
	var parts []string
	mcpReady := 0
	var lspDown, mcpDown bool
	for _, s := range m.cur().be.Servers() {
		switch {
		case s.Kind == "lsp" && s.State == "failed":
			lspDown = true
		case s.Kind == "mcp" && s.State == "failed":
			mcpDown = true
		case s.Kind == "mcp" && s.State == "ready":
			mcpReady++
		}
	}
	if lspDown {
		parts = append(parts, fg(th.Error).Render("✕ LSP"))
	}
	if mcpDown {
		parts = append(parts, fg(th.Error).Render("✕ MCP"))
	}
	if mcpReady > 0 {
		parts = append(parts, fg(th.Success).Render("✓")+" "+fg(th.TextSecondary).Render(itoa(mcpReady)+" MCP"))
	}
	return strings.Join(parts, fg(th.TextFaint).Render("  │  "))
}

// landingTipText is one body tip on the landing page, fitted to the landing
// width less a margin on each side. A tip is cut only when it does not fit, and
// then at a word boundary with an ellipsis. The landing used to cap every tip
// at 68 cells, which cut the longest tips at 120 columns where there was room.
func landingTipText(tip string, w int) string {
	return "✦ " + truncWord(tip, w-10)
}

// viewLanding draws the landing page into exactly rows lines. The input box
// sits at the bottom of the centred block, 60% of the width on the landing
// page and growing during the transition (landingPct).
func (m *Model) viewLanding(w, rows int) []string {
	th := m.th
	t := m.cur()
	center := func(s string) string { return lipgloss.PlaceHorizontal(w, lipgloss.Center, s) }
	spacers := rows >= landingSpacerRows

	var parts []string
	showMascot := rows >= 22
	showTips := rows >= 16
	if showMascot {
		frame := mascotFrames[(m.frame/mascotTick)%len(mascotFrames)]
		for _, l := range frame {
			parts = append(parts, center(fg(th.Brand).Render(l)))
		}
		if spacers {
			parts = append(parts, "")
		}
	}
	if w >= landingWordmarkCols && m.height >= landingWordmarkRows {
		for _, l := range wordmarkRows() {
			parts = append(parts, center(fgb(th.Brand).Render(l)))
		}
	} else {
		parts = append(parts, center(fgb(th.Brand).Render("LECTERN")))
	}
	if spacers {
		parts = append(parts, "")
	}
	parts = append(parts, center(fg(th.TextMuted).Italic(true).Render(tagline)))
	if spacers {
		parts = append(parts, "")
	}

	if showTips {
		parts = append(parts, center(fg(th.TextDim).Render("tips")))
		base := (m.frame / (6 * 20)) % len(tips)
		for i := 0; i < 3; i++ {
			tip := tips[(base+i)%len(tips)]
			parts = append(parts, center(fg(th.TextSecondary).Render(landingTipText(tip, w))))
		}
		if spacers {
			parts = append(parts, "")
		}
	}
	if sb := m.statusBlock(); sb != "" {
		parts = append(parts, center(sb))
	}

	boxW := w * m.landingPct() / 100
	if boxW < 20 {
		boxW = min(w, 20)
	}
	box := m.boxLines(t, boxW, m.modeColor(t))
	if popup := t.in.popupLines(m, boxW); len(popup) > 0 {
		box = append(popup, box...)
	}
	for _, l := range box {
		parts = append(parts, lipgloss.PlaceHorizontal(w, lipgloss.Center, l))
	}
	hint := fg(th.TextFaint).Render("? help  ·  / commands  ·  Ctrl+K palette")
	if m.flash != "" {
		hint = fg(th.Info).Render(m.flash)
	}
	parts = append(parts, center(hint))

	// vertically centre within rows; if too tall, keep the input at the bottom
	if len(parts) > rows {
		parts = parts[len(parts)-rows:]
	}
	pad := (rows - len(parts)) / 2
	out := make([]string, 0, rows)
	for i := 0; i < pad; i++ {
		out = append(out, "")
	}
	out = append(out, parts...)
	return fitLines(out, w, rows)
}
