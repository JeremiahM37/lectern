package trackers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bitbucket Cloud (api.bitbucket.org/2.0) and Bitbucket Data Center
// (/rest/api/1.0) are different APIs under one name, so they are two
// adapters. Neither has labels or reactions on pull requests; Data Center
// has no issues (teams use Jira), and Cloud's issue tracker may be off for a
// repository, in which case listing issues reports that.

// NewBitbucket picks the adapter for ref.Flavor.
func NewBitbucket(ref RepoRef, cr Creds, hc *http.Client) Forge {
	if ref.Flavor == "server" {
		base := cr.BaseURL
		if base == "" {
			base = "https://" + ref.Host
		}
		return &BitbucketServer{c: rest{name: "Bitbucket", base: base, auth: headerAuth("Bearer " + cr.Token), http: hc}, ref: ref, base: base}
	}
	base := cr.BaseURL
	if base == "" {
		base = "https://api.bitbucket.org/2.0"
	}
	auth := headerAuth("Bearer " + cr.Token) // a repository or workspace access token
	if cr.Username != "" {
		auth = basicAuth(cr.Username, cr.Token) // an API token or app password with its account
	}
	return &BitbucketCloud{c: rest{name: "Bitbucket", base: base, auth: auth, http: hc}, ref: ref}
}

// ---- Cloud -----------------------------------------------------------------

type BitbucketCloud struct {
	c   rest
	ref RepoRef
}

func (b *BitbucketCloud) Kind() string  { return "bitbucket" }
func (b *BitbucketCloud) Repo() RepoRef { return b.ref }
func (b *BitbucketCloud) repo() string  { return "repositories/" + b.ref.Path }

type bbUser struct {
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
	AccountID   string `json:"account_id"`
	UUID        string `json:"uuid"`
}

func (u bbUser) login() string { return firstNonEmpty(u.Nickname, u.DisplayName) }

type bbPR struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"` // OPEN | MERGED | DECLINED | SUPERSEDED
	Draft       bool   `json:"draft"`
	Author      bbUser `json:"author"`
	Source      struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name            string   `json:"name"`
			MergeStrategies []string `json:"merge_strategies"`
			DefaultStrategy string   `json:"default_merge_strategy"`
		} `json:"branch"`
	} `json:"destination"`
	Reviewers    []bbUser `json:"reviewers"`
	Participants []struct {
		User     bbUser `json:"user"`
		Role     string `json:"role"`
		Approved bool   `json:"approved"`
		State    string `json:"state"` // approved | changes_requested | null
	} `json:"participants"`
	CloseSourceBranch bool   `json:"close_source_branch"`
	CreatedOn         string `json:"created_on"`
	UpdatedOn         string `json:"updated_on"`
	Links             struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

type bbPage[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

func bbState(s string) string {
	switch s {
	case "OPEN":
		return "open"
	case "MERGED":
		return "merged"
	}
	return "closed"
}

func (b *BitbucketCloud) prItem(p bbPR) Item {
	it := Item{Source: "bitbucket", Kind: "pr", ID: strconv.Itoa(p.ID), Title: p.Title, URL: p.Links.HTML.Href,
		State: bbState(p.State), Author: p.Author.login(), Assignees: []string{}, Labels: []Label{}, UpdatedAt: p.UpdatedOn,
		Draft: p.Draft, Head: p.Source.Branch.Name, Base: p.Destination.Branch.Name}
	for _, pt := range p.Participants {
		if pt.State == "changes_requested" {
			it.Review = "changes_requested"
		} else if pt.Approved && it.Review == "" {
			it.Review = "approved"
		}
	}
	return it
}

// bbQuery quotes a value for Bitbucket's query language.
func bbQuery(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (b *BitbucketCloud) me(ctx context.Context) (bbUser, error) {
	var u bbUser
	err := b.c.do(ctx, "GET", "user", nil, &u)
	return u, err
}

func (b *BitbucketCloud) List(ctx context.Context, kind string, f Filter) ([]Item, error) {
	if kind != "pr" {
		return b.listIssues(ctx, f)
	}
	var qs []string
	switch f.State {
	case "", "open":
		qs = append(qs, `state="OPEN"`)
	case "merged":
		qs = append(qs, `state="MERGED"`)
	case "closed":
		qs = append(qs, `(state="MERGED" OR state="DECLINED")`)
	case "all":
	default:
		return nil, fmt.Errorf("unknown state %q", f.State)
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		qs = append(qs, "title ~ "+bbQuery(s))
	}
	if f.Mine != "" {
		me, err := b.me(ctx)
		if err != nil {
			return nil, err
		}
		switch f.Mine {
		case "authored", "assigned":
			qs = append(qs, "author.uuid = "+bbQuery(me.UUID))
		case "review":
			qs = append(qs, "reviewers.uuid = "+bbQuery(me.UUID))
		}
	}
	q := url.Values{"pagelen": {strconv.Itoa(min(f.limit(), 50))}, "sort": {"-updated_on"}}
	if len(qs) > 0 {
		q.Set("q", strings.Join(qs, " AND "))
	}
	if f.State == "all" {
		// without a state query Bitbucket lists only open pull requests
		for _, s := range []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"} {
			q.Add("state", s)
		}
	}
	var page bbPage[bbPR]
	if err := b.c.do(ctx, "GET", b.repo()+"/pullrequests?"+q.Encode(), nil, &page); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(page.Values))
	for _, p := range page.Values {
		out = append(out, b.prItem(p))
	}
	return out, nil
}

