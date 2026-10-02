package sessions

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// An agent can exit while its terminal lives on: Lectern launches it as
// `bash -c "cd DIR && AGENT; exec bash"`, so when the agent ends the pane
// drops to a shell prompt and the screen reads as idle. The probe here tells
// that apart from an agent that is merely quiet, so the card can say "agent
// exited" and offer Revive instead of waiting forever.

// AgentProbeMarker starts the probe command; the mock executor keys on it.
const AgentProbeMarker = backend.AgentProbeMarker

// agentProbeEvery throttles the probe per target; exits are not urgent.
const agentProbeEvery = 10 * time.Second

// AgentProbe is what the target said about one pane.
type AgentProbe struct {
	RootArgs, Current string
	TTYArgs           []string
}

var shellNames = map[string]bool{"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true, "csh": true, "-bash": true, "-zsh": true}

// AgentExited decides from a probe whether a session's agent has gone while
// its terminal remains. Lectern-launched agents are exact: the pane's root
// process becomes a bare `bash` only after the agent command returned. For an
// adopted session the foreground must be a shell and no process on its
// terminal may still look like the agent.
func AgentExited(row *store.Session, p AgentProbe) bool {
	if row.Agent == "shell" {
		return false
	}
	if row.Origin == "lectern" {
		if !shellNames[strings.TrimSpace(p.RootArgs)] {
			return false
		}
		// The launch command returned, but the agent may have been started
		// again from that shell (a resume typed at the prompt), so a bare root
		// alone is not enough: the agent must be gone from the terminal too.
	} else if !shellNames[strings.TrimSpace(p.Current)] {
		return false
	}
	for _, args := range p.TTYArgs {
		if agentPattern.MatchString(args) || runsCommand(args, row.Agent) {
			return false
		}
	}
	return true
}

// runsCommand reports whether a process's arguments run the named command,
// so catalog agents (opencode, goose, …) count as well as the built-ins.
func runsCommand(args, name string) bool {
	if name == "" {
		return false
	}
	for _, field := range strings.Fields(args) {
		if field == name || strings.HasSuffix(field, "/"+name) {
			return true
		}
	}
	return false
}

// buildAgentProbe reports, per pane, the root process's arguments, the
// foreground command and every process on its terminal — base64 framed like
// the poll so no pane can forge another's answer.
func buildAgentProbe(names []string) string { return backend.Tmux.AgentProbe(names) }

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// parseAgentProbe needs a complete, well-formed answer; anything else is
// treated as no information.
func parseAgentProbe(out string, names []string) (map[string]AgentProbe, bool) {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(names)+1 || lines[len(lines)-1] != PollEnd {
		return nil, false
	}
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	probes := map[string]AgentProbe{}
	for _, line := range lines[:len(lines)-1] {
		parts := strings.Split(line, "\t")
		if len(parts) != 5 {
			return nil, false
		}
		raw, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil || !wanted[string(raw)] {
			return nil, false
		}
		if parts[1] != "ok" {
			continue
		}
		var fields [3]string
		for i := range fields {
			v, err := base64.StdEncoding.DecodeString(parts[i+2])
			if err != nil {
				return nil, false
			}
			fields[i] = string(v)
		}
		p := AgentProbe{RootArgs: fields[0], Current: fields[1]}
		for _, args := range strings.Split(fields[2], "\n") {
			if args = strings.TrimSpace(args); args != "" {
				p.TTYArgs = append(p.TTYArgs, args)
			}
		}
		probes[string(raw)] = p
	}
	return probes, true
}

// probeAgents marks or clears agent_exited_at for a target's live agent
// sessions. It runs at most every agentProbeEvery per target.
func (m *Manager) probeAgents(ctx context.Context, ex executor.Executor, targetID int64, group []*store.Session) {
	var rows []*store.Session
	now := store.Now()
	for _, s := range group {
		if s.Agent == "shell" || s.EndedAt != nil || s.Status == StatusStarting || s.Status == StatusDead || now-s.CreatedAt < 2 {
			continue
		}
		rows = append(rows, s)
	}
	if len(rows) == 0 {
		return
	}
	m.mu.Lock()
	if m.agentProbedAt == nil {
		m.agentProbedAt = map[int64]time.Time{}
	}
	every := agentProbeEvery
	if m.agentProbeEvery > 0 {
		every = m.agentProbeEvery
	}
	if time.Since(m.agentProbedAt[targetID]) < every {
		m.mu.Unlock()
		return
	}
	m.agentProbedAt[targetID] = time.Now()
	m.mu.Unlock()
	// Each session is probed through the backend that holds it.
	byBackend := map[string][]string{}
	backends := map[string]backend.Backend{}
	for _, s := range rows {
		be, ok := backend.ForSession(ex, s)
		if !ok {
			continue
		}
		backends[be.Name()] = be
		byBackend[be.Name()] = append(byBackend[be.Name()], s.TmuxSession)
	}
	probes := map[string]AgentProbe{}
	for name, names := range byBackend {
		r, err := ex.Run(ctx, backends[name].AgentProbe(names), executor.RunOpts{Timeout: 20})
		if err != nil || !r.OK() {
			continue
		}
		got, ok := parseAgentProbe(r.Stdout, names)
		if !ok {
			continue
		}
		for k, v := range got {
			probes[k] = v
		}
	}
	for _, s := range rows {
		p, seen := probes[s.TmuxSession]
		if !seen {
			continue
		}
		fresh, err := m.DB.Session(s.ID)
		if err != nil || fresh.EndedAt != nil {
			continue
		}
		exited := AgentExited(fresh, p)
		if exited == (fresh.AgentExitedAt != nil) {
			continue
		}
		var at any
		if exited {
			at = store.Now()
		}
		if m.DB.Update("sessions", s.ID, map[string]any{"agent_exited_at": at, "updated_at": store.Now()}) == nil {
			if row, err := m.DB.Session(s.ID); err == nil {
				m.publish(row)
				if exited {
					if m.ExpireApprovals != nil {
						m.ExpireApprovals(s.ID)
					}
					m.Log.Info("agent exited; terminal still open", "session", s.ID, "name", s.Name)
				}
			}
		}
	}
}

// SetAgentProbeInterval changes how often the agent probe may run per target;
// tests shorten it.
func (m *Manager) SetAgentProbeInterval(every time.Duration) {
	m.mu.Lock()
	m.agentProbeEvery = every
	m.mu.Unlock()
}
