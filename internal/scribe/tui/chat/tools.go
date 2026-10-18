package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// argFn renders a tool's key argument for its row.
type argFn func(v *View, m map[string]any) string

// toolDef is the display rule for one tool: its labels, glyph, tone and args.
type toolDef struct {
	run, done string
	icon      string
	tone      string
	args      argFn
}

func argPath(v *View, m map[string]any) string { return v.relPath(argStr(m, "path")) }

func argText(limit int, keys ...string) argFn {
	return func(_ *View, m map[string]any) string {
		return clip(strings.ReplaceAll(argStr(m, keys...), "\n", " "), limit)
	}
}

func actionSymbol(v *View, m map[string]any) string {
	return clip(strings.TrimSpace(argStr(m, "action")+" "+argStr(m, "symbol", "file")), 50)
}

func codeArgs(_ *View, m map[string]any) string {
	code := argStr(m, "code")
	if n := strings.Count(code, "await"); n > 0 {
		return fmt.Sprintf("%d tool calls", n)
	}
	return clip(firstLine(code), 50)
}

// toolDefs gives every tool its labels and glyph. Icons come from the icons
// package, so the ASCII set stays one cell per tool.
var toolDefs = map[string]toolDef{
	"read":             {"Reading", "Read", "file", "info", readArgs},
	"read_file":        {"Reading", "Read", "file", "info", readArgs},
	"file_outline":     {"Outlining", "Outlined", "file", "info", argPath},
	"edit_file":        {"Editing", "Edited", "pencil", "amber", argPath},
	"multi_edit":       {"Editing", "Edited", "pencil", "amber", argPath},
	"write_file":       {"Writing", "Wrote", "write", "amber", argPath},
	"undo_edit":        {"Undoing", "Undid", "pencil", "amber", argPath},
	"list_dir":         {"Listing", "Listed", "changes", "info", argPath},
	"shell":            {"Running", "Ran", "terminal", "error", argText(60, "command")},
	"shell_output":     {"Reading output", "Read output", "terminal", "info", argText(50, "id", "job")},
	"shell_kill":       {"Stopping", "Stopped", "shell_kill", "error", argText(50, "id", "job")},
	"grep":             {"Searching", "", "search", "warning", argText(55, "pattern")},
	"glob":             {"Globbing", "", "changes", "info", argText(55, "pattern")},
	"dispatch":         {"Dispatching", "Dispatched", "dispatch", "brand", dispatchArgs},
	"web_search":       {"Searching web", "Searched web", "globe", "info", argText(50, "query")},
	"fetch_page":       {"Fetching page", "Fetched page", "file", "info", argText(50, "url")},
	"memory":           {"Memory", "Memory", "memory", "amber", memoryArgs},
	"skills":           {"Skills", "Skills", "skills", "brand", skillsArgs},
	"editor":           {"Editor", "Editor", "pencil", "info", argPath},
	"git":              {"Git", "Git", "git", "success", gitArgs},
	"project":          {"Project", "Project", "terminal", "error", projectArgs},
	"navigate":         {"Navigating", "Navigated", "arrow_right", "brandAlt", actionSymbol},
	"analyze":          {"Analyzing", "Analyzed", "search", "brandAlt", actionSymbol},
	"code_map":         {"Mapping code", "Mapped code", "map", "info", argText(50, "path", "query")},
	"find_symbol":      {"Finding symbol", "Found symbol", "search", "info", argText(50, "name", "query", "symbol")},
	"symbol_refs":      {"Finding references", "Found references", "references", "info", actionSymbol},
	"code_impact":      {"Impact analysis", "Impact analysis", "impact", "info", actionSymbol},
	"diagnostics":      {"Checking diagnostics", "Checked diagnostics", "diagnostics", "warning", argPath},
	"rename_symbol":    {"Renaming symbol", "Renamed symbol", "rename", "amber", renameSymbolArgs},
	"move_symbol":      {"Moving symbol", "Moved symbol", "arrow_right", "amber", moveSymbolArgs},
	"rename_file":      {"Moving file", "Moved file", "arrow_right", "amber", renameFileArgs},
	"refactor":         {"Refactoring", "Refactored", "wrench", "amber", actionSymbol},
	"format_file":      {"Formatting", "Formatted", "format", "amber", argPath},
	"discover_pattern": {"Discovering", "Discovered", "search", "brandAlt", argText(50, "query")},
	"editor_panel":     {"Opening editor", "Opened editor", "pencil", "info", nil},
	"plan":             {"Planning", "Planned", "plan", "info", argText(50, "title")},
	"write_plan":       {"Planning", "Planned", "plan", "info", argText(50, "title")},
	"update_plan_step": {"Updating plan", "Updated plan", "check", "info", argText(50, "stepId", "step_id")},
	"ask_user":         {"Asking", "Asked", "question", "amber", argText(50, "question")},
	"task_list":        {"Tasks", "Tasks", "plan", "info", nil},
	"code_execution":   {"Running code", "Ran code", "smithy", "amber", codeArgs},
	"ast_edit":         {"Morphing", "Morphed", "morph", "brandAlt", argPath},
	"_nudge":           {"Output nudge", "Output nudge", "lightning", "warning", nil},
}

