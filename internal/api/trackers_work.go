package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/budget"
	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/trackers"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// Starting agents from tracker items: a session or task from an issue with
// the issue as its context, and the two pull request helpers — "Resolve with
// agent" (a session on the PR's branch told which files conflict) and "Fix
// checks with agent" (a session handed the failing jobs' logs, with the CI
// loop watching the PR from then on). The issue text is read server-side:
// the client sends only which item it means.

type startWorkIn struct {
	Source       string `json:"source"` // github | gitlab | linear | jira
	ConnectionID int64  `json:"connection_id"`
	Kind         string `json:"kind"` // issue (default) | pr
	ID           string `json:"id"`
	Mode         string `json:"mode"` // session (default) | task
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	// Branch is the session's new branch; empty uses the issue's suggestion.
	Branch   string `json:"branch"`
	Base     string `json:"base"`
	Dispatch bool   `json:"dispatch"`
	Note     string `json:"note"`
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

func validBranch(b string) bool {
	return branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.HasSuffix(b, "/") &&
		!strings.HasSuffix(b, ".lock") && !strings.Contains(b, "//")
}

// itemBrief is the context an agent gets about a tracker item: what it is,
// where it lives, what it says, and the recent conversation on it.
func itemBrief(label string, d *trackers.IssueDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s\n%s\n", label, d.ID, d.Title, d.URL)
	if d.State != "" {
		fmt.Fprintf(&b, "Status: %s\n", d.State)
	}
	if len(d.Labels) > 0 {
		names := make([]string, 0, len(d.Labels))
		for _, l := range d.Labels {
			names = append(names, l.Name)
		}
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(names, ", "))
	}
	if body := strings.TrimSpace(d.Body); body != "" {
		fmt.Fprintf(&b, "\n%s\n", clipText(body, 12000))
	}
	if d.ParentItem != nil {
		fmt.Fprintf(&b, "\nParent: %s %s\n", d.ParentItem.ID, d.ParentItem.Title)
	}
	if len(d.Children) > 0 {
		b.WriteString("\nSub-issues:\n")
		for _, c := range d.Children {
			fmt.Fprintf(&b, "- %s %s (%s)\n", c.ID, c.Title, c.State)
		}
	}
	var comments []trackers.Event
	for _, e := range d.Timeline {
		if e.Kind == "comment" && strings.TrimSpace(e.Body) != "" {
			comments = append(comments, e)
		}
	}
	if n := len(comments); n > 0 {
		if n > 10 {
			comments = comments[n-10:]
			fmt.Fprintf(&b, "\nLast 10 of %d comments:\n", n)
		} else {
			b.WriteString("\nComments:\n")
		}
		for _, c := range comments {
			fmt.Fprintf(&b, "- %s (%s): %s\n", firstNonEmptyStr(c.Author, "someone"), shortDate(c.At), clipText(strings.TrimSpace(c.Body), 1500))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (trimmed)"
}

func shortDate(at string) string {
	if len(at) >= 10 {
		return at[:10]
	}
	return at
}

var sourceLabel = forgeName

// workItem reads the item a start request names.
func (s *Server) workItem(ctx context.Context, proj *store.Project, in startWorkIn) (*trackers.IssueDetail, error) {
	switch in.Source {
	case "github", "gitlab", "bitbucket", "gitea", "azure":
		f, err := s.projectForge(ctx, proj)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimPrefix(in.ID, "#"))
		if err != nil || n <= 0 {
			return nil, invalid("id must be an issue number")
		}
		return f.Issue(ctx, n)
	case "linear", "jira":
		c, err := s.DB.TrackerConnection(in.ConnectionID)
		if err != nil || c.ProjectID != proj.ID || c.Kind != in.Source {
			return nil, invalid("connection_id does not name a %s connection on this project", in.Source)
		}
		return s.trackerIssueDetail(ctx, c, in.ID)
	}
	return nil, invalid("source must be github, gitlab, bitbucket, gitea, azure, linear or jira")
}

// startWork is POST /api/projects/{id}/work/start. A session gets its own
// worktree on the suggested (or chosen) branch and the item as its opening
// message; a task gets the item as its prompt and is dispatched if asked.
// Like any other session or task creation it needs an authenticated caller,
// not a human: nothing on the tracker changes.
func (s *Server) startWork(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.trackerProject(w, r)
	if !ok {
		return
	}
	var in startWorkIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.Kind == "pr" {
		s.startOnPR(w, r, proj, in)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	d, err := s.workItem(ctx, proj, in)
	if err != nil {
		trackerErr(w, err)
		return
	}
	brief := itemBrief(sourceLabel[in.Source]+" issue", d)
	if note := strings.TrimSpace(in.Note); note != "" {
		brief += "\n\nFrom the person who started this: " + note
	}
	labels := []string{in.Source + "-issue"}
	if in.Mode == "task" {
		agent := (*string)(nil)
		if in.Agent != "" {
			agent = &in.Agent
		}
		task, err := s.buildTask(taskIn{ProjectID: proj.ID, Title: fmt.Sprintf("[%s] %s", d.ID, d.Title),
			Prompt: brief, Labels: labels, Agent: agent, Model: in.Model, BaseBranch: in.Base})
		if err != nil {
			respondTaskErr(w, err)
			return
		}
		if in.Dispatch {
			if fresh, err := s.queueTask(task, dispatchIn{}); err != nil {
				writeJSON(w, 201, map[string]any{"kind": "task", "task": s.view(task), "dispatch_error": err.Error()})
				return
			} else {
				task = fresh
			}
		}
		writeJSON(w, 201, map[string]any{"kind": "task", "task": s.view(task)})
		return
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = d.BranchName
	}
	if !validBranch(branch) {
		httpError(w, 422, "%q is not a usable branch name", branch)
		return
	}
	base := firstNonEmptyStr(strings.TrimSpace(in.Base), proj.DefaultBaseBranch, "HEAD")
	prime := brief + "\n\nWork on this in this worktree (branch " + branch + "). Start by reading the relevant code, then make the change."
	sess, err := s.launchWorkSession(r.Context(), proj, fmt.Sprintf("%s · %s", d.ID, d.Title), in.Agent, in.Model, prime,
		&worktree.InteractiveOptions{Branch: branch, Base: base})
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"kind": "session", "session": s.sessionView(sess), "branch": branch})
}