type bbIssue struct {
	ID       int     `json:"id"`
	Title    string  `json:"title"`
	State    string  `json:"state"`
	Kind     string  `json:"kind"`
	Priority string  `json:"priority"`
	Reporter *bbUser `json:"reporter"`
	Assignee *bbUser `json:"assignee"`
	Content  struct {
		Raw string `json:"raw"`
	} `json:"content"`
	CreatedOn string `json:"created_on"`
	UpdatedOn string `json:"updated_on"`
	Links     struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func bbIssueOpen(s string) bool { return s == "new" || s == "open" || s == "on hold" }

func (b *BitbucketCloud) issueItem(i bbIssue) Item {
	it := Item{Source: "bitbucket", Kind: "issue", ID: strconv.Itoa(i.ID), Title: i.Title, URL: i.Links.HTML.Href,
		State: "closed", Assignees: []string{}, Labels: []Label{}, UpdatedAt: i.UpdatedOn, Priority: i.Priority}
	if bbIssueOpen(i.State) {
		it.State = "open"
	}
	if i.Reporter != nil {
		it.Author = i.Reporter.login()
	}
	if i.Assignee != nil {
		it.Assignees = []string{i.Assignee.login()}
	}
	if i.Kind != "" {
		it.Labels = []Label{{Name: i.Kind}}
	}
	return it
}

func (b *BitbucketCloud) listIssues(ctx context.Context, f Filter) ([]Item, error) {
	var qs []string
	switch f.State {
	case "", "open":
		qs = append(qs, `(state="new" OR state="open" OR state="on hold")`)
	case "closed", "merged":
		qs = append(qs, `(state="resolved" OR state="invalid" OR state="duplicate" OR state="wontfix" OR state="closed")`)
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		qs = append(qs, "title ~ "+bbQuery(s))
	}
	if f.Mine != "" {
		me, err := b.me(ctx)
		if err != nil {
			return nil, err
		}
		if f.Mine == "assigned" {
			qs = append(qs, "assignee.uuid = "+bbQuery(me.UUID))
		} else {
			qs = append(qs, "reporter.uuid = "+bbQuery(me.UUID))
		}
	}
	q := url.Values{"pagelen": {strconv.Itoa(min(f.limit(), 50))}, "sort": {"-updated_on"}}
	if len(qs) > 0 {
		q.Set("q", strings.Join(qs, " AND "))
	}
	var page bbPage[bbIssue]
	if err := b.c.do(ctx, "GET", b.repo()+"/issues?"+q.Encode(), nil, &page); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, unsupported("this Bitbucket repository", "issue tracker turned on")
		}
		return nil, err
	}
	out := make([]Item, 0, len(page.Values))
	for _, i := range page.Values {
		out = append(out, b.issueItem(i))
	}
	return out, nil
}

