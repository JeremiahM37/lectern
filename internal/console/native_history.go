package console

import (
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"net/url"
	"strings"
	"time"
)

type nativeListMsg struct {
	key, session string
	data         []byte
	err          error
}
type nativeSelection struct {
	key, session, conversation string
	before                     *int64
}

func (m *dashboard) savedConversations() tea.Cmd {
	r := m.current()
	if r == nil || sections[m.section] != "sessions" || m.busy {
		return nil
	}
	c, key, sid := m.client, m.key(), id(r)
	m.busy = true
	return func() tea.Msg {
		b, e := c.JSON("GET", "/sessions/"+sid+"/conversations", nil)
		return nativeListMsg{key, sid, b, e}
	}
}
func (m *dashboard) nativePicker(v nativeListMsg) tea.Cmd {
	m.busy = false
	if v.key != m.key() {
		return nil
	}
	if v.err != nil {
		m.notice = v.err.Error()
		return nil
	}
	var data struct {
		Conversations []struct {
			ID, Title string
			Modified  float64
		} `json:"conversations"`
		Current struct {
			State, ID string
			Saved     bool
		} `json:"current"`
		Limited         bool `json:"scan_limited"`
		ForkSupported   bool `json:"fork_supported"`
		ResumeSupported bool `json:"resume_supported"`
	}
	if err := json.Unmarshal(v.data, &data); err != nil {
		m.notice = err.Error()
		return nil
	}
	if len(data.Conversations) == 0 {
		m.notice = "No saved conversations found in this workspace."
		if data.Current.State == "identified" {
			m.notice = "The current terminal has not saved readable messages yet."
		}
		return nil
	}
	choices := []choice{}
	selected := ""
	for _, c := range data.Conversations {
		label := ""
		if data.Current.State == "identified" && data.Current.Saved && c.ID == data.Current.ID {
			label = "Current terminal · "
			selected = c.ID
		}
		choices = append(choices, choice{label + time.Unix(int64(c.Modified), 0).Format("Jan 2 15:04") + " · " + clean(c.Title) + " · " + c.ID, c.ID})
	}
	if selected == "" {
		selected = choices[0].Value
	}
	actions := []choice{{"Read saved messages", "read"}}
	if data.ForkSupported {
		actions = append(actions, choice{"Fork into a new conversation", "fork"})
	}
	if data.ResumeSupported {
		actions = append(actions, choice{"Resume this conversation", "resume"})
	}
	action := "read"
	if m.historyResume && data.ResumeSupported {
		// Opened to restore a session: continuing it is the likely answer.
		action = "resume"
	}
	m.historyResume = false
	fields := []field{{Key: "conversation", Label: "Saved conversation (this workspace)", Value: selected, Options: choices}, {Key: "action", Label: "Action", Value: action, Options: actions}, {Key: "name", Label: "New session name", Value: ""}}
	if data.ForkSupported {
		fields = append(fields, field{Key: "workspace", Label: "Workspace for forks", Value: "shared", Options: []choice{{"Use the same files", "shared"}, {"New isolated Git worktree", "isolated"}}}, field{Key: "branch", Label: "Fork branch (blank = automatic)"}, field{Key: "base", Label: "Fork base commit or branch (blank = HEAD)"})
	}
	return m.openForm("Saved conversations", fields, func(values map[string]any) tea.Cmd {
		cid := str(values["conversation"])
		m.form = nil
		if str(values["action"]) == "fork" {
			body := map[string]any{"conversation_id": cid, "name": values["name"]}
			warning := "Create a new conversation from " + cid + "? Both agents share the workspace files. The original conversation is unchanged."
			if str(values["workspace"]) == "isolated" {
				body["background"] = true
				body["worktree"] = map[string]any{"branch": values["branch"], "base": values["base"]}
				warning = "Fork " + cid + " into a new Git worktree from the selected committed base? Uncommitted changes stay in the original workspace. The original conversation is unchanged."
			}
			m.pending = &dashboardAction{Label: "Fork conversation", Method: "POST", Path: "/sessions/" + v.session + "/fork", Body: body, Warning: warning}
			return nil
		}
		if str(values["action"]) == "resume" {
			m.pending = &dashboardAction{Label: "Resume conversation", Method: "POST", Path: "/sessions/" + v.session + "/resume", Body: map[string]any{"conversation_id": cid, "name": values["name"]}, Warning: "The previous terminal must be stopped. Continue this same saved history in its original workspace? Conversation: " + cid}
			return nil
		}
		m.native = &nativeSelection{key: v.key, session: v.session, conversation: cid}
		return m.readResource("Saved conversation", "/sessions/"+v.session+"/conversations/"+url.PathEscape(cid))
	})
}
func (m *dashboard) olderNative() tea.Cmd {
	n := m.native
	if n == nil || n.key != m.key() || n.before == nil || m.busy {
		return nil
	}
	return m.readResource("Saved conversation", fmt.Sprintf("/sessions/%s/conversations/%s?before=%d", n.session, url.PathEscape(n.conversation), *n.before))
}
func nativeText(data []byte) string {
	var page struct {
		Messages []struct {
			Role, Text string
			Truncated  bool
		}
		Before *int64
	}
	if json.Unmarshal(data, &page) != nil {
		return "Could not parse saved messages"
	}
	var out []string
	for _, message := range page.Messages {
		text := strings.ToUpper(message.Role) + "\n" + message.Text
		if message.Truncated {
			text += "\n[Long message shortened]"
		}
		out = append(out, text)
	}
	if len(out) == 0 {
		out = append(out, "No readable messages in this window.")
	}
	if page.Before != nil {
		out = append(out, "Press O to load earlier messages; H to choose another conversation.")
	}
	return strings.Join(out, "\n\n")
}