func defFor(tool string) toolDef {
	if d, ok := toolDefs[tool]; ok {
		return d
	}
	if strings.HasPrefix(tool, "mcp__") {
		// mcp__<server>__<tool>: show "<server> <tool>".
		parts := strings.SplitN(tool, "__", 3)
		label := strings.TrimPrefix(tool, "mcp__")
		if len(parts) == 3 {
			label = parts[1] + " " + parts[2]
		}
		return toolDef{run: label, done: label, icon: "mcp_tool", tone: "textSecondary", args: func(_ *View, m map[string]any) string {
			return clip(genericArg(m), 50)
		}}
	}
	return toolDef{run: tool, done: tool, icon: "wrench", tone: "textSecondary", args: func(_ *View, m map[string]any) string {
		return clip(genericArg(m), 50)
	}}
}

// hiddenTool reports tools that never get a row.
func hiddenTool(name string, verbose bool) bool {
	switch name {
	case "update_plan_step", "final_response":
		return true
	case "task_list":
		return !verbose
	}
	return false
}

// batchKind groups consecutive calls of one kind into a single row.
func batchKind(tool string) string {
	switch tool {
	case "edit_file", "multi_edit":
		return "edit"
	case "read":
		return "read"
	case "grep", "glob", "navigate", "find_symbol", "symbol_refs", "code_impact", "code_map",
		"soul_grep", "soul_find", "soul_analyze", "soul_impact":
		return "search"
	case "plan", "write_plan", "ask_user", "editor_panel":
		return "meta"
	}
	return ""
}

func isEditTool(tool string) bool {
	switch tool {
	case "edit_file", "multi_edit", "write_file", "undo_edit", "ast_edit",
		"rename_symbol", "move_symbol", "rename_file", "refactor":
		return true
	}
	return false
}

func pathOf(c *Call) string { return argStr(argsOf(c.Args), "path", "file_path", "file") }

// batchTarget is the file a batch row names. A call whose args name no file
// (a failed edit, say) says so, with its error, rather than a blank label.
func (v *View) batchTarget(cs []*Call, p string) string {
	if p != "" {
		return v.relPath(p)
	}
	for _, c := range cs {
		if c.Result != nil && c.Result.IsError {
			return "file not named: " + clip(firstLine(c.Result.Output), 40)
		}
	}
	return "file not named"
}

// rowT is one line group in a rail: its lines and the call it stands for.
type rowT struct {
	lines []string
	call  *Call
	calls []*Call
	rail  bool // the rail's fold row; a click toggles the fold
}

