package chat

import (
	"fmt"
	"strings"
)

// resultSuffix is the text after " → " on a finished row. A subagent
// dispatch counts its agents; otherwise the tool's summary is shown, and a
// tool that set none shows its first output line.
func (v *View) resultSuffix(c *Call) string {
	if c.Tool == "dispatch" && len(c.Agents) > 0 {
		done := 0
		for _, a := range c.Agents {
			if a.Status == "done" {
				done++
			}
		}
		return fmt.Sprintf("%d/%d agents", done, len(c.Agents))
	}
	if c.Result == nil {
		return ""
	}
	// The tool's own summary wins; the first output line is the fallback.
	if c.Result.Summary != "" {
		return clip(c.Result.Summary, 60)
	}
	return clip(firstLine(c.Result.Output), 40)
}

// isEditResultRow reports a finished, successful edit whose row shows the
// tool's own first output line in place of the verb and path.
func isEditResultRow(c *Call) bool {
	return isEditTool(c.Tool) && c.Status == "done" && c.Result != nil &&
		!c.Result.IsError && firstLine(c.Result.Output) != ""
}

// editResultText is that first output line, cut to 40 with "...".
func editResultText(c *Call) string {
	return clip(strings.TrimSpace(firstLine(c.Result.Output)), 40)
}

// awaitingPlanReview is the line under a running plan: the plan waits for
// the user to pick an answer below.
func (v *View) awaitingPlanReview() string {
	return fg(v.th.Amber, "◎ Awaiting review — select below")
}
