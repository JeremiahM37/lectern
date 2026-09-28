package ptyhost

import (
	"strconv"
	"strings"
)

// expandFormat evaluates the part of tmux's format language Lectern uses:
// #{name}, #{?cond,then,else}, #{==:a,b}, #{!=:a,b} and ## for a literal #.
// A name that is not known expands to nothing, as in tmux.
func expandFormat(format string, lookup func(string) (string, bool)) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '#' || i+1 >= len(format) {
			b.WriteByte(c)
			continue
		}
		switch format[i+1] {
		case '#':
			b.WriteByte('#')
			i++
		case '{':
			end := matchBrace(format, i+1)
			if end < 0 {
				b.WriteString(format[i:])
				return b.String()
			}
			b.WriteString(evalExpr(format[i+2:end], lookup))
			i = end
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// matchBrace finds the } closing the { at open, skipping nested ones.
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits s on commas that are not inside a nested #{...}.
func splitTop(s string, n int) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 && (n < 0 || len(parts) < n-1) {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func evalExpr(expr string, lookup func(string) (string, bool)) string {
	switch {
	case strings.HasPrefix(expr, "?"):
		parts := splitTop(expr[1:], 3)
		if len(parts) < 2 {
			return ""
		}
		cond := parts[0]
		var v string
		if strings.Contains(cond, "#{") {
			v = expandFormat(cond, lookup)
		} else {
			v, _ = lookup(cond)
		}
		if truthy(v) {
			return expandFormat(parts[1], lookup)
		}
		if len(parts) > 2 {
			return expandFormat(parts[2], lookup)
		}
		return ""
	case strings.HasPrefix(expr, "==:"), strings.HasPrefix(expr, "!=:"):
		parts := splitTop(expr[3:], 2)
		if len(parts) != 2 {
			return ""
		}
		eq := expandFormat(parts[0], lookup) == expandFormat(parts[1], lookup)
		if strings.HasPrefix(expr, "!=") {
			eq = !eq
		}
		if eq {
			return "1"
		}
		return "0"
	}
	v, _ := lookup(expr)
	return v
}

func truthy(v string) bool { return v != "" && v != "0" }

// formatVars answers format names for one session.
func formatVars(in Info) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if strings.HasPrefix(name, "@") {
			v, ok := in.Options[name]
			return v, ok
		}
		switch name {
		case "session_name":
			return in.Name, true
		case "session_id":
			return "$" + strconv.Itoa(in.ID), true
		case "session_created":
			return strconv.FormatInt(in.Created, 10), true
		case "session_activity":
			return strconv.FormatInt(in.Activity, 10), true
		case "session_attached":
			return strconv.Itoa(in.Clients), true
		case "pane_id":
			return "%" + strconv.Itoa(in.ID), true
		case "pane_pid":
			return strconv.Itoa(in.PID), true
		case "pane_tty":
			return in.TTY, true
		case "pane_current_path":
			return in.Cwd, true
		case "pane_current_command":
			return in.Current, true
		case "pane_start_path":
			return in.StartDir, true
		case "window_width", "pane_width":
			return strconv.Itoa(in.Cols), true
		case "window_height", "pane_height":
			return strconv.Itoa(in.Rows), true
		}
		return "", false
	}
}
