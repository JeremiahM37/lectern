package tui

// /remote turns Scribe's Telegram surface on or off, binds this tab to it and
// pairs a chat. It replaces nothing: the name is new to the registry.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/telegram"
)

func init() {
	overrideCommands(&command{Name: "remote", Args: "[on|off|here|pair|unpair|status|token|allow|deny|pairings|logs]",
		Desc: "Telegram: turn the phone surface on or off, set the bot token, send messages to this tab, allow, pair or unpair chats, show logs", Group: "System",
		Run: func(m *Model, args string) tea.Cmd {
			return m.remoteCommand(strings.TrimSpace(args))
		}})
}

// /hearth is the name the spec gives the chat-surface bridge. Its verbs map
// onto /remote: bind is "here", unbind releases the bound tab.
func init() {
	overrideCommands(&command{Name: "hearth", Args: "[status|bind|unbind|pair|unpair]",
		Desc: "Hearth: the phone chat bridge (Telegram), bind or unbind the tab that takes phone messages", Group: "System",
		Run: func(m *Model, args string) tea.Cmd {
			return m.hearthCommand(strings.TrimSpace(args))
		}})
}

func init() {
	overrideCommands(&command{Name: "unpair", Desc: "Telegram: forget every chat paired from the phone (config chats stay)", Group: "System",
		Run: func(m *Model, args string) tea.Cmd {
			return m.remoteCommand("unpair")
		}})
}

// remoteCommand runs one /remote subcommand and reports the result in this tab.
func (m *Model) remoteCommand(args string) tea.Cmd {
	t := m.cur()
	say := func(kind, text string) {
		m.enterChat()
		t.chat.AddSystem(kind, text)
	}
	verb, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch verb {
	case "token":
		say("info", m.remoteToken(rest))
		return nil
	case "discord":
		ds, ok := t.be.(RemoteDiscordSetter)
		if !ok {
			say("err", "This session cannot change the Discord notifier.")
			return nil
		}
		on := rest == "on"
		if rest != "on" && rest != "off" {
			say("info", "Usage: /remote discord on|off. The webhook comes from SCRIBE_DISCORD_WEBHOOK or the key store; the change applies from the next start.")
			return nil
		}
		if err := ds.SetRemoteDiscord(on); err != nil {
			say("err", "Could not change the Discord notifier: "+err.Error())
			return nil
		}
		say("ok", "Discord notifier "+rest+" (from the next start of Scribe).")
		return nil
	case "allow":
		id, err := telegram.Default.Allow(rest)
		if err != nil {
			say("err", "Could not allow that chat: "+err.Error()+" Usage: /remote allow <chat id>")
			return nil
		}
		say("info", "Chat "+id+" is allowed. It can talk to Scribe now, without a pairing code.")
		return nil
	case "deny":
		ok, err := telegram.Default.Forget(rest)
		switch {
		case errors.Is(err, remote.ErrStaticChat):
			say("err", err.Error())
		case err != nil:
			say("err", "Could not forget that chat: "+err.Error()+" Usage: /remote deny <chat id>")
		case ok:
			say("info", "Chat "+rest+" is no longer paired.")
		default:
			say("info", "Chat "+rest+" was not paired. /remote pairings lists the paired chats.")
		}
		return nil
	case "pairings":
		if rest == "" {
			m.openPairingsView()
			return nil
		}
		say("info", pairingsReport())
		return nil
	case "logs":
		if rest == "" {
			m.openLogsView()
			return nil
		}
		say("info", logsReport())
		return nil
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
	case "unpair":
		n, err := telegram.Default.Unpair()
		if err != nil {
			say("err", "Could not save the change: "+err.Error())
			return nil
		}
		say("info", fmt.Sprintf("Unpaired %d chat(s). Chats listed in the config stay paired. Pair again with /remote pair.", n))
	case "off":
		telegram.Default.Bind(nil)
		telegram.Default.Disable()
		say("info", "Telegram is off. Approvals and questions stay in this terminal.")
	default:
		say("err", "Usage: /remote [on|off|here|pair|unpair|status|token|allow|deny|pairings|logs]")
	}
	return nil
}

// hearthCommand maps the /hearth verbs onto the Telegram surface.
func (m *Model) hearthCommand(args string) tea.Cmd {
	t := m.cur()
	switch args {
	case "bind":
		return m.remoteCommand("here")
	case "unbind":
		telegram.Default.Bind(nil)
		m.enterChat()
		t.chat.AddSystem("info", "This tab no longer takes phone messages. Approvals and questions still reach the phone.")
		return nil
	case "pair", "unpair", "status", "":
		return m.remoteCommand(args)
	}
	m.enterChat()
	t.chat.AddSystem("err", "Usage: /hearth [status|bind|unbind|pair|unpair]")
	return nil
}

// remoteToken handles /remote token, token set <token> and token clear. The
// token is never printed in full, and error text never echoes it.
func (m *Model) remoteToken(args string) string {
	verb, value, _ := strings.Cut(args, " ")
	switch verb {
	case "", "status":
		src := telegram.TokenSource()
		if src == "" {
			return "No Telegram bot token is set. /remote token set <token> stores one (get it from @BotFather)."
		}
		return fmt.Sprintf("Telegram bot token: %s, from the %s. Shown masked; it is never printed in full.", telegram.MaskToken(telegram.CurrentToken()), src)
	case "set":
		if err := telegram.SaveToken(value); err != nil {
			return err.Error()
		}
		msg := fmt.Sprintf("Telegram bot token saved to the key store as %s.", telegram.MaskToken(value))
		if os.Getenv(telegram.TokenEnv) != "" {
			msg += " " + telegram.TokenEnv + " is also set and takes precedence."
		}
		if telegram.Default.Enabled() {
			msg += " Turn Telegram off and on (/remote off, then /remote on) to use it."
		}
		return msg
	case "clear":
		had, err := telegram.ClearToken()
		if err != nil {
			return "Could not update the key store: " + err.Error()
		}
		if !had {
			return "No token was stored in the key store."
		}
		if telegram.Default.Enabled() {
			return "Stored Telegram token removed. The running surface keeps it until /remote off."
		}
		return "Stored Telegram token removed."
	}
	return "Usage: /remote token [set <token>|clear]"
}

// pairingsReport lists the allowed chats. Config chats are marked.
func pairingsReport() string {
	chats, err := telegram.Default.Pairings()
	if err != nil {
		return "Could not read the paired chats: " + err.Error()
	}
	if len(chats) == 0 {
		return "No chats are paired. Send /remote pair, then type the code into the chat, or /remote allow <chat id>."
	}
	lines := []string{fmt.Sprintf("%d paired chat(s):", len(chats))}
	for _, c := range chats {
		note := ""
		if c.Config {
			note = " (config; /remote deny cannot remove it)"
		}
		lines = append(lines, "  "+c.ID+note)
	}
	return strings.Join(lines, "\n")
}

// logsReport shows the last surface log lines, newest last.
func logsReport() string {
	lines := telegram.Default.Logs()
	if len(lines) == 0 {
		return "No Telegram log lines yet."
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return "Last Telegram log lines:\n" + strings.Join(lines, "\n")
}

// remoteStatus says whether Telegram is on, where its messages go and how
// many chats are paired. Chat ids are not shown.
func remoteStatus(be Backend) string {
	if !telegram.Default.Enabled() {
		if telegram.TelegramEnabledInConfig() {
			return "Telegram is off, though the config asks for it. Turn it on with /remote on."
		}
		return "Telegram is off. /remote on turns it on; it needs a bot token (/remote token set, or SCRIBE_TELEGRAM_TOKEN)."
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
