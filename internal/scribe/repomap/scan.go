package repomap

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirs are never indexed, wherever they appear.
var skipDirs = words(".git node_modules vendor dist build target .venv venv __pycache__ .cache .scribe .next .nuxt coverage")

type fileEntry struct {
	rel   string
	abs   string
	size  int64
	mtime int64
}

// collectFiles lists indexable source files under root. A git work tree uses
// `git ls-files` (tracked plus untracked-but-not-ignored); anything else walks
// the tree and applies .gitignore files itself.
func collectFiles(ctx context.Context, root string, opts Options) ([]fileEntry, error) {
	rels := gitListFiles(ctx, root)
	if rels == nil {
		rels = walkFiles(root)
	}
	var out []fileEntry
	for _, rel := range rels {
		if skipPath(rel) || langFor(rel) == nil {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		st, err := os.Lstat(abs) // symlinks are not followed out of the repo
		if err != nil || !st.Mode().IsRegular() || st.Size() > opts.MaxFileBytes {
			continue
		}
		out = append(out, fileEntry{rel: rel, abs: abs, size: st.Size(), mtime: st.ModTime().UnixNano()})
	}
	if len(out) > opts.MaxFiles {
		sort.Slice(out, func(i, j int) bool { return out[i].mtime > out[j].mtime })
		out = out[:opts.MaxFiles]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, ctx.Err()
}

func skipPath(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if skipDirs[seg] {
			return true
		}
	}
	return false
}

func gitListFiles(ctx context.Context, root string) []string {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var rels []string
	for _, p := range bytes.Split(out, []byte{0}) {
		s := string(p)
		// A nested repository (a worktree under the root) is listed with a trailing slash.
		if s == "" || strings.HasSuffix(s, "/") {
			continue
		}
		rels = append(rels, s)
	}
	if rels == nil {
		rels = []string{}
	}
	return rels
}

// walkFiles is the non-git fallback: a recursive walk honouring .gitignore.
func walkFiles(root string) []string {
	var out []string
	var walk func(dirRel string, rules []ignoreRule)
	walk = func(dirRel string, rules []ignoreRule) {
		abs := filepath.Join(root, filepath.FromSlash(dirRel))
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		if data, err := os.ReadFile(filepath.Join(abs, ".gitignore")); err == nil {
			rules = append(append([]ignoreRule(nil), rules...), parseIgnore(dirRel, data)...)
		}
		for _, e := range entries {
			name := e.Name()
			rel := name
			if dirRel != "" {
				rel = path.Join(dirRel, name)
			}
			if e.IsDir() {
				if skipDirs[name] || ignored(rules, rel, true) {
					continue
				}
				walk(rel, rules)
				continue
			}
			if e.Type().IsRegular() && !ignored(rules, rel, false) {
				out = append(out, rel)
			}
		}
	}
	walk("", nil)
	return out
}

// isBinary is a cheap check: a NUL in the first 8000 bytes.
func isBinary(src []byte) bool {
	n := len(src)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(src[:n], 0) >= 0
}
