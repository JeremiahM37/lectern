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

// programWord is the program a launch command runs, or "" when it cannot be
// told without running it (an environment assignment, a subshell…).
func programWord(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	word := fields[0]
	if strings.Contains(word, "=") || strings.ContainsAny(word, "$`(){}<>|;&'\"\\") {
		return ""
	}
	return word
}

// checkAgentInstalled asks the target whether the agent's program exists,
// before a session is created for it. It stays out of the way when it cannot
// answer honestly: the scripted mock target, an isolated launch (the program
// may live inside the sandbox), a launch environment that sets PATH, and a
// command it cannot parse all launch as before.
func (m *Manager) checkAgentInstalled(ctx context.Context, ex executor.Executor, target *store.Target, cfg *LaunchConfiguration, iso isolation.Config) error {
	if _, mock := ex.(*executor.Mock); mock {
		return nil
	}
	if iso.Normalized().Mode != isolation.None {
		return nil
	}
	if _, ok := cfg.Spec.Env["PATH"]; ok {
		return nil
	}
	word := programWord(cfg.Spec.Command)
	if word == "" {
		return nil
	}
	// A login shell too: some machines put agent CLIs on PATH only there.
	lookup := "command -v -- " + shellq.Quote(word) + " >/dev/null 2>&1"
	r, err := ex.Run(ctx, lookup+" || ${SHELL:-sh} -lc "+shellq.Quote(lookup), executor.RunOpts{Timeout: 10})
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
		if w := programWord(spec.Command); w != "" && spec.Name != cfg.Spec.Name && spec.Name != DemoAgent {
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
