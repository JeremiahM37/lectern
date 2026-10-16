package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// diffLine is one line of a unified diff.
type diffLine struct {
	op       byte // '+', '-' or ' '
	old, new int
	text     string
}

type diffInfo struct {
	lines          []diffLine
	added, removed int
	created        bool
}

var hunkRe = regexp.MustCompile(`-(\d+)(?:,\d+)? \+(\d+)`)

// parseDiff reads a unified diff as produced by the edit tools.
func parseDiff(d string) diffInfo {
	var di diffInfo
	oldN, newN := 0, 0
	inHunk := false
	for _, l := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			inHunk = true
			if m := hunkRe.FindStringSubmatch(l); m != nil {
				oldN, _ = strconv.Atoi(m[1])
				newN, _ = strconv.Atoi(m[2])
			}
		case !inHunk && strings.HasPrefix(l, "--- "):
			if strings.Contains(l, "/dev/null") {
				di.created = true
			}
		case !inHunk:
			// file header lines
		case strings.HasPrefix(l, "+"):
			di.lines = append(di.lines, diffLine{op: '+', new: newN, text: l[1:]})
			newN++
			di.added++
		case strings.HasPrefix(l, "-"):
			di.lines = append(di.lines, diffLine{op: '-', old: oldN, text: l[1:]})
			oldN++
			di.removed++
		case strings.HasPrefix(l, " "):
			di.lines = append(di.lines, diffLine{op: ' ', old: oldN, new: newN, text: l[1:]})
			oldN++
			newN++
		}
	}
	return di
}

// largeDiff is the threshold above which a diff is summarised unless the row
// was opened by hand.
const largeDiff = 50

// renderDiff draws the body of a diff. Unchanged runs keep two lines at each
// end; a creation shows its first 3 and last 2 lines. full disables both.
func (v *View) renderDiff(di diffInfo, path string, w int, full bool) []string {
	lang := render.LangFromPath(path)
	gw := 1
	for _, l := range di.lines {
		if n := maxOf(l.old, l.new); len(fmt.Sprint(n)) > gw {
			gw = len(fmt.Sprint(n))
		}
	}
	type item struct {
		dl     *diffLine
		marker string
	}
	var items []item
	lines := di.lines
	switch {
	case !full && di.created && di.removed == 0 && len(lines) > 10:
		for i := 0; i < 3; i++ {
			items = append(items, item{dl: &lines[i]})
		}
		items = append(items, item{marker: fmt.Sprintf("⋯ %d lines", len(lines)-5)})
		for i := len(lines) - 2; i < len(lines); i++ {
			items = append(items, item{dl: &lines[i]})
		}
	default:
		for i := 0; i < len(lines); {
			if lines[i].op != ' ' || full {
				items = append(items, item{dl: &lines[i]})
				i++
				continue
			}
			j := i
			for j < len(lines) && lines[j].op == ' ' {
				j++
			}
			if n := j - i; n > 4 {
				items = append(items, item{dl: &lines[i]}, item{dl: &lines[i+1]})
				items = append(items, item{marker: fmt.Sprintf("⋯ %d unchanged lines", n-4)})
				items = append(items, item{dl: &lines[j-2]}, item{dl: &lines[j-1]})
			} else {
				for k := i; k < j; k++ {
					items = append(items, item{dl: &lines[k]})
				}
			}
			i = j
		}
	}
	var out []string
	for _, it := range items {
		if it.dl == nil {
			out = append(out, fit(fg(v.tone("textFaint"), "  "+it.marker), w))
			continue
		}
		d := it.dl
		var bg, signCol lipgloss.TerminalColor
		switch d.op {
		case '+':
			bg, signCol = v.th.DiffAddedBg, v.th.DiffAddedSign
		case '-':
			bg, signCol = v.th.DiffRemovedBg, v.th.DiffRemovedSign
		default:
			signCol = v.th.TextDim
		}
		text := d.text
		if lang != "" {
			text = render.Highlight(d.text, lang, v.th)
		}
		// Both gutters: old line on the left, new line on the right. A removed
		// line has no new number and an added line has no old one.
		oldNo, newNo := "", ""
		if d.op != '+' && d.old > 0 {
			oldNo = fmt.Sprint(d.old)
		}
		if d.op != '-' && d.new > 0 {
			newNo = fmt.Sprint(d.new)
		}
		gutter := fg(v.th.TextDim, fmt.Sprintf("%*s %*s", gw, oldNo, gw, newNo))
		row := gutter + " " + fg(signCol, string(d.op)) + " " + text
		if bg == nil {
			out = append(out, fit(row, w))
		} else {
			out = append(out, bgLine(row, w, bg))
		}
	}
	return out
}

func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}
