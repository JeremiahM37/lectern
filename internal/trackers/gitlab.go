package trackers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// GitLab is a project on gitlab.com or a self-managed GitLab, reached
// through the target's `glab api` — the REST API with glab's own login, so
// Lectern holds no GitLab token. The REST API is used rather than glab's
// higher-level commands because its JSON is stable across glab versions.
type GitLab struct {
	cli
	ref RepoRef
}

// NewGitLab builds the GitLab forge for ref on ex.
func NewGitLab(ex executor.Executor, ref RepoRef) *GitLab {
	return &GitLab{cli: cli{ex: ex, bin: "glab", name: "GitLab"}, ref: ref}
}

func (g *GitLab) Kind() string  { return "gitlab" }
func (g *GitLab) Repo() RepoRef { return g.ref }

func (g *GitLab) project() string { return "projects/" + url.PathEscape(g.ref.Path) }

// api runs `glab api` against this project's host. body, when non-nil, is
// sent as a JSON request body on stdin.
func (g *GitLab) api(ctx context.Context, method, path string, body any, out any) error {
	args := []string{"api"}
	if g.ref.Host != "" && g.ref.Host != "gitlab.com" {
		args = append(args, "--hostname", g.ref.Host)
	}
	if method != "" && method != "GET" {
		args = append(args, "-X", method)
	}
	args = append(args, path)
	cmd := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		args = append(args, "-H", "Content-Type: application/json", "--input", "-")
		cmd = "printf '%s' " + executor.ShellQuote(string(raw)) + " | " + command(g.bin, args...)
	} else {
		cmd = command(g.bin, args...)
	}
	stdout, err := g.runCmd(ctx, 60, cmd)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), out); err != nil {
		return fmt.Errorf("glab returned unexpected output: %s", clip(stdout, 200))
	}
	return nil
}

type glUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type glPipeline struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

type glMR struct {
	IID                 int         `json:"iid"`
	Title               string      `json:"title"`
	Description         string      `json:"description"`
	WebURL              string      `json:"web_url"`
	State               string      `json:"state"`
	Draft               bool        `json:"draft"`
	Author              glUser      `json:"author"`
	Assignees           []glUser    `json:"assignees"`
	Reviewers           []glUser    `json:"reviewers"`
	Labels              []string    `json:"labels"`
	SourceBranch        string      `json:"source_branch"`
	TargetBranch        string      `json:"target_branch"`
	SHA                 string      `json:"sha"`
	HasConflicts        bool        `json:"has_conflicts"`
	DetailedMergeStatus string      `json:"detailed_merge_status"`
	MergeWhenPipeline   bool        `json:"merge_when_pipeline_succeeds"`
	MergeUser           *glUser     `json:"merge_user"`
	HeadPipeline        *glPipeline `json:"head_pipeline"`
	SourceProjectID     int64       `json:"source_project_id"`
	TargetProjectID     int64       `json:"target_project_id"`
	Upvotes             int         `json:"upvotes"`
	Downvotes           int         `json:"downvotes"`
	CreatedAt           string      `json:"created_at"`
	UpdatedAt           string      `json:"updated_at"`
	MergedAt            string      `json:"merged_at"`
	ClosedAt            string      `json:"closed_at"`
	ChangesCount        string      `json:"changes_count"`
}

type glIssue struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	WebURL      string   `json:"web_url"`
	State       string   `json:"state"`
	Author      glUser   `json:"author"`
	Assignees   []glUser `json:"assignees"`
	Labels      []string `json:"labels"`
	Upvotes     int      `json:"upvotes"`
	Downvotes   int      `json:"downvotes"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type glNote struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Author    glUser `json:"author"`
	CreatedAt string `json:"created_at"`
	System    bool   `json:"system"`
}

type glJob struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Stage  string `json:"stage"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
}

type glBridge struct {
	Name       string `json:"name"`
	Stage      string `json:"stage"`
	Status     string `json:"status"`
	WebURL     string `json:"web_url"`
	Downstream *struct {
		ID        int64 `json:"id"`
		ProjectID int64 `json:"project_id"`
	} `json:"downstream_pipeline"`
}

