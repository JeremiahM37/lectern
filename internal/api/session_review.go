// Review and merge: live diffs, commit/push/PR and inline review comments,
// shared between tasks (finished attempts) and sessions (long-running
// interactive worktrees). See docs/agent-events.md section 4.
//
// Named session_review.go (not review.go) because review.go already exists —
// the terminal workspace's "changes" file browser, an unrelated feature.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// ---- diff (shared by tasks and sessions) ---------------------------------

// maxDiffPatchBytes caps one repository's live patch so a runaway diff can't
// wedge the viewer or blow up the response; the UI is told when this happens.
const maxDiffPatchBytes = 512 << 10

// repoDiff is one repository's live diff. Tasks and single-repo sessions
// return exactly one; a grouped session workspace returns one per repository.
type repoDiff struct {
	Name      string               `json:"name,omitempty"`
	ProjectID *int64               `json:"project_id,omitempty"`
	BaseRef   string               `json:"base_ref"`
	Stats     []worktree.FileStat  `json:"stats"`
	Files     []worktree.FilePatch `json:"files"`
	Truncated bool                 `json:"truncated"`
}

var errNotGitRepo = errors.New("not a git repository")

// liveDiffRepo computes one repository's diff against the merge-base with
// baseRef, live — including uncommitted and untracked changes — the same
// worktree.CaptureDiff/SplitPatch tasks use to capture a diff at attempt
// completion, just run live instead of read back from a stored patch.
func liveDiffRepo(ctx context.Context, ex executor.Executor, dir, baseRef string) (repoDiff, error) {
	if !worktree.IsGitRepo(ctx, ex, dir) {
		return repoDiff{}, errNotGitRepo
	}
	ref := baseRef
	if mb, err := worktree.MergeBase(ctx, ex, dir, baseRef); err == nil && strings.TrimSpace(mb) != "" {
		ref = strings.TrimSpace(mb)
	}
	patch, stats, err := worktree.CaptureDiff(ctx, ex, dir, ref)
	if err != nil {
		return repoDiff{}, err
	}
	truncated := false
	if len(patch) > maxDiffPatchBytes {
		patch = patch[:maxDiffPatchBytes]
		truncated = true
	}
	return repoDiff{BaseRef: ref, Stats: stats, Files: worktree.SplitPatch(patch), Truncated: truncated}, nil
}

// resolveBaseRef mirrors the fallback tasks already use in scheduler.go
// (firstNonEmpty(Task.BaseBranch, Project.DefaultBaseBranch, "main")): prefer
// a real branch name over the literal "HEAD" a worktree plan stores when the
// launcher was given no base — that string only ever meant something at the
// exact moment the worktree was created, not afterward.
func resolveBaseRef(planBase string, proj *store.Project) string {
	if planBase != "" && planBase != "HEAD" {
		return planBase
	}
	if proj != nil && proj.DefaultBaseBranch != "" {
		return proj.DefaultBaseBranch
	}
	return "main"
}

func repoName(row *store.Session, proj *store.Project) string {
	if proj != nil {
		return proj.Name
	}
	return row.Name
}

func (s *Server) projectOrNil(id *int64) *store.Project {
	if id == nil {
		return nil
	}
	p, err := s.DB.Project(*id)
	if err != nil {
		return nil
	}
	return p
}

// sessionRepoDiffs resolves a session's live diff(s): every repository in a
// grouped workspace (see worktree_json / worktree.Interactive.Repositories),
// the session's single worktree, or — for a session with no dedicated
// worktree at all, running straight in a project checkout — the workdir
// itself. A repository that isn't checked out yet (mid workspace-setup) is
// skipped rather than failing the whole request.
func (s *Server) sessionRepoDiffs(ctx context.Context, ex executor.Executor, row *store.Session) ([]repoDiff, error) {
	proj := s.projectOrNil(row.ProjectID)
	var plan worktree.Interactive
	hasPlan := row.WorktreeJSON != "" && json.Unmarshal([]byte(row.WorktreeJSON), &plan) == nil

	var repos []repoDiff
	switch {
	case hasPlan && len(plan.Repositories) > 0:
		for _, repo := range plan.Repositories {
			if repo.Worktree == nil {
				continue
			}
			d, err := liveDiffRepo(ctx, ex, repo.Worktree.Path,
				resolveBaseRef(repo.Worktree.Base, s.projectOrNil(repo.ProjectID)))
			if err != nil {
				if errors.Is(err, errNotGitRepo) {
					continue
				}
				return nil, err
			}
			d.Name, d.ProjectID = repo.Name, repo.ProjectID
			repos = append(repos, d)
		}
	case hasPlan:
		d, err := liveDiffRepo(ctx, ex, plan.Path, resolveBaseRef(plan.Base, proj))
		if err != nil {
			return nil, err
		}
		d.Name, d.ProjectID = repoName(row, proj), row.ProjectID
		repos = append(repos, d)
	default:
		if row.Workdir == "" {
			return nil, errNotGitRepo
		}
		d, err := liveDiffRepo(ctx, ex, row.Workdir, resolveBaseRef("", proj))
		if err != nil {
			return nil, err
		}
		d.Name, d.ProjectID = repoName(row, proj), row.ProjectID
		repos = append(repos, d)
	}
	return repos, nil
}

