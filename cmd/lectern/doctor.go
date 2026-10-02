package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/browser"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/onboard"
)

// doctorCheck is one line of the report. Skip means "not applicable /
// not evaluable right now" rather than pass or fail — e.g. TLS when the
// resolved auth mode doesn't call for it, or an optional agent that isn't
// installed. Warn is a problem worth fixing that does not stop Lectern
// working: it prints its fix but never fails the report.
type doctorCheck struct {
	Name   string
	OK     bool
	Skip   bool
	Warn   bool
	Detail string
	Fix    string
}

func check(name string, ok bool, detail, fix string) doctorCheck {
	return doctorCheck{Name: name, OK: ok, Detail: detail, Fix: fix}
}

func skip(name, detail string) doctorCheck {
	return doctorCheck{Name: name, Skip: true, Detail: detail}
}

// warn is check for something optional: OK when ok, else a warning.
func warn(name string, ok bool, detail, fix string) doctorCheck {
	return doctorCheck{Name: name, OK: ok, Warn: !ok, Detail: detail, Fix: fix}
}

// renderDoctorReport formats checks and reports whether every evaluated
// (non-skipped, non-warning) check passed. Pure and deterministic so it can
// be unit tested without touching tmux, git, or the network.
func renderDoctorReport(checks []doctorCheck) (string, bool) {
	var b strings.Builder
	allOK := true
	for _, c := range checks {
		mark := "OK"
		switch {
		case c.Skip:
			mark = "--"
		case c.OK:
		case c.Warn:
			mark = "WARN"
		default:
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
	if allOK {
		b.WriteString("\nLectern can run agents here.")
		if hasWarnings(checks) {
			b.WriteString(" The WARN lines above are optional.")
		}
		b.WriteByte('\n')
	} else {
		b.WriteString("\nFix the FAIL lines above, then run lectern doctor again.\n")
	}
	return b.String(), allOK
}

func hasWarnings(checks []doctorCheck) bool {
	for _, c := range checks {
		if c.Warn && !c.OK {
			return true
		}
	}
	return false
}

// agentChecks: every builtin agent that is installed is OK and one that is
// not is optional; only having none at all fails, since then nothing can
// run. Credentials are only looked for beside an installed agent, and are a
// warning: a CLI can sign in other ways (the macOS keychain, an API key).
func agentChecks(cfg *config.Config, agents []onboard.AgentCheck, stat func(string) error) []doctorCheck {
	var out []doctorCheck
	found := 0
	for _, a := range agents {
		if a.Found {
			found++
			out = append(out, check("agent: "+a.Name, true, a.Path, ""))
			continue
		}
		out = append(out, skip("agent: "+a.Name, "not installed (optional)"))
	}
	if found == 0 {
		out = append(out, check("an agent to run", false, "no agent CLI found on PATH",
			"install one, for example Claude Code: npm install -g @anthropic-ai/claude-code (then run `claude` once to sign in), and run lectern up again"))
	}
	creds := map[string][2]string{
		"claude": {cfg.ClaudeCredsPath, "run `claude` once and sign in, or set LECTERN_ANTHROPIC_API_KEY"},
		"codex":  {cfg.CodexCredsPath, "run `codex` once and sign in"},
	}
	for _, a := range agents {
		c, ok := creds[a.Name]
		if !a.Found || !ok {
			continue
		}
		if a.Name == "claude" && (cfg.AnthropicAPIKey != "" || os.Getenv("ANTHROPIC_API_KEY") != "") {
			out = append(out, check("claude sign-in", true, "API key set", ""))
			continue
		}
		if err := stat(c[0]); err == nil {
			out = append(out, check(a.Name+" sign-in", true, c[0], ""))
		} else {
			out = append(out, warn(a.Name+" sign-in", false, "no saved sign-in at "+c[0]+" (fine if it signs in another way)", c[1]))
		}
	}
	return out
}

func agentInstallHint(name string) string { return onboard.InstallHint(name) }

func doctorCommand(cfg *config.Config, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printCommandHelp(os.Stdout, "doctor")
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: lectern doctor")
	}
	var checks []doctorCheck

	python := onboard.CheckPython()
	checks = append(checks, check("python3", python.OK, python.Detail, python.Fix))
	tmux := onboard.CheckTmux()
	checks = append(checks, check("tmux", tmux.OK, tmux.Detail, tmux.Fix))
	git := onboard.CheckGit()
	checks = append(checks, check("git", git.OK, git.Detail, git.Fix))

	agents := onboard.DetectAgents(cfg.ClaudeBin, cfg.CodexBin, cfg.GeminiBin)
	checks = append(checks, agentChecks(cfg, agents, func(p string) error { _, err := os.Stat(p); return err })...)

	// The Browser pane and the agent browser tools need a Chromium-family
	// browser on the machine they run on; everything else works without one.
	bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
	bin, version, berr := browser.Find(bctx, func(ctx context.Context, script string) (string, error) {
		out, err := exec.CommandContext(ctx, "sh", "-c", script).Output()
		return string(out), err
	})
	bcancel()
	if berr != nil {
		checks = append(checks, skip("session browser", "no Chromium or Chrome here — the session browser needs one on the machine it runs on "+
			"(install chromium, run `npx playwright install chromium`, or set LECTERN_BROWSER_BIN)"))
	} else {
		checks = append(checks, check("session browser", true, bin+" ("+version+") — available for agent browser tools; opening your desktop browser is separate", ""))
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := auth.New(auth.Settings{
		Mode: cfg.Auth, Host: cfg.Host, Token: cfg.AuthToken,
		Socket: cfg.TailscaleSocket, AllowedUsersCSV: cfg.TailscaleUsers,
		AllowedTagsCSV: cfg.TailscaleTags, TrustServeHeaders: cfg.TrustServeHeaders,
	}, log)
	checks = append(checks, check("sign-in for lectern serve", true, string(resolver.Mode)+" (LECTERN_AUTH="+envOrAuto(cfg.Auth)+"); your private Lectern always uses its own", ""))

	// Only `lectern serve` (a Lectern service) uses this port; `lectern up`
	// picks a free one of its own.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	serving := false
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = conn.Close()
		serving = true
		checks = append(checks, check("port "+strconv.Itoa(cfg.Port), true, "a server is listening (see \"server\" below)", ""))
	} else {
		checks = append(checks, skip("port "+strconv.Itoa(cfg.Port), "free — only `lectern serve` uses it; `lectern up` picks its own port"))
	}

	if !serving {
		checks = append(checks, skip("TLS", "only for lectern serve, which is not running"))
	} else if resolver.Mode == auth.ModeTailscale && cfg.TLSPort <= 0 {
		checks = append(checks, warn("TLS", false, "auth mode is tailscale but LECTERN_TLS_PORT is not set", "set LECTERN_TLS_PORT=8443 for a secure context (needed for push notifications and PWA install)"))
	} else if resolver.Mode == auth.ModeTailscale {
		checks = append(checks, check("TLS", true, fmt.Sprintf("configured on port %d", cfg.TLSPort), ""))
	} else {
		checks = append(checks, skip("TLS", "not applicable — auth mode is "+string(resolver.Mode)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	explicit := os.Getenv("LECTERN_API")
	var choice serverChoice
	if explicit == "" {
		choice = chooseServer(ctx, cfg)
	}
	checks = append(checks, serverChecks(explicit, choice, localStatus(ctx))...)
	base, token := liveEndpoint(ctx, cfg, choice)
	if base == "" {
		checks = append(checks, skip("phone alerts (push keys)", "nothing running yet — `lectern up` provisions them on first start"))
		checks = append(checks, skip("agent hooks", "nothing running yet — start it with `lectern up`, then run doctor again"))
	} else {
		c := console.New(base, token)
		if _, err := c.JSON("GET", "/push/vapid", nil); err != nil {
			checks = append(checks, warn("phone alerts (push keys)", false, err.Error(), "push keys are generated automatically the first time Lectern starts with a database; restart it if this persists"))
		} else {
			checks = append(checks, check("phone alerts (push keys)", true, "configured", ""))
		}
		checks = append(checks, hookRoundTrip(c))
		checks = append(checks, serverSeesAgents(c, agents)...)
	}

	report, ok := renderDoctorReport(checks)
	fmt.Print(report)
	if !ok {
		os.Exit(1)
	}
	return nil
}

// hookRoundTrip asks the server to call the hook address it gives sessions
// and confirm that it is the one answering. Recomputing the address here
// would only repeat the server's own mistake if it has one.
func hookRoundTrip(c *console.Client) doctorCheck {
	const name = "agent hooks"
	data, err := c.JSON("GET", "/diagnostics/hooks", nil)
	if he, ok := err.(*console.HTTPError); ok && he.Status == 404 {
		return skip(name, "the running server is too old to check; update it and run doctor again")
	}
	if err != nil {
		return check(name, false, err.Error(), "the running server did not answer; check it is still alive (`lectern local status`)")
	}
	var got struct {
		HookBase string `json:"hook_base"`
		OK       bool   `json:"ok"`
		Detail   string `json:"detail"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		return check(name, false, err.Error(), "")
	}
	return check(name, got.OK, got.Detail,
		"agents cannot report status or ask for approval; unset LECTERN_HOOK_BASE if you set it, or set it to an address that reaches this server, then restart it (`lectern local stop`)")
}

// serverSeesAgents catches a server started before an agent was installed:
// it looks agents up on its own PATH, so it cannot start one it cannot see.
func serverSeesAgents(c *console.Client, local []onboard.AgentCheck) []doctorCheck {
	data, err := c.JSON("GET", "/onboarding", nil)
	if err != nil {
		return nil
	}
	var remote struct {
		Agents []onboard.AgentCheck `json:"agents"`
	}
	if json.Unmarshal(data, &remote) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, a := range remote.Agents {
		seen[a.Name] = a.Found
	}
	var missing []string
	for _, a := range local {
		if a.Found && a.Builtin && !seen[a.Name] {
			missing = append(missing, a.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []doctorCheck{warn("server's agents", false, "the running Lectern cannot see "+strings.Join(missing, ", ")+" (it started before they were installed, or with another PATH)",
		"run lectern up: it hands your PATH to your private Lectern")}
}

func envOrAuto(mode string) string {
	if mode == "" {
		return "auto"
	}
	return mode
}

// liveEndpoint finds something to run the live checks against: the server
// plain client commands would use — an explicit LECTERN_API, a Lectern
// service on this machine, or an already-running local runtime. It never
// starts one; doctor diagnoses, it doesn't launch background processes.
func liveEndpoint(ctx context.Context, cfg *config.Config, choice serverChoice) (base, token string) {
	if v := os.Getenv("LECTERN_API"); v != "" {
		return v, cfg.AuthToken
	}
	if choice.Hosted != nil {
		return choice.Hosted.URL, cfg.AuthToken
	}
	if ep, ok := localruntime.Peek(ctx); ok {
		return ep.URL, ep.Token
	}
	return "", ""
}

func localStatus(ctx context.Context) localruntime.Status {
	status, err := localruntime.StatusOf(ctx)
	if err != nil {
		return localruntime.Status{State: "unknown", Detail: err.Error()}
	}
	return status
}

// serverChecks reports which server plain `lectern` commands reach and the
// state of the private local runtime. Pure, for tests.
func serverChecks(explicit string, choice serverChoice, local localruntime.Status) []doctorCheck {
	var out []doctorCheck
	localChosen := false
	switch {
	case explicit != "":
		out = append(out, check("server", true, "LECTERN_API="+explicit+" — plain `lectern` commands use it; `lectern local …` uses your private runtime", ""))
	case choice.Hosted != nil:
		out = append(out, check("server", true, fmt.Sprintf("Lectern service at %s (%s) — plain `lectern` commands use it; `lectern local …` uses your private runtime", choice.Hosted.URL, choice.Hosted.Version), ""))
	case choice.RefusedService != nil:
		localChosen = true
		out = append(out, check("server", false, fmt.Sprintf("Lectern service at %s refused this CLI, so plain `lectern` commands use your private runtime", choice.RefusedService.URL),
			"set LECTERN_AUTH_TOKEN to the service's token (or LECTERN_API to its URL)"))
	case choice.InLocalSession:
		localChosen = true
		out = append(out, check("server", true, "private local runtime — this shell runs inside one of its sessions", ""))
	default:
		localChosen = true
		out = append(out, check("server", true, "private local runtime — no Lectern service answers on this machine", ""))
	}
	switch local.State {
	case "running":
		detail := fmt.Sprintf("running %s", local.Version)
		if local.Endpoint != nil {
			detail += " at " + local.Endpoint.URL
		}
		if local.Outdated {
			if localChosen {
				out = append(out, check("local runtime", false, detail+" — older than this CLI ("+local.CLIVersion+")",
					"run `lectern local stop` (refused while tasks are active); the next local command starts this build"))
			} else {
				out = append(out, skip("local runtime", detail+" — older than this CLI ("+local.CLIVersion+"); `lectern local stop` retires it"))
			}
		} else {
			out = append(out, check("local runtime", true, detail, ""))
		}
	case "stopped":
		out = append(out, skip("local runtime", "not running — the first local command starts it"))
	default:
		out = append(out, skip("local runtime", local.State+": "+local.Detail))
	}
	return out
}