// launchWorkSession starts a background-setup session the way the New
// session sheet does: the operator's default permission mode, the budget
// gate, and the project's own defaults for everything else.
func (s *Server) launchWorkSession(ctx context.Context, proj *store.Project, name, agent, model, prime string, wt *worktree.InteractiveOptions) (*store.Session, error) {
	agent = firstNonEmptyStr(agent, proj.DefaultAgent)
	if agent != "" {
		if _, ok := sessions.Find(s.agentSpecs(), agent); !ok {
			return nil, fmt.Errorf("unknown agent %q", agent)
		}
	}
	if err := budget.Gate(s.DB, agent); err != nil {
		return nil, err
	}
	mode := "bypass"
	if strings.TrimSpace(s.DB.Setting("session_permission_mode")) == "ask" {
		mode = "ask"
	}
	pid := proj.ID
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	return s.Sessions.LaunchBackground(ctx, sessions.LaunchOpts{
		ProjectID: &pid, TargetID: proj.TargetID, Name: name, Agent: agent, Model: model,
		Prime: prime, Worktree: wt, Yolo: mode != "ask", PermissionMode: mode,
	})
}

func randSuffix() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// prWorkspace fetches a PR's base and head into the project's clone as
// remote-tracking refs and returns the head's ref name, for a worktree to be
// cut from. A same-repository PR's head is tracked as origin/<branch>, just
// as `git fetch` would; a fork's under lectern/pr-<n>.
func (s *Server) prWorkspace(ctx context.Context, proj *store.Project, f trackers.Forge, d *trackers.PRDetail) (string, error) {
	ex, err := s.trackerExec(proj)
	if err != nil {
		return "", err
	}
	baseSrc, headSrc := f.FetchRefs(d)
	local := "origin/" + d.Head
	if d.CrossRepo {
		local = "lectern/pr-" + d.ID
	} else {
		headSrc = "refs/heads/" + d.Head
	}
	q := executor.ShellQuote
	cmd := fmt.Sprintf("git -C %s fetch --quiet --no-tags origin %s %s", q(proj.RepoPath),
		q("+"+baseSrc+":refs/remotes/origin/"+d.Base), q("+"+headSrc+":refs/remotes/"+local))
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 120})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", fmt.Errorf("fetching the pull request's branches failed: %s", strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return local, nil
}

func (s *Server) prSetup(w http.ResponseWriter, r *http.Request) (*store.Project, trackers.Forge, *trackers.PRDetail, context.Context, context.CancelFunc, bool) {
	proj, f, ok := s.forgeCtx(w, r)
	if !ok {
		return nil, nil, nil, nil, nil, false
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return nil, nil, nil, nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	d, err := f.PR(ctx, n)
	if err != nil {
		cancel()
		trackerErr(w, err)
		return nil, nil, nil, nil, nil, false
	}
	if d.State != "open" {
		cancel()
		httpError(w, 409, "pull request #%d is %s", n, d.State)
		return nil, nil, nil, nil, nil, false
	}
	return proj, f, d, ctx, cancel, true
}

type prAgentIn struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
	Note  string `json:"note"`
}