func (s *Server) sessionExecutor(row *store.Session) (executor.Executor, error) {
	target, err := s.DB.Target(row.TargetID)
	if err != nil {
		return nil, err
	}
	return s.Reg.For(target)
}

func (s *Server) sessionDiff(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	ex, err := s.sessionExecutor(row)
	if err != nil {
		respondErr(w, err)
		return
	}
	repos, err := s.sessionRepoDiffs(r.Context(), ex, row)
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			httpError(w, 409, "this session's workdir is not a git repository")
			return
		}
		respondErr(w, err)
		return
	}
	if repos == nil {
		repos = []repoDiff{}
	}
	writeJSON(w, 200, map[string]any{"repos": repos})
}

// ---- commit / push / PR (shared by tasks and sessions) --------------------

type commitIn struct {
	Message string `json:"message"`
	Push    bool   `json:"push"`
	PR      bool   `json:"pr"`
	// PRTitle/PRBody default to Message and a generic "created by lectern"
	// note when empty — a session's "Generate description" button fills them.
	PRTitle string `json:"pr_title"`
	PRBody  string `json:"pr_body"`
}

// gitStepError is a git/gh step that ran and failed on its own terms (bad
// commit, a real merge conflict) as opposed to the executor itself failing —
// the former is the caller's to fix and maps to 409, the latter is ours and
// maps through respondErr like any other infrastructure error.
type gitStepError struct {
	status int
	msg    string
}

func (e *gitStepError) Error() string { return e.msg }

// gitCommitPushPR runs `git add -A && git commit`, an optional push and an
// optional `gh pr create`, in dir on branch. It is the one place both the
// task and session commit endpoints touch git, so a fix to one path fixes
// both. A push or PR step that runs but fails is recorded in steps and does
// not abort — exactly as the original task-only implementation behaved —
// except a failed push always cancels a requested PR (never open a PR for a
// branch that didn't reach origin).
func gitCommitPushPR(ctx context.Context, ex executor.Executor, dir, branch, message string,
	push, pr bool, prTitle, prBody string) ([]map[string]any, error) {
	q := executor.ShellQuote
	steps := []map[string]any{}

	res, err := ex.Run(ctx, "git add -A && git commit -m "+q(message), executor.RunOpts{Cwd: dir, Timeout: 60})
	if err != nil {
		return steps, err
	}
	output := clipEnd(res.Stdout+res.Stderr, 800)
	steps = append(steps, map[string]any{"step": "commit", "rc": res.RC, "output": output})
	if !res.OK() {
		detail := output
		if strings.Contains(res.Stdout+res.Stderr, "nothing to commit") {
			detail = "nothing to commit"
		}
		return steps, &gitStepError{409, "commit failed: " + detail}
	}

	if push {
		res, err := ex.Run(ctx, "git push -u origin "+q(branch), executor.RunOpts{Cwd: dir, Timeout: 120})
		if err != nil {
			return steps, err
		}
		steps = append(steps, map[string]any{"step": "push", "rc": res.RC,
			"output": clipEnd(res.Stdout+res.Stderr, 800)})
		if pr && res.RC != 0 {
			pr = false // never open a PR for a branch that failed to push
		}
	}

	if pr {
		cmd := fmt.Sprintf("gh pr create --head %s --title %s --body %s",
			q(branch), q(orDefault(prTitle, message)), q(prBody))
		res, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: dir, Timeout: 120})
		if err != nil {
			return steps, err
		}
		url := ""
		if res.OK() {
			lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
			url = strings.TrimSpace(lines[len(lines)-1])
		}
		steps = append(steps, map[string]any{"step": "pr", "rc": res.RC,
			"output": clipEnd(res.Stdout+res.Stderr, 800), "url": url})
	}
	return steps, nil
}

