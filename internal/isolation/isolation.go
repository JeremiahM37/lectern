// Package isolation builds the per-agent-process sandbox a session or task
// attempt can run inside, as a fast, no-clone alternative to the full LXC
// clone in internal/sandbox: bubblewrap (no daemon, Linux only) or a
// disposable Docker container.
//
// internal/sandbox's "container IS the isolation" tier stays the strongest
// guarantee lectern offers — a whole disposable machine. This package is the
// tier competitors actually ship for every agent turn (agent-deck, Sculptor:
// one Docker container per agent; Codex/Cursor: a microVM): sub-second to
// start, filesystem-scoped, with network egress either left alone or routed
// through an allowlist. See docs/isolation.md for the threat model this
// buys and, just as importantly, does not.
package isolation

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
)

// Mode selects how an agent process is contained. The zero value, None, is
// today's behaviour: the agent runs directly on the target, unsandboxed.
type Mode string

const (
	None   Mode = ""
	Bwrap  Mode = "bwrap"
	Docker Mode = "docker"
)

// NetworkPolicy controls egress once a session is isolated.
type NetworkPolicy string

const (
	// NetworkAllow leaves networking exactly as the target already has it —
	// the same as an unisolated launch. This is the default: most agents are
	// useless without their model API.
	NetworkAllow NetworkPolicy = "allow"
	// NetworkDeny routes every request through Lectern's own allowlist proxy
	// (proxy.go). For bwrap this is real, kernel-enforced network denial
	// (--unshare-net) with only the bridged proxy path left open, and it
	// requires a local target — see ValidateForTarget and docs/isolation.md.
	// Docker's network=deny is `--network none`: no proxy bridge exists for
	// Docker in this release, so a denied Docker session has no network at
	// all, including its own model API. That is a deliberate, documented v1
	// limit, not an oversight.
	NetworkDeny NetworkPolicy = "deny"
)

// Modes lists every valid Mode, for validation and the UI's picker.
var Modes = []Mode{None, Bwrap, Docker}

// Networks lists every valid NetworkPolicy.
var Networks = []NetworkPolicy{NetworkAllow, NetworkDeny}

// DefaultDockerImage is used when a Docker isolation config names none. It
// only needs a shell; the agent binary itself is expected to reach the
// container through the workdir/config mounts (a project that wants its own
// toolchain baked in sets docker_image explicitly).
const DefaultDockerImage = "debian:bookworm-slim"

// Config is one session or task's isolation choice. It is small and JSON
// stable on purpose: it rides inside sessions.LaunchConfiguration and
// agents.TaskLaunchConfig so a resume/relaunch reproduces it exactly, and
// inside store.Project as that project's default for new launches.
type Config struct {
	Mode    Mode          `json:"mode,omitempty"`
	Network NetworkPolicy `json:"network,omitempty"`
	// DockerImage is only meaningful when Mode is Docker.
	DockerImage string `json:"docker_image,omitempty"`
	// AllowHosts are additional hostnames the egress proxy accepts CONNECT
	// and forwarded requests for when Network is deny, layered on top of
	// DefaultAllowHosts. Ignored otherwise.
	AllowHosts []string `json:"allow_hosts,omitempty"`
}

// Normalized fills in defaults and clears fields that mean nothing for the
// chosen mode, so two configs that behave identically also compare equal.
func (c Config) Normalized() Config {
	if c.Mode != Bwrap && c.Mode != Docker {
		return Config{}
	}
	if c.Network != NetworkDeny {
		c.Network = NetworkAllow
	}
	if c.Mode == Docker {
		if c.DockerImage == "" {
			c.DockerImage = DefaultDockerImage
		}
	} else {
		c.DockerImage = ""
	}
	if c.Network != NetworkDeny {
		c.AllowHosts = nil
	}
	return c
}

