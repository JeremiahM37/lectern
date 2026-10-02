package sessions

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/isolation"
	"github.com/JeremiahM37/lectern/v2/internal/onboard"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// AgentNotInstalled is a launch refused because the agent's program is not
// on the machine. Without this check the session "started", went idle, and
// its terminal said `command not found`.
type AgentNotInstalled struct {
	Agent, Machine string
	Installed      []string // other agents that are installed there
}

func (e *AgentNotInstalled) Error() string {
	msg := fmt.Sprintf("%s isn't installed on %s. To use it, %s.", e.Agent, e.Machine, onboard.InstallHint(e.Agent))
	if len(e.Installed) > 0 {
		msg += " Installed there: " + strings.Join(e.Installed, ", ") + "."
	} else {
		msg += " No agent is installed there yet; the demo agent needs nothing installed."
	}
	return msg
}

// checkAgentInstalled asks the target whether the agent's program exists,
// before a session or a scratch folder is created for it. The program is the
// command's first literal word (executableWord); the launch's own PATH is the
// one searched. It stays out of the way when it cannot answer honestly: the
// scripted mock target, a docker sandbox (the program lives in the image),
// and a command it cannot read without running it all launch as before.
func (m *Manager) checkAgentInstalled(ctx context.Context, ex executor.Executor, target *store.Target, cfg *LaunchConfiguration, iso isolation.Config, overrides map[string]string) error {
	if _, mock := ex.(*executor.Mock); mock {
		return nil
	}
	if iso.Normalized().Mode == isolation.Docker {
		return nil
	}
	word := executableWord(cfg.Spec.Command)
	if word == "" {
		return nil
	}
	// Only PATH affects command discovery. Never put credentials in a probe.
	path, hasPath := cfg.Spec.Env["PATH"]
	if value, ok := overrides["PATH"]; ok {
		path, hasPath = value, true
	}
	lookup := "command -v -- " + shellq.Quote(word) + " >/dev/null 2>&1"
	probe := lookup
	if hasPath {
		probe = "PATH=" + shellq.Quote(path) + " " + lookup
	} else {
		// A login shell too: some machines put agent CLIs on PATH only there.
		probe = lookup + " || ${SHELL:-sh} -lc " + shellq.Quote(lookup)
	}
	r, err := ex.Run(ctx, probe, executor.RunOpts{Timeout: 10})
	if err != nil || r.OK() {
		// An unreachable target is reported by the launch itself.
		return nil
	}
	missing := &AgentNotInstalled{Agent: cfg.Spec.Name, Machine: target.Name}
	if target.Kind == "local" {
		missing.Machine = "this computer"
	}
	var names []string
	commands := map[string]string{}
	for _, spec := range m.specs() {
		spec = m.Launcher.resolve(spec)
		if w := executableWord(spec.Command); w != "" && spec.Name != cfg.Spec.Name && spec.Name != DemoAgent {
			names = append(names, spec.Name)
			commands[spec.Name] = w
		}
	}
	if len(names) > 0 {
		var probe strings.Builder
		for _, name := range names {
			fmt.Fprintf(&probe, "command -v -- %s >/dev/null 2>&1 && echo %s; ", shellq.Quote(commands[name]), shellq.Quote(name))
		}
		probe.WriteString("true")
		if r, err := ex.Run(ctx, probe.String(), executor.RunOpts{Timeout: 10}); err == nil {
			missing.Installed = strings.Fields(r.Stdout)
		}
	}
	return missing
}

// CheckAgentInstalledFor is the same check for a session about to be started
// again (Revive), made before its old terminal is closed, so a refusal leaves
// it as it was. The captured launch settings, command and PATH included, are
// the ones checked.
func (m *Manager) CheckAgentInstalledFor(ctx context.Context, row *store.Session) error {
	cfg, err := m.SessionLaunchConfiguration(row)
	if err != nil {
		return nil
	}
	target, err := m.DB.Target(row.TargetID)
	if err != nil {
		return nil
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return nil
	}
	return m.checkAgentInstalled(ctx, ex, target, cfg, cfg.Isolation, nil)
}