// respondGitErr maps a gitCommitPushPR error the way the original task-only
// handler did: a step that ran and failed is 409, anything else (the
// executor itself failing) is a normal infrastructure error.
func respondGitErr(w http.ResponseWriter, err error) {
	var se *gitStepError
	if errors.As(err, &se) {
		httpError(w, se.status, "%s", se.msg)
		return
	}
	respondErr(w, err)
}

func (s *Server) commitTask(w http.ResponseWriter, r *http.Request) {
	task, proj, target, att, ok := s.workCtx(w, r)
	if !ok {
		return
	}
	if task.Status != "review" && task.Status != "done" {
		httpError(w, 409, "cannot commit from %s", task.Status)
		return
	}
	var body commitIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	_ = proj
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	msg := orDefault(body.Message, task.Title)
	prBody := orDefault(body.PRBody, fmt.Sprintf("Created by lectern task #%d.", task.ID))
	steps, err := gitCommitPushPR(r.Context(), ex, att.WorktreePath, att.Branch, msg,
		body.Push, body.PR, orDefault(body.PRTitle, msg), prBody)
	if err != nil {
		respondGitErr(w, err)
		return
	}
	s.Bus.Publish(fmt.Sprintf("task:%d", task.ID), "git", map[string]any{"steps": steps})
	writeJSON(w, 200, map[string]any{"steps": steps})
}

// refuseOnBaseBranch is the safety net a task attempt gets for free by always
// running on its own `lec/task...` branch: a session may run straight in a
// project's own checkout with no isolated worktree, and committing there
// would commit directly onto main. Refuse whenever the resolved branch IS
// the base branch (or a well-known default) rather than a branch of its own.
func refuseOnBaseBranch(branch, base string) error {
	if branch == "" {
		return nil
	}
	if branch == base || branch == "main" || branch == "master" {
		return fmt.Errorf("refusing to commit directly on %q — this session has no isolated branch or worktree", branch)
	}
	return nil
}

// sessionCommitTarget is where and on what branch a session's commit runs.
type sessionCommitTarget struct {
	dir, branch, base string
}

// resolveSessionCommitTarget mirrors sessionRepoDiffs' repository resolution,
// but for the one repository a commit acts on: the query param `repo` picks
// among a grouped workspace's repositories, and is otherwise ignored.
func (s *Server) resolveSessionCommitTarget(ctx context.Context, ex executor.Executor,
	row *store.Session, repoParam string) (sessionCommitTarget, error) {
	proj := s.projectOrNil(row.ProjectID)
	var plan worktree.Interactive
	hasPlan := row.WorktreeJSON != "" && json.Unmarshal([]byte(row.WorktreeJSON), &plan) == nil

	switch {
	case hasPlan && len(plan.Repositories) > 0:
		var match *worktree.WorkspaceRepository
		for i := range plan.Repositories {
			if plan.Repositories[i].Name == repoParam {
				match = &plan.Repositories[i]
				break
			}
		}
		if repoParam == "" || match == nil || match.Worktree == nil {
			return sessionCommitTarget{}, invalid(
				"this session has multiple repositories; pass ?repo=<name>")
		}
		return sessionCommitTarget{
			dir: match.Worktree.Path, branch: match.Worktree.Branch,
			base: resolveBaseRef(match.Worktree.Base, s.projectOrNil(match.ProjectID)),
		}, nil
	case hasPlan:
		return sessionCommitTarget{dir: plan.Path, branch: plan.Branch, base: resolveBaseRef(plan.Base, proj)}, nil
	default:
		if row.Workdir == "" || !worktree.IsGitRepo(ctx, ex, row.Workdir) {
			return sessionCommitTarget{}, errNotGitRepo
		}
		r, _ := ex.Run(ctx, "git -C "+executor.ShellQuote(row.Workdir)+" rev-parse --abbrev-ref HEAD",
			executor.RunOpts{Timeout: 15})
		return sessionCommitTarget{
			dir: row.Workdir, branch: strings.TrimSpace(r.Stdout), base: resolveBaseRef("", proj),
		}, nil
	}
}

