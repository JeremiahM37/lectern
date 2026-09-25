// Package triggers lets a project pick up work on its own — from a labelled
// GitHub issue, an @mention in a PR/issue comment, a Slack message, a
// /lectern slash command, or a labelled Linear issue — the way Devin, Cursor,
// Codex, Tembo and Charlie already do. Lectern itself has no equivalent until
// this package: dispatch was always a human pressing a button, or a routine
// on a timer.
//
// Design constraint that shapes everything here: lectern is typically reachable
// only on a private tailnet (see docs/triggers.md), so inbound webhooks from
// GitHub/Slack/Linear usually cannot reach it. GitHub and Linear are polled
// (outbound only); Slack uses Socket Mode, an outbound websocket that carries
// events, slash commands and interactivity without exposing any port. Nothing
// here listens for an inbound webhook.
package triggers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Kind is one of the three sources this package knows how to watch.
type Kind string

const (
	KindGitHub Kind = "github"
	KindSlack  Kind = "slack"
	KindLinear Kind = "linear"
)

func (k Kind) valid() bool { return k == KindGitHub || k == KindSlack || k == KindLinear }

// defaultMaxPerHour is the rate limit applied when a source's config leaves
// max_per_hour unset — enough for active development, not enough for a
// runaway loop (a mis-scoped label match, a bot replying to itself) to spend
// unboundedly before a human notices.
const defaultMaxPerHour = 10

// defaultPollInterval matches routines' own floor (internal/routines
// ParseSchedule refuses under 5m) for the same reason: this dispatches real
// agents, so polling every few seconds is never intentional.
const defaultPollInterval = 300

// GitHubConfig is trigger_sources.config_json for kind "github". Auth is
// never stored here: GitHub polling and posting back both run through the
// project's own target executor and its `gh` CLI login, exactly like the
// existing `gh pr create` task/session commit path (internal/api/session_review.go)
// — one fewer credential to leak, and it inherits whatever repo access that
// machine's gh is already scoped to.
type GitHubConfig struct {
	// Repo is "owner/repo". Required.
	Repo string `json:"repo"`
	// Label is the issue label that creates a task. Default "lectern".
	Label string `json:"label"`
	// QueuedLabel replaces Label once a task has been filed, so the same
	// issue is never picked up twice even across a dedup-ledger loss.
	// Default "lectern:queued".
	QueuedLabel string `json:"queued_label"`
	// MentionHandle triggers a follow-up task from a PR/issue comment.
	// Default "@lectern".
	MentionHandle string `json:"mention_handle"`
	// AllowedAuthors is the GitHub logins allowed to trigger work. Empty
	// means nobody can — see Manager.checkAllowlist.
	AllowedAuthors []string `json:"allowed_authors"`
	BaseBranch     string   `json:"base_branch"`
	Agent          string   `json:"agent"`
	Model          string   `json:"model"`
	MaxPerHour     int      `json:"max_per_hour"`
}

func (c *GitHubConfig) setDefaults() {
	if c.Label == "" {
		c.Label = "lectern"
	}
	if c.QueuedLabel == "" {
		c.QueuedLabel = c.Label + ":queued"
	}
	if c.MentionHandle == "" {
		c.MentionHandle = "@lectern"
	}
	if c.MaxPerHour <= 0 {
		c.MaxPerHour = defaultMaxPerHour
	}
}

func (c GitHubConfig) validate() error {
	if !strings.Contains(strings.TrimSpace(c.Repo), "/") {
		return fmt.Errorf(`github source needs "repo" as "owner/repo"`)
	}
	return nil
}

// ParseGitHubConfig reads a source's config_json, filling in defaults.
func ParseGitHubConfig(raw string) (GitHubConfig, error) {
	var c GitHubConfig
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, fmt.Errorf("bad github config: %w", err)
		}
	}
	c.setDefaults()
	return c, nil
}

// SlackConfig is trigger_sources.config_json for kind "slack".
type SlackConfig struct {
	// Channel restricts which channel is watched; empty means any channel
	// the bot has been invited to.
	Channel      string   `json:"channel"`
	AllowedUsers []string `json:"allowed_users"`
	Agent        string   `json:"agent"`
	Model        string   `json:"model"`
	MaxPerHour   int      `json:"max_per_hour"`
}

func (c *SlackConfig) setDefaults() {
	if c.MaxPerHour <= 0 {
		c.MaxPerHour = defaultMaxPerHour
	}
}

// ParseSlackConfig reads a source's config_json, filling in defaults.
func ParseSlackConfig(raw string) (SlackConfig, error) {
	var c SlackConfig
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, fmt.Errorf("bad slack config: %w", err)
		}
	}
	c.setDefaults()
	return c, nil
}