func pushInstruction(d *trackers.PRDetail) string {
	s := fmt.Sprintf("git push origin HEAD:%s", d.Head)
	if d.CrossRepo {
		s += " (this pull request comes from a fork, so the push may be refused; if it is, stop after committing and say so)"
	}
	return s
}

// forgeResolve is "Resolve with agent": a session in a worktree at the PR's
// head, told which files conflict with the base and how to push the fix.
func (s *Server) forgeResolve(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "starting an agent on a pull request") {
		return
	}
	proj, f, d, ctx, cancel, ok := s.prSetup(w, r)
	if !ok {
		return
	}
	defer cancel()
	var in prAgentIn
	_ = decodeBody(r, &in)
	ex, err := s.trackerExec(proj)
	if err != nil {
		respondErr(w, err)
		return
	}
	baseRef, headRef := f.FetchRefs(d)
	conflicts := trackers.ConflictingFiles(ctx, ex, proj.RepoPath, baseRef, headRef)
	local, err := s.prWorkspace(ctx, proj, f, d)
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request #%s %q (%s) cannot be merged: its branch %s conflicts with %s.\n\n", d.ID, d.Title, d.URL, d.Head, d.Base)
	switch {
	case len(conflicts.Files) > 0:
		b.WriteString("Files that conflict:\n")
		for _, f := range conflicts.Files {
			b.WriteString("- " + f + "\n")
		}
	case conflicts.Detail != "":
		b.WriteString("Lectern could not list the conflicting files (" + conflicts.Detail + "); the merge below will show them.\n")
	default:
		b.WriteString("Lectern found no conflicting files, so the base may have moved; the merge below will show the current state.\n")
	}
	fmt.Fprintf(&b, "\nThis worktree is a local branch at the pull request's head (%s @ %s). To resolve:\n"+
		"1. Merge the base in: git fetch origin %s && git merge origin/%s\n"+
		"2. Resolve every conflict, keeping the intent of both sides. The PR description and the base branch's recent history explain what each side meant.\n"+
		"3. Run the project's checks.\n"+
		"4. Commit the merge and push it to the pull request: %s\n",
		d.Head, shortCommit(d.HeadSHA), d.Base, d.Base, pushInstruction(d))
	if note := strings.TrimSpace(in.Note); note != "" {
		b.WriteString("\nFrom the person who started this: " + note + "\n")
	}
	sess, err := s.launchWorkSession(r.Context(), proj, fmt.Sprintf("PR #%s · resolve conflicts", d.ID), in.Agent, in.Model, b.String(),
		&worktree.InteractiveOptions{Base: local, Branch: fmt.Sprintf("lec/pr-%s-resolve-%s", d.ID, randSuffix())})
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"session": s.sessionView(sess), "conflicts": conflicts})
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// forgeFixChecks is "Fix checks with agent". On GitHub it hands the CI loop
// the work: the loop's own first fix request (failing jobs, trimmed and
// redacted logs) opens a new session on the PR's branch, and the PR is
// watched from then on as if the loop had sent it — so later failures go to
// the same session and a green run pushes a notification. GitLab pipelines
// are outside the loop, so there the session gets the same kind of report
// once, built from the pipeline's failed jobs.
func (s *Server) forgeFixChecks(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "starting an agent on a pull request") {
		return
	}
	proj, f, d, ctx, cancel, ok := s.prSetup(w, r)
	if !ok {
		return
	}
	defer cancel()
	var in prAgentIn
	_ = decodeBody(r, &in)
	if trackers.RollupChecks(d.CheckRuns) != "fail" {
		httpError(w, 409, "no checks are failing on pull request #%s", d.ID)
		return
	}
	var report, headSHA string
	watch := f.Kind() == "github" && s.CILoop != nil
	if watch {
		if v := s.CILoop.Watching(d.URL); v != nil && v.Active {
			httpError(w, 409, "the CI loop is already fixing this pull request (%s)", v.Label)
			return
		}
		ex, err := s.trackerExec(proj)
		if err != nil {
			respondErr(w, err)
			return
		}
		branch := fmt.Sprintf("%s — from this worktree: %s", d.Head, pushInstruction(d))
		report, headSHA, err = s.CILoop.FixReport(ctx, ex, d.URL, branch, proj.CIMaxAttempts)
		if errors.Is(err, ciloop.ErrChecksNotFailing) {
			httpError(w, 409, "no checks are failing on pull request #%s", d.ID)
			return
		}
		if err != nil {
			httpError(w, 502, "%s", err.Error())
			return
		}
	} else {
		report = s.pipelineReport(ctx, f, d)
		headSHA = d.HeadSHA
	}
	local, err := s.prWorkspace(ctx, proj, f, d)
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	prime := fmt.Sprintf("This worktree is a local branch at the head of pull request #%s %q (%s @ %s).\n\n%s",
		d.ID, d.Title, d.Head, shortCommit(d.HeadSHA), report)
	if note := strings.TrimSpace(in.Note); note != "" {
		prime += "\nFrom the person who started this: " + note + "\n"
	}
	sess, err := s.launchWorkSession(r.Context(), proj, fmt.Sprintf("PR #%s · fix checks", d.ID), in.Agent, in.Model, prime,
		&worktree.InteractiveOptions{Base: local, Branch: fmt.Sprintf("lec/pr-%s-fix-%s", d.ID, randSuffix())})
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	out := map[string]any{"session": s.sessionView(sess)}
	if watch {
		sid, pid := sess.ID, proj.ID
		row, err := s.CILoop.ArmAsked(ciloop.Owner{SessionID: &sid, ProjectID: &pid, TargetID: proj.TargetID, Branch: d.Head}, d.URL, headSHA)
		if err != nil && !errors.Is(err, ciloop.ErrAlreadyWatched) {
			s.Log.Warn("trackers: arming the CI loop failed", "pr", d.URL, "err", err)
			out["ci_error"] = err.Error()
		} else if row != nil {
			out["ci"] = ciloop.ViewOf(row)
		}
	}
	writeJSON(w, 202, out)
}

