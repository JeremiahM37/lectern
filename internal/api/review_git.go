package api

// The review workspace's local git flow (docs/review.md): status with staged
// and unstaged patches, stage/unstage/discard per file and per hunk, commit
// with amend and force-with-lease, an AI commit message, "Fix with agent"
// for a failed commit hook, three-way conflict resolution and image blobs.
// Everything runs through the session's own target executor, using
// scripts/review_git.py (or its Go port, `lectern helper review-git`) so a
// remote target needs one round trip per action.
//
// Destructive actions (discarding changes, resolving or aborting a merge,
// amending a pushed commit, force pushing) need a signed-in human, the same
// rule approvals use; staging and a plain commit follow the existing commit
// button, which does not.

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

//go:embed scripts/review_git.py
var reviewGitScript string

// errReviewGit is a refusal from the git script (a stale hunk, a path outside
// the workspace, git itself failing): the caller's to fix, so 409.
type errReviewGit struct{ msg string }

func (e *errReviewGit) Error() string { return e.msg }

// runReviewGit runs one scripts/review_git.py action in dir and decodes its
// JSON result into out.
func runReviewGit(ctx context.Context, ex executor.Executor, dir, action string, params any, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	res, err := ex.Run(ctx, reviewGitCommand(ex, dir, action, base64.StdEncoding.EncodeToString(raw)), executor.RunOpts{Timeout: 90})
	if err != nil {
		return err
	}
	var probe struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(res.Stdout), &probe) != nil {
		return fmt.Errorf("could not run git on this target: %s", clipEnd(res.Stdout+res.Stderr, 300))
	}
	if !res.OK() || probe.Error != "" {
		return &errReviewGit{firstNonEmptyStr(probe.Error, "git failed")}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(res.Stdout), out)
}

// reviewGitCommand runs one review_git action on ex's target: the lectern
// helper when that target has the binary, else the Python script.
func reviewGitCommand(ex executor.Executor, dir, action, params string) string {
	q := executor.ShellQuote
	return helpers.Command(ex, "review-git", []string{dir, action, params},
		"python3 -c "+q(reviewGitScript)+" "+q(dir)+" "+q(action)+" "+q(params))
}

func respondReviewGitErr(w http.ResponseWriter, err error) {
	var rg *errReviewGit
	var ap *worktree.ErrAmendPushed
	switch {
	case errors.As(err, &rg):
		httpError(w, 409, "%s", rg.msg)
	case errors.As(err, &ap):
		writeJSON(w, 409, map[string]any{"detail": ap.Error(), "code": "amend_pushed", "refs": ap.Refs})
	case errors.Is(err, errNotGitRepo):
		httpError(w, 409, "this session's workdir is not a git repository")
	default:
		respondGitErr(w, err)
	}
}

func respondDiffErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotGitRepo) {
		httpError(w, 409, "this session's workdir is not a git repository")
		return
	}
	respondErr(w, err)
}

// reviewRepo resolves the session, its executor and the one repository a git
// call acts on (?repo= or the body's "repo" for a grouped workspace).
func (s *Server) reviewRepo(w http.ResponseWriter, r *http.Request, repo string) (*store.Session, executor.Executor, sessionCommitTarget, bool) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return nil, nil, sessionCommitTarget{}, false
	}
	ex, err := s.sessionExecutor(row)
	if err != nil {
		respondErr(w, err)
		return nil, nil, sessionCommitTarget{}, false
	}
	target, err := s.resolveSessionCommitTarget(r.Context(), ex, row, repo)
	if err != nil {
		respondReviewGitErr(w, err)
		return nil, nil, sessionCommitTarget{}, false
	}
	return row, ex, target, true
}

func liveSession(w http.ResponseWriter, row *store.Session) bool {
	if row.Status == sessions.StatusDead || row.EndedAt != nil {
		httpError(w, 409, "this session is ended or untracked; restore tracking first")
		return false
	}
	return true
}

