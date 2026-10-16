package exec

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Git actions that move refs, the index or the working tree beyond what
// stage and commit do. Destructive forms of these ask the user first (see
// gitDanger and approveGit); the rest run directly.

// gitTransportArgs are flags that make git run a program named on the command
// line. The model has no business passing them, so they are refused.
var gitTransportArgs = []string{"--upload-pack", "--receive-pack", "--exec"}

// interactiveFlag matches -i, --interactive and short-flag clusters that
// contain i (such as -ri). Interactive rebase needs an editor, which a tool
// call cannot drive.
var interactiveFlag = regexp.MustCompile(`^-[A-Za-z]*i[A-Za-z]*$`)

// checkGitArgs refuses pass-through args git would run as programs.
func checkGitArgs(args []string) error {
	for _, a := range args {
		for _, bad := range gitTransportArgs {
			if a == bad || strings.HasPrefix(a, bad+"=") {
				return fmt.Errorf("refused: %s runs a program named on the command line. Pass only plain git flags.", bad)
			}
		}
	}
	return nil
}

// refName rejects a ref or name that git would read as a flag.
func refName(what, s string) error {
	if strings.HasPrefix(strings.TrimSpace(s), "-") {
		return fmt.Errorf("%s %q looks like a flag. Pass a branch, tag or commit name.", what, s)
	}
	return nil
}

// remoteArgv appends the remote and branch of push or pull.
func remoteArgv(argv []string, a gitArgs) ([]string, error) {
	if a.Name != "" && a.Remote == "" {
		return nil, errors.New("name needs remote too, e.g. remote: \"origin\", name: \"main\".")
	}
	if a.Remote != "" {
		if err := refName("remote", a.Remote); err != nil {
			return nil, err
		}
		argv = append(argv, a.Remote)
	}
	if a.Name != "" {
		if err := refName("branch", a.Name); err != nil {
			return nil, err
		}
		argv = append(argv, a.Name)
	}
	return argv, nil
}

// pushArgv builds git push. force uses --force-with-lease, which refuses when
// the remote has moved since this clone last fetched it.
func pushArgv(a gitArgs) ([]string, error) {
	if err := checkGitArgs(a.Args); err != nil {
		return nil, err
	}
	argv := []string{"push"}
	if a.Force {
		argv = append(argv, "--force-with-lease")
	}
	argv = append(argv, a.Args...)
	return remoteArgv(argv, a)
}

// pullArgv builds git pull.
func pullArgv(a gitArgs) ([]string, error) {
	if err := checkGitArgs(a.Args); err != nil {
		return nil, err
	}
	return remoteArgv(append([]string{"pull"}, a.Args...), a)
}

// resetArgv builds git reset. Without files it moves HEAD (soft, mixed or
// hard). With files it is the path form, which only moves the index.
func resetArgv(a gitArgs) ([]string, error) {
	if err := checkGitArgs(a.Args); err != nil {
		return nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	switch mode {
	case "", "mixed", "soft", "hard":
	default:
		return nil, fmt.Errorf("reset mode %q is not one of soft, mixed, hard", a.Mode)
	}
	if a.Ref != "" {
		if err := refName("ref", a.Ref); err != nil {
			return nil, err
		}
	}
	if len(a.Files) > 0 {
		if mode != "" && mode != "mixed" {
			return nil, errors.New("reset with files only moves the index; leave mode out.")
		}
		argv := []string{"reset", "-q"}
		if a.Ref != "" {
			argv = append(argv, a.Ref)
		}
		argv = append(argv, "--")
		return append(argv, a.Files...), nil
	}
	if mode == "" {
		mode = "mixed"
	}
	argv := []string{"reset", "--" + mode}
	if a.Ref != "" {
		argv = append(argv, a.Ref)
	}
	return append(argv, a.Args...), nil
}

// unstageArgv builds the reset that takes paths out of the index. It never
// touches the working tree.
func unstageArgv(a gitArgs) ([]string, error) {
	switch {
	case a.All:
		return []string{"reset", "-q"}, nil
	case len(a.Files) > 0:
		return append([]string{"reset", "-q", "--"}, a.Files...), nil
	}
	return nil, errors.New("unstage needs files: pass the paths to unstage, or all: true.")
}

// restoreArgv builds git restore for files. It also returns the danger text
// when the call would discard uncommitted edits: a working-tree restore of a
// file that differs from the index. Restoring only the index (staged) never
// loses work and is not dangerous.
func restoreArgv(ctx context.Context, dir string, a gitArgs) ([]string, string, error) {
	if len(a.Files) == 0 {
		return nil, "", errors.New("restore needs files: pass the paths to restore.")
	}
	if a.Ref != "" {
		if err := refName("ref", a.Ref); err != nil {
			return nil, "", err
		}
	}
	argv := []string{"restore"}
	if a.Staged {
		argv = append(argv, "--staged")
	}
	if a.Worktree {
		argv = append(argv, "--worktree")
	}
	if a.Ref != "" {
		argv = append(argv, "--source="+a.Ref)
	}
	argv = append(argv, "--")
	argv = append(argv, a.Files...)

	if !a.Worktree && a.Staged {
		return argv, "", nil
	}
	modified, err := gitCapture(ctx, dir, append([]string{"diff", "--name-only", "--"}, a.Files...)...)
	if err != nil {
		return nil, "", err
	}
	if names := strings.Fields(modified); len(names) > 0 {
		return argv, "restore discards uncommitted changes in " + summarize(names, 5), nil
	}
	return argv, "", nil
}

// summarize lists up to n names, then how many more there are.
func summarize(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:n], ", "), len(names)-n)
}

