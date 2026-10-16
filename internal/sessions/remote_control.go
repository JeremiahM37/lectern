package sessions

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/isolation"
)

// Claude Code Remote Control (docs/remote-control.md) makes an interactive
// claude session reachable from claude.ai/code and the Claude app as well. It
// is the same process: the terminal, send_to_session, approvals and restore
// keep working, and Remote Control is one more window onto it.

// SupportsRemoteControl is true for the built-in claude agent only. A custom
// agent can wrap any binary, so Lectern cannot know that it is Claude Code
// signed in with a claude.ai subscription.
func (s Spec) SupportsRemoteControl() bool {
	return s.Name == "claude" && s.Builtin
}

// remoteControlBlockingEnv are the variables with which Claude Code refuses
// Remote Control: another way of authenticating, or traffic it needs turned off.
var remoteControlBlockingEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_GROWTHBOOK",
}

// RemoteControlUnavailable says why a launch with this spec, environment and
// sandbox cannot use Remote Control, or "" when it can. The reason names the
// setting so the person knows what to change.
func RemoteControlUnavailable(spec Spec, env map[string]string, iso isolation.Config) string {
	if !spec.SupportsRemoteControl() {
		return "Remote Control is only available for the built-in claude agent"
	}
	for _, k := range remoteControlBlockingEnv {
		if strings.TrimSpace(env[k]) != "" {
			return "Remote Control needs a claude.ai login, and this session sets " + k
		}
	}
	if base := strings.TrimSpace(env["ANTHROPIC_BASE_URL"]); base != "" {
		if u, err := url.Parse(base); err != nil || u.Hostname() != "api.anthropic.com" {
			return "Remote Control needs Anthropic's own API, and this session sets ANTHROPIC_BASE_URL"
		}
	}
	if iso.Normalized().Mode != isolation.None {
		return "Remote Control is not available in a sandboxed session"
	}
	return ""
}

// RemoteControlName is the title the session gets on claude.ai: its Lectern
// name on one line, or "lectern" when it has none.
func RemoteControlName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	name = strings.TrimRight(strings.TrimSpace(name), "…")
	if r := []rune(name); len(r) > 80 {
		name = strings.TrimSpace(string(r[:80]))
	}
	if name == "" {
		return "lectern"
	}
	return name
}

var remoteControlURLPattern = regexp.MustCompile(`https://claude\.ai/code/session_[A-Za-z0-9]+`)

// RemoteControlURL is the last claude.ai session link on a screen, or "".
func RemoteControlURL(pane string) string {
	all := remoteControlURLPattern.FindAllString(pane, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// remoteControlConsentShown recognises Claude Code's one-time "Enable Remote
// Control" callout, which a running session shows the first time
// /remote-control is used in a config directory.
func remoteControlConsentShown(pane string) bool {
	return strings.Contains(pane, "Enable Remote Control") && strings.Contains(pane, "Never mind")
}

// remoteControlActive recognises the line claude prints once it is connected.
func remoteControlActive(pane string) bool {
	return strings.Contains(pane, "/remote-control is active") || RemoteControlURL(pane) != ""
}

func remoteControlName(on bool, name string) string {
	if !on {
		return ""
	}
	return RemoteControlName(name)
}
