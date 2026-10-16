package main

// The Telegram surface for `lectern code`: the --telegram flag, and the
// wiring that puts every Scribe tab's approvals and questions on the phone.

import (
	"fmt"
	"os"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/discord"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/telegram"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

const telegramTokenEnv = telegram.TokenEnv

// telegramTokenSet reports whether a bot token is configured.
func telegramTokenSet() bool {
	return config.Secret(telegram.TokenEnv, telegram.TokenKey) != ""
}

// telegramInConfig reports whether the config starts the surface with Scribe.
func telegramInConfig() bool { return telegram.TelegramEnabledInConfig() }

// attachRemote connects one Scribe tab to the Telegram surface. Its approvals
// and questions race the paired chats, and its turn ends reach them while it
// is the bound tab. bind turns the surface on and makes this tab the one that
// takes phone messages. Later tabs start unbound; /remote here moves it.
func attachRemote(a *app.App, be tui.Backend, bind bool) {
	a.SetHost(telegram.Default.HostFor(be, a.Host()))
	a.Subscribe(func(e agent.Event) { telegram.Default.Notify(be, e) })
	if n := discordNotifier(); n != nil {
		a.Subscribe(func(e agent.Event) { n.OnEvent(e) })
	}
	if !bind {
		return
	}
	if err := telegram.Default.EnableFromConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "lectern code: telegram: %v\n", err)
		return
	}
	telegram.Default.Bind(be)
}

var (
	discordOnce sync.Once
	discordNote *discord.Notifier
)

// discordNotifier returns the process-wide Discord webhook notifier when the
// config enables it and a webhook URL is set. It is built once, so every tab
// shares one notifier. Notify-only: Discord cannot answer approvals here.
func discordNotifier() *discord.Notifier {
	discordOnce.Do(func() {
		rc, err := config.LoadRemote()
		if err != nil || rc.Discord == nil || !rc.Discord.Enabled {
			return
		}
		url := config.Secret(discord.WebhookEnv, discord.WebhookKey)
		if url == "" {
			fmt.Fprintf(os.Stderr, "lectern code: discord: enabled in config, but no webhook; set %s\n", discord.WebhookEnv)
			return
		}
		n, err := discord.New(url, 0, func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "lectern code: "+format+"\n", args...)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "lectern code: discord: %v\n", err)
			return
		}
		discordNote = n
	})
	return discordNote
}
