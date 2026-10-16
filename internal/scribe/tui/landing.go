package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
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

// mascotFrames is the quill writing a line of ink. The ink line grows and
// shrinks; the nib stays put.
var mascotFrames = [][]string{
	{"     ╱│", "   ╱ │", " ✒   ", "     "},
	{"     ╱│", "   ╱ │", " ✒   ", " ·   "},
	{"     ╱│", "   ╱ │", " ✒   ", " ··  "},
	{"     ╱│", "   ╱ │", " ✒   ", " ···."},
	{"     ╱│", "   ╱ │", " ✒   ", " ··  "},
	{"     ╱│", "   ╱ │", " ✒   ", " ·   "},
}

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
		frame := mascotFrames[(m.frame/5)%len(mascotFrames)]
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
			parts = append(parts, center(fg(th.TextSecondary).Render("✦ "+render.Truncate(tip, min(w-8, 70)))))
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