func (s *Server) commitSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.Status == sessions.StatusDead || row.EndedAt != nil {
		httpError(w, 409, "this session is ended or untracked; restore tracking first")
		return
	}
	var body commitIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	ex, err := s.sessionExecutor(row)
	if err != nil {
		respondErr(w, err)
		return
	}
	target, err := s.resolveSessionCommitTarget(r.Context(), ex, row, r.URL.Query().Get("repo"))
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			httpError(w, 409, "this session's workdir is not a git repository")
			return
		}
		respondErr(w, err)
		return
	}
	if err := refuseOnBaseBranch(target.branch, target.base); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	msg := orDefault(body.Message, row.Name)
	prBody := orDefault(body.PRBody, fmt.Sprintf("Created by lectern session %d.", row.ID))
	steps, err := gitCommitPushPR(r.Context(), ex, target.dir, target.branch, msg,
		body.Push, body.PR, orDefault(body.PRTitle, msg), prBody)
	if err != nil {
		respondGitErr(w, err)
		return
	}
	s.Bus.Publish(fmt.Sprintf("session:%d", row.ID), "git", map[string]any{"steps": steps})
	writeJSON(w, 200, map[string]any{"steps": steps})
}

// ---- PR description generation (shared by tasks and sessions) -------------

type prDescriptionOut struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// runSummary is the one seam a headless cheap-model call goes through. Tests
// set Server.SummaryGen to a stub so a review test never spends a real model
// token; production leaves it nil and gets the real `claude -p`/`codex exec`
// invocation below.
func (s *Server) runSummary(ctx context.Context, ex executor.Executor, agent, model, prompt string) (string, error) {
	if s.SummaryGen != nil {
		return s.SummaryGen(ctx, ex, agent, model, prompt)
	}
	q := executor.ShellQuote
	var cmd string
	switch agent {
	case "codex":
		cmd = fmt.Sprintf("%s exec %s --model %s < /dev/null",
			q(firstNonEmptyStr(s.Cfg.CodexBin, "codex")), q(prompt), q(model))
	default:
		// </dev/null: `claude -p` reads stdin to EOF and an ssh exec channel
		// never EOFs without it (see the deep probe in targets.go).
		cmd = fmt.Sprintf("%s -p %s --model %s < /dev/null",
			q(firstNonEmptyStr(s.Cfg.ClaudeBin, "claude")), q(prompt), q(model))
	}
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 120})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", fmt.Errorf("summary agent failed: %s", clipEnd(res.Stdout+res.Stderr, 400))
	}
	return res.Stdout, nil
}

func buildPRDescriptionPrompt(defaultTitle, diffText string) string {
	diff := diffText
	if len(diff) > 20000 {
		diff = diff[:20000] + "\n... (diff truncated)"
	}
	return "Write a concise pull request description for the diff below.\n" +
		`Reply with ONLY minified JSON of the shape {"title":"...","body":"..."} ` +
		"— no markdown code fences, no commentary before or after it. The title " +
		"is one line, imperative mood, under 70 characters. The body is a short " +
		"Markdown summary (1-4 sentences or a short bullet list) of what changed " +
		"and why.\n\nSuggested title if nothing better fits: " + defaultTitle +
		"\n\nDIFF:\n" + diff
}

// parsePRDescription tolerates a model that didn't follow instructions
// exactly: fenced JSON, or chatter around the object, still parses; a model
// that returned prose instead of JSON becomes the body rather than an error,
// so a marginal response is still useful.
func parsePRDescription(raw, fallbackTitle string) prDescriptionOut {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			var out prDescriptionOut
			if json.Unmarshal([]byte(raw[i:j+1]), &out) == nil && strings.TrimSpace(out.Title) != "" {
				return out
			}
		}
	}
	title := fallbackTitle
	if title == "" {
		title = "Update"
	}
	return prDescriptionOut{Title: title, Body: raw}
}

func (s *Server) generatePRDescription(ctx context.Context, ex executor.Executor,
	defaultTitle, diffText string) (prDescriptionOut, error) {
	if strings.TrimSpace(diffText) == "" {
		return prDescriptionOut{}, invalid("no diff to describe yet")
	}
	agent := firstNonEmptyStr(s.DB.Setting("summary_agent"), "claude")
	model := firstNonEmptyStr(s.DB.Setting("summary_model"), "haiku")
	raw, err := s.runSummary(ctx, ex, agent, model, buildPRDescriptionPrompt(defaultTitle, diffText))
	if err != nil {
		return prDescriptionOut{}, err
	}
	return parsePRDescription(raw, defaultTitle), nil
}

func concatPatches(repos []repoDiff) string {
	var b strings.Builder
	for _, d := range repos {
		for _, f := range d.Files {
			b.WriteString(f.Patch)
		}
	}
	return b.String()
}

