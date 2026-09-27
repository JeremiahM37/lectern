package trackers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// GitHub is a repository on GitHub or GitHub Enterprise, reached through the
// target's `gh`. Every call names the repository with -R, so none of them
// depend on the directory gh runs in.
type GitHub struct {
	cli
	ref RepoRef
}

// NewGitHub builds the GitHub forge for ref on ex.
func NewGitHub(ex executor.Executor, ref RepoRef) *GitHub {
	return &GitHub{cli: cli{ex: ex, bin: "gh", name: "GitHub"}, ref: ref}
}

func (g *GitHub) Kind() string  { return "github" }
func (g *GitHub) Repo() RepoRef { return g.ref }

type ghActor struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Type  string `json:"__typename"`
}

type ghLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type ghReactionGroup struct {
	Content string `json:"content"`
	Users   struct {
		TotalCount int `json:"totalCount"`
	} `json:"users"`
}

type ghCheck struct {
	Type       string `json:"__typename"`
	Name       string `json:"name"`
	Workflow   string `json:"workflowName"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	Context    string `json:"context"`
	State      string `json:"state"`
	TargetURL  string `json:"targetUrl"`
}

type ghComment struct {
	Author    ghActor           `json:"author"`
	Body      string            `json:"body"`
	CreatedAt string            `json:"createdAt"`
	URL       string            `json:"url"`
	Reactions []ghReactionGroup `json:"reactionGroups"`
}

type ghReview struct {
	Author      ghActor `json:"author"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	SubmittedAt string  `json:"submittedAt"`
}

type ghCommit struct {
	OID      string    `json:"oid"`
	Headline string    `json:"messageHeadline"`
	Date     string    `json:"committedDate"`
	Authors  []ghActor `json:"authors"`
}

type ghPR struct {
	Number         int               `json:"number"`
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	URL            string            `json:"url"`
	State          string            `json:"state"`
	IsDraft        bool              `json:"isDraft"`
	Author         ghActor           `json:"author"`
	HeadRefName    string            `json:"headRefName"`
	HeadRefOid     string            `json:"headRefOid"`
	BaseRefName    string            `json:"baseRefName"`
	IsCrossRepo    bool              `json:"isCrossRepository"`
	Mergeable      string            `json:"mergeable"`
	MergeState     string            `json:"mergeStateStatus"`
	ReviewDecision string            `json:"reviewDecision"`
	ReviewRequests []ghActor         `json:"reviewRequests"`
	LatestReviews  []ghReview        `json:"latestReviews"`
	Reviews        []ghReview        `json:"reviews"`
	Labels         []ghLabel         `json:"labels"`
	Assignees      []ghActor         `json:"assignees"`
	Comments       []ghComment       `json:"comments"`
	Commits        []ghCommit        `json:"commits"`
	Additions      int               `json:"additions"`
	Deletions      int               `json:"deletions"`
	ChangedFiles   int               `json:"changedFiles"`
	Checks         []ghCheck         `json:"statusCheckRollup"`
	AutoMerge      *ghAutoMerge      `json:"autoMergeRequest"`
	Reactions      []ghReactionGroup `json:"reactionGroups"`
	CreatedAt      string            `json:"createdAt"`
	UpdatedAt      string            `json:"updatedAt"`
	MergedAt       string            `json:"mergedAt"`
	ClosedAt       string            `json:"closedAt"`
}

type ghAutoMerge struct {
	MergeMethod string  `json:"mergeMethod"`
	EnabledBy   ghActor `json:"enabledBy"`
}

type ghIssue struct {
	Number    int               `json:"number"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	URL       string            `json:"url"`
	State     string            `json:"state"`
	Author    ghActor           `json:"author"`
	Labels    []ghLabel         `json:"labels"`
	Assignees []ghActor         `json:"assignees"`
	Comments  []ghComment       `json:"comments"`
	Reactions []ghReactionGroup `json:"reactionGroups"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
}

