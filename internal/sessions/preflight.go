package sessions

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// executableWord reads the first literal shell word without evaluating it.
// Complex shell expressions remain supported; their executable is determined
// by the shell at launch, not by executing arbitrary setup during a probe.
func executableWord(command string) string {
	var word strings.Builder
	var quote rune
	escape := false
	for _, r := range strings.TrimSpace(command) {
		if escape {
			word.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escape = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			if quote == '"' && (r == '$' || r == '`') {
				return ""
			}
			word.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			break
		}
		if strings.ContainsRune("$`;|&()<>", r) {
			return ""
		}
		word.WriteRune(r)
	}
	if quote != 0 || escape || strings.Contains(word.String(), "=") {
		return ""
	}
	return word.String()
}

func checkAgentExecutable(ctx context.Context, ex executor.Executor, spec Spec, overrides map[string]string) error {
	bin := executableWord(spec.Command)
	if bin == "" {
		return nil
	}
	// Only PATH affects command discovery. Never put credentials in a probe.
	path := spec.Env["PATH"]
	if value, ok := overrides["PATH"]; ok {
		path = value
	}
	prefix := ""
	if path != "" {
		prefix = "PATH=" + shellq.Quote(path) + " "
	}
	r, err := ex.Run(ctx, prefix+"command -v "+shellq.Quote(bin)+" >/dev/null 2>&1", executor.RunOpts{Timeout: 10})
	if err != nil {
		return fmt.Errorf("check %s on the selected machine: %w", spec.Name, err)
	}
	if !r.OK() {
		return fmt.Errorf("%s is not installed on the selected machine (executable %s). Install it there or choose the demo agent", spec.Name, bin)
	}
	return nil
}

func sessionName(workdir, prompt, agent string) string {
	name := path.Base(strings.ReplaceAll(workdir, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = agent
	}
	words := strings.Join(strings.Fields(prompt), " ")
	if runes := []rune(words); len(runes) > 48 {
		words = string(runes[:48]) + "…"
	}
	if words != "" {
		name += " — " + words
	}
	return name
}

// stageLaunchEnvironment keeps hook/MCP credentials out of a shell's crash
// diagnostics and process arguments. mktemp creates the directory privately
// before WriteFile (whose generic contract does not promise a private mode).
func stageLaunchEnvironment(ctx context.Context, ex executor.Executor, assignments string) (string, func(), error) {
	r, err := ex.Run(ctx, "umask 077; mktemp -d /tmp/lectern-launch-env.XXXXXXXX", executor.RunOpts{Timeout: 10})
	if err != nil {
		return "", func() {}, err
	}
	dir := strings.TrimSpace(r.Stdout)
	if !r.OK() || !strings.HasPrefix(dir, "/tmp/lectern-launch-env.") || strings.ContainsAny(dir, "\r\n") {
		return "", func() {}, fmt.Errorf("could not create a private launch environment")
	}
	file := dir + "/env"
	// Cleanup is deferred until the shell has opened the file (the manager's
	// post-launch probes run after the launch), but never unlink on success here:
	// the launch shell owns consumption. Empty directories are safe to remove.
	cleanup := func() {
		_, _ = ex.Run(context.Background(), "rmdir "+shellq.Quote(dir)+" 2>/dev/null || true", executor.RunOpts{Timeout: 5})
	}
	if err := ex.WriteFile(ctx, file, []byte(strings.TrimSpace(assignments)+"\n")); err != nil {
		_, _ = ex.Run(context.Background(), "rm -f "+shellq.Quote(file), executor.RunOpts{Timeout: 5})
		cleanup()
		return "", func() {}, err
	}
	return file, cleanup, nil
}

// CheckSessionExecutable validates a continuation before its old process is
// stopped. The captured profile, including command and PATH, remains binding.
func (m *Manager) CheckSessionExecutable(ctx context.Context, row *store.Session) error {
	target, err := m.DB.Target(row.TargetID)
	if err != nil {
		return err
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return err
	}
	cfg, err := m.SessionLaunchConfiguration(row)
	if err != nil {
		return err
	}
	return checkAgentExecutable(ctx, ex, cfg.Spec, nil)
}
