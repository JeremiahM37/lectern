package exec

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// lintFixTimeout bounds the lint fix that runs after sub-agents edit files
// (spec 06 section 9.5: 30 seconds).
const lintFixTimeout = 30 * time.Second

var jsLintExts = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".vue", ".svelte"}

// scopedFixer says how a fix command takes file arguments: which tool it
// is, the extensions it accepts, and whether it takes package directories
// instead of files. Tools not listed here cannot be scoped to files, so the
// post-dispatch fix skips them rather than fixing the whole project.
type scopedFixer struct {
	match string
	exts  []string
	dirs  bool
}

var scopedFixers = []scopedFixer{
	{"golangci-lint run", []string{".go"}, true},
	{"eslint", jsLintExts, false},
	{"biome check", append(append([]string{}, jsLintExts...), ".json", ".jsonc", ".css"), false},
	{"oxlint", jsLintExts, false},
	{"ruff check", []string{".py", ".pyi"}, false},
	{"rubocop", []string{".rb"}, false},
	{"swiftlint", []string{".swift"}, false},
}

// scopeFix returns the fixed lint command with its project-wide arguments
// dropped and the given files appended, and how many targets it names. ok is
// false when the linter cannot be scoped or no given file is one it handles.
func scopeFix(fixed string, files []string, workdir string) (cmd string, n int, ok bool) {
	var fixer *scopedFixer
	for i := range scopedFixers {
		if strings.Contains(fixed, scopedFixers[i].match) {
			fixer = &scopedFixers[i]
			break
		}
	}
	if fixer == nil {
		return "", 0, false
	}
	var words []string
	for _, w := range strings.Fields(fixed) {
		if !formatWords[w] {
			words = append(words, w)
		}
	}
	targets := map[string]bool{}
	for _, f := range files {
		rel := f
		if filepath.IsAbs(f) {
			r, err := filepath.Rel(workdir, f)
			if err != nil || strings.HasPrefix(r, "..") {
				continue
			}
			rel = r
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if !hasExt(rel, fixer.exts) {
			continue
		}
		if fixer.dirs {
			targets["./"+filepath.ToSlash(filepath.Dir(rel))] = true
		} else {
			targets[rel] = true
		}
	}
	if len(targets) == 0 {
		return "", 0, false
	}
	list := make([]string, 0, len(targets))
	for t := range targets {
		list = append(list, t)
	}
	sort.Strings(list)
	for _, t := range list {
		words = append(words, shellQuote(t))
	}
	return strings.Join(words, " "), len(list), true
}

func hasExt(name string, exts []string) bool {
	low := strings.ToLower(name)
	for _, e := range exts {
		if strings.HasSuffix(low, e) {
			return true
		}
	}
	return false
}

// LintFixFiles runs the project's lint auto-fix on only the given files, once,
// after sub-agents have edited them. It is deterministic: no model is
// involved. The report says what ran; ran is false when nothing was fixed
// (no lint command, no fix for it, no file it can scope to, or the gate
// refused). It is bounded by timeout, and the fix goes through the same
// command gate as project fix.
func (t *projectTool) LintFixFiles(ctx context.Context, env *tools.Env, files []string, timeout time.Duration) (string, bool) {
	if env == nil || len(files) == 0 {
		return "no files to lint-fix", false
	}
	p := Detect(env.Workdir)
	lint := p.Commands["lint"]
	if lint == "" {
		return "no lint command detected for this project; the changed files were not auto-fixed", false
	}
	fixed, ok := fixCommand(lint)
	if !ok {
		return fmt.Sprintf("no auto-fix is known for %q; the changed files were not auto-fixed", lint), false
	}
	cmd, n, ok := scopeFix(fixed, files, env.Workdir)
	if !ok {
		return "no file-scoped auto-fix for this linter; the changed files were not auto-fixed", false
	}
	if res, blocked := gate(ctx, env, t.jobs.opts, "project", cmd, ""); blocked {
		return "lint fix blocked: " + firstLine(res.Output), false
	}
	env0 := []string{"FORCE_COLOR=0", "NO_COLOR=1"}
	fg := runForegroundIn(ctx, env, env.Workdir, cmd, timeout, env0)
	if fg.exit != 0 && fg.err == nil && !fg.timedOut && !fg.cancelled {
		if legacy, ok := legacyFixCommand(cmd, fg.raw); ok {
			fg = runForegroundIn(ctx, env, env.Workdir, legacy, timeout, env0)
		}
	}
	switch {
	case fg.err != nil:
		return "lint fix could not start: " + fg.err.Error(), false
	case fg.timedOut:
		return fmt.Sprintf("lint fix timed out after %s; the changed files may be partly fixed", timeout), true
	case fg.cancelled:
		return "lint fix was cancelled", false
	case fg.exit != 0:
		return fmt.Sprintf("lint fix on %d target(s) exited %d: %s", n, fg.exit, firstLine(fg.body)), true
	}
	return fmt.Sprintf("lint fix ran on %d target(s); clean", n), true
}
