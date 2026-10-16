package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// access says what a tool is about to do with a path, which decides which
// gates apply.
type access int

const (
	accessRead   access = iota // read a file's content
	accessWrite                // create or change a file
	accessSearch               // list names or search content (no secret gate)
)

// target is a resolved path. abs is the lexical absolute path, rel is abs
// relative to the workdir for display, real has symlinks resolved.
type target struct {
	abs     string
	rel     string
	real    string
	outside bool
}

// resolve applies the path gates for one tool call:
//   - writes into .git internals are refused outright;
//   - secret-looking files (read) and sensitive files (write) need approval
//     (Approval.Kind "sensitive");
//   - writes whose real path leaves the workdir need approval
//     (Kind "write_outside") unless env.Mode is "auto".
//
// Symlinks are resolved before the checks, so a link inside the project that
// points at a .env file or outside the project is judged by its target.
func resolve(ctx context.Context, env *tools.Env, toolName, p string, acc access) (target, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return target{}, errors.New("path is required")
	}
	if env.Workdir == "" {
		return target{}, errors.New("no project directory is set")
	}
	wd := filepath.Clean(env.Workdir)
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(wd, abs)
	}
	abs = filepath.Clean(abs)
	t := target{abs: abs, rel: relSlash(wd, abs), real: realPath(abs)}
	t.outside = !within(realPath(wd), t.real)

	if acc == accessWrite {
		for _, part := range strings.Split(t.rel, "/") {
			if part == ".git" {
				return target{}, fmt.Errorf("Refusing to write inside .git (%s). Git internals are not edited directly.", t.rel)
			}
		}
	}
	if acc == accessRead || acc == accessWrite {
		sensitive := isSecretName(filepath.Base(abs)) || isSecretName(filepath.Base(t.real))
		if acc == accessWrite && !sensitive {
			sensitive = isSensitiveWriteName(t.rel)
		}
		if sensitive {
			if acc == accessWrite {
				if !approve(ctx, env, toolName, "sensitive", "Edit sensitive file", t.rel) {
					return target{}, fmt.Errorf("Denied: edit to sensitive file %s", t.rel)
				}
			} else if !approve(ctx, env, toolName, "sensitive", "Read secret-looking file", t.rel) {
				return target{}, fmt.Errorf("Refusing to read %s: it looks like a file of secrets. Ask the user before reading it.", t.rel)
			}
		}
	}
	if acc == accessWrite && t.outside && env.Mode != "auto" {
		if !approve(ctx, env, toolName, "write_outside", "Write outside project directory", abs) {
			return target{}, fmt.Errorf("Denied: %s outside project directory → %s", toolName, t.rel)
		}
	}
	return t, nil
}

// approve asks the host. With no host, the action is refused.
func approve(ctx context.Context, env *tools.Env, toolName, kind, title, detail string) bool {
	if env.Host == nil {
		return false
	}
	return env.Host.Approve(ctx, tools.Approval{Tool: toolName, Kind: kind, Title: title, Detail: detail}) != tools.Deny
}

// realPath resolves symlinks in p. For a path that does not exist yet, it
// resolves the deepest existing ancestor and appends the rest, so new files
// are judged where they would really land.
func realPath(p string) string {
	cur := p
	var rest []string
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// within reports whether p is root or below it. Both must be cleaned.
func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// isSecretName reports whether a file name looks like it holds credentials.
// It is matched case-insensitively against the base name.
func isSecretName(name string) bool {
	n := strings.ToLower(name)
	switch {
	case n == ".env" || strings.HasPrefix(n, ".env."):
		return true
	case hasSuffixAny(n, ".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"):
		return true
	case hasPrefixAny(n, "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"):
		return true
	case strings.Contains(n, "credentials"):
		return true
	case strings.Contains(n, "private_key") || strings.Contains(n, "private-key"):
		return true
	case n == ".npmrc" || n == ".pypirc" || n == ".netrc" || n == ".htpasswd":
		return true
	case n == "token.json" || n == "tokens.json":
		return true
	case hasPrefixAny(n, "secret.", "secrets."):
		return true
	case strings.HasPrefix(n, "service-account") && strings.HasSuffix(n, ".json"):
		return true
	}
	return false
}

// isSensitiveWriteName adds CI and container definitions to the secret names:
// changes to these run code on the next build or deploy.
func isSensitiveWriteName(rel string) bool {
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	lower := strings.ToLower(base)
	switch {
	case strings.HasPrefix(rel, ".github/workflows/") || strings.Contains(rel, "/.github/workflows/"):
		return true
	case lower == ".gitlab-ci.yml", base == "Jenkinsfile", lower == "dockerfile":
		return true
	case strings.HasPrefix(lower, "docker-compose") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")):
		return true
	}
	return false
}

// secretGlobs are the names above, expressed as globs for ripgrep's --glob.
var secretGlobs = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks",
	"id_rsa*", "id_ed25519*", "id_ecdsa*", "id_dsa*", "*credentials*", "*private_key*",
	".npmrc", ".pypirc", ".netrc", ".htpasswd", "secret.*", "secrets.*",
	"token.json", "tokens.json", "service-account*.json",
}

func hasSuffixAny(s string, suffixes ...string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}

func hasPrefixAny(s string, prefixes ...string) bool {
	for _, x := range prefixes {
		if strings.HasPrefix(s, x) {
			return true
		}
	}
	return false
}

// statPath is os.Stat with the error wrapped for the model.
func statPath(t target) (os.FileInfo, error) {
	fi, err := os.Stat(t.abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("File not found: %s. Use glob or list_dir to find the right path.", t.rel)
		}
		return nil, err
	}
	return fi, nil
}
