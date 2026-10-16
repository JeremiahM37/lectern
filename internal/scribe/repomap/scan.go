package repomap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits for the non-git walk (spec 4.2): at most walkCap files, and a walk
// that runs past walkTimeout stops with whatever it has found.
const (
	walkCap     = 50000
	walkTimeout = 60 * time.Second
	// recencyCommits bounds the git history read when the file cap bites.
	recencyCommits = 1000
)

// ErrUnsafeRoot is returned by Build for a home directory or a filesystem
// root. Indexing either walks the whole disk, so nothing is indexed.
var ErrUnsafeRoot = errors.New("repomap: refusing to index a home directory or filesystem root")

// unsafeRoot reports whether root is the filesystem root or the user's home.
func unsafeRoot(root string) bool {
	if filepath.Dir(root) == root {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && filepath.Clean(home) == filepath.Clean(root)
}

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
// the tree and applies .gitignore files itself. The returned note explains a
// truncated walk, and is empty otherwise.
func collectFiles(ctx context.Context, root string, opts Options) ([]fileEntry, string, error) {
	rels := gitListFiles(ctx, root)
	note := ""
	if rels == nil {
		rels, note = walkFiles(root)
	} else {
		rels = dropScribeIgnored(root, rels)
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
		// Keep the files the author touched most recently: git history first,
		// then mtime for anything git has not seen.
		rank := gitRecency(ctx, root)
		sort.SliceStable(out, func(i, j int) bool {
			ri, oki := rank[out[i].rel]
			rj, okj := rank[out[j].rel]
			switch {
			case oki != okj:
				return oki
			case oki && ri != rj:
				return ri < rj
			}
			return out[i].mtime > out[j].mtime
		})
		out = out[:opts.MaxFiles]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, note, ctx.Err()
}

// gitRecency ranks paths by how recently git saw them change: the first
// occurrence in the last recencyCommits commits wins. Nil when there is no
// usable history.
func gitRecency(ctx context.Context, root string) map[string]int {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "log", "--all", "--name-only",
		"--no-renames", "-n", fmt.Sprint(recencyCommits), "--pretty=format:").Output()
	if err != nil {
		return nil
	}
	rank := map[string]int{}
	for _, ln := range strings.Split(string(out), "\n") {
		if ln == "" {
			continue
		}
		if _, ok := rank[ln]; !ok {
			rank[ln] = len(rank)
		}
	}
	return rank
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
// It stops at walkCap files or walkTimeout, and says so in the note.
func walkFiles(root string) (out []string, note string) {
	deadline := time.Now().Add(walkTimeout)
	stopped := false
	var walk func(dirRel string, rules []ignoreRule)
	walk = func(dirRel string, rules []ignoreRule) {
		if stopped {
			return
		}
		if len(out) >= walkCap || time.Now().After(deadline) {
			stopped = true
			return
		}
		abs := filepath.Join(root, filepath.FromSlash(dirRel))
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		for _, name := range []string{".gitignore", scribeIgnoreFile} {
			if data, err := os.ReadFile(filepath.Join(abs, name)); err == nil {
				rules = append(append([]ignoreRule(nil), rules...), parseIgnore(dirRel, data)...)
			}
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
	if stopped {
		note = fmt.Sprintf("the walk stopped early (limit %d files or %s); the map covers only part of the tree", walkCap, walkTimeout)
	}
	return out, note
}

// isBinary is a cheap check: a NUL in the first 8000 bytes.
func isBinary(src []byte) bool {
	n := len(src)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(src[:n], 0) >= 0
}
