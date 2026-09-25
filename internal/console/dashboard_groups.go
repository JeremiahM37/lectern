package console

import (
	"sort"
	"strings"
)

// Group headers are presentation rows only. current() never returns one, so
// session actions cannot mistake a group for a session ID.
func isGroup(r row) bool { return r["group_header"] == true }
func displayID(r row) string {
	if isGroup(r) {
		return "group:" + str(r["group_key"])
	}
	return id(r)
}
func (m *dashboard) selectionID() string {
	if m.selected < 0 || m.selected >= len(m.visible) {
		return ""
	}
	return displayID(m.visible[m.selected])
}
func (m *dashboard) selectedGroup() row {
	if m.selected >= 0 && m.selected < len(m.visible) && isGroup(m.visible[m.selected]) {
		return m.visible[m.selected]
	}
	return nil
}
func (m *dashboard) treeMode() bool { return m.section == 0 && m.grouping == 3 }

type consoleGroup struct {
	path, label      string
	children         map[string]*consoleGroup
	sessions         []row
	count, attention int
}

func (m *dashboard) groupTree(rows []row) []row {
	root := &consoleGroup{children: map[string]*consoleGroup{}}
	for _, r := range rows {
		path := str(r["group_path"])
		parts := strings.Split(path, "/")
		if path == "" {
			parts = []string{"\x00"} // Distinct from a group actually named Ungrouped.
		}
		node := root
		for i, label := range parts {
			child := node.children[label]
			if child == nil {
				child = &consoleGroup{path: strings.Join(parts[:i+1], "/"), label: label, children: map[string]*consoleGroup{}}
				if label == "\x00" {
					child.label = "Ungrouped"
				}
				node.children[label] = child
			}
			child.count++
			status := str(r["status"])
			if r["setup_state"] == "failed" {
				status = "failed"
			}
			switch status {
			case "waiting", "review", "pending", "failed":
				child.attention++
			}
			node = child
		}
		node.sessions = append(node.sessions, r)
	}
	var out []row
	var visit func(*consoleGroup, int)
	visit = func(node *consoleGroup, depth int) {
		keys := make([]string, 0, len(node.children))
		for k := range node.children {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := node.children[k]
			out = append(out, row{"group_header": true, "group_key": child.path, "name": child.label, "depth": depth, "count": child.count, "attention": child.attention})
			if !m.collapsed[child.path] {
				out = append(out, child.sessions...)
				visit(child, depth+1)
			}
		}
	}
	visit(root, 0)
	return out
}

func (m *dashboard) setGroupCollapsed(path string, collapsed bool) {
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	m.collapsed[path] = collapsed
	m.filter()
	for i, r := range m.visible {
		if isGroup(r) && str(r["group_key"]) == path {
			m.selected = i
			break
		}
	}
	m.ensureSelection()
	m.updatePreview()
	m.savePreferences()
}
func (m *dashboard) toggleGroup() {
	if r := m.selectedGroup(); r != nil {
		path := str(r["group_key"])
		m.setGroupCollapsed(path, !m.collapsed[path])
	}
}
func (m *dashboard) collapseGroup() {
	if !m.treeMode() {
		return
	}
	if strings.TrimSpace(m.query.Value()) != "" {
		m.notice = "Clear search with Esc to fold groups."
		return
	}
	path := ""
	if r := m.selectedGroup(); r != nil {
		path = str(r["group_key"])
		if m.collapsed[path] {
			index := strings.LastIndex(path, "/")
			if index < 0 {
				return
			}
			path = path[:index]
		}
	} else if r := m.current(); r != nil {
		path = str(r["group_path"])
		if path == "" {
			path = "\x00"
		}
	} else {
		return
	}
	m.setGroupCollapsed(path, true)
}
func (m *dashboard) expandGroup() {
	if r := m.selectedGroup(); r != nil {
		path := str(r["group_key"])
		if m.collapsed[path] {
			m.setGroupCollapsed(path, false)
		} else {
			m.selected++
			m.ensureSelection()
			m.updatePreview()
		}
	}
}
func (m *dashboard) rowHeight(i int) int {
	if isGroup(m.visible[i]) {
		return 1
	}
	if m.treeMode() {
		return 2
	}
	return 3
}
func (m *dashboard) rowsHeight(start, end int) int {
	total := 0
	for i := start; i < end && i < len(m.visible); i++ {
		total += m.rowHeight(i)
	}
	return total
}
func (m *dashboard) rowAt(y int) int {
	remaining := max(3, m.height-9)
	if y < 0 || y >= remaining {
		return -1
	}
	for i := m.offset; i < len(m.visible); i++ {
		if m.rowHeight(i) > remaining {
			return -1
		}
		if y < m.rowHeight(i) {
			return i
		}
		y -= m.rowHeight(i)
		remaining -= m.rowHeight(i)
	}
	return -1
}
