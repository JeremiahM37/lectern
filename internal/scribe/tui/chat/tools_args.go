package chat

import (
	"fmt"
	"strconv"
	"strings"
)

// readArgs is the args column of a read row: "N files" for several paths,
// otherwise the path with its detail (formatReadDetail).
func readArgs(v *View, m map[string]any) string {
	if n := readFileCount(m); n > 1 {
		return fmt.Sprintf("%d files", n)
	}
	if argStr(m, "symbol") != "" {
		return symbolArgs(v, m)
	}
	p := v.relPath(readPath(m))
	if d := readDetail(m); d != "" {
		return p + " " + d
	}
	return p
}

// readPath is the file a read call names. The read tool takes path, or a
// one-file paths or files list, and a near-miss file_path or file key is
// read the same way: a row must never lose the file it is about.
func readPath(m map[string]any) string {
	for _, k := range []string{"path", "file_path", "file"} {
		if p := argStr(m, k); p != "" {
			return p
		}
	}
	if ps, ok := m["paths"].([]any); ok && len(ps) > 0 {
		if p, ok := ps[0].(string); ok {
			return p
		}
	}
	if fs, ok := m["files"].([]any); ok && len(fs) > 0 {
		if f, ok := fs[0].(map[string]any); ok {
			return readPath(f)
		}
	}
	return ""
}

// readFileCount is how many files one read call names.
func readFileCount(m map[string]any) int {
	if ps, ok := m["paths"].([]any); ok && len(ps) > 1 {
		return len(ps)
	}
	if fs, ok := m["files"].([]any); ok && len(fs) > 1 {
		return len(fs)
	}
	return 0
}

// readDetail is formatReadDetail for the one range Scribe's read takes: a
// line range as "(L10-20)", "(L10-)" when open ended, and no range as
// "(full)". A symbol read is handled by symbolArgs before this is reached.
func readDetail(m map[string]any) string {
	if lines := argStr(m, "lines"); lines != "" {
		start, end, ok := parseLineRange(lines)
		if ok {
			if end > 0 {
				return fmt.Sprintf("(L%d-%d)", start, end)
			}
			return fmt.Sprintf("(L%d-)", start)
		}
	}
	if off, lim := argInt(m, "offset"), argInt(m, "limit"); off > 0 && lim > 0 {
		return fmt.Sprintf("(L%d-%d)", off, off+lim-1)
	} else if off > 0 {
		return fmt.Sprintf("(L%d-)", off)
	}
	return "(full)"
}

// parseLineRange reads "120-180" or "120-" (end 0 when open ended).
func parseLineRange(s string) (start, end int, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(s), "-")
	start, err := strconv.Atoi(strings.TrimSpace(a))
	if err != nil || start < 1 {
		return 0, 0, false
	}
	if !found {
		return start, 0, true
	}
	if strings.TrimSpace(b) == "" {
		return start, 0, true
	}
	end, err = strconv.Atoi(strings.TrimSpace(b))
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}

// symbolArgs is the args of a read of one definition: "name in path".
func symbolArgs(v *View, m map[string]any) string {
	return clip(argStr(m, "symbol")+" in "+v.relPath(argStr(m, "path")), 50)
}

// dispatchArgs is "[role] task" (task 45), or "[roles] objective" (55) for
// several subagents.
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
		return "[" + roles[0] + "] " + clip(first, 45)
	}
	return clip("["+strings.Join(roles, "/")+"] "+first, 55)
}

// memoryArgs follows the memory tool's actions: save shows "[cat] title",
// search the query, list its filters, and the id actions an 8-character id.
func memoryArgs(_ *View, m map[string]any) string {
	switch action := argStr(m, "action"); action {
	case "save", "write":
		cat := argStr(m, "category")
		if cat != "" {
			cat = "[" + cat + "]"
		}
		return clip(joinNonEmpty(" ", cat, argStr(m, "title")), 60)
	case "search":
		return clip(argStr(m, "query"), 50)
	case "list":
		return clip(joinNonEmpty(" ", argStr(m, "category"), argStr(m, "scope")), 60)
	case "delete", "pin", "unpin", "get", "restore":
		r := []rune(argStr(m, "id"))
		if len(r) > 8 {
			r = r[:8]
		}
		return string(r)
	default:
		return clip(joinNonEmpty(": ", action, argStr(m, "query", "title", "id", "summary")), 60)
	}
}

// skillsArgs: "load: name" for a load, the bare action otherwise.
func skillsArgs(_ *View, m map[string]any) string {
	action := argStr(m, "action")
	if action == "load" {
		return clip("load: "+argStr(m, "name"), 50)
	}
	return action
}

// gitArgs is the args column of a git row. A commit shows its message;
// log shows its count, diff notes staged, stash and branch show the sub action.
func gitArgs(_ *View, m map[string]any) string {
	action := argStr(m, "action")
	switch action {
	case "commit":
		return clip(firstLine(argStr(m, "message")), 50)
	case "log":
		if n := gitLogCount(argArgs(m)); n > 0 {
			return fmt.Sprintf("log last %d", n)
		}
		return "log"
	case "diff":
		if b, _ := m["staged"].(bool); b {
			return "diff staged"
		}
		return "diff"
	case "stash":
		return "stash " + defaultStr(argStr(m, "sub_action"), "push")
	case "branch":
		return "branch " + defaultStr(argStr(m, "sub_action"), "list")
	}
	return clip(joinNonEmpty(" ", action, argStr(m, "name", "ref", "message")), 50)
}

// argArgs returns the string entries of the git "args" array.
func argArgs(m map[string]any) []string {
	arr, _ := m["args"].([]any)
	var out []string
	for _, a := range arr {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// gitLogCount reads N from "-n N", "-nN" or "-N" in git log arguments.
func gitLogCount(args []string) int {
	for i, a := range args {
		switch {
		case a == "-n" && i+1 < len(args):
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				return n
			}
		case strings.HasPrefix(a, "-n") && len(a) > 2:
			if n, err := strconv.Atoi(a[2:]); err == nil {
				return n
			}
		case len(a) > 1 && a[0] == '-':
			if n, err := strconv.Atoi(a[1:]); err == nil {
				return n
			}
		}
	}
	return 0
}

// projectArgs is "action target", where target is the file, package or test
// filter the project command runs against.
func projectArgs(_ *View, m map[string]any) string {
	return clip(joinNonEmpty(" ", argStr(m, "action"), argStr(m, "target")), 50)
}

// arrowArgs renders "a → b" for rename-like tools, clipped to n.
func arrowArgs(a, b string, n int) string {
	return clip(joinNonEmpty(" → ", a, b), n)
}

func renameSymbolArgs(_ *View, m map[string]any) string {
	return arrowArgs(argStr(m, "symbol", "path"), argStr(m, "new_name", "to"), 50)
}

func moveSymbolArgs(_ *View, m map[string]any) string {
	return arrowArgs(argStr(m, "symbol"), argStr(m, "to"), 50)
}

func renameFileArgs(_ *View, m map[string]any) string {
	return arrowArgs(argStr(m, "from"), argStr(m, "to"), 60)
}

// defaultStr returns s, or def when s is empty.
func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
