//go:build unix

package helpers

// Port of internal/worktree/multi_preflight.py: read-only validation of a
// grouped allocation before it is persisted. worktree-group repeats it under
// its operation lock. argv: check-create PLAN_JSON.

import (
	"io"
	"os"
	"strings"
	"syscall"
)

func init() {
	Register("worktree-preflight", func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		if len(args) < 1 {
			return wtUncaught(stderr, &wtTypeError{"list index out of range"})
		}
		var plan *pyObj
		err := wtTry(func() {
			if args[0] != "check-create" {
				wtValue("Unknown workspace preflight operation")
			}
			if len(args) < 2 {
				wtRaise(&wtTypeError{"list index out of range"})
			}
			plan = wtPreflight(wtPlan(args[1]), 0)
		})
		if err != nil {
			io.WriteString(stdout, pyDumps(newObj("error", err.Error()))+"\n")
			return 1
		}
		io.WriteString(stdout, pyDumps(newObj("workspace", plan))+"\n")
		return 0
	})
}

// preflightGit is the preflight's git: stripped output, ValueError on failure.
func preflightGit(repo string, args ...string) string {
	res, err := pyRun{Argv: append([]string{"git", "--no-optional-locks", "-C", repo}, args...), Timeout: 10, TimeoutText: "10"}.run()
	wtMust(err)
	o, e, err := decodePair(res)
	wtMust(err)
	if res.RC != 0 {
		wtValue(firstNonEmpty(pyStrip(e), pyStrip(o), "Git command failed"))
	}
	return pyStrip(o)
}

// wtPreflight is preflight(plan, existing); the first existing repositories
// are already allocated and are checked rather than planned.
func wtPreflight(planValue any, existing int) *pyObj {
	plan := wtObj(planValue)
	var repositories []any
	if v, ok := plan.Get("repositories"); ok {
		l, isList := v.([]any)
		if !isList {
			wtRaise(&wtTypeError{"object of type '" + pyTypeName(v) + "' has no len()"})
		}
		repositories = l
	}
	if len(repositories) < 1 || len(repositories) > 8 {
		wtValue("A workspace needs between one and eight repositories")
	}
	root := wtString(plan, "path")
	if !pyIsAbs(root) {
		wtValue("Workspace paths must be absolute")
	}
	if existing == 0 && pyLexists(root) {
		wtValue("Workspace path already exists; nothing was changed")
	}
	root = pyRealpath(root)
	token := wtItem(plan, "token")
	paths, commons, tokens := map[string]bool{}, map[string]bool{}, []any{token}
	if !pyTruthy(token) {
		wtValue("Workspace ownership is missing")
	}
	hasToken := func(v any) bool {
		for _, t := range tokens {
			if pyEqual(t, v) {
				return true
			}
		}
		return false
	}
	for index, entryValue := range repositories {
		child := wtObj(wtItem(entryValue, "worktree"))
		if pyTruthy(child.Val("repositories")) {
			wtValue("Nested repository groups are not supported")
		}
		repoArg := wtString(child, "repo")
		if !pyIsAbs(repoArg) {
			wtValue("Repository paths must be absolute")
		}
		pathArg := wtString(child, "path")
		if !pyIsAbs(pathArg) {
			wtValue("Repository paths must be absolute")
		}
		dest := pyRealpath(pathArg)
		if pyDirname(dest) != root || paths[dest] {
			wtValue("Repository paths must be distinct children of the workspace")
		}
		if index >= existing && pyLexists(pathArg) {
			wtValue("Repository allocation already exists")
		}
		childToken := wtItem(child, "token")
		if !pyTruthy(childToken) || hasToken(childToken) {
			wtValue("Each repository needs separate ownership")
		}
		if !pyEqual(wtItem(child, "branch"), wtItem(plan, "branch")) {
			wtValue("Repository branch must match the workspace branch")
		}
		repo := pyRealpath(repoArg)
		common := pyRealpath(preflightGit(repo, "rev-parse", "--path-format=absolute", "--git-common-dir"))
		if commons[common] {
			wtValue("The same Git repository was selected more than once")
		}
		if index < existing {
			if pyIsLink(pathArg) || preflightGit(dest, "rev-parse", "--show-toplevel") != dest {
				wtValue("An existing checkout path was replaced")
			}
			if pyRealpath(preflightGit(dest, "rev-parse", "--path-format=absolute", "--git-common-dir")) != common {
				wtValue("An existing checkout repository changed")
			}
			if !pyEqual(preflightGit(dest, "symbolic-ref", "--quiet", "--short", "HEAD"), wtItem(child, "branch")) {
				wtValue("An existing checkout branch changed")
			}
			owner := pathlibJoin(preflightGit(dest, "rev-parse", "--absolute-git-dir"), "lectern-owner")
			if !ownerMatches(owner, childToken) {
				wtValue("An existing checkout owner changed")
			}
			commons[common], paths[dest] = true, true
			tokens = append(tokens, childToken)
			continue
		}
		branch := wtString(child, "branch")
		preflightGit(repo, "check-ref-format", "--branch", branch)
		preflightGit(repo, "check-ref-format", "refs/heads/"+branch)
		// rev-parse --verify is read-only; show-ref --verify distinguishes a
		// missing branch from an invalid repository/other unexpected error.
		res, err := pyRun{Argv: []string{"git", "--no-optional-locks", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/" + branch},
			Timeout: 10, TimeoutText: "10"}.run()
		wtMust(err)
		_, errText, err := decodePair(res)
		wtMust(err)
		if res.RC == 0 {
			wtValue("Workspace branch already exists in repository: " + wtString(entryValue, "name"))
		}
		if res.RC != 1 {
			wtValue(firstNonEmpty(pyStrip(errText), "Could not check workspace branch"))
		}
		base := wtString(child, "base")
		if base == "" || strings.HasPrefix(base, "-") {
			wtValue("Choose a branch, tag or commit as the base")
		}
		commit := preflightGit(repo, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
		child.Update(newObj("repo", repo, "path", dest, "commit", commit))
		commons[common], paths[dest] = true, true
		tokens = append(tokens, childToken)
	}
	first := wtObj(wtItem(repositories[0], "worktree"))
	plan.Update(newObj("path", root, "repo", wtItem(first, "repo"), "commit", wtItem(first, "commit")))
	return plan
}

// ownerMatches opens the owner file without following a link and compares
// it with the token, as the scripts do.
func ownerMatches(owner string, token any) bool {
	f, err := os.OpenFile(owner, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	wtMust(pyErr(err, owner))
	defer f.Close()
	info, err := f.Stat()
	wtMust(pyErr(err))
	if !info.Mode().IsRegular() {
		return false
	}
	data, err := io.ReadAll(f)
	wtMust(pyErr(err))
	text, err := wtReadText(data)
	wtMust(err)
	return pyEqual(text, token)
}