// gitStatus is GET /api/sessions/{id}/git.
func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request) {
	row, ex, target, ok := s.reviewRepo(w, r, r.URL.Query().Get("repo"))
	if !ok {
		return
	}
	var out map[string]any
	if err := runReviewGit(r.Context(), ex, target.dir, "status", map[string]any{}, &out); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	out["base"] = target.base
	out["on_base_branch"] = refuseOnBaseBranch(target.branch, target.base) != nil
	// Push starts unticked when there is nowhere to push to.
	remotes, _ := ex.Run(r.Context(), "git remote", executor.RunOpts{Cwd: target.dir, Timeout: 15})
	out["has_remote"] = remotes.OK() && strings.TrimSpace(remotes.Stdout) != ""
	// own_checkout: the session works in the person's own folder (no Lectern
	// worktree), so a new branch is checked out right there and they are told
	// so first. dir names that folder.
	out["own_checkout"] = row.WorktreeJSON == ""
	out["dir"] = target.dir
	out["suggested_message"] = suggestCommitMessage(row.LastPromptExcerpt, out["files"])
	out["session_live"] = row.Status != sessions.StatusDead && row.EndedAt == nil
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

type gitPathsIn struct {
	Repo  string   `json:"repo"`
	Paths []string `json:"paths"`
}

// gitIndexAction is POST /api/sessions/{id}/git/{stage|unstage|discard}.
func (s *Server) gitIndexAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "stage" && action != "unstage" && action != "discard" {
		httpError(w, 404, "not found")
		return
	}
	if action == "discard" && !s.humanPrincipal(w, r, "discarding changes") {
		return
	}
	var body gitPathsIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	_, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok {
		return
	}
	var out map[string]any
	if err := runReviewGit(r.Context(), ex, target.dir, action, map[string]any{"paths": body.Paths}, &out); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type gitHunkIn struct {
	Repo        string `json:"repo"`
	Path        string `json:"path"`
	Op          string `json:"op"` // stage | unstage | discard
	Index       int    `json:"index"`
	Fingerprint string `json:"fingerprint"`
	// Lines, when set, applies only these lines of the hunk: indices into
	// the hunk's body (the lines after its @@ header).
	Lines []int `json:"lines,omitempty"`
}

// gitHunk is POST /api/sessions/{id}/git/hunk.
func (s *Server) gitHunk(w http.ResponseWriter, r *http.Request) {
	var body gitHunkIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if body.Op == "discard" && !s.humanPrincipal(w, r, "discarding changes") {
		return
	}
	_, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok {
		return
	}
	params := map[string]any{"op": body.Op, "path": body.Path, "index": body.Index, "fingerprint": body.Fingerprint}
	if body.Lines != nil {
		params["lines"] = body.Lines
	}
	if err := runReviewGit(r.Context(), ex, target.dir, "hunk", params, nil); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

type gitCommitIn struct {
	Repo    string `json:"repo"`
	Message string `json:"message"`
	// StageAll commits every change; otherwise only what is staged.
	StageAll         bool   `json:"stage_all"`
	Amend            bool   `json:"amend"`
	AllowPushedAmend bool   `json:"allow_pushed_amend"`
	Push             bool   `json:"push"`
	ForceWithLease   bool   `json:"force_with_lease"`
	Lease            string `json:"lease"`
	PR               bool   `json:"pr"`
	PRTitle          string `json:"pr_title"`
	PRBody           string `json:"pr_body"`
	// A session working straight on the default branch (every session started
	// without a worktree) has two ways to commit, and must pick one: NewBranch
	// creates that branch from the current state first and commits there; a
	// taken name gets a -2, -3… suffix. AllowBaseBranch commits onto the
	// default branch itself, which needs a signed-in person.
	NewBranch       string `json:"new_branch"`
	AllowBaseBranch bool   `json:"allow_base_branch"`
}

// newBranchScript creates and switches to a branch for "Commit on a new
// branch", printing the name it used. Exit 3 is a name git refuses.
const newBranchScript = `n=__NAME__
git check-ref-format --branch "$n" >/dev/null 2>&1 || { echo "not a valid branch name" >&2; exit 3; }
b=$n; i=2
while git show-ref --verify --quiet "refs/heads/$b"; do b="$n-$i"; i=$((i + 1)); [ "$i" -gt 50 ] && { echo "too many branches named $n" >&2; exit 4; }; done
git switch -q -c "$b" && printf '%s' "$b"`

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// hookFailureRe matches output that names a hook runner, for repositories
// whose hooks live outside .git/hooks (husky, lint-staged, pre-commit).
var hookFailureRe = regexp.MustCompile(`(?i)\b(pre-commit|commit-msg|husky|lint-staged|lefthook)\b`)

// activeCommitHooks lists the commit-time hooks installed in dir.
func activeCommitHooks(ctx context.Context, ex executor.Executor, dir string) []string {
	res, err := ex.Run(ctx, `d=$(git rev-parse --git-path hooks) && for h in pre-commit prepare-commit-msg commit-msg; do [ -x "$d/$h" ] && echo "$h"; done; true`,
		executor.RunOpts{Cwd: dir, Timeout: 15})
	if err != nil {
		return nil
	}
	return strings.Fields(res.Stdout)
}

// gitIdentityScript fails exactly when `git commit` would stop with "Please
// tell me who you are".
const gitIdentityScript = "git var GIT_AUTHOR_IDENT >/dev/null 2>&1 && git var GIT_COMMITTER_IDENT >/dev/null 2>&1"

// gitCommit is POST /api/sessions/{id}/git/commit. A commit that runs and
// fails answers 200 with the failed step, plus hook_failure when a commit
// hook rejected it, so the client can offer "Fix with agent".
func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	var body gitCommitIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		httpError(w, 422, "a commit message is required")
		return
	}
	if body.Lease != "" && !shaRe.MatchString(body.Lease) {
		httpError(w, 422, "lease must be a commit id")
		return
	}
	if (body.ForceWithLease || (body.Amend && body.AllowPushedAmend)) &&
		!s.humanPrincipal(w, r, "rewriting pushed history") {
		return
	}
	row, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok || !liveSession(w, row) {
		return
	}
	// A brand-new machine often has no git name and email. Say so before
	// anything moves: otherwise "Commit on a new branch" switches the
	// person's folder and only then fails at the commit itself.
	if res, err := ex.Run(r.Context(), gitIdentityScript, executor.RunOpts{Cwd: target.dir, Timeout: 15}); err == nil && !res.OK() {
		writeJSON(w, 409, map[string]any{"detail": `git does not know your name and email yet, so it cannot commit. Run git config --global user.name "Your Name" and git config --global user.email you@example.com, then try again.`, "code": "no_git_identity"})
		return
	}
	if err := refuseOnBaseBranch(target.branch, target.base); err != nil {
		switch {
		case strings.TrimSpace(body.NewBranch) != "":
			if row.WorktreeJSON != "" {
				httpError(w, 409, "this session has its own worktree; commit on its branch")
				return
			}
			res, runErr := ex.Run(r.Context(), strings.Replace(newBranchScript, "__NAME__", executor.ShellQuote(strings.TrimSpace(body.NewBranch)), 1),
				executor.RunOpts{Cwd: target.dir, Timeout: 30})
			if runErr != nil {
				respondErr(w, runErr)
				return
			}
			if !res.OK() {
				httpError(w, 409, "could not create the branch: %s", strings.TrimSpace(res.Stderr+res.Stdout))
				return
			}
			previous := target.branch
			target.branch = strings.TrimSpace(body.NewBranch)
			if made := strings.TrimSpace(res.Stdout); made != "" {
				target.branch = made
			}
			// A name that says which branch it is on ("myapp · main", as
			// `lectern claude` names one) follows the folder to its new branch.
			if suffix := " · " + previous; previous != "" && strings.HasSuffix(row.Name, suffix) {
				renamed := strings.TrimSuffix(row.Name, suffix) + " · " + target.branch
				if s.DB.Update("sessions", row.ID, map[string]any{"name": renamed, "updated_at": store.Now()}) == nil {
					if fresh, err := s.DB.Session(row.ID); err == nil {
						s.Bus.Publish("board", "session", s.sessionView(fresh))
					}
				}
			}
		case body.AllowBaseBranch:
			if !s.humanPrincipal(w, r, "committing directly to the default branch") {
				return
			}
		default:
			writeJSON(w, 409, map[string]any{"detail": err.Error(), "code": "on_base_branch", "branch": target.branch})
			return
		}
	}
	if body.Amend && target.base != "" {
		// Before the session's first commit, HEAD is the base branch's own
		// commit: amending it would rewrite the base, not this work.
		res, err := ex.Run(r.Context(), "git merge-base --is-ancestor HEAD "+executor.ShellQuote(target.base),
			executor.RunOpts{Cwd: target.dir, Timeout: 30})
		if err == nil && res.OK() {
			httpError(w, 409, "the last commit belongs to %s, not to this session; make a new commit instead", target.base)
			return
		}
	}
	hooks := activeCommitHooks(r.Context(), ex, target.dir)
	steps, err := worktree.Commit(r.Context(), ex, target.dir, target.branch, worktree.CommitOptions{
		Message: body.Message, StageAll: body.StageAll, Amend: body.Amend, AllowPushedAmend: body.AllowPushedAmend,
		Push: body.Push, ForceWithLease: body.ForceWithLease, Lease: body.Lease,
		PR: body.PR, PRTitle: body.PRTitle,
		PRBody: orDefault(body.PRBody, fmt.Sprintf("Created by lectern session %d.", row.ID)),
	})
	var se *worktree.StepError
	if errors.As(err, &se) && len(steps) > 0 {
		out := map[string]any{"steps": steps, "failed": "commit", "detail": se.Msg, "branch": target.branch}
		output, _ := steps[0]["output"].(string)
		if se.Msg != "commit failed: nothing to commit" && (len(hooks) > 0 || hookFailureRe.MatchString(output)) {
			out["hook_failure"] = map[string]any{"hooks": hooks, "output": output}
		}
		writeJSON(w, 200, out)
		return
	}
	if err != nil {
		respondReviewGitErr(w, err)
		return
	}
	sessionID := row.ID
	ci := s.armCIFromSteps(ciloop.Owner{SessionID: &sessionID, ProjectID: target.projectID,
		TargetID: row.TargetID, Branch: target.branch}, steps)
	s.Bus.Publish(fmt.Sprintf("session:%d", row.ID), "git", map[string]any{"steps": steps})
	writeJSON(w, 200, map[string]any{"steps": steps, "ci": ci, "branch": target.branch})
}

