package fs

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// CheckWrite applies the write rules the edit tools use to abs, for a caller
// that writes a file without going through them, such as the editor's
// refactors and symbol edits. It refuses .git internals, secret-named files
// (.env, keys, credentials: never written by the agent), and paths the
// forbidden-pattern config matches. CI and container definitions need the
// user's approval. It does not check whether the file was read first; that is
// the caller's own staleness rule.
func CheckWrite(ctx context.Context, env *tools.Env, toolName, abs string) error {
	if env == nil {
		return fmt.Errorf("no environment to check the write against")
	}
	abs = filepath.Clean(abs)
	real := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		real = r
	}
	rel := abs
	if env.Workdir != "" {
		if r, err := filepath.Rel(env.Workdir, abs); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	t := target{abs: abs, rel: rel, real: real}
	if err := checkForbidden(env, t); err != nil {
		return err
	}
	for _, part := range splitSlash(rel) {
		if part == ".git" {
			return fmt.Errorf("Refusing to write inside .git (%s). Git internals are not edited directly.", rel)
		}
	}
	if isSecretName(filepath.Base(abs)) || isSecretName(filepath.Base(real)) {
		return fmt.Errorf("Denied: %s is a secret file (.env, key or credentials). Do not write secrets from the agent; ask the user to edit it by hand.", rel)
	}
	if isSensitiveWriteName(rel) {
		if !approve(ctx, env, toolName, "sensitive", "Edit sensitive file", rel) {
			return fmt.Errorf("Denied: edit to sensitive file %s", rel)
		}
	}
	return nil
}

// splitSlash splits a slash-separated relative path into its parts.
func splitSlash(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return out
}
