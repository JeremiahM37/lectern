package telegram

// The bot token is kept in the same key store as the provider keys
// (<Home>/keys.json, under TokenKey), so it is written mode 0600 with an
// atomic rename and read through config.Secret. An environment variable of
// the same name still takes precedence. The token is never logged and never
// shown in full: MaskToken is what the UI prints.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// tokenShape is the BotFather form: a numeric bot id, a colon, then the secret.
var tokenShape = regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{20,}$`)

// ErrBadToken is returned for input that is not shaped like a bot token. The
// message never echoes the input.
var ErrBadToken = errors.New("that is not a Telegram bot token; it looks like 123456789:AA... and comes from @BotFather")

// ValidToken reports whether s has the BotFather shape.
func ValidToken(s string) bool { return tokenShape.MatchString(strings.TrimSpace(s)) }

// MaskToken shows the bot id and the last four characters, and nothing else.
// Anything that is not a token shape is shown as a bare mask.
func MaskToken(s string) string {
	s = strings.TrimSpace(s)
	if !ValidToken(s) {
		return "(set, not a recognised token shape)"
	}
	id, secret, _ := strings.Cut(s, ":")
	return id + ":…" + secret[len(secret)-4:]
}

// TokenSource says where the token comes from now: "environment", "key
// store" or "" when none is set. The value itself is not returned.
func TokenSource() string {
	if strings.TrimSpace(os.Getenv(TokenEnv)) != "" {
		return "environment"
	}
	if config.Secret("", TokenKey) != "" {
		return "key store"
	}
	return ""
}

// CurrentToken returns the token in effect, for the caller to pass to Enable.
// Callers must not print it.
func CurrentToken() string { return config.Secret(TokenEnv, TokenKey) }

// SaveToken stores token under TokenKey in the key store, keeping the other
// entries. It validates the shape first.
func SaveToken(token string) error {
	token = strings.TrimSpace(token)
	if !ValidToken(token) {
		return ErrBadToken
	}
	return updateKeyStore(func(keys map[string]string) { keys[TokenKey] = token })
}

// ClearToken removes the stored token. It reports whether one was stored.
// An environment variable is not touched and is named in the reply by the
// caller if it is still set.
func ClearToken() (bool, error) {
	had := false
	err := updateKeyStore(func(keys map[string]string) {
		_, had = keys[TokenKey]
		delete(keys, TokenKey)
	})
	return had, err
}

func keyStorePath() string { return filepath.Join(config.Home(), "keys.json") }

// updateKeyStore reads the store, applies edit and writes it back atomically.
// A missing store is empty. Unknown entries are preserved.
func updateKeyStore(edit func(map[string]string)) error {
	path := keyStorePath()
	keys := map[string]string{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read key store: %w", err)
	case len(b) > 0:
		if err := json.Unmarshal(b, &keys); err != nil {
			return fmt.Errorf("key store is not valid JSON: %w", err)
		}
		if keys == nil {
			keys = map[string]string{}
		}
	}
	edit(keys)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create key store dir: %w", err)
	}
	out, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return fmt.Errorf("write key store: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace key store: %w", err)
	}
	return nil
}