// visibleCalls drops hidden tools and failed edits that a later edit to the
// same path superseded.
func (v *View) visibleCalls(calls []*Call) []*Call {
	lastEdit := map[string]int{}
	for i, c := range calls {
		if c.Tool == "edit_file" || c.Tool == "multi_edit" {
			lastEdit[pathOf(c)] = i
		}
	}
	var out []*Call
	for i, c := range calls {
		if hiddenTool(c.Tool, v.opt.Verbose) {
			continue
		}
		if (c.Tool == "edit_file" || c.Tool == "multi_edit") && c.Status == "error" && lastEdit[pathOf(c)] > i {
			continue
		}
		out = append(out, c)
	}
	return out
}

// groupRows turns calls into rows. Auto layout groups consecutive reads,
// edits, searches and meta calls; verbose gives one row per call.
func (v *View) groupRows(calls []*Call, now time.Time, cw int) []rowT {
	calls = v.visibleCalls(calls)
	var rows []rowT
	for i := 0; i < len(calls); {
		c := calls[i]
		k := batchKind(c.Tool)
		if v.opt.Verbose || k == "" {
			rows = append(rows, v.singleRow(c, now, cw))
			i++
			continue
		}
		j := i + 1
		for j < len(calls) && batchKind(calls[j].Tool) == k {
			j++
		}
		if j-i >= 2 || k == "meta" {
			rows = append(rows, v.batchRow(k, calls[i:j], now, cw))
		} else {
			rows = append(rows, v.singleRow(c, now, cw))
		}
		i = j
	}
	return rows
}

func (v *View) expandedFor(c *Call) bool { return v.expanded || c.Toggled }

func (v *View) singleRow(c *Call, now time.Time, cw int) rowT {
	r := rowT{call: c, calls: []*Call{c}}
	r.lines = append(r.lines, v.callHeader(c, now))
	r.lines = append(r.lines, v.agentLines(c, now)...)
	r.lines = append(r.lines, v.readImageLines(c, cw)...)
	if v.expandedFor(c) {
		r.lines = append(r.lines, v.callDetails(c, cw)...)
	} else if line := v.compactEdit(c); line != "" {
		r.lines = append(r.lines, line)
	}
	return r
}

// callHeader is the one-line summary of a call. Its parts, in order: status
// glyph, tool icon, category tag, backend tag, location badge, label, args
// and the suffix (duration while running, else the result or error).
func (v *View) callHeader(c *Call, now time.Time) string {
	th := v.th
	d := defFor(c.Tool)
	m := argsOf(c.Args)
	running := c.running()
	if rt, code, ok := inlineCode(argStr(m, "command")); c.Tool == "shell" && ok {
		d = toolDef{run: rt, done: rt, icon: "smithy", tone: "amber",
			args: func(*View, map[string]any) string { return codeRowArgs(rt, code) }}
	}

	var status string
	switch {
	case running:
		status = fg(th.Brand, render.Frame(render.Braille, frameAt(now)))
	case c.Status == "error":
		status = fg(th.Error, icons.Icon("fail"))
	case c.Status == "denied":
		status = fg(th.TextSecondary, icons.Icon("skip"))
	default:
		status = fg(th.Success, icons.Icon("success"))
	}

	iconTone := d.tone
	if !running && c.Status != "error" && c.Status != "denied" {
		iconTone = "textMuted"
	}
	iconStr := fg(v.tone(iconTone), icons.Icon(d.icon))

	// A finished edit replaces its label and args with its own first line.
	if isEditResultRow(c) {
		return joinNonEmpty(" ", status, iconStr, fg(th.TextMuted, editResultText(c)))
	}

	label := ""
	switch {
	case running:
		label = bold(th.Brand, d.run)
	case c.Status == "error" && d.run != "":
		// A failed call did not do the thing: "Renamed symbol" over an
		// error read as a success.
		label = fg(th.TextMuted, "Failed: "+strings.ToLower(d.run[:1])+d.run[1:])
	case d.done != "":
		label = fg(th.TextMuted, d.done)
	}

	args := ""
	if d.args != nil {
		args = d.args(v, m)
	}
	if running {
		args = fg(th.TextSecondary, args)
	} else {
		args = fg(th.TextMuted, args)
	}

	tags := joinNonEmpty(" ", v.categoryTag(c.Tool, running), v.backendTag(c.Result),
		v.badgeTag(locationBadge(v.opt.Workdir, pathArgs(m)...)))
	head := joinNonEmpty(" ", status, iconStr, tags, label, args)

	suffix := ""
	switch {
	case running:
		if el := now.Sub(c.Start); el >= time.Second {
			suffix = fg(th.TextDim, " "+render.HumanDuration(el.Truncate(time.Second)))
			if c.Tool == "dispatch" && len(c.Agents) > 0 {
				suffix += fg(th.TextDim, " · "+agentProgress(c.Agents))
			}
		}
	case c.Status == "error":
		msg := "failed"
		if c.Result != nil && firstLine(c.Result.Output) != "" {
			msg = firstLine(c.Result.Output)
		}
		suffix = fg(th.Error, " → "+clip(msg, 60))
	case c.Status == "denied" && c.Tool == "dispatch" && rejectedDispatch(c):
		suffix = fg(th.Warning, " → rejected — "+clip(rejectReason(c.Result.Output), 60))
	case c.Status == "denied":
		suffix = fg(th.TextSecondary, " → denied")
	default:
		if s := v.resultSuffix(c); s != "" {
			suffix = fg(th.TextMuted, " → "+s)
		}
	}
	return head + suffix
}

