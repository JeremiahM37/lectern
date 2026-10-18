package tui

// The /remote pairings and logs views. /remote pairings opens a picker of the
// paired chats, where d unpairs one (config chats cannot be removed here);
// /remote logs opens the surface log in a read-only view. The text forms stay
// in the chat for scripts and for a tab without a modal stack.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/telegram"
)

// remotePairingsKeys is the footer of the pairings picker.
const remotePairingsKeys = "↑↓ move · d unpair · Esc close"

// openPairingsView lists the paired chats. It reads the allowlist again after
// every unpair, so the list never shows a chat that is already gone.
func (m *Model) openPairingsView() {
	chats, err := telegram.Default.Pairings()
	if err != nil {
		m.enterChat()
		m.cur().chat.AddSystem("err", "Could not read the paired chats: "+err.Error())
		return
	}
	p := NewPicker(m.th, "Telegram pairings", "lightning")
	p.Hint = remotePairingsKeys
	build := func(list []telegram.ChatInfo) []PickItem {
		items := make([]PickItem, 0, len(list))
		for _, c := range list {
			desc := "paired from the phone"
			if c.Config {
				desc = "from the config (cannot be removed here)"
			}
			items = append(items, PickItem{Value: c.ID, Label: c.ID, Desc: desc, Disabled: c.Config})
		}
		return items
	}
	p.SetItems(build(chats))
	if len(chats) == 0 {
		p.Blurb = "No chats are paired. /remote pair makes a code."
	} else {
		p.Blurb = fmt.Sprintf("%d paired chat(s)", len(chats))
	}
	p.OnKey = func(key string) (tea.Cmd, bool) {
		if key != "d" || p.searching {
			return nil, false
		}
		it, ok := p.Selected()
		if !ok || it.Disabled {
			return nil, true
		}
		ok, err := telegram.Default.Forget(it.Value)
		if err != nil {
			m.failAlert("Could not unpair " + it.Value + ": " + err.Error())
			return nil, true
		}
		if ok {
			m.setFlash("ok", "Chat "+it.Value+" is no longer paired.")
		}
		now, _ := telegram.Default.Pairings()
		p.SetItems(build(now))
		p.Blurb = fmt.Sprintf("%d paired chat(s)", len(now))
		return nil, true
	}
	m.pushModal(p)
}

// openLogsView shows the last surface log lines in a read-only view.
func (m *Model) openLogsView() {
	lines := telegram.Default.Logs()
	if len(lines) == 0 {
		lines = []string{"No Telegram log lines yet."}
	}
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\n")
	}
	m.openInfo("Telegram logs", "bug", lines)
}