// pipelineReport is the fix request for a host the CI loop does not watch:
// failing jobs and the end of their logs, trimmed and redacted the loop's way.
func (s *Server) pipelineReport(ctx context.Context, f trackers.Forge, d *trackers.PRDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CI is failing on %s (commit %s).\n\nFailing jobs:\n", d.URL, shortCommit(d.HeadSHA))
	logged := 0
	var logs strings.Builder
	for _, c := range d.CheckRuns {
		if c.Status != "fail" {
			continue
		}
		fmt.Fprintf(&b, "- %s", c.Name)
		if c.URL != "" {
			fmt.Fprintf(&b, " — %s", c.URL)
		}
		b.WriteString("\n")
		if c.HasLog && logged < 4 {
			if raw, err := f.JobLog(ctx, d, c.ID); err == nil {
				logged++
				fmt.Fprintf(&logs, "\nLog tail for %q:\n```\n%s\n```\n", c.Name, strings.TrimSpace(ciloop.TrimForAgent(raw)))
			}
		}
	}
	b.WriteString(logs.String())
	fmt.Fprintf(&b, "\nPlease fix these failures, commit, and push to the pull request: %s\n", pushInstruction(d))
	return b.String()
}

// startOnPR is "Start session" on a pull request page: a session in a
// worktree at the PR's head with the PR as its context.
func (s *Server) startOnPR(w http.ResponseWriter, r *http.Request, proj *store.Project, in startWorkIn) {
	f, err := s.projectForge(r.Context(), proj)
	if err != nil {
		httpError(w, 424, "%s", err.Error())
		return
	}
	n, err := strconv.Atoi(strings.TrimPrefix(in.ID, "#"))
	if err != nil || n <= 0 {
		httpError(w, 422, "id must be a pull request number")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	d, err := f.PR(ctx, n)
	if err != nil {
		trackerErr(w, err)
		return
	}
	local, err := s.prWorkspace(ctx, proj, f, d)
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	issue := &trackers.IssueDetail{Item: d.Item, Body: d.Body, Timeline: d.Timeline}
	prime := itemBrief(sourceLabel[f.Kind()]+" pull request", issue) +
		fmt.Sprintf("\n\nThis worktree is a local branch at the pull request's head (%s @ %s, into %s). To update the pull request, commit and run: %s",
			d.Head, shortCommit(d.HeadSHA), d.Base, pushInstruction(d))
	if note := strings.TrimSpace(in.Note); note != "" {
		prime += "\n\nFrom the person who started this: " + note
	}
	sess, err := s.launchWorkSession(r.Context(), proj, fmt.Sprintf("PR #%s · %s", d.ID, d.Title), in.Agent, in.Model, prime,
		&worktree.InteractiveOptions{Base: local, Branch: fmt.Sprintf("lec/pr-%s-%s", d.ID, randSuffix())})
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"kind": "session", "session": s.sessionView(sess)})
}