// callDetails is the expanded body of a call, indented under its row.
func (v *View) callDetails(c *Call, cw int) []string {
	th := v.th
	m := argsOf(c.Args)
	switch c.Tool {
	case "edit_file", "multi_edit", "write_file", "ast_edit":
		return v.editDetails(c, m, cw)
	}
	var lines []string
	var out []string
	switch c.Tool {
	case "shell":
		out = append(out, "  "+fg(th.TextSecondary, "$ "+clip(argStr(m, "command"), cw-4)))
		// Live progress keeps its tail; a finished call keeps its full output.
		src := c.Progress
		if !c.running() && c.Result != nil {
			src = splitLines(c.Result.Output)
			lines = src
		} else {
			lines = tailOf(src, 20)
		}
	case "read":
		if c.Result == nil {
			return nil
		}
		lines = v.readDetail(c, readPath(m))
	default:
		if c.Result == nil {
			return nil
		}
		col := th.TextSecondary
		if c.Result.IsError {
			col = th.Error
		}
		for _, l := range headOf(strings.Split(strings.TrimRight(c.Result.Output, "\n"), "\n"), 20) {
			lines = append(lines, fg(col, l))
		}
	}
	if c.Result != nil && !c.running() && len(lines) > 0 {
		out = append(out, "", "  "+fg(th.TextMuted, "╰─ result ── click to collapse"))
	}
	for _, l := range lines {
		out = append(out, "  "+l)
	}
	return out
}