type glProject struct {
	MergeMethod              string `json:"merge_method"`
	SquashOption             string `json:"squash_option"`
	RemoveSourceBranchAfter  bool   `json:"remove_source_branch_after_merge"`
	MergeTrainsEnabled       bool   `json:"merge_trains_enabled"`
	OnlyAllowMergeIfPipeline bool   `json:"only_allow_merge_if_pipeline_succeeds"`
	Permissions              struct {
		ProjectAccess *struct {
			AccessLevel int `json:"access_level"`
		} `json:"project_access"`
		GroupAccess *struct {
			AccessLevel int `json:"access_level"`
		} `json:"group_access"`
	} `json:"permissions"`
}

func glState(s string) string {
	if s == "opened" {
		return "open"
	}
	return s
}

func glLabels(in []string) []Label {
	out := make([]Label, 0, len(in))
	for _, l := range in {
		out = append(out, Label{Name: l})
	}
	return out
}

func glLogins(in []glUser) []string {
	out := make([]string, 0, len(in))
	for _, u := range in {
		out = append(out, u.Username)
	}
	return out
}

func glPipelineRollup(p *glPipeline) string {
	if p == nil {
		return "none"
	}
	switch p.Status {
	case "success":
		return "pass"
	case "failed", "canceled":
		return "fail"
	case "skipped", "manual":
		return "none"
	}
	return "pending"
}

func (g *GitLab) mrItem(m glMR) Item {
	return Item{
		Source: "gitlab", Kind: "pr", ID: strconv.Itoa(m.IID), Title: m.Title, URL: m.WebURL,
		State: glState(m.State), Author: m.Author.Username, Assignees: glLogins(m.Assignees),
		Labels: glLabels(m.Labels), UpdatedAt: m.UpdatedAt, Draft: m.Draft,
		Checks: glPipelineRollup(m.HeadPipeline), Conflicts: m.HasConflicts,
		Head: m.SourceBranch, Base: m.TargetBranch,
	}
}

func (g *GitLab) issueItem(i glIssue) Item {
	return Item{
		Source: "gitlab", Kind: "issue", ID: strconv.Itoa(i.IID), Title: i.Title, URL: i.WebURL,
		State: glState(i.State), Author: i.Author.Username, Assignees: glLogins(i.Assignees),
		Labels: glLabels(i.Labels), UpdatedAt: i.UpdatedAt,
	}
}

func (g *GitLab) me(ctx context.Context) (glUser, error) {
	var u glUser
	err := g.api(ctx, "GET", "user", nil, &u)
	return u, err
}

// List reads open merge requests or issues.
func (g *GitLab) List(ctx context.Context, kind string, f Filter) ([]Item, error) {
	q := url.Values{}
	switch f.State {
	case "", "open":
		q.Set("state", "opened")
	case "closed":
		q.Set("state", "closed")
	case "merged":
		if kind == "pr" {
			q.Set("state", "merged")
		} else {
			q.Set("state", "closed")
		}
	case "all":
	default:
		return nil, fmt.Errorf("unknown state %q", f.State)
	}
	q.Set("per_page", strconv.Itoa(f.limit()))
	q.Set("order_by", "updated_at")
	if s := strings.TrimSpace(f.Query); s != "" {
		q.Set("search", s)
	}
	switch f.Mine {
	case "assigned":
		q.Set("scope", "assigned_to_me")
	case "authored":
		q.Set("scope", "created_by_me")
	case "review":
		if kind == "pr" {
			me, err := g.me(ctx)
			if err != nil {
				return nil, err
			}
			q.Set("reviewer_username", me.Username)
		}
	}
	if kind == "pr" {
		var rows []glMR
		if err := g.api(ctx, "GET", g.project()+"/merge_requests?"+q.Encode(), nil, &rows); err != nil {
			return nil, err
		}
		out := make([]Item, 0, len(rows))
		for _, m := range rows {
			out = append(out, g.mrItem(m))
		}
		return out, nil
	}
	var rows []glIssue
	if err := g.api(ctx, "GET", g.project()+"/issues?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, i := range rows {
		out = append(out, g.issueItem(i))
	}
	return out, nil
}

func glJobStatus(s string) string {
	switch s {
	case "success":
		return "pass"
	case "failed":
		return "fail"
	case "canceled":
		return "cancel"
	case "skipped", "manual":
		return "skipping"
	}
	return "pending"
}

func glReactions(up, down int) []Reaction {
	var out []Reaction
	if up > 0 {
		out = append(out, Reaction{Emoji: "👍", Count: up})
	}
	if down > 0 {
		out = append(out, Reaction{Emoji: "👎", Count: down})
	}
	return out
}

