package main

// The Telegram surface for `lectern code`: the --telegram flag, and the
// wiring that puts every Scribe tab's approvals and questions on the phone.

import (
	"fmt"
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
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
	a.SetHost(telegram.Default.Host(a.Host()))
	a.Subscribe(func(e agent.Event) { telegram.Default.Notify(be, e) })
	if !bind {
		return
	}
	if err := telegram.Default.EnableFromConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "lectern code: telegram: %v\n", err)
		return
	}
	telegram.Default.Bind(be)
}