type bbComment struct {
	ID      int64 `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User      bbUser `json:"user"`
	CreatedOn string `json:"created_on"`
	Deleted   bool   `json:"deleted"`
	Inline    *struct {
		Path string `json:"path"`
	} `json:"inline"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (b *BitbucketCloud) PR(ctx context.Context, n int) (*PRDetail, error) {
	base := fmt.Sprintf("%s/pullrequests/%d", b.repo(), n)
	var (
		p        bbPR
		prErr    error
		comments bbPage[bbComment]
		commits  bbPage[struct {
			Hash    string `json:"hash"`
			Message string `json:"message"`
			Date    string `json:"date"`
			Author  struct {
				Raw  string  `json:"raw"`
				User *bbUser `json:"user"`
			} `json:"author"`
		}]
		statuses bbPage[struct {
			Key   string `json:"key"`
			Name  string `json:"name"`
			State string `json:"state"`
			URL   string `json:"url"`
		}]
		diffstat bbPage[struct {
			Added   int `json:"lines_added"`
			Removed int `json:"lines_removed"`
		}]
		open bbPage[bbPR]
		wg   sync.WaitGroup
	)
	wg.Add(6)
	go func() { defer wg.Done(); prErr = b.c.do(ctx, "GET", base, nil, &p) }()
	go func() { defer wg.Done(); _ = b.c.do(ctx, "GET", base+"/comments?pagelen=100", nil, &comments) }()
	go func() { defer wg.Done(); _ = b.c.do(ctx, "GET", base+"/commits?pagelen=50", nil, &commits) }()
	go func() { defer wg.Done(); _ = b.c.do(ctx, "GET", base+"/statuses?pagelen=100", nil, &statuses) }()
	go func() { defer wg.Done(); _ = b.c.do(ctx, "GET", base+"/diffstat?pagelen=500", nil, &diffstat) }()
	go func() {
		defer wg.Done()
		_ = b.c.do(ctx, "GET", b.repo()+"/pullrequests?pagelen=50&q="+url.QueryEscape(`state="OPEN"`), nil, &open)
	}()
	wg.Wait()
	if prErr != nil {
		return nil, prErr
	}
	d := &PRDetail{Item: b.prItem(p), Body: p.Description, HeadSHA: p.Source.Commit.Hash, CreatedAt: p.CreatedOn,
		CheckRuns: []Check{}, Reviewers: []Reviewer{}, Timeline: []Event{}, Mergeable: "unknown", MergeState: "unknown"}
	d.CrossRepo = p.Source.Repository.FullName != "" && !strings.EqualFold(p.Source.Repository.FullName, b.ref.Path)
	for _, s := range diffstat.Values {
		d.Additions += s.Added
		d.Deletions += s.Removed
	}
	d.ChangedFiles = len(diffstat.Values)
	for _, s := range statuses.Values {
		c := Check{ID: "status:" + s.Key, Name: firstNonEmpty(s.Name, s.Key), URL: s.URL}
		switch s.State {
		case "SUCCESSFUL":
			c.Status = "pass"
		case "FAILED":
			c.Status = "fail"
		case "STOPPED":
			c.Status = "cancel"
		default:
			c.Status = "pending"
		}
		d.CheckRuns = append(d.CheckRuns, c)
	}
	d.Checks = RollupChecks(d.CheckRuns)
	decided := map[string]string{}
	for _, pt := range p.Participants {
		switch {
		case pt.State == "changes_requested":
			decided[pt.User.login()] = "changes_requested"
		case pt.Approved:
			decided[pt.User.login()] = "approved"
		}
	}
	for _, r := range p.Reviewers {
		st := decided[r.login()]
		if st == "" {
			st = "requested"
		}
		delete(decided, r.login())
		d.Reviewers = append(d.Reviewers, Reviewer{Login: r.login(), State: st})
	}
	for login, st := range decided {
		d.Reviewers = append(d.Reviewers, Reviewer{Login: login, State: st})
	}
	for _, c := range comments.Values {
		if c.Deleted {
			continue
		}
		body := c.Content.Raw
		if c.Inline != nil && c.Inline.Path != "" {
			body = "`" + c.Inline.Path + "`: " + body
		}
		d.Timeline = append(d.Timeline, Event{Kind: "comment", Author: c.User.login(), Body: body, At: c.CreatedOn, URL: c.Links.HTML.Href})
	}
	for _, c := range commits.Values {
		author := c.Author.Raw
		if c.Author.User != nil {
			author = c.Author.User.login()
		}
		headline, _, _ := strings.Cut(c.Message, "\n")
		d.Timeline = append(d.Timeline, Event{Kind: "commit", Author: author, Body: headline, State: shortSHA(c.Hash), At: c.Date})
	}
	sort.SliceStable(d.Timeline, func(i, j int) bool { return d.Timeline[i].At < d.Timeline[j].At })
	o := MergeOptions{DeleteBranchDefault: p.CloseSourceBranch, CanMerge: true}
	for _, s := range p.Destination.Branch.MergeStrategies {
		if m := bbMethod[s]; m != "" && !contains(o.Methods, m) {
			o.Methods = append(o.Methods, m)
		}
	}
	if len(o.Methods) == 0 {
		o.Methods = []string{"merge", "squash", "rebase"}
	}
	o.Default = o.Methods[0]
	if m := bbMethod[p.Destination.Branch.DefaultStrategy]; contains(o.Methods, m) {
		o.Default = m
	}
	d.Merge = o
	entries := []StackEntry{}
	found := false
	for _, x := range open.Values {
		found = found || x.ID == p.ID
		entries = append(entries, StackEntry{Number: x.ID, Title: x.Title, Head: x.Source.Branch.Name, Base: x.Destination.Branch.Name, URL: x.Links.HTML.Href, State: bbState(x.State)})
	}
	if !found {
		entries = append(entries, StackEntry{Number: p.ID, Title: p.Title, Head: p.Source.Branch.Name, Base: p.Destination.Branch.Name, URL: p.Links.HTML.Href, State: d.State})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	d.Stack = BuildStack(entries, p.ID)
	return d, nil
}

// bbMethod maps Bitbucket's merge strategies onto the page's three methods.
var bbMethod = map[string]string{"merge_commit": "merge", "squash": "squash", "squash_fast_forward": "squash",
	"fast_forward": "rebase", "rebase_fast_forward": "rebase", "rebase_merge": "rebase"}

var bbStrategy = map[string]string{"merge": "merge_commit", "squash": "squash", "rebase": "fast_forward"}

func (b *BitbucketCloud) Issue(ctx context.Context, n int) (*IssueDetail, error) {
	var i bbIssue
	base := fmt.Sprintf("%s/issues/%d", b.repo(), n)
	if err := b.c.do(ctx, "GET", base, nil, &i); err != nil {
		return nil, err
	}
	var comments bbPage[bbComment]
	_ = b.c.do(ctx, "GET", base+"/comments?pagelen=100", nil, &comments)
	d := &IssueDetail{Item: b.issueItem(i), Body: i.Content.Raw, CreatedAt: i.CreatedOn, Children: []Item{}, Transitions: []Transition{},
		Timeline: []Event{}, BranchName: BranchName(strconv.Itoa(i.ID), i.Title)}
	for _, c := range comments.Values {
		if strings.TrimSpace(c.Content.Raw) != "" {
			d.Timeline = append(d.Timeline, Event{Kind: "comment", Author: c.User.login(), Body: c.Content.Raw, At: c.CreatedOn})
		}
	}
	return d, nil
}

func (b *BitbucketCloud) JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error) {
	for _, c := range pr.CheckRuns {
		if c.ID == checkID {
			return "", fmt.Errorf("%s reports only a build status; open its log on the CI site: %s", c.Name, c.URL)
		}
	}
	return "", fmt.Errorf("no such check on this pull request")
}

// Merge merges now. Bitbucket Cloud's merge call cannot be pinned to a
// commit, so the head is read first and a moved branch is refused here.
func (b *BitbucketCloud) Merge(ctx context.Context, n int, m MergeRequest) (string, error) {
	if m.Auto {
		return "", unsupported("Bitbucket Cloud's API", "auto-merge")
	}
	strategy := bbStrategy[m.Method]
	if strategy == "" {
		return "", fmt.Errorf("merge method must be merge, squash or rebase")
	}
	base := fmt.Sprintf("%s/pullrequests/%d", b.repo(), n)
	if m.HeadSHA != "" {
		var p bbPR
		if err := b.c.do(ctx, "GET", base, nil, &p); err != nil {
			return "", err
		}
		if !shaMatch(p.Source.Commit.Hash, m.HeadSHA) {
			return "", fmt.Errorf("the branch moved since you confirmed (now %s); review and try again", shortSHA(p.Source.Commit.Hash))
		}
	}
	var res bbPR
	if err := b.c.do(ctx, "POST", base+"/merge", map[string]any{"type": "pullrequest", "merge_strategy": strategy, "close_source_branch": m.DeleteBranch}, &res); err != nil {
		return "", err
	}
	return bbState(res.State), nil
}

// shaMatch compares a full and a possibly abbreviated commit id (Bitbucket
// Cloud reports 12-character hashes).
func shaMatch(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == "" || b == "" {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

func (b *BitbucketCloud) DisableAutoMerge(ctx context.Context, n int) error {
	return unsupported("Bitbucket Cloud's API", "auto-merge")
}

// EditReviewers sets the whole reviewer list (the only way Cloud's API
// changes it), resolving names through the workspace's members.
func (b *BitbucketCloud) EditReviewers(ctx context.Context, n int, add, remove []string) error {
	base := fmt.Sprintf("%s/pullrequests/%d", b.repo(), n)
	var p bbPR
	if err := b.c.do(ctx, "GET", base, nil, &p); err != nil {
		return err
	}
	users, err := b.members(ctx)
	if err != nil && len(add) > 0 {
		return err
	}
	byLogin := map[string]string{}
	for _, u := range users {
		byLogin[strings.ToLower(u.login())] = u.UUID
		byLogin[strings.ToLower(u.DisplayName)] = u.UUID
	}
	var list []map[string]string
	have := map[string]bool{}
	for _, r := range p.Reviewers {
		if containsFold(remove, r.login()) {
			continue
		}
		have[r.UUID] = true
		list = append(list, map[string]string{"uuid": r.UUID})
	}
	for _, a := range add {
		id := byLogin[strings.ToLower(a)]
		if id == "" {
			return fmt.Errorf("%q is not a member of this Bitbucket workspace", a)
		}
		if !have[id] {
			list = append(list, map[string]string{"uuid": id})
		}
	}
	body := map[string]any{"title": p.Title, "reviewers": list}
	return b.c.do(ctx, "PUT", base, body, nil)
}

func (b *BitbucketCloud) members(ctx context.Context) ([]bbUser, error) {
	ws, _, _ := strings.Cut(b.ref.Path, "/")
	var page bbPage[struct {
		User bbUser `json:"user"`
	}]
	if err := b.c.do(ctx, "GET", "workspaces/"+ws+"/members?pagelen=100", nil, &page); err != nil {
		return nil, err
	}
	out := make([]bbUser, 0, len(page.Values))
	for _, m := range page.Values {
		out = append(out, m.User)
	}
	return out, nil
}

func (b *BitbucketCloud) EditLabels(ctx context.Context, kind string, n int, add, remove []string) error {
	return unsupported("Bitbucket", "labels")
}

func (b *BitbucketCloud) Comment(ctx context.Context, kind string, n int, body string) error {
	sub := "issues"
	if kind == "pr" {
		sub = "pullrequests"
	}
	return b.c.do(ctx, "POST", fmt.Sprintf("%s/%s/%d/comments", b.repo(), sub, n), map[string]any{"content": map[string]string{"raw": body}}, nil)
}

// SetState declines a pull request (Cloud cannot reopen a declined one) or
// resolves/reopens an issue.
func (b *BitbucketCloud) SetState(ctx context.Context, kind string, n int, open bool) error {
	if kind == "pr" {
		if open {
			return unsupported("Bitbucket Cloud", "way to reopen a declined pull request")
		}
		return b.c.do(ctx, "POST", fmt.Sprintf("%s/pullrequests/%d/decline", b.repo(), n), nil, nil)
	}
	state := "resolved"
	if open {
		state = "open"
	}
	return b.c.do(ctx, "PUT", fmt.Sprintf("%s/issues/%d", b.repo(), n), map[string]any{"state": state}, nil)
}

func (b *BitbucketCloud) Labels(ctx context.Context) ([]Label, error) { return []Label{}, nil }

func (b *BitbucketCloud) Users(ctx context.Context) ([]User, error) {
	users, err := b.members(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(users))
	for _, u := range users {
		out = append(out, User{Login: u.login(), Name: u.DisplayName})
	}
	return out, nil
}

// FetchRefs: Cloud keeps no pull request refs on the repository, so the head
// is the source branch itself (a fork's branch cannot be fetched from origin).
func (b *BitbucketCloud) FetchRefs(pr *PRDetail) (string, string) {
	return "refs/heads/" + pr.Base, "refs/heads/" + pr.Head
}

func (b *BitbucketCloud) React(ctx context.Context, kind string, n int, subject, emoji string) error {
	return unsupported("Bitbucket", "reactions")
}

// ---- Data Center -----------------------------------------------------------

type BitbucketServer struct {
	c    rest
	ref  RepoRef
	base string
}

func (b *BitbucketServer) Kind() string  { return "bitbucket" }
func (b *BitbucketServer) Repo() RepoRef { return b.ref }

func (b *BitbucketServer) repo() string {
	project, repo, _ := strings.Cut(b.ref.Path, "/")
	return "rest/api/1.0/projects/" + url.PathEscape(project) + "/repos/" + url.PathEscape(repo)
}

type bsUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Slug        string `json:"slug"`
}

type bsRef struct {
	ID           string `json:"id"`
	DisplayID    string `json:"displayId"`
	LatestCommit string `json:"latestCommit"`
	Repository   struct {
		Slug    string `json:"slug"`
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
	} `json:"repository"`
}

type bsPR struct {
	ID          int    `json:"id"`
	Version     int    `json:"version"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Draft       bool   `json:"draft"`
	Author      struct {
		User bsUser `json:"user"`
	} `json:"author"`
	Reviewers []struct {
		User   bsUser `json:"user"`
		Status string `json:"status"` // APPROVED | UNAPPROVED | NEEDS_WORK
	} `json:"reviewers"`
	FromRef     bsRef `json:"fromRef"`
	ToRef       bsRef `json:"toRef"`
	CreatedDate int64 `json:"createdDate"`
	UpdatedDate int64 `json:"updatedDate"`
	ClosedDate  int64 `json:"closedDate"`
	Properties  struct {
		MergeResult *struct {
			Outcome string `json:"outcome"` // CLEAN | CONFLICTED
		} `json:"mergeResult"`
	} `json:"properties"`
	Links struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

func msTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func (b *BitbucketServer) prItem(p bsPR) Item {
	it := Item{Source: "bitbucket", Kind: "pr", ID: strconv.Itoa(p.ID), Title: p.Title, State: bbState(p.State),
		Author: firstNonEmpty(p.Author.User.Name, p.Author.User.DisplayName), Assignees: []string{}, Labels: []Label{},
		UpdatedAt: msTime(p.UpdatedDate), Draft: p.Draft, Head: p.FromRef.DisplayID, Base: p.ToRef.DisplayID}
	if len(p.Links.Self) > 0 {
		it.URL = p.Links.Self[0].Href
	}
	if p.Properties.MergeResult != nil && p.Properties.MergeResult.Outcome == "CONFLICTED" {
		it.Conflicts = true
	}
	for _, r := range p.Reviewers {
		if r.Status == "NEEDS_WORK" {
			it.Review = "changes_requested"
		} else if r.Status == "APPROVED" && it.Review == "" {
			it.Review = "approved"
		}
	}
	return it
}

func (b *BitbucketServer) List(ctx context.Context, kind string, f Filter) ([]Item, error) {
	if kind != "pr" {
		return nil, unsupported("Bitbucket Data Center", "issues (it uses Jira)")
	}
	q := url.Values{"limit": {strconv.Itoa(f.limit())}, "order": {"NEWEST"}}
	switch f.State {
	case "", "open":
		q.Set("state", "OPEN")
	case "merged":
		q.Set("state", "MERGED")
	case "closed":
		q.Set("state", "ALL")
	case "all":
		q.Set("state", "ALL")
	default:
		return nil, fmt.Errorf("unknown state %q", f.State)
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		q.Set("filterText", s)
	}
	if f.Mine != "" {
		// the whoami endpoint answers with the username as plain text
		var name string
		if err := b.c.do(ctx, "GET", "plugins/servlet/applinks/whoami", nil, &name); err != nil {
			return nil, err
		}
		role := map[string]string{"authored": "AUTHOR", "assigned": "AUTHOR", "review": "REVIEWER"}[f.Mine]
		q.Set("role.1", role)
		q.Set("username.1", strings.TrimSpace(name))
	}
	var page struct {
		Values []bsPR `json:"values"`
	}
	if err := b.c.do(ctx, "GET", b.repo()+"/pull-requests?"+q.Encode(), nil, &page); err != nil {
		return nil, err
	}
	out := []Item{}
	for _, p := range page.Values {
		if f.State == "closed" && p.State == "OPEN" {
			continue
		}
		out = append(out, b.prItem(p))
	}
	return out, nil
}

func (b *BitbucketServer) PR(ctx context.Context, n int) (*PRDetail, error) {
	base := fmt.Sprintf("%s/pull-requests/%d", b.repo(), n)
	var (
		p          bsPR
		prErr      error
		activities struct {
			Values []struct {
				ID          int64  `json:"id"`
				Action      string `json:"action"`
				CreatedDate int64  `json:"createdDate"`
				User        bsUser `json:"user"`
				Comment     *struct {
					ID     int64  `json:"id"`
					Text   string `json:"text"`
					Author bsUser `json:"author"`
				} `json:"comment"`
				Added *struct {
					Commits []struct {
						ID      string `json:"id"`
						Message string `json:"message"`
					} `json:"commits"`
				} `json:"added"`
			} `json:"values"`
		}
		changes struct {
			Values []struct{} `json:"values"`
		}
		open struct {
			Values []bsPR `json:"values"`
		}
		wg sync.WaitGroup
	)
	wg.Add(4)
	go func() { defer wg.Done(); prErr = b.c.do(ctx, "GET", base, nil, &p) }()
	go func() { defer wg.Done(); _ = b.c.do(ctx, "GET", base+"/activities?limit=100", nil, &activities) }()
	go func() { defer wg.Done(); _ = b.c.do(ctx, "GET", base+"/changes?limit=1000", nil, &changes) }()
	go func() {
		defer wg.Done()
		_ = b.c.do(ctx, "GET", b.repo()+"/pull-requests?state=OPEN&limit=100", nil, &open)
	}()
	wg.Wait()
	if prErr != nil {
		return nil, prErr
	}
	d := &PRDetail{Item: b.prItem(p), Body: p.Description, HeadSHA: p.FromRef.LatestCommit, CreatedAt: msTime(p.CreatedDate),
		ChangedFiles: len(changes.Values), CheckRuns: []Check{}, Reviewers: []Reviewer{}, Timeline: []Event{}}
	d.CrossRepo = !strings.EqualFold(p.FromRef.Repository.Project.Key+"/"+p.FromRef.Repository.Slug, p.ToRef.Repository.Project.Key+"/"+p.ToRef.Repository.Slug)
	d.Mergeable, d.MergeState = "mergeable", "clean"
	if d.Conflicts {
		d.Mergeable, d.MergeState = "conflicting", "dirty"
	}
	if p.State == "MERGED" {
		d.MergedAt = msTime(p.ClosedDate)
	} else if p.State == "DECLINED" {
		d.ClosedAt = msTime(p.ClosedDate)
	}
	for _, r := range p.Reviewers {
		st := map[string]string{"APPROVED": "approved", "NEEDS_WORK": "changes_requested"}[r.Status]
		if st == "" {
			st = "requested"
		}
		d.Reviewers = append(d.Reviewers, Reviewer{Login: r.User.Name, State: st})
	}
	for _, a := range activities.Values {
		at := msTime(a.CreatedDate)
		switch {
		case a.Action == "COMMENTED" && a.Comment != nil:
			d.Timeline = append(d.Timeline, Event{Kind: "comment", Author: a.Comment.Author.Name, Body: a.Comment.Text, At: at})
		case a.Action == "APPROVED":
			d.Timeline = append(d.Timeline, Event{Kind: "review", Author: a.User.Name, State: "approved", At: at})
		case a.Action == "REVIEWED":
			d.Timeline = append(d.Timeline, Event{Kind: "review", Author: a.User.Name, State: "changes_requested", At: at})
		case a.Action == "RESCOPED" && a.Added != nil:
			for _, c := range a.Added.Commits {
				headline, _, _ := strings.Cut(c.Message, "\n")
				d.Timeline = append(d.Timeline, Event{Kind: "commit", Author: a.User.Name, Body: headline, State: shortSHA(c.ID), At: at})
			}
		case a.Action == "MERGED" || a.Action == "DECLINED" || a.Action == "REOPENED" || a.Action == "OPENED":
			d.Timeline = append(d.Timeline, Event{Kind: "event", Author: a.User.Name, Body: strings.ToLower(a.Action), At: at})
		}
	}
	sort.SliceStable(d.Timeline, func(i, j int) bool { return d.Timeline[i].At < d.Timeline[j].At })
	if d.HeadSHA != "" {
		var builds struct {
			Values []struct {
				Key   string `json:"key"`
				Name  string `json:"name"`
				State string `json:"state"`
				URL   string `json:"url"`
			} `json:"values"`
		}
		if b.c.do(ctx, "GET", "rest/build-status/1.0/commits/"+url.PathEscape(d.HeadSHA), nil, &builds) == nil {
			for _, s := range builds.Values {
				c := Check{ID: "build:" + s.Key, Name: firstNonEmpty(s.Name, s.Key), URL: s.URL}
				switch s.State {
				case "SUCCESSFUL":
					c.Status = "pass"
				case "FAILED":
					c.Status = "fail"
				default:
					c.Status = "pending"
				}
				d.CheckRuns = append(d.CheckRuns, c)
			}
		}
	}
	d.Checks = RollupChecks(d.CheckRuns)
	d.Merge = MergeOptions{Methods: []string{"merge", "squash", "rebase"}, Default: "merge", CanMerge: true}
	entries := []StackEntry{}
	found := false
	for _, x := range open.Values {
		found = found || x.ID == p.ID
		entries = append(entries, StackEntry{Number: x.ID, Title: x.Title, Head: x.FromRef.DisplayID, Base: x.ToRef.DisplayID, State: bbState(x.State)})
	}
	if !found {
		entries = append(entries, StackEntry{Number: p.ID, Title: p.Title, Head: p.FromRef.DisplayID, Base: p.ToRef.DisplayID, State: d.State})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	d.Stack = BuildStack(entries, p.ID)
	return d, nil
}

func (b *BitbucketServer) Issue(ctx context.Context, n int) (*IssueDetail, error) {
	return nil, unsupported("Bitbucket Data Center", "issues (it uses Jira)")
}

func (b *BitbucketServer) JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error) {
	for _, c := range pr.CheckRuns {
		if c.ID == checkID {
			return "", fmt.Errorf("%s reports only a build status; open its log on the CI site: %s", c.Name, c.URL)
		}
	}
	return "", fmt.Errorf("no such check on this pull request")
}

var bsStrategy = map[string]string{"merge": "no-ff", "squash": "squash", "rebase": "rebase-no-ff"}

// Merge pins the pull request's version (which changes on every push and
// edit) and checks the head commit, then merges; the branch is deleted
// afterwards when asked.
func (b *BitbucketServer) Merge(ctx context.Context, n int, m MergeRequest) (string, error) {
	if m.Auto {
		return "", unsupported("this Bitbucket Data Center adapter", "auto-merge")
	}
	strategy := bsStrategy[m.Method]
	if strategy == "" {
		return "", fmt.Errorf("merge method must be merge, squash or rebase")
	}
	base := fmt.Sprintf("%s/pull-requests/%d", b.repo(), n)
	var p bsPR
	if err := b.c.do(ctx, "GET", base, nil, &p); err != nil {
		return "", err
	}
	if m.HeadSHA != "" && !shaMatch(p.FromRef.LatestCommit, m.HeadSHA) {
		return "", fmt.Errorf("the branch moved since you confirmed (now %s); review and try again", shortSHA(p.FromRef.LatestCommit))
	}
	var res bsPR
	if err := b.c.do(ctx, "POST", fmt.Sprintf("%s/merge?version=%d", base, p.Version), map[string]any{"strategyId": strategy}, &res); err != nil {
		return "", err
	}
	if m.DeleteBranch && !bsIsFork(p) {
		project, repo, _ := strings.Cut(b.ref.Path, "/")
		_ = b.c.do(ctx, "DELETE", "rest/branch-utils/1.0/projects/"+url.PathEscape(project)+"/repos/"+url.PathEscape(repo)+"/branches",
			map[string]any{"name": p.FromRef.ID, "dryRun": false}, nil)
	}
	return bbState(res.State), nil
}

func bsIsFork(p bsPR) bool {
	return p.FromRef.Repository.Slug != p.ToRef.Repository.Slug || p.FromRef.Repository.Project.Key != p.ToRef.Repository.Project.Key
}

func (b *BitbucketServer) DisableAutoMerge(ctx context.Context, n int) error {
	return unsupported("this Bitbucket Data Center adapter", "auto-merge")
}

// EditReviewers updates the reviewer list with the version it was read at.
func (b *BitbucketServer) EditReviewers(ctx context.Context, n int, add, remove []string) error {
	base := fmt.Sprintf("%s/pull-requests/%d", b.repo(), n)
	var p bsPR
	if err := b.c.do(ctx, "GET", base, nil, &p); err != nil {
		return err
	}
	var list []map[string]any
	have := map[string]bool{}
	for _, r := range p.Reviewers {
		if containsFold(remove, r.User.Name) {
			continue
		}
		have[strings.ToLower(r.User.Name)] = true
		list = append(list, map[string]any{"user": map[string]string{"name": r.User.Name}})
	}
	for _, a := range add {
		if !have[strings.ToLower(a)] {
			list = append(list, map[string]any{"user": map[string]string{"name": a}})
		}
	}
	return b.c.do(ctx, "PUT", base, map[string]any{"version": p.Version, "title": p.Title, "reviewers": list}, nil)
}

func (b *BitbucketServer) EditLabels(ctx context.Context, kind string, n int, add, remove []string) error {
	return unsupported("Bitbucket", "labels")
}

func (b *BitbucketServer) Comment(ctx context.Context, kind string, n int, body string) error {
	if kind != "pr" {
		return unsupported("Bitbucket Data Center", "issues")
	}
	return b.c.do(ctx, "POST", fmt.Sprintf("%s/pull-requests/%d/comments", b.repo(), n), map[string]any{"text": body}, nil)
}

func (b *BitbucketServer) SetState(ctx context.Context, kind string, n int, open bool) error {
	if kind != "pr" {
		return unsupported("Bitbucket Data Center", "issues")
	}
	base := fmt.Sprintf("%s/pull-requests/%d", b.repo(), n)
	var p bsPR
	if err := b.c.do(ctx, "GET", base, nil, &p); err != nil {
		return err
	}
	verb := "decline"
	if open {
		verb = "reopen"
	}
	return b.c.do(ctx, "POST", fmt.Sprintf("%s/%s?version=%d", base, verb, p.Version), map[string]any{}, nil)
}

func (b *BitbucketServer) Labels(ctx context.Context) ([]Label, error) { return []Label{}, nil }

func (b *BitbucketServer) Users(ctx context.Context) ([]User, error) {
	project, _, _ := strings.Cut(b.ref.Path, "/")
	var page struct {
		Values []struct {
			User bsUser `json:"user"`
		} `json:"values"`
	}
	if err := b.c.do(ctx, "GET", "rest/api/1.0/projects/"+url.PathEscape(project)+"/permissions/users?limit=100", nil, &page); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(page.Values))
	for _, v := range page.Values {
		out = append(out, User{Login: v.User.Name, Name: v.User.DisplayName})
	}
	return out, nil
}

// FetchRefs uses refs/pull-requests/N/from, which Data Center keeps on the
// target repository for every pull request, forks included.
func (b *BitbucketServer) FetchRefs(pr *PRDetail) (string, string) {
	return "refs/heads/" + pr.Base, "refs/pull-requests/" + pr.ID + "/from"
}

func (b *BitbucketServer) React(ctx context.Context, kind string, n int, subject, emoji string) error {
	return unsupported("Bitbucket", "reactions")
}
