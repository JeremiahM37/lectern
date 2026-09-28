package console

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Pending approvals are polled alongside whatever pane is open, so the count,
// the banner and a session's "needs you" word are right on every pane — not
// only after someone thinks to open Approvals.

type approvalsMsg struct {
	rows []row
	err  error
}

func (m *dashboard) pollApprovals() tea.Cmd {
	if m.approvalsLoading {
		return nil
	}
	m.approvalsLoading = true
	c := m.client
	return func() tea.Msg {
		b, err := c.JSON("GET", "/approvals?status=pending", nil)
		var rows []row
		if err == nil {
			err = json.Unmarshal(b, &rows)
		}
		return approvalsMsg{rows, err}
	}
}

// approvalFor returns the oldest pending approval a session is blocked on.
func (m *dashboard) approvalFor(r row) row {
	if r == nil || sections[m.section] != "sessions" && sections[m.section] != "approvals" {
		return nil
	}
	if sections[m.section] == "approvals" {
		return r
	}
	sid := id(r)
	if sid == "" {
		return nil
	}
	for _, a := range m.approvals {
		if approvalSessionID(a) == sid {
			return a
		}
	}
	return nil
}

// approvalSessionID is the session an approval belongs to; a task attempt's
// approval has none, so "allow for this session" does not apply to it.
func approvalSessionID(a row) string {
	switch v := a["session_id"].(type) {
	case float64:
		if v > 0 {
			return fmt.Sprint(int64(v))
		}
	case int64:
		if v > 0 {
			return fmt.Sprint(v)
		}
	case string:
		return v
	}
	return ""
}

// approvalSummary is the one line a person reads before deciding, the same
// fields the web's approval-summary.ts reads.
func approvalSummary(a row) string {
	tool := str(a["tool_name"])
	input, _ := a["input"].(map[string]any)
	for _, field := range []string{"command", "path", "file_path", "url", "pattern"} {
		if s, ok := input[field].(string); ok && strings.TrimSpace(s) != "" {
			return oneLine(tool + ": " + truncateRunes(strings.TrimSpace(s), 90))
		}
	}
	if len(input) > 0 {
		b, _ := json.Marshal(input)
		return oneLine(tool + ": " + truncateRunes(string(b), 90))
	}
	return oneLine(tool)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func approvalWho(a row) string {
	if s := str(a["session_name"]); s != "" {
		return s
	}
	if t := str(a["task_title"]); t != "" {
		return t
	}
	return "an agent"
}

const decideLabel = "Approval decision"

// decide sends one decision. Allow once and deny go straight through: the
// row itself is the question, so a second "are you sure" only slows the
// person down. Allow for this session widens what the agent may do without
// asking, so it is confirmed first.
func (m *dashboard) decide(a row, decision string, forSession bool, note string) tea.Cmd {
	if a == nil {
		return nil
	}
	body := map[string]any{"decision": decision}
	if forSession {
		body["for_session"] = true
	}
	if note != "" {
		body["note"] = note
	}
	notice := "Allowed once: " + approvalSummary(a)
	switch {
	case decision == "denied":
		notice = "Denied: " + approvalSummary(a)
	case forSession:
		notice = "Allowed for this session: " + approvalSummary(a)
	}
	action := dashboardAction{Label: decideLabel, Method: "POST", Path: "/approvals/" + id(a) + "/decision", Body: body, Notice: notice}
	if forSession {
		action.Warning = fmt.Sprintf("Allow %q for the rest of this session?\n%s will not ask again for this tool (for Bash, for commands starting with the same word).", approvalSummary(a), approvalWho(a))
		action.Confirm = "allow for this session"
		m.pending = &action
		return nil
	}
	return m.execute(action)
}

func (m *dashboard) denyWithReasonForm(a row) tea.Cmd {
	if a == nil {
		return nil
	}
	return m.openForm("Deny with a reason", []field{{Key: "note", Label: "Reason the agent will see", Required: true}}, func(body map[string]any) tea.Cmd {
		m.form = nil
		return m.decide(a, "denied", false, str(body["note"]))
	})
}

// approvalKey handles y / a / n on a row that is asking for a decision. It
// reports false when the key means something else here.
func (m *dashboard) approvalKey(key string) (tea.Cmd, bool) {
	r := m.current()
	a := m.approvalFor(r)
	if a == nil {
		if key == "y" && sections[m.section] == "sessions" && r != nil {
			m.notice = "Nothing to allow: this session is not waiting for an approval."
			return nil, true
		}
		return nil, false
	}
	switch key {
	case "y":
		return m.decide(a, "approved", false, ""), true
	case "a":
		if approvalSessionID(a) == "" {
			m.notice = "A task's approval can only be allowed once or denied."
			return nil, true
		}
		return m.decide(a, "approved", true, ""), true
	case "n":
		if sections[m.section] != "approvals" {
			return nil, false
		}
		return m.decide(a, "denied", false, ""), true
	}
	return nil, false
}

// attachApprovalSession jumps from an approval to the session that asked.
func (m *dashboard) attachApprovalSession() tea.Cmd {
	a := m.current()
	sid := approvalSessionID(a)
	if sid == "" {
		m.notice = "This approval belongs to a task; open it from Tasks (4)."
		return nil
	}
	cmd := m.switchSection(0)
	m.focusSessionID = sid
	m.attachAfterRefresh = !m.controlOnly
	return cmd
}

func (m *dashboard) approvalBanner() string {
	n := len(m.approvals)
	if n == 0 {
		return ""
	}
	if n == 1 {
		return fmt.Sprintf(" ⏸ %s needs you: %s — press 2, or y on the session", approvalWho(m.approvals[0]), approvalSummary(m.approvals[0]))
	}
	return fmt.Sprintf(" ⏸ %d approvals need you — press 2", n)
}

// approvalPreview is what the Approvals pane shows beside the list: what is
// being asked, by whom, and the keys that answer it.
func approvalPreview(a row) string {
	lines := []string{approvalWho(a) + " asks to use " + str(a["tool_name"])}
	if input, ok := a["input"].(map[string]any); ok && len(input) > 0 {
		lines = append(lines, "")
		lines = append(lines, readable(input))
	}
	lines = append(lines, "", "y allow once")
	if approvalSessionID(a) != "" {
		lines = append(lines, "a allow for this session (asks first)")
	}
	lines = append(lines, "n deny", "m deny with a reason")
	if approvalSessionID(a) != "" {
		lines = append(lines, "Enter open the session")
	}
	return strings.Join(lines, "\n")
}
