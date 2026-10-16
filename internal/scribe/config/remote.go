package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Remote holds the phone-surface settings (Telegram, Discord). It is the
// "remote" key of the global config file, kept out of Config so the merge
// logic in config.go does not change. Secrets are never stored here: see
// Secret.
type Remote struct {
	Telegram *RemoteTelegram `json:"telegram,omitempty"`
	Discord  *RemoteDiscord  `json:"discord,omitempty"`
	// QuestionTimeoutSec bounds how long a question waits for an answer on a
	// phone surface before the local host answers (default 300).
	QuestionTimeoutSec int `json:"question_timeout_sec,omitempty"`
}

// RemoteTelegram configures the Telegram surface.
type RemoteTelegram struct {
	Enabled bool `json:"enabled"`
	// ChatIDs is the static per-chat allowlist. Chats that pair with a code
	// are recorded in the state file instead (remote/telegram).
	ChatIDs []int64 `json:"chat_ids,omitempty"`
	// APIBase overrides https://api.telegram.org (proxies, tests).
	APIBase string `json:"api_base,omitempty"`
}

// RemoteDiscord configures the Discord webhook notifier. The webhook URL is
// a secret: it comes from SCRIBE_DISCORD_WEBHOOK or the key store.
type RemoteDiscord struct {
	Enabled bool `json:"enabled"`
}

// remoteFile is the part of the global config file this file reads.
type remoteFile struct {
	Remote Remote `json:"remote"`
}

// LoadRemote reads the "remote" section of the global config file. A missing
// file or section gives the zero value.
func LoadRemote() (Remote, error) {
	var f remoteFile
	if err := readJSON(GlobalPath(), &f); err != nil {
		return Remote{}, err
	}
	return f.Remote, nil
}

// RemoteStateDir is where surface state (paired chats) is kept, under the
// Scribe home directory.
func RemoteStateDir() string { return filepath.Join(Home(), "remote") }

// Secret returns a secret by environment variable first, then by name in the
// key store (<Home>/keys.json, the same file the provider keys use). The key
// store is read here directly because the registry package imports config.
// Empty means not configured.
func Secret(envName, keyName string) string {
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v
	}
	b, err := os.ReadFile(filepath.Join(Home(), "keys.json"))
	if err != nil || len(b) == 0 {
		return ""
	}
	var keys map[string]string
	if json.Unmarshal(b, &keys) != nil {
		return ""
	}
	return strings.TrimSpace(keys[keyName])
}
