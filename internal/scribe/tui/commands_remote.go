package tui

// /remote turns Scribe's Telegram surface on or off, binds this tab to it and
// pairs a chat. It replaces nothing: the name is new to the registry.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/telegram"
)

func init() {
	overrideCommands(&command{Name: "remote", Args: "[on|off|here|pair|status]",
		Desc: "Telegram: turn the phone surface on or off, send messages to this tab, pair a chat", Group: "System",
		Run: func(m *Model, args string) tea.Cmd {
			return m.remoteCommand(strings.TrimSpace(args))
		}})
}

// remoteCommand runs one /remote subcommand and reports the result in this tab.
func (m *Model) remoteCommand(args string) tea.Cmd {
	t := m.cur()
	say := func(kind, text string) {
		m.enterChat()
		t.chat.AddSystem(kind, text)
	}
	switch args {
	case "", "status":
		say("info", remoteStatus(t.be))
	case "on":
		if err := telegram.Default.EnableFromConfig(); err != nil {
			say("err", err.Error())
			return nil
		}
		telegram.Default.Bind(t.be)
		say("info", "Telegram is on. Messages from paired chats go to this tab. Pair a chat with /remote pair.")
	case "here":
		if !telegram.Default.Enabled() {
			say("err", telegram.ErrOff.Error())
			return nil
		}
		telegram.Default.Bind(t.be)
		say("info", "Telegram messages now go to this tab.")
	case "pair":
		code, expires, err := telegram.Default.Pair()
		if err != nil {
			say("err", err.Error())
			return nil
		}
		say("info", fmt.Sprintf("Send this code to the Scribe bot from the chat to pair: %s\n"+
			"It works once, until %s. Five wrong codes lock that chat out for ten minutes.",
			code, expires.Format("15:04")))
	case "off":
		telegram.Default.Bind(nil)
		telegram.Default.Disable()
		say("info", "Telegram is off. Approvals and questions stay in this terminal.")
	default:
		say("err", "Usage: /remote [on|off|here|pair|status]")
	}
	return nil
}

// remoteStatus says whether Telegram is on, where its messages go and how
// many chats are paired. Chat ids are not shown.
func remoteStatus(be Backend) string {
	if !telegram.Default.Enabled() {
		if telegram.TelegramEnabledInConfig() {
			return "Telegram is off, though the config asks for it. Turn it on with /remote on."
		}
		return "Telegram is off. /remote on turns it on; it needs SCRIBE_TELEGRAM_TOKEN."
	}
	target := "No tab receives Telegram messages yet. Run /remote here in the tab you want."
	switch {
	case telegram.Default.IsBound(be):
		target = "Telegram messages go to this tab."
	case telegram.Default.Bound():
		target = "Telegram messages go to another tab. /remote here moves them to this one."
	}
	return fmt.Sprintf("Telegram is on. Paired chats: %d. %s", len(telegram.Default.Chats()), target)
}