// gitPush is POST /api/sessions/{id}/git/push: push the branch, plainly or
// with --force-with-lease pinned to the remote commit the client last saw.
func (s *Server) gitPush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo           string `json:"repo"`
		ForceWithLease bool   `json:"force_with_lease"`
		Lease          string `json:"lease"`
	}
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if body.Lease != "" && !shaRe.MatchString(body.Lease) {
		httpError(w, 422, "lease must be a commit id")
		return
	}
	if body.ForceWithLease && !s.humanPrincipal(w, r, "force pushing") {
		return
	}
	row, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok || !liveSession(w, row) {
		return
	}
	if err := refuseOnBaseBranch(target.branch, target.base); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	var step map[string]any
	var err error
	if body.ForceWithLease {
		step, err = worktree.ForcePushWithLease(r.Context(), ex, target.dir, target.branch, body.Lease)
	} else {
		step, err = worktree.Push(r.Context(), ex, target.dir, target.branch)
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"steps": []map[string]any{step}})
}

// ---- AI commit message ------------------------------------------------------

func buildCommitMessagePrompt(diff string) string {
	if len(diff) > 60000 {
		diff = diff[:60000] + "\n... (diff truncated)"
	}
	return "Write a git commit message for the change below.\n" +
		"The first line is in the imperative mood, at most 72 characters, with no " +
		"trailing period. Add a blank line and a short body wrapped at 72 columns " +
		"only when it helps explain why. Reply with ONLY the message: no code " +
		"fences, no quotes, no commentary and no Co-authored-by trailers.\n\nDIFF:\n" + diff
}

