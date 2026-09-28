package api

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// terminalSplit is what a split of a native attachment runs
// (docs/terminal-client.md): a shell on the session's own target, in the
// directory its pane is in now, falling back to the session's workdir. The
// native client asks for it and runs the command, as it does attach_argv.
func (s *Server) terminalSplit(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	att, target, err := s.resolveAttachment(kind, id)
	if err != nil {
		httpError(w, 404, "%s", err)
		return
	}
	dir, _, _ := s.terminalDirectory(kind, id)
	if current := s.paneDirectory(r.Context(), att, target); current != "" {
		dir = current
	}
	argv, err := terminal.ShellArgv(att, target, dir)
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	writeJSON(w, 200, map[string]any{"dir": dir, "target": target.Name, "attach_argv": argv})
}

// paneDirectory is the current directory of the attachment's active pane,
// asked of tmux on the target, or "" when it cannot be known.
func (s *Server) paneDirectory(ctx context.Context, att terminal.Attachment, target *store.Target) string {
	if target.Kind == "sandbox" || att.TmuxSession == "" {
		return ""
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := ex.Run(ctx, "tmux display-message -p -t "+shellq.Quote("="+att.TmuxSession+":")+" '#{pane_current_path}'", executor.RunOpts{Timeout: 15})
	if err != nil || out.RC != 0 {
		return ""
	}
	dir := strings.TrimSpace(out.Stdout)
	if !path.IsAbs(dir) || strings.ContainsAny(dir, "\n\r") {
		return ""
	}
	return dir
}
