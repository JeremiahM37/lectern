package exec

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The system prompt carries a small git block and the project's command line.
// Both are cheap to compute but run git, so each is cached briefly: within a
// turn the same answer is reused, and the prompt memo sees identical text.
const contextTTL = 10 * time.Second

// contextLogCount is how many recent commits the git block lists.
const contextLogCount = 5

// contextSubjectRunes caps each commit subject in the git block.
const contextSubjectRunes = 72

type ttlCache struct {
	mu  sync.Mutex
	key string
	at  time.Time
	val string
	ok  bool
}

func (c *ttlCache) get(key string, fn func() string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.key == key && time.Since(c.at) < contextTTL {
		return c.val
	}
	c.val = fn()
	c.key, c.at, c.ok = key, time.Now(), true
	return c.val
}

var (
	gitBlockCache ttlCache
	commandsCache ttlCache
)

// GitContextBlock returns the git state of dir for the system prompt: the
// branch with its upstream position, a count of staged, unstaged and untracked
// files, any merge conflicts, and the last few commits. It returns "" outside a
// repository.
func GitContextBlock(dir string) string {
	return gitBlockCache.get(dir, func() string { return gitContext(dir) })
}

func gitContext(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitQuickTimeout)
	defer cancel()
	st, err := gitCapture(ctx, dir, "status", "--porcelain=v1", "-b")
	if err != nil {
		return ""
	}
	var branch string
	var staged, unstaged, untracked int
	var conflicts []string
	for _, ln := range strings.Split(strings.TrimRight(st, "\n"), "\n") {
		if strings.HasPrefix(ln, "## ") {
			branch = branchSummary(ln[3:])
			continue
		}
		if len(ln) < 4 {
			continue
		}
		x, y, path := ln[0], ln[1], strings.TrimSpace(ln[3:])
		switch {
		case x == '?' && y == '?':
			untracked++
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			conflicts = append(conflicts, path)
		default:
			if x != ' ' {
				staged++
			}
			if y != ' ' {
				unstaged++
			}
		}
	}
	var b strings.Builder
	if branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", branch)
	}
	if staged+unstaged+untracked == 0 {
		b.WriteString("Working tree: clean\n")
	} else {
		fmt.Fprintf(&b, "Working tree: %d staged, %d unstaged, %d untracked\n", staged, unstaged, untracked)
	}
	if len(conflicts) > 0 {
		fmt.Fprintf(&b, "Merge conflicts: %d (%s). Resolve them before committing.\n", len(conflicts), strings.Join(firstN(conflicts, 5), ", "))
	}
	if log, err := gitCapture(ctx, dir, "log", fmt.Sprintf("-%d", contextLogCount), "--format=%h %s"); err == nil {
		if lines := strings.Split(strings.TrimSpace(log), "\n"); len(lines) > 0 && lines[0] != "" {
			b.WriteString("Last commits:\n")
			for _, ln := range lines {
				b.WriteString("  " + clipRunes(ln, contextSubjectRunes) + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// branchSummary turns the "## " line of git status -b into a short label.
func branchSummary(s string) string {
	switch {
	case strings.HasPrefix(s, "No commits yet on "):
		return "no commits yet on " + strings.TrimPrefix(s, "No commits yet on ")
	case strings.HasPrefix(s, "HEAD (no branch)"):
		return "detached HEAD"
	}
	name := s
	extra := ""
	if i := strings.Index(s, " ["); i >= 0 {
		name, extra = s[:i], s[i:]
	}
	if i := strings.Index(name, "..."); i >= 0 {
		name = name[:i]
	}
	return name + extra
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// ProjectCommandsLine returns the "Project commands" line for the system
// prompt: the detected lint, typecheck, test and build commands for dir, or ""
// when the project has none.
func ProjectCommandsLine(dir string) string {
	return commandsCache.get(dir, func() string {
		p := Detect(dir)
		var parts []string
		for _, action := range []string{"lint", "typecheck", "test", "build"} {
			if c := p.Commands[action]; c != "" {
				parts = append(parts, action+": "+c)
			}
		}
		return strings.Join(parts, "; ")
	})
}
