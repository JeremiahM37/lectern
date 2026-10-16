package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Timeouts for git actions. Hooks run during commit, so commit gets longer.
const (
	gitQuickTimeout  = 5 * time.Second
	gitShowTimeout   = 10 * time.Second
	gitWriteTimeout  = 30 * time.Second
	gitCommitTimeout = 2 * time.Minute
)

// gitActions are the actions the git tool accepts.
var gitActions = []string{"status", "diff", "log", "commit", "branch", "stash", "show", "blame", "add"}

// sensitiveNames are file names the git tool will not stage on the model's
// behalf. Staging is not a secret leak until push, but these almost never
// belong in a commit, and the user can still add them by hand.
var sensitiveNames = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.keystore", "*.jks",
	"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "credentials.json", "service-account*.json",
	"secrets.json", "secrets.yaml", "secrets.yml", ".npmrc", ".pypirc", ".netrc",
	"token.json", "tokens.json", ".htpasswd",
}

type gitTool struct{}

type gitArgs struct {
	Action    string   `json:"action"`
	SubAction string   `json:"sub_action"`
	Args      []string `json:"args"`
	Message   string   `json:"message"`
	Files     []string `json:"files"`
	All       bool     `json:"all"`
	NoVerify  bool     `json:"no_verify"`
	Ref       string   `json:"ref"`
	Name      string   `json:"name"`
	Staged    bool     `json:"staged"`
	Count     int      `json:"count"`
}

func (t *gitTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "git",
		Description: "Work with the project's git repository. Read actions: status, diff, log, show, blame, branch (list). " +
			"Changing actions: add (stages files), commit (the message is written exactly as given; pre-commit hooks run unless no_verify is true; no trailer is added), " +
			"branch (create, switch, delete), stash (push, pop, list, show, drop). " +
			"Hooks run on commit unless no_verify is true, which needs the user's explicit request.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("What to do.", gitActions...),
			"sub_action": tools.Str("branch: list, create, switch, delete. stash: push (default), pop, list, show, drop."),
			"args":       tools.Arr("Extra arguments passed to git after the action, e.g. paths or flags.", map[string]any{"type": "string"}),
			"message":    tools.Str("Commit message (commit) or stash message (stash push). Written exactly as given."),
			"files":      tools.Arr("Paths to stage (add) or to stash (stash push).", map[string]any{"type": "string"}),
			"all":        tools.Bool("add: stage every change (git add -A). Use only when the user asked for everything."),
			"no_verify":  tools.Bool("commit: skip pre-commit hooks (--no-verify). Only when the user explicitly asks."),
			"ref":        tools.Str("diff, show, blame: the commit or ref to use."),
			"name":       tools.Str("branch: the branch name for create, switch and delete."),
			"staged":     tools.Bool("diff: show staged changes (--cached)."),
			"count":      tools.Int("log: number of commits (default 20, max 200)."),
		}, "action"),
	}
}

// ReadOnly is false: add, commit and stash change the repository.
func (t *gitTool) ReadOnly() bool { return false }

// ReadOnlyCall reports whether a particular call only reads the repository.
// Plan modes can use it to allow git status and git log while refusing commits.
func (t *gitTool) ReadOnlyCall(raw json.RawMessage) bool {
	var a gitArgs
	if tools.Decode(raw, &a) != nil {
		return false
	}
	switch a.Action {
	case "status", "diff", "log", "show", "blame":
		return true
	case "branch":
		sub := strings.ToLower(a.SubAction)
		return sub == "" || sub == "list"
	}
	return false
}