// Validate rejects a config this package cannot honestly build a wrapper
// for, independent of which target it would run on.
func (c Config) Validate() error {
	switch c.Mode {
	case None, Bwrap, Docker:
	default:
		return fmt.Errorf("isolation mode must be one of %v", Modes)
	}
	if c.Mode == None {
		return nil
	}
	switch c.Network {
	case "", NetworkAllow, NetworkDeny:
	default:
		return fmt.Errorf("isolation network must be one of %v", Networks)
	}
	return nil
}

// ValidateForTarget additionally rejects combinations that are only
// deployable on the machine lectern's own control process runs on. Network
// deny needs its allowlist proxy reachable by an operator-local unix-domain
// socket (bwrap) or `--network none` (docker); neither of those crosses a
// network to an ssh/pct target. Isolation on a "sandbox" target is also
// rejected — that target IS a disposable container already; wrapping it
// again buys nothing and only adds friction. See docs/isolation.md.
func ValidateForTarget(c Config, targetKind string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	n := c.Normalized()
	if n.Mode == None {
		return nil
	}
	if targetKind == "sandbox" {
		return fmt.Errorf("isolation is redundant on a sandbox target — the cloned container is already the isolation boundary")
	}
	if n.Mode == Bwrap && targetKind != "local" && targetKind != "ssh" {
		return fmt.Errorf("bwrap isolation needs a local or ssh target (this target is %q)", targetKind)
	}
	if n.Network == NetworkDeny && targetKind != "local" {
		return fmt.Errorf("isolation network=deny requires a local target (this target is %q); the allowlist proxy only exists on the machine lectern itself runs on — use network=allow, or mode=none", targetKind)
	}
	return nil
}

// AgentHomePaths is the explicit, per-agent list of $HOME entries an agent
// keeps its own login/auth/config in, bound read-write into the sandbox so
// an already-authenticated CLI keeps working and any token refresh persists.
// This is deliberately a fixed, reviewed list rather than "the whole home
// directory": that is the entire point of the sandbox. A custom agent name
// not listed here gets no extra bind — see docs/isolation.md.
var AgentHomePaths = map[string][]string{
	"claude": {".claude", ".claude.json"},
	"codex":  {".codex"},
	"gemini": {".gemini", ".config/gemini"},
}

// HomePaths returns agent's explicit list of $HOME entries, or nil for an
// agent this package does not know the config layout of.
func HomePaths(agent string) []string {
	return AgentHomePaths[agent]
}

// DefaultAllowHosts is the baseline egress allowlist under network=deny,
// before any project-specific AllowHosts: this agent's own model API, the
// package registries an agent-driven build commonly reaches for, and —
// whenever hookURL is non-empty — the Lectern host that session's own hooks
// call back to. Omitting that last one silently kills the statusline and
// Stop/UserPromptSubmit events for every isolated session (docs/agent-events.md).
func DefaultAllowHosts(agent, hookURL string) []string {
	hosts := map[string]bool{
		"registry.npmjs.org":            true,
		"pypi.org":                      true,
		"files.pythonhosted.org":        true,
		"github.com":                    true,
		"objects.githubusercontent.com": true,
		"raw.githubusercontent.com":     true,
		"codeload.github.com":           true,
	}
	switch agent {
	case "claude":
		hosts["api.anthropic.com"] = true
	case "codex":
		hosts["api.openai.com"] = true
		hosts["chatgpt.com"] = true
	case "gemini":
		hosts["generativelanguage.googleapis.com"] = true
	}
	if hookURL != "" {
		if u, err := url.Parse(hookURL); err == nil && u.Host != "" {
			if h, _, err := net.SplitHostPort(u.Host); err == nil {
				hosts[h] = true
			} else {
				hosts[u.Host] = true
			}
		}
	}
	out := make([]string, 0, len(hosts))
	for h := range hosts {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// homeJoin renders a $HOME-relative path as a double-quoted shell token so
// bash expands $HOME at run time on whichever machine the command actually
// executes on — this package never assumes it knows the target's home
// directory, local or remote.
func homeJoin(rel string) string {
	if rel == "" {
		return `"$HOME"`
	}
	return `"$HOME/` + filepath.ToSlash(rel) + `"`
}