func headOf(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func tailOf(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// editDetails shows the diff in a box, or the error for a failed edit.
func (v *View) editDetails(c *Call, m map[string]any, cw int) []string {
	th := v.th
	if c.Result == nil {
		return nil
	}
	if c.Result.IsError {
		var out []string
		for _, l := range headOf(strings.Split(strings.TrimRight(c.Result.Output, "\n"), "\n"), 6) {
			out = append(out, "  "+fg(th.Error, l))
		}
		return out
	}
	diff := joinedDiff(c, argStr(m, "path"))
	if diff == "" {
		return nil
	}
	path := v.relPath(argStr(m, "path"))
	di := parseDiff(diff)
	total := di.added + di.removed
	if total > largeDiff && !c.Toggled {
		return []string{"  " + fg(th.TextMuted, fmt.Sprintf("%d lines changed (click to show)", total))}
	}
	if v.opt.DiffStyle == DiffSideBySide {
		return v.sideBySideBody(diff, cw)
	}
	verb := "Edit"
	if di.created {
		verb = "New"
	}
	title := fg(th.Brand, icons.Icon("pencil")) + " " + bold(th.Brand, verb) + fg(th.Border, " ─ ") + fg(th.TextPrimary, path)
	if di.added > 0 {
		title += " " + fg(th.Success, fmt.Sprintf("+%d", di.added))
	}
	if di.removed > 0 {
		title += " " + fg(th.Error, fmt.Sprintf("-%d", di.removed))
	}
	body := v.renderDiff(di, path, cw-4, false)
	var out []string
	for _, l := range boxed(title, body, cw, 1, th.Border, nil) {
		out = append(out, "  "+l)
	}
	if impact, _ := c.Result.Meta["impact"].(string); impact != "" {
		out = append(out, "  "+fg(th.Amber, icons.Icon("impact"))+" "+fg(th.TextSecondary, "[impact: "+clip(impact, cw-16)+"]"))
	}
	return out
}

// joinedDiff is the diff an edit call shows. A multi_edit batch carries the
// whole file before and after in Meta, so its changes read as one diff, not
// one per edit. Other edits show the diff their result carries.
func joinedDiff(c *Call, path string) string {
	if c.Tool == "multi_edit" && c.Result != nil {
		before, okB := c.Result.Meta["before"].(string)
		after, okA := c.Result.Meta["after"].(string)
		if okB && okA {
			d, _, _ := fs.UnifiedDiff(path, before, after)
			return d
		}
	}
	return c.Result.Diff
}

// dispatchWindow is how many subagent rows a dispatch shows at once. Older
// rows fold into a "+N earlier" line.
const dispatchWindow = 6

// agentLines is the subagent tree under a dispatch row.
func (v *View) agentLines(c *Call, now time.Time) []string {
	th := v.th
	var out []string
	// A finished dispatch collapses to its header; its children show when expanded.
	if !c.running() && !v.expandedFor(c) {
		return nil
	}
	agents := c.Agents
	if len(agents) > dispatchWindow {
		hidden := len(agents) - dispatchWindow
		out = append(out, fg(th.TextFaint, fmt.Sprintf("  ┌ +%d earlier", hidden)))
		agents = agents[hidden:]
	}
	n := len(agents)
	for i, a := range agents {
		conn := "├ "
		switch {
		case n == 1:
			conn = "└ "
		case i == 0 && len(out) == 0:
			conn = "┌ "
		case i == n-1:
			conn = "└ "
		}
		var glyph string
		switch a.Status {
		case "running":
			glyph = fg(th.Brand, render.Frame(render.Dispatch, frameAt(now)))
		case "done":
			glyph = fg(th.Success, icons.Icon("success"))
		case "error":
			glyph = fg(th.Error, icons.Icon("fail"))
		case "cancelled":
			// Cancelled is not pending: a distinct glyph, and muted text.
			glyph = fg(th.TextMuted, "⊘")
		default:
			glyph = fg(th.TextMuted, "○")
		}
		meta := fmt.Sprintf("%s %s", a.ID, a.Role)
		if a.ID == "" {
			meta = a.Role
		}
		if a.Model != "" {
			meta += " · " + shortModelId(a.Model)
		}
		if a.Step > 0 {
			meta += fmt.Sprintf(" · step %d", a.Step)
		}
		if t := a.Tokens.InputTokens + a.Tokens.OutputTokens; t > 0 {
			meta += " · " + render.HumanTokens(t)
		}
		metaCol := th.TextPrimary
		if a.Status == "cancelled" {
			metaCol = th.TextMuted
		}
		line := fg(th.TextFaint, "  "+conn) + glyph + " " + fg(metaCol, meta)
		// Prompt-cache reads this agent got back, as spec 01 2.9 shows them.
		if cr := a.Tokens.CacheReadTokens; cr > 0 {
			line += " " + fg(th.Amber, "[cache "+render.HumanTokens(cr)+"]")
		}
		// The dispatch cache's state for this subagent's reads (spec 01 §2.9).
		// Without the dispatch cache there is no state, so the row is unchanged.
		stated := false
		if g, label, col := dispatchCacheState(a, th); g != "" {
			line += " " + fg(col, g+" "+label)
			stated = true
		}
		// With a cache state shown, the task and error give way to it so the
		// row stays in the width. Without one the widths are the usual 40 and 50.
		taskMax := 40
		if stated {
			taskMax = min(taskMax, v.rowRoom(line)-2)
		}
		line += "  " + fg(th.TextMuted, clip(a.Task, taskMax))
		if a.Status == "error" && a.Error != "" {
			errMax := 50
			if stated {
				errMax = min(errMax, v.rowRoom(line)-3)
			}
			line += fg(th.Error, " → "+clip(a.Error, errMax))
		}
		out = append(out, line)
		if a.Status == "running" && a.Last != "" {
			out = append(out, fg(th.TextFaint, "  │ ")+fg(th.TextDim, clip(a.Last, 60)))
		}
	}
	return out
}

// dispatchCacheState is the glyph, label and colour of a subagent's dispatch
// cache state (spec 01 §2.9). The glyphs come from the icon table, so they
// fall back to plain symbols when nerd-font icons are off. An empty glyph means
// there is no state, which is the case whenever the dispatch cache is off.
func dispatchCacheState(a *agent.AgentInfo, th theme.Theme) (glyph, label string, col lipgloss.TerminalColor) {
	switch a.Cache {
	case "hit":
		label = "from cache"
		if a.CacheFrom != "" {
			label = "from " + a.CacheFrom
		}
		return "", label, th.TextMuted
	case "wait":
		label = "waiting"
		if a.CacheFrom != "" {
			label = "waiting on " + a.CacheFrom
		}
		return "", label, th.Warning
	case "store":
		return "", "cached", th.TextMuted
	case "invalidate":
		return "", "updated cache", th.TextMuted
	}
	return "", "", nil
}

// rowRoom is how many columns a subagent row can still take after line. The
// width is the view's, and 80 when the view has none yet.
func (v *View) rowRoom(line string) int {
	w := v.w
	if w <= 0 {
		w = 80
	}
	return w - ansi.StringWidth(line)
}

// groupStatus is the glyph for a batch: running, all failed, partial, or ok.
func (v *View) groupStatus(calls []*Call, now time.Time) string {
	th := v.th
	running, errs := 0, 0
	for _, c := range calls {
		switch {
		case c.running():
			running++
		case c.Status == "error" || c.Status == "denied":
			errs++
		}
	}
	switch {
	case running > 0:
		return fg(th.Brand, render.Frame(render.Braille, frameAt(now)))
	case errs == len(calls):
		return fg(th.Error, icons.Icon("fail"))
	case errs > 0:
		return fg(th.Warning, "⚠")
	}
	return fg(th.Success, icons.Icon("success"))
}

func anyRunning(calls []*Call) bool {
	for _, c := range calls {
		if c.running() {
			return true
		}
	}
	return false
}

func allDone(calls []*Call) bool {
	for _, c := range calls {
		if c.running() {
			return false
		}
	}
	return true
}

// batchRow draws a group of 2+ calls of one kind.
func (v *View) batchRow(kind string, calls []*Call, now time.Time, cw int) rowT {
	th := v.th
	r := rowT{calls: calls}
	status := v.groupStatus(calls, now)
	d := defFor(calls[0].Tool)
	running := anyRunning(calls)
	ok, failed := 0, 0
	for _, c := range calls {
		if c.Status == "error" || c.Status == "denied" {
			failed++
		} else if !c.running() {
			ok++
		}
	}
	counts := ""
	if failed > 0 {
		counts = fmt.Sprintf(" (%d ok, %d failed)", ok, failed)
	}
	verb := d.done
	if running {
		verb = d.run
	}
	label := func(s string) string {
		if running {
			return bold(th.Brand, s)
		}
		return fg(th.TextMuted, s)
	}
	iconStr := fg(v.tone(d.tone), icons.Icon(d.icon))

	switch kind {
	case "search":
		r.lines = []string{joinNonEmpty(" ", status, iconStr, label(fmt.Sprintf("%d searches", len(calls))))}
		return r
	case "meta":
		var names []string
		seen := map[string]bool{}
		for _, c := range calls {
			if !seen[c.Tool] {
				seen[c.Tool] = true
				names = append(names, c.Tool)
			}
		}
		noun := "tool calls"
		if len(calls) == 1 {
			noun = "tool call"
		}
		r.lines = []string{joinNonEmpty(" ", status, iconStr,
			label(fmt.Sprintf("%d %s (%s)", len(calls), noun, strings.Join(names, ", "))))}
		for _, c := range calls {
			if (c.Tool == "plan" || c.Tool == "write_plan") && c.running() {
				r.lines = append(r.lines, "  "+v.awaitingPlanReview())
				break
			}
		}
		return r
	}

	// edit or read: files grouped by path, in first-seen order.
	var paths []string
	byPath := map[string][]*Call{}
	for _, c := range calls {
		p := pathOf(c)
		if _, ok := byPath[p]; !ok {
			paths = append(paths, p)
		}
		byPath[p] = append(byPath[p], c)
	}
	if len(paths) < 2 {
		name := v.batchTarget(calls, paths[0])
		r.lines = []string{joinNonEmpty(" ", status, iconStr, label(verb), fg(th.TextPrimary, name)+fg(th.TextMuted, counts))}
		r.lines = append(r.lines, v.batchDetails(calls, cw)...)
		return r
	}
	r.lines = []string{joinNonEmpty(" ", status, iconStr, label(fmt.Sprintf("%d files", len(paths))))}
	r.lines[0] += fg(th.TextMuted, counts)
	for i, p := range paths {
		conn := "├ "
		if i == len(paths)-1 {
			conn = "└ "
		}
		cs := byPath[p]
		st := v.groupStatus(cs, now)
		summary := ""
		for _, c := range cs {
			if c.Result != nil && c.Result.Summary != "" {
				summary = c.Result.Summary
			}
		}
		shown := v.batchTarget(cs, p)
		if kind == "read" && p != "" {
			shown = v.outsideTreePath(p)
		}
		line := fg(th.TextFaint, "  "+conn) + st + " " + fg(th.TextPrimary, shown)
		if summary != "" {
			line += " " + fg(th.TextMuted, clip(summary, 40))
		}
		r.lines = append(r.lines, line)
	}
	r.lines = append(r.lines, v.batchDetails(calls, cw)...)
	return r
}

// batchDetails is the expanded body of each call in a file batch.
func (v *View) batchDetails(calls []*Call, cw int) []string {
	var out []string
	for _, c := range calls {
		if v.expandedFor(c) {
			out = append(out, v.callDetails(c, cw)...)
		} else if line := v.compactEdit(c); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// assembleRows adds tree connectors: ┌ ├ └ when there are several rows, and
// a continuation bar for the lines of a row that is not the last.
func (v *View) assembleRows(rows []rowT) builder {
	var b builder
	n := len(rows)
	for i, r := range rows {
		conn, cont := "", "  "
		if n > 1 {
			switch {
			case i == 0:
				conn = "┌ "
			case i == n-1:
				conn = "└ "
			default:
				conn = "├ "
			}
			if i < n-1 {
				cont = "│ "
			}
		}
		for j, l := range r.lines {
			prefix := cont
			if j == 0 {
				prefix = conn
				if r.call != nil {
					b.mark(r.call, nil)
				} else if r.rail {
					b.markRail()
				}
			}
			if prefix != "" {
				prefix = fg(v.th.TextFaint, prefix)
			}
			b.add(prefix + l)
		}
	}
	return b
}

// renderRail is the final-response rail: a status header, then at most five
// rows. Older rows fold into one "+N completed" row. Once the turn is done
// and the view is expanded, intermediate text joins the rows in order.
func (v *View) renderRail(rec *Record, now time.Time, cw int, thinking bool) builder {
	var b builder
	calls := allCalls(rec)
	rows := v.railRows(rec, now, cw, (rec.Done && v.expanded) || rec.RailOpen)
	if len(rows) > 5 && rec.RailOpen {
		// Unfolded: every row shows, and one row folds them again.
		txt := fg(v.th.TextMuted, fmt.Sprintf("▴ collapse +%d above", len(rows)-4))
		rows = append([]rowT{{lines: []string{txt}, rail: true}}, rows...)
	} else if len(rows) > 5 {
		hidden := rows[:len(rows)-4]
		edits := 0
		for _, r := range hidden {
			for _, c := range r.calls {
				if isEditTool(c.Tool) {
					edits++
				}
			}
		}
		txt := fg(v.th.Success, icons.Icon("success")) + fg(v.th.TextMuted, fmt.Sprintf(" +%d completed", len(hidden)))
		if edits > 0 {
			txt += fg(v.th.TextDim, fmt.Sprintf(" [%d edits]", edits))
		}
		txt += fg(v.th.TextSubtle, " (click to expand)")
		rows = append([]rowT{{lines: []string{txt}, rail: true}}, rows[len(rows)-4:]...)
	}
	if thinking {
		frame := render.Frame(render.LoadingDots, frameAt(now)/4)
		rows = append(rows, rowT{lines: []string{joinNonEmpty(" ",
			fg(v.th.Brand, render.Frame(render.Braille, frameAt(now))),
			fg(v.th.TextMuted, "Thinking"+frame))}})
	}
	b.add(v.railHeader(calls, rec, now))
	ab := v.assembleRows(rows)
	off := len(b.lines)
	b.lines = append(b.lines, ab.lines...)
	for _, h := range ab.hits {
		h.line += off
		b.hits = append(b.hits, h)
	}
	return b
}

// railRows turns the turn's calls into rail rows. Without texts the calls
// are grouped as one run; with texts each segment is grouped on its own so
// the intermediate text keeps its place between the tool rows.
func (v *View) railRows(rec *Record, now time.Time, cw int, texts bool) []rowT {
	if !texts {
		return v.groupRows(allCalls(rec), now, cw)
	}
	lastTools := -1
	for i, s := range rec.Segments {
		if s.Kind == "tools" {
			lastTools = i
		}
	}
	var rows []rowT
	for i, s := range rec.Segments {
		switch {
		case s.Kind == "tools":
			rows = append(rows, v.groupRows(s.Calls, now, cw)...)
		case s.Kind == "text" && i < lastTools && trimmed(s.Text) != "":
			rows = append(rows, v.intermediateRow(s.Text, cw))
		}
	}
	return rows
}

// intermediateRow is a line of narration between tool calls, dim, at most
// eight lines.
func (v *View) intermediateRow(text string, cw int) rowT {
	lines := wrapText(trimmed(text), maxInt(cw-2, 10))
	if len(lines) > 8 {
		lines = append(lines[:7], "…")
	}
	r := rowT{}
	for _, l := range lines {
		r.lines = append(r.lines, fg(v.th.TextDim, l))
	}
	return r
}

// phaseOf picks the phase from the current tool: the last running call when
// one runs, otherwise the most recent call. Dispatch and edit tools have their
// own phases; everything else is exploration.
func phaseOf(calls []*Call) string {
	if len(calls) == 0 {
		return "explore"
	}
	cur := calls[len(calls)-1]
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].running() {
			cur = calls[i]
			break
		}
	}
	switch {
	case cur.running() && (cur.Tool == "plan" || cur.Tool == "ask_user"):
		// The turn is blocked on the user, not reading anything.
		return "review"
	case cur.Tool == "dispatch":
		return "dispatch"
	case isEditTool(cur.Tool):
		return "edit"
	}
	return "explore"
}

// rejectedDispatch reports whether a dispatch was turned away (its result says
// so), which is shown as rejected rather than denied.
func rejectedDispatch(c *Call) bool {
	return c.Result != nil && strings.Contains(strings.ToLower(c.Result.Output), "rejected")
}

// rejectReason is the reason in a rejected dispatch's first line: the text
// after "rejected", without the separators around it.
func rejectReason(out string) string {
	line := firstLine(out)
	if i := strings.Index(strings.ToLower(line), "rejected"); i >= 0 {
		line = line[i+len("rejected"):]
	}
	return strings.Trim(strings.TrimSpace(line), ":—-–. ")
}