func (t *gitTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var a gitArgs
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if env == nil {
		return tools.Errorf("git needs a project environment; this call has none")
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	sub := strings.ToLower(strings.TrimSpace(a.SubAction))

	var argv []string
	var timeout time.Duration
	switch action {
	case "status":
		argv, timeout = []string{"status", "-sb"}, gitQuickTimeout
		argv = append(argv, a.Args...)
	case "diff":
		argv, timeout = []string{"diff"}, gitShowTimeout
		if a.Staged {
			argv = append(argv, "--cached")
		}
		if a.Ref != "" {
			argv = append(argv, a.Ref)
		}
		argv = append(argv, a.Args...)
	case "log":
		n := a.Count
		if n <= 0 {
			n = 20
		}
		if n > 200 {
			n = 200
		}
		argv, timeout = []string{"log", "--oneline", "--decorate", "-n", strconv.Itoa(n)}, gitQuickTimeout
		argv = append(argv, a.Args...)
	case "show":
		ref := a.Ref
		if ref == "" {
			ref = "HEAD"
		}
		argv, timeout = []string{"show", "--stat", ref}, gitShowTimeout
		argv = append(argv, a.Args...)
	case "blame":
		if len(a.Files) == 0 && len(a.Args) == 0 {
			return tools.Errorf("blame needs a file: pass the path in files, e.g. files: [\"main.go\"].")
		}
		argv, timeout = []string{"blame"}, gitQuickTimeout
		argv = append(argv, a.Args...)
		if len(a.Files) > 0 {
			argv = append(argv, "--", a.Files[0])
		}
	case "branch":
		argv, timeout = branchArgv(sub, a.Name)
		if argv == nil {
			return tools.Errorf("branch needs sub_action list, create, switch or delete. Create and switch also need name.")
		}
		argv = append(argv, a.Args...)
	case "stash":
		argv, timeout = stashArgv(sub, a.Message, a.Files, a.Name)
		if argv == nil {
			return tools.Errorf("stash sub_action must be push, pop, list, show or drop.")
		}
		argv = append(argv, a.Args...)
	case "add":
		if !a.All && len(a.Files) == 0 {
			return tools.Errorf("add needs files: pass the paths to stage, or all: true to stage everything.")
		}
		if a.All {
			argv = []string{"add", "-A"}
		} else {
			for _, f := range a.Files {
				if name, bad := sensitive(f); bad {
					return tools.Errorf("refused: %s looks like a secret file (%s). Do not stage it. If it really belongs in the repo, ask the user to add it by hand.", f, name)
				}
			}
			argv = append([]string{"add", "--"}, a.Files...)
		}
		timeout = gitWriteTimeout
	case "commit":
		if strings.TrimSpace(a.Message) == "" {
			return tools.Errorf("commit needs message. Write a short subject line describing the change.")
		}
		if !a.NoVerify && hasArg(a.Args, "--no-verify", "-n") {
			return tools.Errorf("refused: --no-verify skips the pre-commit hooks. Pass no_verify: true only if the user asked for it.")
		}
		argv = []string{"commit", "-m", a.Message}
		if a.NoVerify {
			argv = append(argv, "--no-verify")
		}
		argv = append(argv, a.Args...)
		timeout = gitCommitTimeout
	case "":
		return tools.Errorf("action is required. One of: %s.", strings.Join(gitActions, ", "))
	default:
		return tools.Errorf("unknown git action %q. One of: %s.", a.Action, strings.Join(gitActions, ", "))
	}

	if err := requireGit(); err != nil {
		return tools.Errorf("%v", err)
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res := runProcess(tctx, procSpec{
		argv:         append([]string{"git"}, argv...),
		dir:          env.Workdir,
		env:          ScrubEnv(os.Environ()),
		collectBytes: collectLimit,
	})
	body, omitted := truncateOutput(res.output)
	text := strings.TrimRight(body, "\n")
	if text == "" {
		text = "(no output)"
	}
	if res.err != nil {
		return tools.Errorf("could not run git: %v", res.err)
	}
	if res.cancelled {
		return tools.Result{Output: text + "\n[git " + argv[0] + " timed out and was stopped]", IsError: true, Summary: "timed out"}
	}
	meta := map[string]any{"action": action, "omitted": omitted, "exit_code": res.exitCode}
	if res.exitCode != 0 {
		return tools.Result{
			Output:  fmt.Sprintf("git %s failed (exit %d):\n%s", argv[0], res.exitCode, text),
			IsError: true,
			Summary: "git " + argv[0] + " failed",
			Meta:    meta,
		}
	}
	summary := "git " + argv[0] + " ok"
	if action == "commit" {
		summary = "committed"
		text = "Committed.\n" + text
	}
	return tools.Result{Output: text, Summary: summary, Meta: meta}
}

// branchArgv returns the git argv for a branch sub-action, or nil when the
// sub-action or its name is wrong.
func branchArgv(sub, name string) ([]string, time.Duration) {
	name = strings.TrimSpace(name)
	switch sub {
	case "", "list":
		return []string{"branch", "-vv"}, gitQuickTimeout
	case "create":
		if name == "" {
			return nil, 0
		}
		return []string{"branch", "--", name}, gitQuickTimeout
	case "switch":
		if name == "" {
			return nil, 0
		}
		return []string{"switch", name}, gitWriteTimeout
	case "delete":
		if name == "" {
			return nil, 0
		}
		// -d only: git refuses to delete an unmerged branch.
		return []string{"branch", "-d", "--", name}, gitQuickTimeout
	}
	return nil, 0
}

// stashArgv returns the git argv for a stash sub-action, or nil.
func stashArgv(sub, message string, files []string, name string) ([]string, time.Duration) {
	if sub == "" {
		sub = "push"
	}
	switch sub {
	case "push":
		argv := []string{"stash", "push"}
		if strings.TrimSpace(message) != "" {
			argv = append(argv, "-m", message)
		}
		if len(files) > 0 {
			argv = append(argv, "--")
			argv = append(argv, files...)
		}
		return argv, gitWriteTimeout
	case "pop", "list", "show", "drop":
		argv := []string{"stash", sub}
		if name != "" {
			argv = append(argv, name)
		}
		return argv, gitWriteTimeout
	}
	return nil, 0
}

func hasArg(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}

// sensitive reports whether path's base name matches a secret-file pattern.
func sensitive(path string) (string, bool) {
	base := filepath.Base(filepath.ToSlash(path))
	for _, pat := range sensitiveNames {
		if ok, _ := filepath.Match(pat, base); ok {
			return pat, true
		}
	}
	return "", false
}

func requireGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed or not on PATH")
	}
	return nil
}

