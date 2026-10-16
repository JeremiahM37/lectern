package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// tagline is the landing page line under the wordmark.
const tagline = "Knows your code. Writes it carefully."

// wordmarkRows returns the five rows of the block wordmark.
func wordmarkRows() []string {
	letters := map[rune][5]string{
		'S': {".####", "#....", ".###.", "....#", "####."},
		'C': {".###.", "#...#", "#....", "#...#", ".###."},
		'R': {"####.", "#...#", "####.", "#..#.", "#...#"},
		'I': {"#####", "..#..", "..#..", "..#..", "#####"},
		'B': {"####.", "#...#", "####.", "#...#", "####."},
		'E': {"#####", "#....", "####.", "#....", "#####"},
	}
	out := make([]string, 5)
	for i, r := range "SCRIBE" {
		g := letters[r]
		for y := 0; y < 5; y++ {
			if i > 0 {
				out[y] += " "
			}
			out[y] += strings.ReplaceAll(strings.ReplaceAll(g[y], "#", "█"), ".", " ")
		}
	}
	return out
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

// viewLanding draws the landing page into exactly rows lines. The input box
// sits at the bottom of the centred block at 60% of the width.
func (m *Model) viewLanding(w, rows int) []string {
	th := m.th
	t := m.cur()
	center := func(s string) string { return lipgloss.PlaceHorizontal(w, lipgloss.Center, s) }

	var parts []string
	showMascot := rows >= 22
	showTips := rows >= 16
	if showMascot {
		frame := mascotFrames[(m.frame/mascotTick)%len(mascotFrames)]
		for _, l := range frame {
			parts = append(parts, center(fg(th.Brand).Render(l)))
		}
		parts = append(parts, "")
	}
	if w >= 36 {
		for _, l := range wordmarkRows() {
			parts = append(parts, center(fgb(th.Brand).Render(l)))
		}
	} else {
		parts = append(parts, center(fgb(th.Brand).Render("SCRIBE")))
	}
	parts = append(parts, "", center(fg(th.TextMuted).Italic(true).Render(tagline)), "")

	if showTips {
		parts = append(parts, center(fg(th.TextDim).Render("tips")))
		base := (m.frame / (6 * 20)) % len(tips)
		for i := 0; i < 3; i++ {
			tip := tips[(base+i)%len(tips)]
			parts = append(parts, center(fg(th.TextSecondary).Render("✦ "+truncWord(tip, min(w-10, 68)))))
		}
		parts = append(parts, "")
	}

	boxW := w * 60 / 100
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
