package sessions

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/isolation"
)

// secretEnvDelimiter ends the here-document that carries the secrets.
const secretEnvDelimiter = "LECTERN_SECRET_ENV"

var safeFileName = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// writeSecretEnv puts a session's secrets in a private file on its target
// (~/.lectern/hooks/<session>.env, mode 0600), which the launch command
// reads and deletes (Start.EnvFile). It returns the file, the names it
// holds, and — when the file cannot be used — the secrets as a prefix, the
// way they were always passed. A docker sandbox only sees what is inside its
// own command line, so there the prefix is kept.
func writeSecretEnv(ctx context.Context, ex executor.Executor, tmuxName string, secrets map[string]string, iso isolation.Config) (file string, names []string, prefix string) {
	if len(secrets) == 0 {
		return "", nil, ""
	}
	fallback, err := EnvPrefix(secrets)
	if err != nil {
		return "", nil, ""
	}
	if iso.Normalized().Mode == isolation.Docker || ex == nil {
		return "", nil, fallback
	}
	for k := range secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	var body strings.Builder
	for _, k := range names {
		line, _ := EnvPrefix(map[string]string{k: secrets[k]})
		if strings.Contains(line, secretEnvDelimiter) {
			return "", nil, fallback
		}
		body.WriteString("export " + strings.TrimSpace(line) + "\n")
	}
	file = "$HOME/.lectern/hooks/" + safeFileName.ReplaceAllString(tmuxName, "_") + ".env"
	cmd := `umask 077 && mkdir -p "$HOME/.lectern/hooks" && cat > "` + file + `" <<'` + secretEnvDelimiter + "'\n" + body.String() + secretEnvDelimiter
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30})
	if err != nil || !r.OK() {
		return "", nil, fallback
	}
	return file, names, ""
}
