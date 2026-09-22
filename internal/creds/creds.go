// Package creds provisions agent authentication onto targets.
//
// Two supported auth models, in precedence order:
//
//  1. API key (LECTERN_ANTHROPIC_API_KEY) — rotation-proof. Injected as
//     ANTHROPIC_API_KEY into every agent launch; nothing is pushed to targets.
//
//  2. OAuth / subscription (the default) — the control plane holds
//     ~/.claude/.credentials.json, which its own Claude Code keeps refreshed. The
//     subtle failure this package fixes: when the source refreshes, the OAuth
//     REFRESH token rotates, so any copy previously pushed to a target becomes
//     invalid and the target 401s. The fix is to push the CURRENT credentials at
//     dispatch time, so an agent never runs on a stale, rotated-out copy.
package creds

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Spec says where the control plane keeps an agent's credentials and where the
// target expects to find them.
type Spec struct {
	SourcePath string
	HomeDir    string
	Dest       string
}

// Provisioner pushes credentials for the agents that have them.
type Provisioner struct {
	// Specs is keyed by agent name. gemini has no known credential file, so it is
	// absent and provisioning is a no-op for it.
	Specs map[string]Spec
	// APIKey short-circuits everything for claude when set.
	APIKey string
	Log    *slog.Logger
}

// New builds a provisioner from resolved credential paths.
func New(claudePath, codexPath, apiKey string, log *slog.Logger) *Provisioner {
	return &Provisioner{
		Specs: map[string]Spec{
			"claude": {claudePath, "~/.claude", "~/.claude/.credentials.json"},
			"codex":  {codexPath, "~/.codex", "~/.codex/auth.json"},
		},
		APIKey: apiKey,
		Log:    log,
	}
}

// BaseAgentEnv is the auth env injected into every agent launch. An API key wins
// when configured.
func (p *Provisioner) BaseAgentEnv() map[string]string {
	if p.APIKey != "" {
		return map[string]string{"ANTHROPIC_API_KEY": p.APIKey}
	}
	return map[string]string{}
}

// Provision makes sure a target can authenticate for this dispatch.
//
// No-op with an API key (env injection covers it, claude only) or for
// local/mock (which use the control plane's own credentials). Otherwise it
// pushes the control plane's CURRENT credentials for the agent being dispatched
// — codex tokens rotate exactly like claude's, so a remote codex run needs the
// same treatment. Best-effort: a push failure is logged, not fatal, because the
// target may still hold a working copy and the deep probe surfaces real auth
// failures.
func (p *Provisioner) Provision(ctx context.Context, ex executor.Executor, targetKind, targetName, agent string) {
	if agent == "" {
		agent = "claude"
	}
	if agent == "claude" && p.APIKey != "" {
		return
	}
	if targetKind == "local" || targetKind == "mock" {
		return
	}
	spec, ok := p.Specs[agent]
	if !ok {
		return
	}
	raw, err := os.ReadFile(spec.SourcePath)
	if err != nil {
		p.Log.Warn("no control-plane credentials to provision",
			"agent", agent, "path", spec.SourcePath)
		return
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	// ~ expands to the target user's home uniformly across local/ssh/pct
	cmd := fmt.Sprintf("mkdir -p %s && chmod 700 %s && echo %s | base64 -d > %s && chmod 600 %s",
		spec.HomeDir, spec.HomeDir, b64, spec.Dest, spec.Dest)
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		p.Log.Warn("credential provision errored", "agent", agent, "target", targetName, "err", err)
		return
	}
	if !r.OK() {
		p.Log.Warn("credential provision failed", "agent", agent, "target", targetName,
			"stderr", r.Stderr)
	}
}
