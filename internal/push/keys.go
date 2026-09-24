package push

import (
	"log/slog"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Settings keys the resolved VAPID key pair is persisted under. Deliberately
// NOT in sinks.Keys: GET/PUT /api/settings never touches them, so there is no
// API path — not even an admin one — that can read the private key back out,
// and PUT /api/settings rejects them the same way it rejects any unknown key.
const (
	SettingPrivateKey = "vapid_private"
	SettingPublicKey  = "vapid_public"
)

const defaultEmail = "admin@example.com"

// ResolveKeys builds the Sender lectern runs with, so a phone alert works
// without the owner ever configuring keys by hand.
//
// LECTERN_VAPID_PRIVATE/PUBLIC win when both are set — an operator's explicit
// choice, e.g. to share one identity across installs. Otherwise a pair is
// loaded from the settings table; the very first start generates one and
// persists it, so every start after that reuses the same identity (a browser
// subscription is bound to the public key it subscribed with — rotating it
// silently would orphan every existing subscription).
func ResolveKeys(db *store.DB, envPrivate, envPublic, envEmail string, log *slog.Logger) (*Sender, error) {
	email := envEmail
	if email == "" {
		email = defaultEmail
	}
	if envPrivate != "" && envPublic != "" {
		logSource(log, "env")
		return &Sender{PrivateKey: envPrivate, PublicKey: envPublic, Email: email, Log: log}, nil
	}
	priv := db.Setting(SettingPrivateKey)
	pub := db.Setting(SettingPublicKey)
	source := "database"
	if priv == "" || pub == "" {
		var err error
		priv, pub, err = GenerateKeyPair()
		if err != nil {
			return nil, err
		}
		if err := db.SetSetting(SettingPrivateKey, priv); err != nil {
			return nil, err
		}
		if err := db.SetSetting(SettingPublicKey, pub); err != nil {
			return nil, err
		}
		source = "generated"
	}
	logSource(log, source)
	return &Sender{PrivateKey: priv, PublicKey: pub, Email: email, Log: log}, nil
}

// logSource logs once, at startup, which source the running key pair came
// from — env, an existing database row, or a fresh generation. Never the key
// material itself.
func logSource(log *slog.Logger, source string) {
	if log == nil {
		return
	}
	log.Info("vapid keys resolved", "source", source)
}
