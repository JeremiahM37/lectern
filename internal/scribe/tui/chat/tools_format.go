package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// failedAfter matches a task report line the dispatch writes when a task
// failed after its attempts, e.g. "FAILED after 2 attempts: ...".
var failedAfter = regexp.MustCompile(`FAILED after (\d+) attempts?`)

// attemptNote summarises the failed tasks of a dispatch result: how many
// failed and the most attempts any one of them made. Empty when none failed
// after running.
func attemptNote(out string) string {
	ms := failedAfter.FindAllStringSubmatch(out, -1)
	if len(ms) == 0 {
		return ""
	}
	most := 0
	for _, m := range ms {
		if n, _ := strconv.Atoi(m[1]); n > most {
			most = n
		}
	}
	word := "attempts"
	if most == 1 {
		word = "attempt"
	}
	if len(ms) == 1 {
		return fmt.Sprintf("failed after %d %s", most, word)
	}
	return fmt.Sprintf("%d failed, up to %d %s", len(ms), most, word)
}

// agentProgress is the live count a running dispatch shows: "2/3 agents".
func agentProgress(agents []*agent.AgentInfo) string {
	done := 0
	for _, a := range agents {
		if a.Status == "done" {
			done++
		}
	}
	return fmt.Sprintf("%d/%d agents", done, len(agents))
}

// resultSuffix is the text after " → " on a finished row. A subagent
// dispatch counts its agents; otherwise the tool's summary is shown, and a
// tool that set none shows its first output line.
func (v *View) resultSuffix(c *Call) string {
	if c.Tool == "dispatch" && len(c.Agents) > 0 {
		done, cancelled := 0, 0
		for _, a := range c.Agents {
			switch a.Status {
			case "done":
				done++
			case "cancelled":
				cancelled++
			}
		}
		out := fmt.Sprintf("%d/%d agents", done, len(c.Agents))
		if cancelled > 0 {
			out += fmt.Sprintf(" · %d cancelled", cancelled)
		}
		if c.Result != nil {
			if note := attemptNote(c.Result.Output); note != "" {
				out += " · " + note
			}
		}
		return out
	}
	if c.Result == nil {
		return ""
	}
	// The tool's own summary wins. Without one, a tool with a known shape
	// gets its count (spec 01, formatResult); the first output line is the
	// last fallback.
	if c.Result.Summary != "" {
		return clip(c.Result.Summary, 60)
	}
	if s, ok := countSuffix(c); ok {
		return s
	}
	return clip(firstLine(c.Result.Output), 40)
}

// countSuffix is the count a finished row shows for tools whose result has a
// fixed shape: `N lines` for a read, `N hit(s) in M file(s)` or `0 hits` for a
// grep, `N file(s)` for a glob and `N result(s)` for soul_find. A failed read
// or web search shows its first line cut short. It reports false for other
// tools and for failures it does not shape.
func countSuffix(c *Call) (string, bool) {
	out := c.Result.Output
	if c.Result.IsError {
		switch c.Tool {
		case "read":
			return clip(firstLine(out), 27), true
		case "web_search":
			return clip(firstLine(out), 50), true
		}
		return "", false
	}
	lines := outputLines(out)
	switch c.Tool {
	case "read":
		return fmt.Sprintf("%d %s", len(lines), plural(len(lines), "line", "lines")), true
	case "grep":
		if len(lines) == 0 {
			return "0 hits", true
		}
		files := map[string]bool{}
		for _, l := range lines {
			if i := strings.Index(l, ":"); i > 0 {
				files[l[:i]] = true
			}
		}
		return fmt.Sprintf("%d %s in %d %s", len(lines), plural(len(lines), "hit", "hits"),
			len(files), plural(len(files), "file", "files")), true
	case "glob":
		return fmt.Sprintf("%d %s", len(lines), plural(len(lines), "file", "files")), true
	case "soul_find":
		return fmt.Sprintf("%d %s", len(lines), plural(len(lines), "result", "results")), true
	}
	return "", false
}

// outputLines is the non-empty lines of a tool's output.
func outputLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