type ghRepo struct {
	MergeCommitAllowed  bool      `json:"mergeCommitAllowed"`
	SquashMergeAllowed  bool      `json:"squashMergeAllowed"`
	RebaseMergeAllowed  bool      `json:"rebaseMergeAllowed"`
	DeleteBranchOnMerge bool      `json:"deleteBranchOnMerge"`
	ViewerPermission    string    `json:"viewerPermission"`
	DefaultMergeMethod  string    `json:"viewerDefaultMergeMethod"`
	Labels              []ghLabel `json:"labels"`
	AssignableUsers     []ghActor `json:"assignableUsers"`
}

const (
	ghListPRFields    = "number,title,url,state,isDraft,author,headRefName,baseRefName,labels,assignees,reviewDecision,updatedAt,statusCheckRollup,mergeable"
	ghListIssueFields = "number,title,url,state,author,labels,assignees,updatedAt"
	ghPRFields        = "number,title,body,url,state,isDraft,author,headRefName,headRefOid,baseRefName,isCrossRepository," +
		"mergeable,mergeStateStatus,reviewDecision,reviewRequests,latestReviews,reviews,labels,assignees,comments,commits," +
		"additions,deletions,changedFiles,statusCheckRollup,autoMergeRequest,reactionGroups,createdAt,updatedAt,mergedAt,closedAt"
	ghIssueFields = "number,title,body,url,state,author,labels,assignees,comments,reactionGroups,createdAt,updatedAt"
	ghRepoFields  = "mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed,deleteBranchOnMerge,viewerPermission,viewerDefaultMergeMethod"
)

func (g *GitHub) json(ctx context.Context, out any, args ...string) error {
	stdout, err := g.run(ctx, 60, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), out); err != nil {
		return fmt.Errorf("gh returned unexpected output: %s", clip(stdout, 200))
	}
	return nil
}

// List is `gh pr list` / `gh issue list` for the repository.
func (g *GitHub) List(ctx context.Context, kind string, f Filter) ([]Item, error) {
	sub, fields := "issue", ghListIssueFields
	if kind == "pr" {
		sub, fields = "pr", ghListPRFields
	}
	state := f.State
	switch state {
	case "", "open":
		state = "open"
	case "closed", "all":
	case "merged":
		if kind != "pr" {
			state = "closed"
		}
	default:
		return nil, fmt.Errorf("unknown state %q", f.State)
	}
	args := []string{sub, "list", "-R", g.ref.Slug(), "--state", state, "--limit", strconv.Itoa(f.limit()), "--json", fields}
	search := strings.TrimSpace(f.Query)
	switch f.Mine {
	case "assigned":
		args = append(args, "--assignee", "@me")
	case "authored":
		args = append(args, "--author", "@me")
	case "review":
		if kind == "pr" {
			search = strings.TrimSpace(search + " review-requested:@me")
		}
	}
	if search != "" {
		args = append(args, "--search", search)
	}
	if kind == "pr" {
		var rows []ghPR
		if err := g.json(ctx, &rows, args...); err != nil {
			return nil, err
		}
		out := make([]Item, 0, len(rows))
		for _, p := range rows {
			out = append(out, g.prItem(p))
		}
		return out, nil
	}
	var rows []ghIssue
	if err := g.json(ctx, &rows, args...); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, i := range rows {
		out = append(out, g.issueItem(i))
	}
	return out, nil
}

func ghLabels(in []ghLabel) []Label {
	out := make([]Label, 0, len(in))
	for _, l := range in {
		out = append(out, Label{Name: l.Name, Color: l.Color})
	}
	return out
}

func ghLogins(in []ghActor) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, a.Login)
	}
	return out
}

func ghState(s string) string { return strings.ToLower(s) }

func ghDecision(decision string) string {
	switch decision {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes_requested"
	case "REVIEW_REQUIRED":
		return "review_required"
	}
	return ""
}

func (g *GitHub) prItem(p ghPR) Item {
	return Item{
		Source: "github", Kind: "pr", ID: strconv.Itoa(p.Number), Title: p.Title, URL: p.URL,
		State: ghState(p.State), Author: p.Author.Login, Assignees: ghLogins(p.Assignees),
		Labels: ghLabels(p.Labels), UpdatedAt: p.UpdatedAt, Draft: p.IsDraft,
		Checks: RollupChecks(ghChecks(p.Checks)), Review: ghDecision(p.ReviewDecision),
		Conflicts: p.Mergeable == "CONFLICTING", Head: p.HeadRefName, Base: p.BaseRefName,
	}
}