// cleanCommitMessage strips what a model adds despite being asked not to.
func cleanCommitMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if strings.HasPrefix(msg, "```") {
		msg = strings.TrimPrefix(msg, "```")
		if i := strings.Index(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		msg = strings.TrimSuffix(strings.TrimSpace(msg), "```")
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(msg), "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "co-authored-by:") {
			continue
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	return strings.Trim(strings.TrimSpace(strings.Join(kept, "\n")), "\"'`")
}

// commitMessageModel picks the headless agent and model: the configured
// commit_message_agent/_model settings first, then the session's own agent
// (when it has a headless mode), then the PR-description summary settings.
func (s *Server) commitMessageModel(row *store.Session) (agent, model string) {
	agent = s.DB.Setting("commit_message_agent")
	model = s.DB.Setting("commit_message_model")
	if agent == "" {
		switch row.Agent {
		case "claude", "codex":
			agent = row.Agent
		default:
			agent = firstNonEmptyStr(s.DB.Setting("summary_agent"), "claude")
		}
	}
	if model == "" {
		switch agent {
		case "claude":
			model = "haiku"
			if s.DB.Setting("summary_agent") == "" || s.DB.Setting("summary_agent") == "claude" {
				model = firstNonEmptyStr(s.DB.Setting("summary_model"), "haiku")
			}
		case row.Agent:
			model = row.Model
		}
	}
	return agent, model
}

// gitCommitMessage is POST /api/sessions/{id}/git/commit-message.
func (s *Server) gitCommitMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
	}
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	row, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok {
		return
	}
	res, err := ex.Run(r.Context(), "git diff --cached --no-color --no-ext-diff", executor.RunOpts{Cwd: target.dir, Timeout: 60})
	if err != nil {
		respondErr(w, err)
		return
	}
	diff := res.Stdout
	if strings.TrimSpace(diff) == "" {
		res, err = ex.Run(r.Context(), worktree.IntentToAddUntracked(target.dir)+" && git diff HEAD --no-color --no-ext-diff", executor.RunOpts{Cwd: target.dir, Timeout: 60})
		if err != nil {
			respondErr(w, err)
			return
		}
		diff = res.Stdout
	}
	if strings.TrimSpace(diff) == "" {
		httpError(w, 422, "there are no changes to describe")
		return
	}
	agent, model := s.commitMessageModel(row)
	raw, err := s.runSummary(r.Context(), ex, agent, model, buildCommitMessagePrompt(diff))
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	msg := cleanCommitMessage(raw)
	if msg == "" {
		httpError(w, 502, "the model returned an empty message")
		return
	}
	writeJSON(w, 200, map[string]any{"message": msg, "agent": agent, "model": model})
}

