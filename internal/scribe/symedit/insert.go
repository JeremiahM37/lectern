package symedit

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// insertText adds code at a file-level anchor. The anchor is index: 0 for the
// top of the file, -1 for the bottom, or a 1-based line to insert before. Or
// it is value: "after-imports" or "before-exports".
func insertText(text string, o op) (string, string, error) {
	if isBlank(o.NewCode) {
		return "", "", fmt.Errorf("new_code is the text to insert")
	}
	code := reindent(o.NewCode, "")
	idx := newLineIndex(text)
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	n, byIndex, err := parseIndex(o.Index)
	if err != nil {
		return "", "", err
	}
	var line int // 1-based line to insert before; 0 means the bottom of the file
	var where string
	switch {
	case byIndex:
		switch {
		case n == 0:
			line, where = 1, "the top of the file"
		case n == -1:
			where = "the bottom of the file"
		case n < -1:
			return "", "", fmt.Errorf("index %d is not a line. Use 0 for the top, -1 for the bottom, or a line number", n)
		case n > idx.count()+1:
			return "", "", fmt.Errorf("index %d is past the end: the file has %d lines. Use -1 to add at the bottom", n, idx.count())
		case n == idx.count()+1:
			where = "the bottom of the file"
		default:
			line, where = n, fmt.Sprintf("before line %d", n)
		}
	case strings.TrimSpace(o.Value) == "after-imports":
		l, ok := importsEnd(lines)
		if !ok {
			l = 1
		}
		line, where = l, "after the imports"
		if !ok {
			where = "the top of the file (it has no imports)"
		}
	case strings.TrimSpace(o.Value) == "before-exports":
		found := 0
		for i, l := range lines {
			if strings.HasPrefix(l, "export") {
				found = i + 1
				break
			}
		}
		if found == 0 {
			return "", "", fmt.Errorf("the file has no export to insert before. Use index instead: 0 for the top, -1 for the bottom")
		}
		line, where = found, "before the exports"
	case strings.TrimSpace(o.Value) != "":
		return "", "", fmt.Errorf("unknown value %q for insert_text. Use after-imports or before-exports, or give index", o.Value)
	default:
		return "", "", fmt.Errorf("insert_text needs an anchor: index (0 for the top, -1 for the bottom, or a line number) or value (after-imports, before-exports)")
	}
	if line == 0 || line > idx.count() {
		if text != "" && !strings.HasSuffix(text, "\n") {
			return text + "\n" + code + "\n", "inserted at " + where, nil
		}
		return text + code + "\n", "inserted at " + where, nil
	}
	pos := idx.start(line)
	return text[:pos] + code + "\n" + text[pos:], "inserted at " + where, nil
}

// parseIndex reads the index argument. Models send it as a number, a numeric
// string or top/bottom; all of those are accepted.
func parseIndex(raw json.RawMessage) (int, bool, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return 0, false, fmt.Errorf("index must be a line number: 0 for the top, -1 for the bottom")
	}
	str = strings.TrimSpace(str)
	switch strings.ToLower(str) {
	case "top", "start":
		return 0, true, nil
	case "bottom", "end":
		return -1, true, nil
	case "":
		return 0, false, nil
	}
	v, err := strconv.Atoi(str)
	if err != nil {
		return 0, false, fmt.Errorf("index %q is not a number. Use 0 for the top, -1 for the bottom, or a line number", str)
	}
	return v, true, nil
}

// importsEnd returns the 1-based line that follows the last top-level import,
// or false when there is none. Multi-line import blocks are followed to their
// closing line.
func importsEnd(lines []string) (int, bool) {
	last, open := -1, false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if open {
			if strings.ContainsAny(l, ")}") {
				open, last = false, i
			}
			continue
		}
		if l != strings.TrimLeft(l, " \t") || !isImportLine(t) {
			continue
		}
		last = i
		if strings.HasSuffix(t, "(") || (strings.HasSuffix(t, "{") && !strings.Contains(t, "}")) {
			open = true
		}
	}
	if last < 0 {
		return 0, false
	}
	return last + 2, true
}

func isImportLine(t string) bool {
	switch {
	case strings.HasPrefix(t, "import "), t == "import (", strings.HasPrefix(t, "import{"), strings.HasPrefix(t, "import {"):
		return true
	case strings.HasPrefix(t, "from ") && strings.Contains(t, " import"):
		return true
	case strings.HasPrefix(t, "use "), strings.HasPrefix(t, "#include"):
		return true
	}
	return false
}