func (g *GitHub) issueItem(i ghIssue) Item {
	return Item{
		Source: "github", Kind: "issue", ID: strconv.Itoa(i.Number), Title: i.Title, URL: i.URL,
		State: ghState(i.State), Author: i.Author.Login, Assignees: ghLogins(i.Assignees),
		Labels: ghLabels(i.Labels), UpdatedAt: i.UpdatedAt,
	}
}

// ghChecks maps statusCheckRollup (check runs and commit statuses) onto
// Check. A check run's ID is its details URL, which is what JobLog reads
// the Actions run and job out of.
func ghChecks(in []ghCheck) []Check {
	out := make([]Check, 0, len(in))
	for _, c := range in {
		var k Check
		if c.Type == "StatusContext" || (c.Context != "" && c.Name == "") {
			k = Check{Name: c.Context, URL: c.TargetURL}
			switch c.State {
			case "SUCCESS":
				k.Status = "pass"
			case "FAILURE", "ERROR":
				k.Status = "fail"
			default:
				k.Status = "pending"
			}
		} else {
			k = Check{Name: c.Name, Workflow: c.Workflow, URL: c.DetailsURL}
			switch {
			case c.Status != "COMPLETED":
				k.Status = "pending"
			case c.Conclusion == "SUCCESS" || c.Conclusion == "NEUTRAL":
				k.Status = "pass"
			case c.Conclusion == "SKIPPED":
				k.Status = "skipping"
			case c.Conclusion == "CANCELLED":
				k.Status = "cancel"
			default:
				k.Status = "fail"
			}
		}
		k.ID = k.URL
		if k.ID == "" {
			k.ID = "name:" + k.Name
		}
		k.HasLog = ciloop.IsActionsLink(k.URL)
		out = append(out, k)
	}
	return out
}