// ------------------------------------------------ helpers for the TUI

// gitEntry is one line of git status --porcelain=v1 -z.
type gitEntry struct {
	X, Y byte
	Path string
}

// parsePorcelain parses `git status --porcelain=v1 -z`. Rename and copy
// entries carry the original path as a following NUL-separated field, which
// is skipped.
func parsePorcelain(out string) []gitEntry {
	var entries []gitEntry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		e := gitEntry{X: f[0], Y: f[1], Path: f[3:]}
		entries = append(entries, e)
		if e.X == 'R' || e.X == 'C' {
			i++ // skip the original path
		}
	}
	return entries
}

// gitCapture runs git in dir and returns its stdout. Stderr becomes the error.
func gitCapture(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = ScrubEnv(os.Environ())
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// Status reports the repository state at dir for the TUI: the current branch
// (or a detached/unborn marker), whether anything is modified, staged or
// untracked, and commits ahead of and behind the upstream. A directory that
// is not a repository returns an error. A branch with no upstream reports 0
// ahead and 0 behind.
func Status(dir string) (branch string, dirty bool, ahead, behind int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitQuickTimeout)
	defer cancel()

	inside, err := gitCapture(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return "", false, 0, 0, errors.New("not a git repository")
	}

	if out, err := gitCapture(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && strings.TrimSpace(out) != "" {
		branch = strings.TrimSpace(out)
	} else if sha, err := gitCapture(ctx, dir, "rev-parse", "--short", "HEAD"); err == nil && strings.TrimSpace(sha) != "" {
		branch = "(detached " + strings.TrimSpace(sha) + ")"
	} else {
		branch = "(no commits)"
	}

	st, err := gitCapture(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return branch, false, 0, 0, err
	}
	dirty = len(parsePorcelain(st)) > 0

	if counts, err := gitCapture(ctx, dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			ahead, _ = strconv.Atoi(fields[0])
			behind, _ = strconv.Atoi(fields[1])
		}
	}
	return branch, dirty, ahead, behind, nil
}

// ChangedFiles lists files that differ from HEAD or are untracked, as paths
// relative to dir. Files outside dir (when dir is a subdirectory of the
// repository) are left out.
func ChangedFiles(dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitQuickTimeout)
	defer cancel()

	prefix, err := gitCapture(ctx, dir, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, errors.New("not a git repository")
	}
	prefix = strings.TrimSpace(prefix)

	st, err := gitCapture(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range parsePorcelain(st) {
		if prefix != "" {
			if !strings.HasPrefix(e.Path, prefix) {
				continue
			}
			files = append(files, strings.TrimPrefix(e.Path, prefix))
			continue
		}
		files = append(files, e.Path)
	}
	return files, nil
}
