package telegram

// Host-side management of the chat allowlist: allow a chat by id, forget a
// paired chat and list who is paired. These work whether or not the surface
// is on; when it is off they go to the state file directly.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
)

// ChatInfo is one allowed chat. The id is shown on the host; it is not a
// secret, but it is not sent anywhere either.
type ChatInfo struct {
	ID     string
	Config bool // listed in the config, so it cannot be forgotten here
}

// ErrBadChatID means the argument is not a Telegram chat id.
var ErrBadChatID = errors.New("a Telegram chat id is a number, such as 123456789 (a group id starts with -)")

// allowlist returns the live allowlist when the surface is on, or a fresh
// one over the state file otherwise.
func (s *Surface) allowlist() (*remote.Allowlist, error) {
	s.mu.Lock()
	bot := s.bot
	s.mu.Unlock()
	if bot != nil {
		return bot.allow, nil
	}
	rc, err := config.LoadRemote()
	if err != nil {
		return nil, err
	}
	var static []string
	if rc.Telegram != nil {
		for _, id := range rc.Telegram.ChatIDs {
			static = append(static, strconv.FormatInt(id, 10))
		}
	}
	return remote.NewAllowlist(StatePath(), static)
}

// ParseChatID checks that s is a chat id and returns it in canonical form.
func ParseChatID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if _, err := strconv.ParseInt(s, 10, 64); err != nil || s == "" || s == "0" {
		return "", ErrBadChatID
	}
	return s, nil
}

// Allow pairs chat id without a code. It is an operator action on the host,
// so it does not need the phone to pair first.
func (s *Surface) Allow(chat string) (string, error) {
	id, err := ParseChatID(chat)
	if err != nil {
		return "", err
	}
	al, err := s.allowlist()
	if err != nil {
		return "", err
	}
	if err := al.Add(id); err != nil {
		return "", fmt.Errorf("could not save the chat: %w", err)
	}
	return id, nil
}

// Forget unpairs one chat. It reports whether the chat was paired. A chat
// listed in the config returns remote.ErrStaticChat and stays.
func (s *Surface) Forget(chat string) (bool, error) {
	id, err := ParseChatID(chat)
	if err != nil {
		return false, err
	}
	al, err := s.allowlist()
	if err != nil {
		return false, err
	}
	return al.Remove(id)
}

// Pairings lists every allowed chat, config chats included.
func (s *Surface) Pairings() ([]ChatInfo, error) {
	al, err := s.allowlist()
	if err != nil {
		return nil, err
	}
	var out []ChatInfo
	for _, id := range al.Members() {
		out = append(out, ChatInfo{ID: id, Config: al.IsStatic(id)})
	}
	return out, nil
}
