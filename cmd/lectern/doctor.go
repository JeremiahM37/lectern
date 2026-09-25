package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/onboard"
)

const doctorHelp = `lectern doctor — checks the things a working install needs.

Reports on tmux, git, agent CLIs and their credentials, the configured port,
the resolved auth mode, TLS, push notification keys, and whether a running
instance answers its own health check — with a suggested fix for anything
that's missing. Exits non-zero if something needs attention.
`

// doctorCheck is one line of the report. Skip means "not applicable /
// not evaluable right now" rather than pass or fail — e.g. TLS when the
// resolved auth mode doesn't call for it, or the live checks when nothing is
// running yet.
type doctorCheck struct {
	Name   string
	OK     bool
	Skip   bool
	Detail string
	Fix    string
}

func check(name string, ok bool, detail, fix string) doctorCheck {
	return doctorCheck{Name: name, OK: ok, Detail: detail, Fix: fix}
}

func skip(name, detail string) doctorCheck {
	return doctorCheck{Name: name, Skip: true, Detail: detail}
}

// renderDoctorReport formats checks and reports whether every evaluated
// (non-skipped) check passed. Pure and deterministic so it can be unit
// tested without touching tmux, git, or the network.
func renderDoctorReport(checks []doctorCheck) (string, bool) {
	var b strings.Builder
	allOK := true
	for _, c := range checks {
		mark := "OK"
		if c.Skip {
			mark = "--"
		} else if !c.OK {
			mark = "FAIL"
			allOK = false
		}
		fmt.Fprintf(&b, "[%s] %s", mark, c.Name)
		if c.Detail != "" {
			fmt.Fprintf(&b, " — %s", c.Detail)
		}
		b.WriteByte('\n')
		if !c.Skip && !c.OK && c.Fix != "" {
			fmt.Fprintf(&b, "       fix: %s\n", c.Fix)
		}
	}
	return b.String(), allOK
}

func agentInstallHint(name string) string {
	switch name {
	case "claude":
		return "install Claude Code (https://docs.claude.com/claude-code) and make sure `claude` is on PATH, or set LECTERN_CLAUDE_BIN"
	case "codex":
		return "install the Codex CLI (https://github.com/openai/codex) and make sure `codex` is on PATH, or set LECTERN_CODEX_BIN"
	case "gemini":
		return "install the Gemini CLI and make sure `gemini` is on PATH, or set LECTERN_GEMINI_BIN"
	default:
		return fmt.Sprintf("install %s and make sure it's on PATH, or configure a custom agent (see docs/agents.md) pointing at wherever it lives", name)
	}
}

func agentCredHint(path string) (ok bool, detail string) {
	if _, err := os.Stat(path); err == nil {
		return true, path
	}
	return false, "no credentials at " + path
}

func doctorCommand(cfg *config.Config, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Print(doctorHelp)
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: lectern doctor")
	}
	var checks []doctorCheck

	tmux := onboard.CheckTmux()
	checks = append(checks, check("tmux", tmux.OK, tmux.Detail, tmux.Fix))
	git := onboard.CheckGit()
	checks = append(checks, check("git", git.OK, git.Detail, git.Fix))

	for _, a := range onboard.DetectAgents(cfg.ClaudeBin, cfg.CodexBin, cfg.GeminiBin) {
		detail := a.Path
		if !a.Found {
			detail = "not found on PATH"
		}
		checks = append(checks, check("agent: "+a.Name, a.Found, detail, agentInstallHint(a.Name)))
	}
	claudeCredsOK, claudeCredsDetail := agentCredHint(cfg.ClaudeCredsPath)
	checks = append(checks, check("claude credentials", claudeCredsOK, claudeCredsDetail, "run `claude` once and sign in, or set LECTERN_ANTHROPIC_API_KEY"))
	codexCredsOK, codexCredsDetail := agentCredHint(cfg.CodexCredsPath)
	checks = append(checks, check("codex credentials", codexCredsOK, codexCredsDetail, "run `codex` once and sign in"))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := auth.New(auth.Settings{
		Mode: cfg.Auth, Host: cfg.Host, Token: cfg.AuthToken,
		Socket: cfg.TailscaleSocket, AllowedUsersCSV: cfg.TailscaleUsers,
		AllowedTagsCSV: cfg.TailscaleTags, TrustServeHeaders: cfg.TrustServeHeaders,
	}, log)
	checks = append(checks, check("auth mode", true, string(resolver.Mode)+" (LECTERN_AUTH="+envOrAuto(cfg.Auth)+")", ""))

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = conn.Close()
		checks = append(checks, check("port "+strconv.Itoa(cfg.Port), true, "something is already listening on "+addr, ""))
	} else {
		checks = append(checks, skip("port "+strconv.Itoa(cfg.Port), "nothing listening yet — `lectern serve` or `lectern up` will bind it"))
	}

	if resolver.Mode == auth.ModeTailscale && cfg.TLSPort <= 0 {
		checks = append(checks, check("TLS", false, "auth mode is tailscale but LECTERN_TLS_PORT is not set", "set LECTERN_TLS_PORT=8443 for a secure context (needed for push notifications and PWA install)"))
	} else if resolver.Mode == auth.ModeTailscale {
		checks = append(checks, check("TLS", true, fmt.Sprintf("configured on port %d", cfg.TLSPort), ""))
	} else {
		checks = append(checks, skip("TLS", "not applicable — auth mode is "+string(resolver.Mode)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base, token := liveEndpoint(ctx, cfg)
	if base == "" {
		checks = append(checks, skip("push keys", "no running instance found — `lectern up` provisions them on first start"))
		checks = append(checks, skip("hook reachability", "no running instance found — start one with `lectern up` or `lectern serve`"))
	} else {
		c := console.New(base, token)
		if _, err := c.JSON("GET", "/push/vapid-key", nil); err != nil {
			checks = append(checks, check("push keys", false, err.Error(), "push keys are generated automatically the first time Lectern starts with a database; restart it if this persists"))
		} else {
			checks = append(checks, check("push keys", true, "configured", ""))
		}
		if _, err := c.JSON("GET", "/health", nil); err != nil {
			checks = append(checks, check("hook reachability", false, err.Error(), "the running instance did not answer its own health check over loopback — check it's still alive"))
		} else {
			checks = append(checks, check("hook reachability", true, base, ""))
		}
	}

	report, ok := renderDoctorReport(checks)
	fmt.Print(report)
	if !ok {
		os.Exit(1)
	}
	return nil
}

func envOrAuto(mode string) string {
	if mode == "" {
		return "auto"
	}
	return mode
}

// liveEndpoint finds something to run the live checks against: an explicit
// remote server, or an already-running local runtime. It never starts one —
// doctor diagnoses, it doesn't launch background processes.
func liveEndpoint(ctx context.Context, cfg *config.Config) (base, token string) {
	if v := os.Getenv("LECTERN_API"); v != "" {
		return v, cfg.AuthToken
	}
	if ep, ok := localruntime.Peek(ctx); ok {
		return ep.URL, ep.Token
	}
	return "", ""
}