// ---- commit hook failure → agent -------------------------------------------

// clipMiddle keeps the start and (mostly) the end of long hook output, where
// the summary line usually is.
func clipMiddle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head := n * 35 / 100
	return s[:head] + "\n… (output shortened) …\n" + s[len(s)-(n-head):]
}

func buildFixHookPrompt(dir, message string, hooks []string, output string) string {
	which := "a commit hook"
	if len(hooks) > 0 {
		which = "its " + strings.Join(hooks, "/") + " hook"
	}
	return fmt.Sprintf("A git commit in %s was rejected by %s. Please fix what the hook "+
		"reports.\n\nRules: start with `git status`; do not use --no-verify; do not reset, "+
		"checkout, clean or stash; do not commit, push or open a PR (I will commit again). "+
		"Stage only the files you fix, then run the failing hook's checks to confirm they pass.\n\n"+
		"Attempted commit message: %s\n\nHook output:\n```\n%s\n```\n\n"+
		"Reply with the root cause, the files you changed and what you ran to confirm the fix.\n",
		dir, which, strings.TrimSpace(message), strings.TrimSpace(clipMiddle(output, 12000)))
}

// gitFixHook is POST /api/sessions/{id}/git/fix-hook: send a failed commit
// hook's output to the session's agent.
func (s *Server) gitFixHook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo    string   `json:"repo"`
		Message string   `json:"message"`
		Output  string   `json:"output"`
		Hooks   []string `json:"hooks"`
	}
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(body.Output) == "" {
		httpError(w, 422, "hook output is required")
		return
	}
	row, _, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok || !liveSession(w, row) {
		return
	}
	prompt := buildFixHookPrompt(target.dir, body.Message, body.Hooks, body.Output)
	if err := s.Sessions.SendText(r.Context(), row.ID, prompt); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"sent": true})
}

// ---- merge conflicts --------------------------------------------------------