// RollupChecks is a list's one-word summary: any failure wins, then
// anything still running, then pass; "none" when there are no checks.
func RollupChecks(cs []Check) string {
	if len(cs) == 0 {
		return "none"
	}
	pending := false
	for _, c := range cs {
		switch c.Status {
		case "fail", "cancel":
			return "fail"
		case "pending":
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "pass"
}

var ghEmoji = map[string]string{
	"THUMBS_UP": "👍", "THUMBS_DOWN": "👎", "LAUGH": "😄", "HOORAY": "🎉",
	"CONFUSED": "😕", "HEART": "❤️", "ROCKET": "🚀", "EYES": "👀",
}

func ghReactions(in []ghReactionGroup) []Reaction {
	var out []Reaction
	for _, r := range in {
		if r.Users.TotalCount > 0 {
			e := ghEmoji[r.Content]
			if e == "" {
				e = strings.ToLower(r.Content)
			}
			out = append(out, Reaction{Emoji: e, Count: r.Users.TotalCount})
		}
	}
	return out
}

func ghReviewState(s string) string {
	switch s {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes_requested"
	case "DISMISSED":
		return "dismissed"
	}
	return "commented"
}

// PR reads the pull request page: the PR itself, the repository's merge
// settings, the open PRs it may be stacked with, and (best effort) whether
// auto-merge and a merge queue are available.
func (g *GitHub) PR(ctx context.Context, n int) (*PRDetail, error) {
	var (
		p              ghPR
		repo           ghRepo
		open           []ghPR
		extra          ghRepoExtra
		wg             sync.WaitGroup
		prErr, repoErr error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		prErr = g.json(ctx, &p, "pr", "view", strconv.Itoa(n), "-R", g.ref.Slug(), "--json", ghPRFields)
	}()
	go func() {
		defer wg.Done()
		repoErr = g.json(ctx, &repo, "repo", "view", g.ref.Slug(), "--json", ghRepoFields)
	}()
	go func() {
		defer wg.Done()
		_ = g.json(ctx, &open, "pr", "list", "-R", g.ref.Slug(), "--state", "open", "--limit", "100",
			"--json", "number,title,url,state,headRefName,baseRefName")
	}()
	wg.Wait()
	if prErr != nil {
		return nil, prErr
	}
	if p.BaseRefName != "" {
		extra = g.repoExtra(ctx, p.BaseRefName)
	}
	d := &PRDetail{
		Item: g.prItem(p), Body: p.Body, HeadSHA: p.HeadRefOid, CrossRepo: p.IsCrossRepo,
		Mergeable: strings.ToLower(p.Mergeable), MergeState: strings.ToLower(p.MergeState),
		CheckRuns: ghChecks(p.Checks), Additions: p.Additions, Deletions: p.Deletions,
		ChangedFiles: p.ChangedFiles, Reactions: ghReactions(p.Reactions),
		CreatedAt: p.CreatedAt, MergedAt: p.MergedAt, ClosedAt: p.ClosedAt,
	}
	if p.AutoMerge != nil {
		d.AutoMerge = &AutoMerge{Method: strings.ToLower(p.AutoMerge.MergeMethod), EnabledBy: p.AutoMerge.EnabledBy.Login}
	}
	d.Reviewers = ghReviewers(p)
	if d.Reviewers == nil {
		d.Reviewers = []Reviewer{}
	}
	d.Timeline = ghTimeline(p)
	if repoErr == nil {
		d.Merge = ghMergeOptions(repo, extra)
	} else {
		// Without the repository's settings, offer what gh itself accepts and
		// let GitHub refuse a method the repository disallows.
		d.Merge = MergeOptions{Methods: []string{"merge", "squash", "rebase"}, Default: "merge", CanMerge: true,
			AutoMergeAllowed: extra.AutoMergeAllowed, MergeQueue: extra.MergeQueue}
	}
	entries := []StackEntry{}
	for _, o := range open {
		entries = append(entries, StackEntry{Number: o.Number, Title: o.Title, Head: o.HeadRefName, Base: o.BaseRefName, URL: o.URL, State: ghState(o.State)})
	}
	found := false
	for _, e := range entries {
		found = found || e.Number == p.Number
	}
	if !found {
		entries = append(entries, StackEntry{Number: p.Number, Title: p.Title, Head: p.HeadRefName, Base: p.BaseRefName, URL: p.URL, State: ghState(p.State)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	d.Stack = BuildStack(entries, p.Number)
	return d, nil
}

func ghReviewers(p ghPR) []Reviewer {
	var out []Reviewer
	seen := map[string]bool{}
	for _, r := range p.ReviewRequests {
		login, team := r.Login, false
		if login == "" {
			login, team = firstNonEmpty(r.Slug, r.Name), true
		}
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, Reviewer{Login: login, State: "requested", Team: team})
	}
	reviews := p.LatestReviews
	if len(reviews) == 0 {
		reviews = p.Reviews
	}
	latest := map[string]string{}
	order := []string{}
	for _, r := range reviews {
		if r.Author.Login == "" {
			continue
		}
		if _, ok := latest[r.Author.Login]; !ok {
			order = append(order, r.Author.Login)
		}
		latest[r.Author.Login] = ghReviewState(r.State)
	}
	for _, login := range order {
		if seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, Reviewer{Login: login, State: latest[login]})
	}
	return out
}

func ghTimeline(p ghPR) []Event {
	var ev []Event
	for _, c := range p.Comments {
		ev = append(ev, Event{Kind: "comment", Author: c.Author.Login, Body: c.Body, At: c.CreatedAt, URL: c.URL, Reactions: ghReactions(c.Reactions)})
	}
	for _, r := range p.Reviews {
		if r.State == "PENDING" {
			continue
		}
		ev = append(ev, Event{Kind: "review", Author: r.Author.Login, Body: r.Body, State: ghReviewState(r.State), At: r.SubmittedAt})
	}
	for _, c := range p.Commits {
		author := ""
		if len(c.Authors) > 0 {
			author = firstNonEmpty(c.Authors[0].Login, c.Authors[0].Name)
		}
		ev = append(ev, Event{Kind: "commit", Author: author, Body: c.Headline, State: shortSHA(c.OID), At: c.Date})
	}
	if p.MergedAt != "" {
		ev = append(ev, Event{Kind: "event", Body: "merged", At: p.MergedAt})
	} else if p.ClosedAt != "" {
		ev = append(ev, Event{Kind: "event", Body: "closed", At: p.ClosedAt})
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].At < ev[j].At })
	return ev
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func ghMergeOptions(r ghRepo, extra ghRepoExtra) MergeOptions {
	o := MergeOptions{DeleteBranchDefault: r.DeleteBranchOnMerge, AutoMergeAllowed: extra.AutoMergeAllowed, MergeQueue: extra.MergeQueue}
	if r.MergeCommitAllowed {
		o.Methods = append(o.Methods, "merge")
	}
	if r.SquashMergeAllowed {
		o.Methods = append(o.Methods, "squash")
	}
	if r.RebaseMergeAllowed {
		o.Methods = append(o.Methods, "rebase")
	}
	o.Default = strings.ToLower(r.DefaultMergeMethod)
	if !contains(o.Methods, o.Default) && len(o.Methods) > 0 {
		o.Default = o.Methods[0]
	}
	switch r.ViewerPermission {
	case "ADMIN", "MAINTAIN", "WRITE":
		o.CanMerge = true
	}
	return o
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type ghRepoExtra struct {
	AutoMergeAllowed bool
	MergeQueue       bool
}

// repoExtra asks GraphQL for the two things gh's --json does not expose.
// Both are best effort: an older GitHub Enterprise without merge queues
// answers with an error, and the page simply does not offer them.
func (g *GitHub) repoExtra(ctx context.Context, base string) ghRepoExtra {
	owner, name, _ := strings.Cut(g.ref.Path, "/")
	args := []string{"api"}
	if g.ref.Host != "" && g.ref.Host != "github.com" {
		args = append(args, "--hostname", g.ref.Host)
	}
	args = append(args, "graphql",
		"-f", "query=query($o:String!,$n:String!,$b:String!){repository(owner:$o,name:$n){autoMergeAllowed mergeQueue(branch:$b){id}}}",
		"-f", "o="+owner, "-f", "n="+name, "-f", "b="+base)
	var res struct {
		Data struct {
			Repository struct {
				AutoMergeAllowed bool `json:"autoMergeAllowed"`
				MergeQueue       *struct {
					ID string `json:"id"`
				} `json:"mergeQueue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := g.json(ctx, &res, args...); err != nil {
		return ghRepoExtra{}
	}
	return ghRepoExtra{AutoMergeAllowed: res.Data.Repository.AutoMergeAllowed, MergeQueue: res.Data.Repository.MergeQueue != nil}
}

// Issue reads one issue with its comments.
func (g *GitHub) Issue(ctx context.Context, n int) (*IssueDetail, error) {
	var i ghIssue
	if err := g.json(ctx, &i, "issue", "view", strconv.Itoa(n), "-R", g.ref.Slug(), "--json", ghIssueFields); err != nil {
		return nil, err
	}
	d := &IssueDetail{Item: g.issueItem(i), Body: i.Body, Reactions: ghReactions(i.Reactions), CreatedAt: i.CreatedAt,
		Children: []Item{}, Transitions: []Transition{}, BranchName: BranchName(strconv.Itoa(i.Number), i.Title)}
	for _, c := range i.Comments {
		d.Timeline = append(d.Timeline, Event{Kind: "comment", Author: c.Author.Login, Body: c.Body, At: c.CreatedAt, URL: c.URL, Reactions: ghReactions(c.Reactions)})
	}
	if d.Timeline == nil {
		d.Timeline = []Event{}
	}
	return d, nil
}

// JobLog reads a GitHub Actions job's log through the CI loop's own fetcher.
func (g *GitHub) JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error) {
	for _, c := range pr.CheckRuns {
		if c.ID != checkID {
			continue
		}
		if !c.HasLog {
			return "", fmt.Errorf("%s is not a GitHub Actions job; open it on its own site: %s", c.Name, c.URL)
		}
		raw, ok := ciloop.JobLog(ctx, g.ex, pr.URL, c.URL)
		if !ok {
			return "", fmt.Errorf("no log is available for %s", c.Name)
		}
		return raw, nil
	}
	return "", fmt.Errorf("no such check on this pull request")
}

// Merge merges now, or arms auto-merge (which also enqueues when the base
// branch has a merge queue). --match-head-commit makes GitHub refuse if the
// branch moved after the person confirmed.
func (g *GitHub) Merge(ctx context.Context, n int, m MergeRequest) (string, error) {
	flag := map[string]string{"merge": "--merge", "squash": "--squash", "rebase": "--rebase"}[m.Method]
	if flag == "" {
		return "", fmt.Errorf("merge method must be merge, squash or rebase")
	}
	args := []string{"pr", "merge", strconv.Itoa(n), "-R", g.ref.Slug(), flag}
	if m.DeleteBranch {
		args = append(args, "--delete-branch")
	}
	if m.Auto {
		args = append(args, "--auto")
	}
	if m.HeadSHA != "" {
		args = append(args, "--match-head-commit", m.HeadSHA)
	}
	out, err := g.run(ctx, 120, args...)
	return strings.TrimSpace(out), err
}

func (g *GitHub) DisableAutoMerge(ctx context.Context, n int) error {
	_, err := g.run(ctx, 60, "pr", "merge", strconv.Itoa(n), "-R", g.ref.Slug(), "--disable-auto")
	return err
}

func (g *GitHub) EditReviewers(ctx context.Context, n int, add, remove []string) error {
	args := []string{"pr", "edit", strconv.Itoa(n), "-R", g.ref.Slug()}
	if len(add) > 0 {
		args = append(args, "--add-reviewer", strings.Join(add, ","))
	}
	if len(remove) > 0 {
		args = append(args, "--remove-reviewer", strings.Join(remove, ","))
	}
	if len(add)+len(remove) == 0 {
		return nil
	}
	_, err := g.run(ctx, 60, args...)
	return err
}

func (g *GitHub) EditLabels(ctx context.Context, kind string, n int, add, remove []string) error {
	sub := "issue"
	if kind == "pr" {
		sub = "pr"
	}
	args := []string{sub, "edit", strconv.Itoa(n), "-R", g.ref.Slug()}
	if len(add) > 0 {
		args = append(args, "--add-label", strings.Join(add, ","))
	}
	if len(remove) > 0 {
		args = append(args, "--remove-label", strings.Join(remove, ","))
	}
	if len(add)+len(remove) == 0 {
		return nil
	}
	_, err := g.run(ctx, 60, args...)
	return err
}

func (g *GitHub) Comment(ctx context.Context, kind string, n int, body string) error {
	sub := "issue"
	if kind == "pr" {
		sub = "pr"
	}
	_, err := g.run(ctx, 60, sub, "comment", strconv.Itoa(n), "-R", g.ref.Slug(), "--body", body)
	return err
}

func (g *GitHub) SetState(ctx context.Context, kind string, n int, open bool) error {
	sub, verb := "issue", "close"
	if kind == "pr" {
		sub = "pr"
	}
	if open {
		verb = "reopen"
	}
	_, err := g.run(ctx, 60, sub, verb, strconv.Itoa(n), "-R", g.ref.Slug())
	return err
}

func (g *GitHub) Labels(ctx context.Context) ([]Label, error) {
	var rows []ghLabel
	if err := g.json(ctx, &rows, "label", "list", "-R", g.ref.Slug(), "--limit", "200", "--json", "name,color"); err != nil {
		return nil, err
	}
	return ghLabels(rows), nil
}

func (g *GitHub) Users(ctx context.Context) ([]User, error) {
	var repo ghRepo
	if err := g.json(ctx, &repo, "repo", "view", g.ref.Slug(), "--json", "assignableUsers"); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(repo.AssignableUsers))
	for _, u := range repo.AssignableUsers {
		out = append(out, User{Login: u.Login, Name: u.Name})
	}
	return out, nil
}

// FetchRefs uses refs/pull/N/head for the head, which exists on the base
// repository for same-repository and fork PRs alike.
func (g *GitHub) FetchRefs(pr *PRDetail) (string, string) {
	return "refs/heads/" + pr.Base, "refs/pull/" + pr.ID + "/head"
}
