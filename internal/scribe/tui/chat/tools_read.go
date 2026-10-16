package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// readPlainLimit is the output size above which a read is shown as plain
// text: highlighting a very large file costs more than it shows.
const readPlainLimit = 64000

// readDetailRows is how many lines of a read result the row shows.
const readDetailRows = 30

// numberedLine matches the gutter the read tool writes before each line:
// a right-aligned number and two spaces.
var numberedLine = regexp.MustCompile(`^\s*(\d+)\s{2}`)

// stripLineNumbers removes the read tool's line-number gutter when at least
// half of the lines carry one. It returns the bare lines and, for each, its
// number (0 for a line without one).
func stripLineNumbers(lines []string) (bare []string, nums []int) {
	hits := 0
	for _, l := range lines {
		if numberedLine.MatchString(l) {
			hits++
		}
	}
	bare = make([]string, len(lines))
	nums = make([]int, len(lines))
	copy(bare, lines)
	if len(lines) == 0 || hits*2 < len(lines) {
		return bare, nums
	}
	for i, l := range lines {
		if m := numberedLine.FindStringSubmatchIndex(l); m != nil {
			nums[i], _ = strconv.Atoi(l[m[2]:m[3]])
			bare[i] = l[m[1]:]
		}
	}
	return bare, nums
}

// readDetail is the expanded body of a read: the first lines, highlighted by
// the file's extension, with the file's own line numbers in a gutter. Output
// over readPlainLimit is shown plain.
func (v *View) readDetail(c *Call, path string) []string {
	th := v.th
	out := c.Result.Output
	src := headOf(strings.Split(out, "\n"), readDetailRows)
	if len(out) > readPlainLimit {
		var lines []string
		for _, l := range src {
			lines = append(lines, fg(th.TextSecondary, l))
		}
		return lines
	}
	bare, nums := stripLineNumbers(src)
	body := splitLines(render.Highlight(strings.Join(bare, "\n"), render.LangFromPath(path), th))
	if len(body) < len(src) {
		// Highlighting may not keep a trailing blank line; keep the rows aligned.
		for len(body) < len(src) {
			body = append(body, "")
		}
	}
	width := 0
	for _, n := range nums {
		if w := len(fmt.Sprint(n)); w > width {
			width = w
		}
	}
	var lines []string
	for i, l := range body {
		if i >= len(src) {
			break
		}
		if width > 0 {
			gutter := ""
			if nums[i] > 0 {
				gutter = fmt.Sprint(nums[i])
			}
			l = fg(th.TextDim, fmt.Sprintf("%*s ", width, gutter)) + l
		}
		lines = append(lines, l)
	}
	return lines
}
