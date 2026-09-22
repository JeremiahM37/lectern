package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

var simpleAgentCommand = regexp.MustCompile(`^[A-Za-z0-9_./+][A-Za-z0-9_./+-]*$`)

type agentCommandStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// targetAgentCommands only resolves executable names. It never executes an
// agent, its version command, trust hooks, or login/model requests.
func (s *Server) targetAgentCommands(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such target")
		return
	}
	target, err := s.DB.Target(id)
	if err != nil {
		httpError(w, 404, "no such target")
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		httpError(w, 503, "Could not connect to target")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	rows := make([]agentCommandStatus, 0)
	for _, original := range s.agentSpecs() {
		spec := s.Sessions.Resolve(original)
		row := agentCommandStatus{Name: spec.Name, State: "unchecked"}
		if !simpleAgentCommand.MatchString(spec.Command) || hasCommandPath(spec.Env) {
			row.Detail = "Custom shell command or PATH; check its launch environment"
		} else if mock, ok := ex.(*executor.Mock); ok {
			if spec.Builtin && mock.Probe()[spec.Name] != nil {
				row.State = "available"
				row.Detail = "Demo target command"
			} else {
				row.State = "unchecked"
				row.Detail = "Custom command on demo target"
			}
		} else {
			result, runErr := ex.Run(ctx, "command -v "+executor.ShellQuote(spec.Command), executor.RunOpts{Timeout: 5})
			if runErr != nil || ctx.Err() != nil || result.RC == 124 || result.RC == 255 {
				httpError(w, 503, "Target command lookup did not complete; retry when the target is reachable")
				return
			}
			switch {
			case result.OK() && strings.TrimSpace(result.Stdout) != "":
				row.State = "available"
				row.Path = strings.TrimSpace(result.Stdout)
			case result.RC == 1:
				row.State = "missing"
				row.Detail = "Not found on the target's default PATH"
			default:
				row.Detail = "The target could not determine command availability"
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, 200, rows)
}

func hasCommandPath(env map[string]string) bool {
	_, ok := env["PATH"]
	return ok
}