// gitConflicts is GET /api/sessions/{id}/git/conflicts[?path=].
func (s *Server) gitConflicts(w http.ResponseWriter, r *http.Request) {
	_, ex, target, ok := s.reviewRepo(w, r, r.URL.Query().Get("repo"))
	if !ok {
		return
	}
	params := map[string]any{}
	if p := r.URL.Query().Get("path"); p != "" {
		params["path"] = p
	}
	var out map[string]any
	if err := runReviewGit(r.Context(), ex, target.dir, "conflicts", params, &out); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

type gitResolveIn struct {
	Repo    string  `json:"repo"`
	Path    string  `json:"path"`
	Content *string `json:"content"`
	// Take resolves the whole file to one side ("ours" or "theirs").
	Take         string `json:"take"`
	AllowMarkers bool   `json:"allow_markers"`
}

// gitResolve is POST /api/sessions/{id}/git/resolve: write the resolved file
// and stage it. The content travels through a staged temp file on the
// target (the same way SendText stages a prompt), never the command line.
func (s *Server) gitResolve(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "resolving a merge conflict") {
		return
	}
	var body gitResolveIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if (body.Content == nil) == (body.Take == "") || (body.Take != "" && body.Take != "ours" && body.Take != "theirs") {
		httpError(w, 422, "send either the resolved content or take: ours|theirs")
		return
	}
	row, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok {
		return
	}
	params := map[string]any{"path": body.Path, "allow_markers": body.AllowMarkers}
	if body.Take != "" {
		params["take"] = body.Take
	} else {
		staged := fmt.Sprintf("/tmp/lectern-resolve-%d-%d", row.ID, time.Now().UnixNano())
		if err := ex.WriteFile(r.Context(), staged, []byte(*body.Content)); err != nil {
			respondErr(w, err)
			return
		}
		params["staged_file"] = staged
		defer ex.Run(context.WithoutCancel(r.Context()), "rm -f "+executor.ShellQuote(staged), executor.RunOpts{Timeout: 10})
	}
	var out map[string]any
	if err := runReviewGit(r.Context(), ex, target.dir, "resolve", params, &out); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// gitAbort is POST /api/sessions/{id}/git/abort: abandon the merge, rebase
// or cherry-pick in progress.
func (s *Server) gitAbort(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "aborting a merge") {
		return
	}
	var body gitPathsIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	_, ex, target, ok := s.reviewRepo(w, r, body.Repo)
	if !ok {
		return
	}
	var out map[string]any
	if err := runReviewGit(r.Context(), ex, target.dir, "abort", map[string]any{}, &out); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// ---- image blobs --------------------------------------------------------------

var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".bmp": "image/bmp", ".ico": "image/x-icon", ".avif": "image/avif",
	".svg": "image/svg+xml",
}

// gitBlob is GET /api/sessions/{id}/git/blob?path=&side=old|new&ref=: one
// side of an image for the image diff, as base64 JSON so the page can show
// it as a data: URL (no script ever runs from an SVG shown that way).
func (s *Server) gitBlob(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mime, ok := imageTypes[strings.ToLower(path.Ext(q.Get("path")))]
	if !ok {
		httpError(w, 422, "only images are served here")
		return
	}
	side := q.Get("side")
	if side != "old" && side != "new" {
		httpError(w, 422, "side must be old or new")
		return
	}
	ref := q.Get("ref")
	if ref != "" && ref != "HEAD" && !shaRe.MatchString(ref) {
		httpError(w, 422, "ref must be a commit id")
		return
	}
	_, ex, target, ok2 := s.reviewRepo(w, r, q.Get("repo"))
	if !ok2 {
		return
	}
	var out map[string]any
	if err := runReviewGit(r.Context(), ex, target.dir, "blob",
		map[string]any{"path": q.Get("path"), "side": side, "ref": firstNonEmptyStr(ref, "HEAD")}, &out); err != nil {
		respondReviewGitErr(w, err)
		return
	}
	out["mime"] = mime
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// suggestCommitMessage is the commit message the form starts with: what the
// person last asked the agent for, or else the files that changed — never the
// session's name (re-audit N5).
func suggestCommitMessage(lastPrompt string, files any) string {
	if prompt := strings.TrimSpace(strings.SplitN(lastPrompt, "\n", 2)[0]); prompt != "" {
		runes := []rune(prompt)
		if len(runes) > 72 {
			runes = append(runes[:71], '…')
		}
		return strings.ToUpper(string(runes[:1])) + string(runes[1:])
	}
	var names []string
	rows, _ := files.([]any)
	for _, raw := range rows {
		if f, ok := raw.(map[string]any); ok {
			if p, _ := f["path"].(string); p != "" {
				names = append(names, path.Base(p))
			}
		}
	}
	switch {
	case len(names) == 0:
		return ""
	case len(names) > 3:
		return fmt.Sprintf("Update %s and %d more", strings.Join(names[:3], ", "), len(names)-3)
	default:
		return "Update " + strings.Join(names, ", ")
	}
}
