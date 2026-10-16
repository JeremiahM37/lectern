package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// gitTimeout bounds a git clone.
const gitTimeout = 90 * time.Second

// Install copies a skill into the project (scope "project", needs root) or
// the global skills directory (scope "global"). src is a local directory or
// a git URL (https://, http://, ssh://, git@, file://). Either form may end
// in "#subdir" to pick one skill out of a repository or collection.
//
// The skill is copied into a hidden temporary directory first and renamed
// into place, so a failed copy never leaves a half-written skill behind. An
// existing skill of the same name is not overwritten.
func Install(root, src, scope string) (Skill, error) {
	dest, err := destination(root, scope)
	if err != nil {
		return Skill{}, err
	}
	location, sub := splitSub(src)
	if strings.TrimSpace(location) == "" {
		return Skill{}, errors.New("install needs a source: a local directory or a git URL")
	}

	base := location
	if isGitURL(location) {
		tmp, err := os.MkdirTemp("", "scribe-skill-src-*")
		if err != nil {
			return Skill{}, err
		}
		defer os.RemoveAll(tmp)
		if err := gitClone(location, tmp); err != nil {
			return Skill{}, err
		}
		base = tmp
	} else {
		abs, err := filepath.Abs(location)
		if err != nil {
			return Skill{}, err
		}
		base = abs
		if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
			return Skill{}, fmt.Errorf("source %s is not a directory", location)
		}
	}
	if sub != "" {
		base = filepath.Join(base, filepath.FromSlash(sub))
	}

	skillDir, err := findSkillDir(base, src)
	if err != nil {
		return Skill{}, err
	}
	mdSrc, err := os.ReadFile(filepath.Join(skillDir, SkillFile))
	if err != nil {
		return Skill{}, err
	}
	fm, _, perr := ParseFrontmatter(string(mdSrc))
	name := filepath.Base(skillDir)
	if perr == nil && strings.TrimSpace(fm.Name) != "" {
		name = strings.TrimSpace(fm.Name)
	}
	if err := validName(name); err != nil {
		return Skill{}, err
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Skill{}, err
	}
	final := filepath.Join(dest, name)
	if _, err := os.Lstat(final); err == nil {
		return Skill{}, fmt.Errorf("a skill named %q already exists at %s; remove it first", name, final)
	}
	staging, err := os.MkdirTemp(dest, ".install-"+name+"-")
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(staging)
	if err := copyTree(skillDir, staging); err != nil {
		return Skill{}, fmt.Errorf("copy skill: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		return Skill{}, err
	}
	sk := Skill{Name: name, Description: oneLine(fm.Description), Dir: final, Scope: scope, Source: dest}
	if perr != nil {
		sk.Problem = perr.Error()
	}
	return sk, nil
}

// Remove deletes the effective skill called name that Discover finds for
// root. A symlinked skill directory has only its link removed.
func Remove(root, name string) error {
	set, err := Discover(root)
	if err != nil {
		return err
	}
	sk, ok := set.Find(name)
	if !ok {
		return fmt.Errorf("no skill named %q", name)
	}
	if err := os.RemoveAll(sk.Dir); err != nil {
		return fmt.Errorf("remove %s: %w", sk.Dir, err)
	}
	return nil
}

func destination(root, scope string) (string, error) {
	switch scope {
	case ScopeProject:
		if root == "" {
			return "", errors.New("project scope needs a project root")
		}
		return filepath.Join(root, ".scribe", "skills"), nil
	case ScopeGlobal, "":
		return filepath.Join(config.Home(), "skills"), nil
	}
	return "", fmt.Errorf("unknown scope %q; use project or global", scope)
}

// splitSub splits "location#subdir" at the last '#'.
func splitSub(src string) (string, string) {
	if i := strings.LastIndex(src, "#"); i >= 0 {
		return src[:i], strings.Trim(src[i+1:], "/")
	}
	return src, ""
}

func isGitURL(s string) bool {
	for _, p := range []string{"https://", "http://", "ssh://", "git@", "file://"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return strings.HasSuffix(s, ".git")
}

func gitClone(url, into string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed; install a skill from a local directory instead")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--quiet", url, into)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return fmt.Errorf("git clone %s failed: %v: %s", url, err, msg)
	}
	return nil
}

// findSkillDir returns base when it holds SKILL.md, else the single
// immediate child directory that does.
func findSkillDir(base, src string) (string, error) {
	if fi, err := os.Stat(filepath.Join(base, SkillFile)); err == nil && fi.Mode().IsRegular() {
		return base, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no SKILL.md in %s", src)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if fi, err := os.Stat(filepath.Join(base, e.Name(), SkillFile)); err == nil && fi.Mode().IsRegular() {
			found = append(found, e.Name())
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no SKILL.md in %s", src)
	case 1:
		return filepath.Join(base, found[0]), nil
	}
	return "", fmt.Errorf("%s holds %d skills (%s); pick one with %s#<subdir>",
		src, len(found), strings.Join(found, ", "), src)
}

// copyTree copies regular files and directories from src into dst, skipping
// .git and symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" && rel != "." {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and devices are not copied
		}
		return copyFile(p, target)
	})
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