// PR reads a merge request page: the MR, its notes and approvals, its head
// pipeline's jobs (child pipelines' jobs included, one level down), the
// project's merge settings, and the open MRs it may be stacked with.
func (g *GitLab) PR(ctx context.Context, n int) (*PRDetail, error) {
	base := fmt.Sprintf("%s/merge_requests/%d", g.project(), n)
	var (
		m         glMR
		notes     []glNote
		approvals struct {
			ApprovedBy []struct {
				User glUser `json:"user"`
			} `json:"approved_by"`
		}
		proj  glProject
		open  []glMR
		mrErr error
		wg    sync.WaitGroup
	)
	wg.Add(5)
	go func() { defer wg.Done(); mrErr = g.api(ctx, "GET", base, nil, &m) }()
	go func() {
		defer wg.Done()
		_ = g.api(ctx, "GET", base+"/notes?sort=asc&order_by=created_at&per_page=100", nil, &notes)
	}()
	go func() { defer wg.Done(); _ = g.api(ctx, "GET", base+"/approvals", nil, &approvals) }()
	go func() { defer wg.Done(); _ = g.api(ctx, "GET", g.project(), nil, &proj) }()
	go func() {
		defer wg.Done()
		_ = g.api(ctx, "GET", g.project()+"/merge_requests?state=opened&per_page=100", nil, &open)
	}()
	wg.Wait()
	if mrErr != nil {
		return nil, mrErr
	}
	d := &PRDetail{
		Item: g.mrItem(m), Body: m.Description, HeadSHA: m.SHA,
		CrossRepo: m.SourceProjectID != 0 && m.SourceProjectID != m.TargetProjectID,
		Reactions: glReactions(m.Upvotes, m.Downvotes), CreatedAt: m.CreatedAt,
		MergedAt: m.MergedAt, ClosedAt: m.ClosedAt, CheckRuns: []Check{},
	}
	d.ChangedFiles, _ = strconv.Atoi(strings.TrimSuffix(m.ChangesCount, "+"))
	d.Mergeable = "mergeable"
	if m.HasConflicts {
		d.Mergeable = "conflicting"
	}
	d.MergeState = glMergeState(m.DetailedMergeStatus)
	if m.MergeWhenPipeline {
		by := ""
		if m.MergeUser != nil {
			by = m.MergeUser.Username
		}
		d.AutoMerge = &AutoMerge{EnabledBy: by}
	}
	approved := map[string]bool{}
	for _, a := range approvals.ApprovedBy {
		approved[a.User.Username] = true
	}
	for _, r := range m.Reviewers {
		state := "requested"
		if approved[r.Username] {
			state = "approved"
		}
		d.Reviewers = append(d.Reviewers, Reviewer{Login: r.Username, State: state})
	}
	for _, a := range approvals.ApprovedBy {
		if !glHasReviewer(m.Reviewers, a.User.Username) {
			d.Reviewers = append(d.Reviewers, Reviewer{Login: a.User.Username, State: "approved"})
		}
	}
	if len(d.Reviewers) == 0 {
		d.Reviewers = []Reviewer{}
	}
	if len(approved) > 0 {
		d.Review = "approved"
	}
	for _, nt := range notes {
		kind := "comment"
		if nt.System {
			kind = "event"
		}
		ev := Event{Kind: kind, Author: nt.Author.Username, Body: nt.Body, At: nt.CreatedAt}
		if !nt.System {
			ev.ID = strconv.FormatInt(nt.ID, 10)
		}
		d.Timeline = append(d.Timeline, ev)
	}
	if d.Timeline == nil {
		d.Timeline = []Event{}
	}
	if m.HeadPipeline != nil {
		d.CheckRuns = g.pipelineChecks(ctx, m.HeadPipeline.ID)
	}
	d.Merge = glMergeOptions(proj)
	entries := []StackEntry{}
	found := false
	for _, o := range open {
		found = found || o.IID == m.IID
		entries = append(entries, StackEntry{Number: o.IID, Title: o.Title, Head: o.SourceBranch, Base: o.TargetBranch, URL: o.WebURL, State: glState(o.State)})
	}
	if !found {
		entries = append(entries, StackEntry{Number: m.IID, Title: m.Title, Head: m.SourceBranch, Base: m.TargetBranch, URL: m.WebURL, State: glState(m.State)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	d.Stack = BuildStack(entries, m.IID)
	return d, nil
}

func glHasReviewer(rs []glUser, login string) bool {
	for _, r := range rs {
		if r.Username == login {
			return true
		}
	}
	return false
}

func glMergeState(s string) string {
	switch s {
	case "mergeable":
		return "clean"
	case "broken_status", "conflict":
		return "dirty"
	case "need_rebase":
		return "behind"
	case "draft_status":
		return "draft"
	case "ci_must_pass", "ci_still_running":
		return "unstable"
	case "":
		return "unknown"
	}
	return "blocked"
}

// pipelineChecks lists a pipeline's jobs, and the jobs of any child or
// multi-project pipeline its bridge jobs started, as checks whose ID is
// "<project id>:<job id>" — what JobLog's trace call needs.
func (g *GitLab) pipelineChecks(ctx context.Context, pipelineID int64) []Check {
	var jobs []glJob
	var bridges []glBridge
	pipe := fmt.Sprintf("%s/pipelines/%d", g.project(), pipelineID)
	_ = g.api(ctx, "GET", pipe+"/jobs?per_page=100", nil, &jobs)
	_ = g.api(ctx, "GET", pipe+"/bridges?per_page=100", nil, &bridges)
	out := []Check{}
	for _, j := range jobs {
		out = append(out, Check{ID: "self:" + strconv.FormatInt(j.ID, 10), Name: j.Name, Workflow: j.Stage,
			Status: glJobStatus(j.Status), URL: j.WebURL, HasLog: true})
	}
	for _, b := range bridges {
		out = append(out, Check{ID: "bridge:" + b.Name, Name: b.Name, Workflow: b.Stage + " (trigger)",
			Status: glJobStatus(b.Status), URL: b.WebURL})
		if b.Downstream == nil {
			continue
		}
		var child []glJob
		path := fmt.Sprintf("projects/%d/pipelines/%d/jobs?per_page=100", b.Downstream.ProjectID, b.Downstream.ID)
		if g.api(ctx, "GET", path, nil, &child) != nil {
			continue
		}
		for _, j := range child {
			out = append(out, Check{ID: fmt.Sprintf("%d:%d", b.Downstream.ProjectID, j.ID), Name: b.Name + " › " + j.Name,
				Workflow: j.Stage, Status: glJobStatus(j.Status), URL: j.WebURL, HasLog: true})
		}
	}
	return out
}

func glMergeOptions(p glProject) MergeOptions {
	o := MergeOptions{DeleteBranchDefault: p.RemoveSourceBranchAfter, AutoMergeAllowed: true, MergeQueue: p.MergeTrainsEnabled}
	// GitLab's merge method (merge commit, semi-linear, fast-forward) is a
	// project setting, not a per-merge choice; squash is the per-merge one.
	switch p.SquashOption {
	case "always":
		o.Methods = []string{"squash"}
	case "never":
		o.Methods = []string{"merge"}
	case "default_on":
		o.Methods = []string{"squash", "merge"}
	default:
		o.Methods = []string{"merge", "squash"}
	}
	o.Default = o.Methods[0]
	level := 0
	if p.Permissions.ProjectAccess != nil {
		level = p.Permissions.ProjectAccess.AccessLevel
	}
	if p.Permissions.GroupAccess != nil && p.Permissions.GroupAccess.AccessLevel > level {
		level = p.Permissions.GroupAccess.AccessLevel
	}
	// Access levels only appear to a signed-in member; with none reported,
	// offer the merge and let GitLab decide.
	o.CanMerge = level >= 30 || level == 0 // 30 is Developer
	return o
}

// Issue reads one issue and its comments.
func (g *GitLab) Issue(ctx context.Context, n int) (*IssueDetail, error) {
	base := fmt.Sprintf("%s/issues/%d", g.project(), n)
	var i glIssue
	if err := g.api(ctx, "GET", base, nil, &i); err != nil {
		return nil, err
	}
	var notes []glNote
	_ = g.api(ctx, "GET", base+"/notes?sort=asc&order_by=created_at&per_page=100", nil, &notes)
	d := &IssueDetail{Item: g.issueItem(i), Body: i.Description, Reactions: glReactions(i.Upvotes, i.Downvotes),
		CreatedAt: i.CreatedAt, Children: []Item{}, Transitions: []Transition{}, Timeline: []Event{},
		BranchName: BranchName(strconv.Itoa(i.IID), i.Title)}
	for _, nt := range notes {
		kind := "comment"
		if nt.System {
			kind = "event"
		}
		ev := Event{Kind: kind, Author: nt.Author.Username, Body: nt.Body, At: nt.CreatedAt}
		if !nt.System {
			ev.ID = strconv.FormatInt(nt.ID, 10)
		}
		d.Timeline = append(d.Timeline, ev)
	}
	return d, nil
}

// JobLog reads a job's trace. The check ID names the project it ran in, so
// child pipelines in other projects are read from the right place.
func (g *GitLab) JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error) {
	var c *Check
	for i := range pr.CheckRuns {
		if pr.CheckRuns[i].ID == checkID {
			c = &pr.CheckRuns[i]
		}
	}
	if c == nil {
		return "", fmt.Errorf("no such check on this merge request")
	}
	if !c.HasLog {
		return "", fmt.Errorf("%s has no log of its own; open its downstream pipeline: %s", c.Name, c.URL)
	}
	proj, job, _ := strings.Cut(c.ID, ":")
	path := g.project()
	if proj != "self" {
		path = "projects/" + proj
	}
	args := []string{"api"}
	if g.ref.Host != "" && g.ref.Host != "gitlab.com" {
		args = append(args, "--hostname", g.ref.Host)
	}
	args = append(args, path+"/jobs/"+job+"/trace")
	return g.run(ctx, 60, args...)
}

// Merge merges now or, with Auto, sets "merge when pipeline succeeds"
// (which joins the merge train when the project uses one). sha makes GitLab
// refuse if the branch moved after the person confirmed.
func (g *GitLab) Merge(ctx context.Context, n int, m MergeRequest) (string, error) {
	if m.Method != "merge" && m.Method != "squash" {
		return "", fmt.Errorf("GitLab merges with merge or squash; rebase is a project setting")
	}
	body := map[string]any{"squash": m.Method == "squash", "should_remove_source_branch": m.DeleteBranch}
	if m.Auto {
		body["merge_when_pipeline_succeeds"] = true
	}
	if m.HeadSHA != "" {
		body["sha"] = m.HeadSHA
	}
	var res struct {
		State string `json:"state"`
	}
	if err := g.api(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d/merge", g.project(), n), body, &res); err != nil {
		return "", err
	}
	return res.State, nil
}

func (g *GitLab) DisableAutoMerge(ctx context.Context, n int) error {
	return g.api(ctx, "POST", fmt.Sprintf("%s/merge_requests/%d/cancel_merge_when_pipeline_succeeds", g.project(), n), nil, nil)
}

// EditReviewers resolves usernames to user ids, then sets the MR's whole
// reviewer list — GitLab has no add/remove call for reviewers.
func (g *GitLab) EditReviewers(ctx context.Context, n int, add, remove []string) error {
	var m glMR
	path := fmt.Sprintf("%s/merge_requests/%d", g.project(), n)
	if err := g.api(ctx, "GET", path, nil, &m); err != nil {
		return err
	}
	ids := []int64{}
	drop := map[string]bool{}
	for _, r := range remove {
		drop[r] = true
	}
	have := map[string]bool{}
	for _, r := range m.Reviewers {
		if !drop[r.Username] {
			ids = append(ids, r.ID)
			have[r.Username] = true
		}
	}
	for _, login := range add {
		if have[login] {
			continue
		}
		var users []glUser
		if err := g.api(ctx, "GET", "users?username="+url.QueryEscape(login), nil, &users); err != nil {
			return err
		}
		if len(users) == 0 {
			return fmt.Errorf("no GitLab user %q", login)
		}
		ids = append(ids, users[0].ID)
	}
	return g.api(ctx, "PUT", path, map[string]any{"reviewer_ids": ids}, nil)
}

func (g *GitLab) itemPath(kind string, n int) string {
	if kind == "pr" {
		return fmt.Sprintf("%s/merge_requests/%d", g.project(), n)
	}
	return fmt.Sprintf("%s/issues/%d", g.project(), n)
}

func (g *GitLab) EditLabels(ctx context.Context, kind string, n int, add, remove []string) error {
	body := map[string]any{}
	if len(add) > 0 {
		body["add_labels"] = strings.Join(add, ",")
	}
	if len(remove) > 0 {
		body["remove_labels"] = strings.Join(remove, ",")
	}
	if len(body) == 0 {
		return nil
	}
	return g.api(ctx, "PUT", g.itemPath(kind, n), body, nil)
}

func (g *GitLab) Comment(ctx context.Context, kind string, n int, body string) error {
	return g.api(ctx, "POST", g.itemPath(kind, n)+"/notes", map[string]any{"body": body}, nil)
}

func (g *GitLab) SetState(ctx context.Context, kind string, n int, open bool) error {
	ev := "close"
	if open {
		ev = "reopen"
	}
	return g.api(ctx, "PUT", g.itemPath(kind, n), map[string]any{"state_event": ev}, nil)
}

func (g *GitLab) Labels(ctx context.Context) ([]Label, error) {
	var rows []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := g.api(ctx, "GET", g.project()+"/labels?per_page=100", nil, &rows); err != nil {
		return nil, err
	}
	out := make([]Label, 0, len(rows))
	for _, r := range rows {
		out = append(out, Label{Name: r.Name, Color: strings.TrimPrefix(r.Color, "#")})
	}
	return out, nil
}

func (g *GitLab) Users(ctx context.Context) ([]User, error) {
	var rows []glUser
	if err := g.api(ctx, "GET", g.project()+"/members/all?per_page=100", nil, &rows); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, u := range rows {
		out = append(out, User{Login: u.Username, Name: u.Name})
	}
	return out, nil
}

// FetchRefs uses refs/merge-requests/N/head, which GitLab keeps on the
// target project for every MR, forks included.
func (g *GitLab) FetchRefs(pr *PRDetail) (string, string) {
	return "refs/heads/" + pr.Base, "refs/merge-requests/" + pr.ID + "/head"
}

var glAward = map[string]string{"+1": "thumbsup", "-1": "thumbsdown", "laugh": "laughing", "hooray": "tada",
	"confused": "confused", "heart": "heart", "rocket": "rocket", "eyes": "eyes"}

// React awards an emoji to the merge request or issue, or to one of its notes.
func (g *GitLab) React(ctx context.Context, kind string, n int, subject, emoji string) error {
	name := glAward[emoji]
	if name == "" {
		return fmt.Errorf("unknown reaction %q", emoji)
	}
	path := g.itemPath(kind, n)
	if subject != "" {
		if _, err := strconv.ParseInt(subject, 10, 64); err != nil {
			return fmt.Errorf("bad note id %q", subject)
		}
		path += "/notes/" + subject
	}
	return g.api(ctx, "POST", path+"/award_emoji", map[string]any{"name": name}, nil)
}

type glTrain struct {
	ID           int64 `json:"id"`
	MergeRequest struct {
		IID    int    `json:"iid"`
		Title  string `json:"title"`
		WebURL string `json:"web_url"`
	} `json:"merge_request"`
	User      glUser      `json:"user"`
	Pipeline  *glPipeline `json:"pipeline"`
	Status    string      `json:"status"`
	CreatedAt string      `json:"created_at"`
}

// Queue reads the active merge train for a target branch, oldest (next to
// merge) first.
func (g *GitLab) Queue(ctx context.Context, base string) ([]QueueEntry, error) {
	q := url.Values{"scope": {"active"}, "sort": {"asc"}, "per_page": {"100"}}
	if base != "" {
		q.Set("target_branch", base)
	}
	var rows []glTrain
	if err := g.api(ctx, "GET", g.project()+"/merge_trains?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	out := make([]QueueEntry, 0, len(rows))
	for i, t := range rows {
		e := QueueEntry{ID: strconv.Itoa(t.MergeRequest.IID), Number: t.MergeRequest.IID, Title: t.MergeRequest.Title,
			URL: t.MergeRequest.WebURL, Author: t.User.Username, Position: i + 1, Status: t.Status, EnqueuedAt: t.CreatedAt}
		if t.Pipeline != nil {
			e.Pipeline = t.Pipeline.Status
		}
		out = append(out, e)
	}
	return out, nil
}

// Dequeue takes a merge request off the train by cancelling its auto-merge,
// which is how GitLab removes a train entry.
func (g *GitLab) Dequeue(ctx context.Context, e QueueEntry) error {
	return g.DisableAutoMerge(ctx, e.Number)
}

func (g *GitLab) DefaultBranch(ctx context.Context) (string, error) {
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.api(ctx, "GET", g.project(), nil, &p); err != nil {
		return "", err
	}
	return p.DefaultBranch, nil
}