// tagArgv builds git tag. list is read-only; create makes a lightweight tag,
// or an annotated one when message is set; delete removes a tag.
func tagArgv(sub string, a gitArgs) ([]string, error) {
	name := strings.TrimSpace(a.Name)
	switch sub {
	case "", "list":
		return []string{"tag", "--list", "--sort=-creatordate"}, nil
	case "create":
		if name == "" {
			return nil, errors.New("tag create needs name.")
		}
		if err := refName("tag", name); err != nil {
			return nil, err
		}
		var argv []string
		if strings.TrimSpace(a.Message) != "" {
			argv = []string{"tag", "-a", name, "-m", a.Message}
		} else {
			argv = []string{"tag", name}
		}
		if a.Ref != "" {
			if err := refName("ref", a.Ref); err != nil {
				return nil, err
			}
			argv = append(argv, a.Ref)
		}
		return argv, nil
	case "delete":
		if name == "" {
			return nil, errors.New("tag delete needs name.")
		}
		if err := refName("tag", name); err != nil {
			return nil, err
		}
		return []string{"tag", "-d", name}, nil
	}
	return nil, fmt.Errorf("tag sub_action %q is not one of list, create, delete", sub)
}

// cherryPickArgv builds git cherry-pick. start needs ref; the others resume
// a pick that stopped on a conflict.
func cherryPickArgv(sub string, a gitArgs) ([]string, error) {
	if err := checkGitArgs(a.Args); err != nil {
		return nil, err
	}
	switch sub {
	case "", "start":
		if strings.TrimSpace(a.Ref) == "" {
			return nil, errors.New("cherry_pick needs ref: the commit to apply.")
		}
		if err := refName("ref", a.Ref); err != nil {
			return nil, err
		}
		return append(append([]string{"cherry-pick"}, a.Args...), a.Ref), nil
	case "abort", "continue", "skip":
		return []string{"cherry-pick", "--" + sub}, nil
	}
	return nil, fmt.Errorf("cherry_pick sub_action %q is not one of start, abort, continue, skip", sub)
}

// rebaseArgv builds a non-interactive git rebase. start needs ref, the
// branch or commit to rebase onto; the others resume or stop a rebase in
// progress. Interactive rebase is refused: it needs an editor.
func rebaseArgv(sub string, a gitArgs) ([]string, error) {
	if err := checkGitArgs(a.Args); err != nil {
		return nil, err
	}
	for _, arg := range a.Args {
		if arg == "--interactive" || (!strings.HasPrefix(arg, "--") && interactiveFlag.MatchString(arg)) {
			return nil, errors.New("refused: interactive rebase needs an editor, which a tool call cannot drive. Rebase onto a ref without -i.")
		}
	}
	switch sub {
	case "", "start":
		if strings.TrimSpace(a.Ref) == "" {
			return nil, errors.New("rebase needs ref: the branch or commit to rebase onto.")
		}
		if err := refName("ref", a.Ref); err != nil {
			return nil, err
		}
		return append(append([]string{"rebase"}, a.Args...), a.Ref), nil
	case "abort", "continue", "skip":
		return []string{"rebase", "--" + sub}, nil
	}
	return nil, fmt.Errorf("rebase sub_action %q is not one of start, abort, continue, skip", sub)
}

// gitDanger names the destructive effect of a finished argv, or "" when the
// call is not destructive. restore is classified by restoreArgv, which can
// see the working tree.
func gitDanger(action, sub string, argv []string) string {
	switch action {
	case "reset":
		if hasArg(argv, "--hard") {
			return "hard reset (discards uncommitted changes)"
		}
	case "push":
		for _, a := range argv {
			switch {
			case strings.HasPrefix(a, "--force"), a == "-f":
				return "force push (may overwrite remote history)"
			case a == "--delete", a == "-d", a == "--prune", a == "--mirror":
				return "push that deletes remote branches or tags"
			}
		}
	case "pull":
		for _, a := range argv {
			if a == "-r" || strings.HasPrefix(a, "--rebase") {
				return "pull with rebase (rewrites local history)"
			}
		}
	case "rebase":
		if sub != "abort" {
			return "rebase (rewrites history)"
		}
	}
	return ""
}

// approveGit asks the host before a destructive git call. Unlike the shell
// prompt it is not skipped in auto mode: these forms are the ones a user
// should see. With no host there is no one to ask, so the call is refused.
func approveGit(ctx context.Context, env *tools.Env, title string, argv []string) (tools.Result, bool) {
	if env.Host == nil {
		return tools.Errorf("Denied: %s. No one is available to approve it. Pick a narrower command.", title), true
	}
	rctx, reason := tools.WithReasonSlot(ctx)
	dec := env.Host.Approve(rctx, tools.Approval{
		Tool:   "git",
		Kind:   "destructive",
		Title:  title,
		Detail: "git " + strings.Join(argv, " "),
		Danger: true,
	})
	if dec == tools.Deny {
		return tools.Errorf("Denied: %s. Do not retry this command. Ask the user what they want, or use a narrower command.%s", title, tools.DeniedWithReason(reason())), true
	}
	tools.NoteApproval(ctx, reason())
	return tools.Result{}, false
}

// gitEnvOverrides stop git from opening an editor during rebase, cherry-pick
// or merge, which would hang a tool call.
var gitEnvOverrides = []string{"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true"}
