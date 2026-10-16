package app

import (
	"errors"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/discord"
)

// errNoDiscordWebhook refuses turning Discord on before a webhook exists.
var errNoDiscordWebhook = errors.New("no Discord webhook: set SCRIBE_DISCORD_WEBHOOK or the discord_webhook key first")

// SetRemoteDiscord turns the Discord webhook notifier on or off in the global
// config (remote.discord.enabled). Turning it on needs a webhook URL, from
// SCRIBE_DISCORD_WEBHOOK or the discord_webhook key; without one the notifier
// could not be built, so the change is refused rather than saved. The notifier
// is built at start, so the change applies from the next start.
func (b *scribeBackend) SetRemoteDiscord(on bool) error {
	if on && config.Secret(discord.WebhookEnv, discord.WebhookKey) == "" {
		return errNoDiscordWebhook
	}
	return config.SetRemoteDiscordEnabled(on)
}
