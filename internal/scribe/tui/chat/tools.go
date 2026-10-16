package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// argFn renders a tool's key argument for its row.
type argFn func(v *View, m map[string]any) string

// toolDef is the display rule for one tool: labels, icon, category and args.
type toolDef struct {
	run, done string
	icon      string
	tone      string
	cat       string
	catTone   string
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

func readArgs(v *View, m map[string]any) string {
	if p, ok := m["paths"].([]any); ok && len(p) > 1 {
		return fmt.Sprintf("%d files", len(p))
	}
	p := v.relPath(argStr(m, "path"))
	off, lim := argInt(m, "offset"), argInt(m, "limit")
	switch {
	case off > 0 && lim > 0:
		return fmt.Sprintf("%s (L%d-%d)", p, off, off+lim-1)
	case off > 0:
		return fmt.Sprintf("%s (L%d-)", p, off)
	}
	return p
}

func dispatchArgs(_ *View, m map[string]any) string {
	tasks, _ := m["tasks"].([]any)
	if len(tasks) == 0 {
		return ""
	}
	roles := []string{}
	seen := map[string]bool{}
	first := ""
	for i, t := range tasks {
		tm, _ := t.(map[string]any)
		role := argStr(tm, "role")
		if role == "" {
			role = "explore"
		}
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
		if i == 0 {
			first = argStr(tm, "task")
		}
	}
	if len(tasks) == 1 {
		return clip("["+roles[0]+"] "+first, 45)
	}
	return clip("["+strings.Join(roles, "/")+"] "+first, 55)
}

func memoryArgs(_ *View, m map[string]any) string {
	action := argStr(m, "action")
	detail := argStr(m, "query", "title", "id", "summary")
	return clip(joinNonEmpty(": ", action, detail), 60)
}

func codeArgs(_ *View, m map[string]any) string {
	code := argStr(m, "code")
	if n := strings.Count(code, "await"); n > 0 {
		return fmt.Sprintf("%d tools in the furnace", n)
	}
	return clip(firstLine(code), 50)
}

var toolDefs = map[string]toolDef{
	"read":             {"Reading", "Read", "file", "info", "file", "info", readArgs},
	"edit_file":        {"Editing", "Edited", "pencil", "amber", "file", "info", argPath},
	"multi_edit":       {"Editing", "Edited", "pencil", "amber", "file", "info", argPath},
	"write_file":       {"Writing", "Wrote", "pencil", "amber", "file", "info", argPath},
	"undo_edit":        {"Undoing", "Undid", "pencil", "amber", "file", "info", argPath},
	"list_dir":         {"Listing", "Listed", "changes", "info", "file", "info", argPath},
	"shell":            {"Running", "Ran", "terminal", "error", "shell", "error", argText(60, "command")},
	"grep":             {"Searching", "", "search", "warning", "file", "info", argText(55, "pattern")},
	"glob":             {"Globbing", "", "changes", "info", "file", "info", argText(55, "pattern")},
	"dispatch":         {"Dispatching", "Dispatched", "explore", "brand", "agent", "brand", dispatchArgs},
	"web_search":       {"Searching web", "Searched web", "globe", "info", "web", "info", argText(50, "query")},
	"fetch_page":       {"Fetching page", "Fetched page", "file", "info", "web", "info", argText(50, "url")},
	"memory":           {"Memory", "Memory", "bookmark", "amber", "memory", "amber", memoryArgs},
	"skills":           {"Skills", "Skills", "skills", "brand", "agent", "brand", argText(50, "query", "name")},
	"editor":           {"Editor", "Editor", "pencil", "info", "editor", "info", argPath},
	"git":              {"Git", "Git", "git", "success", "git", "success", argText(50, "message", "action")},
	"navigate":         {"Navigating", "Navigated", "arrow_right", "brandAlt", "code", "warning", actionSymbol},
	"analyze":          {"Analyzing", "Analyzed", "search", "brandAlt", "code", "warning", actionSymbol},
	"rename_symbol":    {"Renaming symbol", "Renamed symbol", "rename", "amber", "code", "warning", argText(50, "new_name", "to")},
	"move_symbol":      {"Moving symbol", "Moved symbol", "arrow_right", "amber", "code", "warning", argText(50, "to")},
	"rename_file":      {"Moving file", "Moved file", "arrow_right", "amber", "code", "warning", argText(60, "to")},
	"refactor":         {"Refactoring", "Refactored", "wrench", "amber", "code", "warning", actionSymbol},
	"project":          {"Project", "Project", "terminal", "error", "shell", "error", argText(50, "action", "command")},
	"discover_pattern": {"Discovering", "Discovered", "search", "brandAlt", "code", "warning", argText(50, "query")},
	"editor_panel":     {"Opening editor", "Opened editor", "pencil", "info", "editor", "info", nil},
	"plan":             {"Planning", "Planned", "plan", "info", "ui", "info", argText(50, "title")},
	"write_plan":       {"Planning", "Planned", "plan", "info", "ui", "info", argText(50, "title")},
	"update_plan_step": {"Updating plan", "Updated plan", "check", "info", "ui", "info", argText(50, "stepId", "step_id")},
	"ask_user":         {"Asking", "Asked", "question", "amber", "ui", "info", argText(50, "question")},
	"task_list":        {"Tasks", "Tasks", "plan", "info", "ui", "info", nil},
	"code_execution":   {"Forging", "Forged", "smithy", "amber", "smithy", "amber", codeArgs},
	"soul_vision":      {"Visualizing", "Visualized", "image", "textSecondary", "vision", "brand", nil},
	"soul_grep":        {"Searching", "", "search", "success", "soul-map", "success", argText(55, "pattern")},
	"soul_find":        {"Finding", "", "search", "success", "soul-map", "success", argText(50, "query")},
	"soul_analyze":     {"Analyzing", "Analyzed", "repomap", "success", "soul-map", "success", actionSymbol},
	"soul_impact":      {"Impact analysis", "Impact analysis", "repomap", "success", "soul-map", "success", actionSymbol},
	"ast_edit":         {"morphing", "morphed", "morph", "brandAlt", "morph", "brandAlt", argPath},
	"_nudge":           {"Output nudge", "Output nudge", "lightning", "warning", "", "", nil},
}

func defFor(tool string) toolDef {
	if d, ok := toolDefs[tool]; ok {
		return d
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
	case "grep", "glob", "soul_grep", "soul_find", "navigate", "soul_analyze", "soul_impact":
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

func pathOf(c *Call) string { return argStr(argsOf(c.Args), "path") }

// rowT is one line group in a rail: its lines and the call it stands for.
type rowT struct {
	lines []string
	call  *Call
	calls []*Call
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
	if v.expandedFor(c) {
		r.lines = append(r.lines, v.callDetails(c, cw)...)
	}
	return r
}

// callHeader is the one-line summary of a call.
func (v *View) callHeader(c *Call, now time.Time) string {
	th := v.th
	d := defFor(c.Tool)
	m := argsOf(c.Args)
	running := c.running()

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

	catStr := ""
	if d.cat != "" {
		if running {
			catStr = fg(v.tone(d.catTone), "["+d.cat+"]")
		} else {
			catStr = fg(th.TextDim, "["+d.cat+"]")
		}
	}

	label := ""
	if running {
		label = bold(th.Brand, d.run)
	} else if d.done != "" {
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

	suffix := ""
	switch {
	case running:
		if el := now.Sub(c.Start); el >= time.Second {
			suffix = fg(th.TextDim, " "+render.HumanDuration(el.Truncate(time.Second)))
		}
	case c.Status == "error":
		msg := "failed"
		if c.Result != nil && firstLine(c.Result.Output) != "" {
			msg = firstLine(c.Result.Output)
		}
		suffix = fg(th.Error, " → "+clip(msg, 60))
	case c.Status == "denied":
		suffix = fg(th.TextSecondary, " → denied")
	default:
		if c.Result != nil && c.Result.Summary != "" {
			suffix = fg(th.TextMuted, " → "+clip(c.Result.Summary, 60))
		}
		if c.Duration >= time.Second {
			suffix += fg(th.TextDim, " · "+render.HumanDuration(c.Duration))
		}
	}
	return joinNonEmpty(" ", status, iconStr, catStr, label, args) + suffix
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
		src := c.Progress
		if !c.running() && c.Result != nil {
			src = strings.Split(strings.TrimRight(c.Result.Output, "\n"), "\n")
		}
		lines = tailOf(src, 20)
	case "read":
		if c.Result == nil {
			return nil
		}
		src := headOf(strings.Split(c.Result.Output, "\n"), 30)
		lines = splitLines(render.Highlight(strings.Join(src, "\n"), render.LangFromPath(argStr(m, "path")), th))
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
	if c.Result.Diff == "" {
		return nil
	}
	path := v.relPath(argStr(m, "path"))
	di := parseDiff(c.Result.Diff)
	total := di.added + di.removed
	if total > largeDiff && !c.Toggled {
		return []string{"  " + fg(th.TextMuted, fmt.Sprintf("%d lines changed (click to show)", total))}
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
	return out
}

// agentLines is the subagent tree under a dispatch row.
func (v *View) agentLines(c *Call, now time.Time) []string {
	th := v.th
	var out []string
	n := len(c.Agents)
	for i, a := range c.Agents {
		conn := "├ "
		switch {
		case n == 1:
			conn = "└ "
		case i == 0:
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
		default:
			glyph = fg(th.TextMuted, "○")
		}
		meta := fmt.Sprintf("%s %s · %s", a.ID, a.Role, shortModelId(a.Model))
		if a.Step > 0 {
			meta += fmt.Sprintf(" · step %d", a.Step)
		}
		if t := a.Tokens.InputTokens + a.Tokens.OutputTokens; t > 0 {
			meta += " · " + render.HumanTokens(t)
		}
		line := fg(th.TextFaint, "  "+conn) + glyph + " " + fg(th.TextPrimary, meta) + "  " + fg(th.TextMuted, clip(a.Task, 40))
		if a.Status == "error" && a.Error != "" {
			line += fg(th.Error, " → "+clip(a.Error, 50))
		}
		out = append(out, line)
		if a.Status == "running" && a.Last != "" {
			out = append(out, fg(th.TextFaint, "  │ ")+fg(th.TextDim, clip(a.Last, 60)))
		}
	}
	return out
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
		name := v.relPath(paths[0])
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
		line := fg(th.TextFaint, "  "+conn) + st + " " + fg(th.TextPrimary, v.relPath(p))
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
// rows. Older rows fold into one "+N completed" row.
func (v *View) renderRail(rec *Record, calls []*Call, now time.Time, cw int, thinking bool) builder {
	var b builder
	rows := v.groupRows(calls, now, cw)
	if len(rows) > 5 {
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
		rows = append([]rowT{{lines: []string{txt}}}, rows[len(rows)-4:]...)
	}
	if thinking {
		frame := render.Frame(render.LoadingDots, frameAt(now)/4)
		rows = append(rows, rowT{lines: []string{joinNonEmpty(" ",
			fg(v.th.Brand, render.Frame(render.Braille, frameAt(now))),
			fg(v.th.TextMuted, "Thinking"+frame))}})
	}
	b.add(v.statusHeader(phaseOf(calls), !rec.Done, rec.At, now))
	ab := v.assembleRows(rows)
	off := len(b.lines)
	b.lines = append(b.lines, ab.lines...)
	for _, h := range ab.hits {
		h.line += off
		b.hits = append(b.hits, h)
	}
	return b
}

func phaseOf(calls []*Call) string {
	phase := "explore"
	for _, c := range calls {
		switch {
		case c.Tool == "dispatch":
			return "dispatch"
		case isEditTool(c.Tool):
			phase = "edit"
		}
	}
	return phase
}