// SlackSecrets is trigger_sources.secrets_json for kind "slack".
type SlackSecrets struct {
	// AppToken is the app-level token (xapp-...) Socket Mode connects with.
	AppToken string `json:"app_token"`
	// BotToken (xoxb-...) posts messages and reads channel/user info.
	BotToken string `json:"bot_token"`
}

func ParseSlackSecrets(raw string) (SlackSecrets, error) {
	var s SlackSecrets
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return s, fmt.Errorf("bad slack secrets: %w", err)
		}
	}
	return s, nil
}

func (s SlackSecrets) validate() error {
	if !strings.HasPrefix(s.AppToken, "xapp-") {
		return fmt.Errorf("slack app_token must be an app-level token (xapp-...)")
	}
	if !strings.HasPrefix(s.BotToken, "xoxb-") {
		return fmt.Errorf("slack bot_token must be a bot token (xoxb-...)")
	}
	return nil
}

// LinearConfig is trigger_sources.config_json for kind "linear".
type LinearConfig struct {
	// TeamKey is the Linear team's short key, e.g. "ENG". Required.
	TeamKey string `json:"team_key"`
	// Label is the issue label that creates a task. Default "lectern".
	Label string `json:"label"`
	// AllowedUsers is the Linear emails or display names allowed to trigger
	// work (an issue's creator or assignee). Empty means nobody can.
	AllowedUsers []string `json:"allowed_users"`
	// DoneStateName is the workflow state a Linear issue moves to when its
	// task completes. Default "Done".
	DoneStateName string `json:"done_state_name"`
	Agent         string `json:"agent"`
	Model         string `json:"model"`
	MaxPerHour    int    `json:"max_per_hour"`
}

func (c *LinearConfig) setDefaults() {
	if c.Label == "" {
		c.Label = "lectern"
	}
	if c.DoneStateName == "" {
		c.DoneStateName = "Done"
	}
	if c.MaxPerHour <= 0 {
		c.MaxPerHour = defaultMaxPerHour
	}
}

func (c LinearConfig) validate() error {
	if strings.TrimSpace(c.TeamKey) == "" {
		return fmt.Errorf("linear source needs a team_key")
	}
	return nil
}

// ParseLinearConfig reads a source's config_json, filling in defaults.
func ParseLinearConfig(raw string) (LinearConfig, error) {
	var c LinearConfig
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return c, fmt.Errorf("bad linear config: %w", err)
		}
	}
	c.setDefaults()
	return c, nil
}

// LinearSecrets is trigger_sources.secrets_json for kind "linear".
type LinearSecrets struct {
	APIKey string `json:"api_key"`
}

func ParseLinearSecrets(raw string) (LinearSecrets, error) {
	var s LinearSecrets
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return s, fmt.Errorf("bad linear secrets: %w", err)
		}
	}
	return s, nil
}

func (s LinearSecrets) validate() error {
	if strings.TrimSpace(s.APIKey) == "" {
		return fmt.Errorf("linear source needs an api_key")
	}
	return nil
}

// ValidateConfig checks one source's config/secrets before it is saved,
// dispatching on kind. It never mutates — callers apply defaults themselves
// via the Parse* helpers when they build a source to save.
func ValidateConfig(kind Kind, configJSON, secretsJSON string) error {
	switch kind {
	case KindGitHub:
		c, err := ParseGitHubConfig(configJSON)
		if err != nil {
			return err
		}
		return c.validate()
	case KindSlack:
		c, err := ParseSlackConfig(configJSON)
		if err != nil {
			return err
		}
		_ = c
		s, err := ParseSlackSecrets(secretsJSON)
		if err != nil {
			return err
		}
		return s.validate()
	case KindLinear:
		c, err := ParseLinearConfig(configJSON)
		if err != nil {
			return err
		}
		if err := c.validate(); err != nil {
			return err
		}
		s, err := ParseLinearSecrets(secretsJSON)
		if err != nil {
			return err
		}
		return s.validate()
	default:
		return fmt.Errorf("unknown trigger kind %q; use github, slack or linear", kind)
	}
}

// RedactSecrets returns a JSON object with every secret value replaced by a
// fixed placeholder when present, so the API can show "configured / not" per
// field without ever round-tripping the real value to a browser. Field names
// are read generically (works for both SlackSecrets and LinearSecrets) so
// this needs no per-kind branch.
func RedactSecrets(secretsJSON string) map[string]bool {
	raw := map[string]any{}
	if secretsJSON != "" {
		_ = json.Unmarshal([]byte(secretsJSON), &raw)
	}
	out := map[string]bool{}
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = strings.TrimSpace(s) != ""
		} else {
			out[k] = v != nil
		}
	}
	return out
}