func (s *Server) taskPRDescription(w http.ResponseWriter, r *http.Request) {
	task, proj, target, att, ok := s.workCtx(w, r)
	if !ok {
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	patch := ""
	if raw, rerr := os.ReadFile(filepath.Join(s.Cfg.DiffDir(),
		fmt.Sprintf("attempt-%d.patch", att.ID))); rerr == nil {
		patch = string(raw)
	}
	if strings.TrimSpace(patch) == "" {
		// no captured patch yet (attempt still running) — compute it live
		base := firstNonEmptyStr(task.BaseBranch, proj.DefaultBaseBranch, "main")
		if d, derr := liveDiffRepo(r.Context(), ex, att.WorktreePath, base); derr == nil {
			patch = concatPatches([]repoDiff{d})
		}
	}
	out, err := s.generatePRDescription(r.Context(), ex, task.Title, patch)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) sessionPRDescription(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	ex, err := s.sessionExecutor(row)
	if err != nil {
		respondErr(w, err)
		return
	}
	repos, err := s.sessionRepoDiffs(r.Context(), ex, row)
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			httpError(w, 409, "this session's workdir is not a git repository")
			return
		}
		respondErr(w, err)
		return
	}
	out, err := s.generatePRDescription(r.Context(), ex, row.Name, concatPatches(repos))
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// ---- inline review comments (shared by tasks and sessions) ----------------

// reviewComment is one comment on one diff line, held as a client-side draft
// until the batch is sent. Code is the quoted source line the client already
// has from the diff it rendered — carrying it here keeps the formatted
// prompt self-contained without the server re-reading the diff.
type reviewComment struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Side string `json:"side"` // "old" or "new"
	Text string `json:"text"`
	Code string `json:"code,omitempty"`
}

type reviewIn struct {
	Comments []reviewComment `json:"comments"`
	Summary  string          `json:"summary"`
}

// formatReviewPrompt turns a batch of inline diff comments (plus an optional
// overall summary) into one clear message: a file:line header, the quoted
// code line when the client sent one, then the comment. Sent verbatim to a
// session over SendText, or as a task's request-changes feedback.
func formatReviewPrompt(comments []reviewComment, summary string) string {
	var b strings.Builder
	b.WriteString("Code review feedback")
	switch len(comments) {
	case 0:
		b.WriteString(":\n\n")
	case 1:
		b.WriteString(" (1 comment):\n\n")
	default:
		fmt.Fprintf(&b, " (%d comments):\n\n", len(comments))
	}
	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	for i, c := range comments {
		side := c.Side
		if side != "old" && side != "new" {
			side = "new"
		}
		fmt.Fprintf(&b, "%d. %s:%d (%s side)\n", i+1, c.File, c.Line, side)
		if code := strings.TrimSpace(c.Code); code != "" {
			b.WriteString("   > " + code + "\n")
		}
		b.WriteString("   " + strings.TrimSpace(c.Text) + "\n\n")
	}
	if len(comments) > 0 {
		b.WriteString("Please address each point above.")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func validReview(body reviewIn) error {
	if len(body.Comments) == 0 && strings.TrimSpace(body.Summary) == "" {
		return invalid("review needs at least one comment or a summary")
	}
	for _, c := range body.Comments {
		if strings.TrimSpace(c.File) == "" || strings.TrimSpace(c.Text) == "" {
			return invalid("every comment needs a file and text")
		}
	}
	return nil
}

func (s *Server) reviewSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.Status == sessions.StatusDead || row.EndedAt != nil {
		httpError(w, 409, "this session is ended or untracked; restore tracking first")
		return
	}
	var body reviewIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if err := validReview(body); err != nil {
		respondErr(w, err)
		return
	}
	prompt := formatReviewPrompt(body.Comments, body.Summary)
	if len(prompt) > 32000 {
		httpError(w, 422, "review is too long (maximum 32000 bytes)")
		return
	}
	if err := s.Sessions.SendText(r.Context(), row.ID, prompt); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"sent": true, "comments": len(body.Comments)})
}

func (s *Server) reviewTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	if task.Status != "review" {
		httpError(w, 409, "review only from review")
		return
	}
	var body reviewIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if err := validReview(body); err != nil {
		respondErr(w, err)
		return
	}
	s.followupWithFeedback(w, r, task, formatReviewPrompt(body.Comments, body.Summary))
}
