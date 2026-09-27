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
)

// Gitea is a repository on Gitea or Forgejo (Codeberg included), reached
// over its REST API (/api/v1) with a personal access token.
type Gitea struct {
	c   rest
	ref RepoRef
}

// NewGitea builds the Gitea/Forgejo forge.
func NewGitea(ref RepoRef, cr Creds, hc *http.Client) *Gitea {
	base := cr.BaseURL
	if base == "" {
		base = "https://" + ref.Host + "/api/v1"
	}
	return &Gitea{c: rest{name: "Gitea", base: base, auth: headerAuth("token " + cr.Token), http: hc}, ref: ref}
}

func (g *Gitea) Kind() string  { return "gitea" }
func (g *Gitea) Repo() RepoRef { return g.ref }
func (g *Gitea) repo() string  { return "repos/" + g.ref.Path }

type gtUser struct {
	Login    string `json:"login"`
	FullName string `json:"full_name"`
}

type gtLabel struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type gtPR struct {
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	HTMLURL      string    `json:"html_url"`
	State        string    `json:"state"`
	Draft        bool      `json:"draft"`
	Merged       bool      `json:"merged"`
	Mergeable    bool      `json:"mergeable"`
	User         gtUser    `json:"user"`
	Labels       []gtLabel `json:"labels"`
	Assignees    []gtUser  `json:"assignees"`
	Requested    []gtUser  `json:"requested_reviewers"`
	Additions    int       `json:"additions"`
	Deletions    int       `json:"deletions"`
	ChangedFiles int       `json:"changed_files"`
	Head         struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	MergedAt  string `json:"merged_at"`
	ClosedAt  string `json:"closed_at"`
}

type gtIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	State       string    `json:"state"`
	User        gtUser    `json:"user"`
	Labels      []gtLabel `json:"labels"`
	Assignees   []gtUser  `json:"assignees"`
	CreatedAt   string    `json:"created_at"`
	UpdatedAt   string    `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
}

type gtComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	User      gtUser `json:"user"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
}

type gtReaction struct {
	Content string `json:"content"`
}

func gtLabels(in []gtLabel) []Label {
	out := make([]Label, 0, len(in))
	for _, l := range in {
		out = append(out, Label{Name: l.Name, Color: strings.TrimPrefix(l.Color, "#")})
	}
	return out
}

func gtLogins(in []gtUser) []string {
	out := make([]string, 0, len(in))
	for _, u := range in {
		out = append(out, u.Login)
	}
	return out
}

func (g *Gitea) prItem(p gtPR) Item {
	state := p.State
	if p.Merged {
		state = "merged"
	}
	return Item{Source: "gitea", Kind: "pr", ID: strconv.Itoa(p.Number), Title: p.Title, URL: p.HTMLURL,
		State: state, Author: p.User.Login, Assignees: gtLogins(p.Assignees), Labels: gtLabels(p.Labels),
		UpdatedAt: p.UpdatedAt, Draft: p.Draft, Conflicts: p.State == "open" && !p.Mergeable,
		Head: p.Head.Ref, Base: p.Base.Ref}
}

func (g *Gitea) issueItem(i gtIssue) Item {
	return Item{Source: "gitea", Kind: "issue", ID: strconv.Itoa(i.Number), Title: i.Title, URL: i.HTMLURL,
		State: i.State, Author: i.User.Login, Assignees: gtLogins(i.Assignees), Labels: gtLabels(i.Labels), UpdatedAt: i.UpdatedAt}
}

func (g *Gitea) me(ctx context.Context) (string, error) {
	var u gtUser
	err := g.c.do(ctx, "GET", "user", nil, &u)
	return u.Login, err
}

// List reads pull requests or issues. Gitea's pull list has no people
// filter, so "mine" narrows the page it returns.
func (g *Gitea) List(ctx context.Context, kind string, f Filter) ([]Item, error) {
	state := "open"
	switch f.State {
	case "", "open":
	case "closed", "merged":
		state = "closed"
	case "all":
		state = "all"
	default:
		return nil, fmt.Errorf("unknown state %q", f.State)
	}
	me := ""
	if f.Mine != "" {
		var err error
		if me, err = g.me(ctx); err != nil {
			return nil, err
		}
	}
	q := url.Values{"state": {state}, "limit": {strconv.Itoa(f.limit())}}
	if kind == "pr" {
		q.Set("sort", "recentupdate")
		var rows []gtPR
		if err := g.c.do(ctx, "GET", g.repo()+"/pulls?"+q.Encode(), nil, &rows); err != nil {
			return nil, err
		}
		out := []Item{}
		for _, p := range rows {
			if f.State == "merged" && !p.Merged {
				continue
			}
			switch f.Mine {
			case "authored":
				if p.User.Login != me {
					continue
				}
			case "assigned":
				if !containsFold(gtLogins(p.Assignees), me) {
					continue
				}
			case "review":
				if !containsFold(gtLogins(p.Requested), me) {
					continue
				}
			}
			it := g.prItem(p)
			if !matchesQuery(it, f.Query) {
				continue
			}
			out = append(out, it)
		}
		return out, nil
	}
	q.Set("type", "issues")
	if s := strings.TrimSpace(f.Query); s != "" {
		q.Set("q", s)
	}
	switch f.Mine {
	case "assigned":
		q.Set("assigned_by", me)
	case "authored":
		q.Set("created_by", me)
	}
	var rows []gtIssue
	if err := g.c.do(ctx, "GET", g.repo()+"/issues?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, i := range rows {
		if i.PullRequest == nil {
			out = append(out, g.issueItem(i))
		}
	}
	return out, nil
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// matchesQuery is a title/branch filter for hosts whose list call has no
// search of its own.
func matchesQuery(it Item, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	hay := strings.ToLower(it.ID + " " + it.Title + " " + it.Head + " " + it.Author)
	for _, w := range strings.Fields(q) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

var gtReviewState = map[string]string{"APPROVED": "approved", "REQUEST_CHANGES": "changes_requested", "COMMENT": "commented", "REQUEST_REVIEW": "requested"}

// PR reads the pull request page: the PR, reviews, comments, commits, the
// head commit's statuses, repository merge settings and the open PRs it may
// be stacked with.
func (g *Gitea) PR(ctx context.Context, n int) (*PRDetail, error) {
	base := fmt.Sprintf("%s/pulls/%d", g.repo(), n)
	var (
		p       gtPR
		prErr   error
		reviews []struct {
			ID    int64  `json:"id"`
			User  gtUser `json:"user"`
			State string `json:"state"`
			Body  string `json:"body"`
			At    string `json:"submitted_at"`
		}
		comments []gtComment
		commits  []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
			Author *gtUser `json:"author"`
		}
		repo struct {
			AllowMerge  bool   `json:"allow_merge_commits"`
			AllowSquash bool   `json:"allow_squash_merge"`
			AllowRebase bool   `json:"allow_rebase"`
			DeleteAfter bool   `json:"default_delete_branch_after_merge"`
			DefaultMS   string `json:"default_merge_style"`
			Permissions *struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		open      []gtPR
		reactions []gtReaction
		wg        sync.WaitGroup
	)
	wg.Add(6)
	go func() { defer wg.Done(); prErr = g.c.do(ctx, "GET", base, nil, &p) }()
	go func() { defer wg.Done(); _ = g.c.do(ctx, "GET", base+"/reviews", nil, &reviews) }()
	go func() {
		defer wg.Done()
		_ = g.c.do(ctx, "GET", fmt.Sprintf("%s/issues/%d/comments", g.repo(), n), nil, &comments)
	}()
	go func() { defer wg.Done(); _ = g.c.do(ctx, "GET", base+"/commits?limit=50", nil, &commits) }()
	go func() { defer wg.Done(); _ = g.c.do(ctx, "GET", g.repo(), nil, &repo) }()
	go func() {
		defer wg.Done()
		_ = g.c.do(ctx, "GET", g.repo()+"/pulls?state=open&limit=50", nil, &open)
		_ = g.c.do(ctx, "GET", fmt.Sprintf("%s/issues/%d/reactions", g.repo(), n), nil, &reactions)
	}()
	wg.Wait()
	if prErr != nil {
		return nil, prErr
	}
	d := &PRDetail{Item: g.prItem(p), Body: p.Body, HeadSHA: p.Head.SHA, Additions: p.Additions,
		Deletions: p.Deletions, ChangedFiles: p.ChangedFiles, CreatedAt: p.CreatedAt, MergedAt: p.MergedAt,
		ClosedAt: p.ClosedAt, CheckRuns: []Check{}, Reviewers: []Reviewer{}, Timeline: []Event{},
		Reactions: gtReactionCounts(reactions)}
	d.CrossRepo = p.Head.Repo != nil && !strings.EqualFold(p.Head.Repo.FullName, g.ref.Path)
	d.Mergeable, d.MergeState = "mergeable", "clean"
	if p.State == "open" && !p.Mergeable {
		d.Mergeable, d.MergeState = "conflicting", "dirty"
	}
	if p.Head.SHA != "" {
		var st struct {
			Statuses []struct {
				Context   string `json:"context"`
				Status    string `json:"status"`
				TargetURL string `json:"target_url"`
			} `json:"statuses"`
		}
		if g.c.do(ctx, "GET", g.repo()+"/commits/"+url.PathEscape(p.Head.SHA)+"/status", nil, &st) == nil {
			for _, s := range st.Statuses {
				c := Check{ID: "status:" + s.Context, Name: s.Context, URL: s.TargetURL}
				switch s.Status {
				case "success":
					c.Status = "pass"
				case "failure", "error":
					c.Status = "fail"
				case "warning":
					c.Status = "pass"
				default:
					c.Status = "pending"
				}
				d.CheckRuns = append(d.CheckRuns, c)
			}
		}
	}
	d.Checks = RollupChecks(d.CheckRuns)
	seen := map[string]bool{}
	for _, u := range p.Requested {
		seen[u.Login] = true
		d.Reviewers = append(d.Reviewers, Reviewer{Login: u.Login, State: "requested"})
	}
	latest := map[string]string{}
	var order []string
	for _, r := range reviews {
		st := gtReviewState[r.State]
		if st == "" || st == "requested" {
			continue
		}
		if _, ok := latest[r.User.Login]; !ok {
			order = append(order, r.User.Login)
		}
		latest[r.User.Login] = st
		d.Timeline = append(d.Timeline, Event{Kind: "review", Author: r.User.Login, Body: r.Body, State: st, At: r.At})
	}
	for _, l := range order {
		if !seen[l] {
			d.Reviewers = append(d.Reviewers, Reviewer{Login: l, State: latest[l]})
		}
		switch latest[l] {
		case "approved":
			if d.Review == "" {
				d.Review = "approved"
			}
		case "changes_requested":
			d.Review = "changes_requested"
		}
	}
	for _, c := range comments {
		d.Timeline = append(d.Timeline, Event{ID: strconv.FormatInt(c.ID, 10), Kind: "comment", Author: c.User.Login, Body: c.Body, At: c.CreatedAt, URL: c.HTMLURL})
	}
	for _, c := range commits {
		author := c.Commit.Author.Name
		if c.Author != nil && c.Author.Login != "" {
			author = c.Author.Login
		}
		headline, _, _ := strings.Cut(c.Commit.Message, "\n")
		d.Timeline = append(d.Timeline, Event{Kind: "commit", Author: author, Body: headline, State: shortSHA(c.SHA), At: c.Commit.Author.Date})
	}
	sort.SliceStable(d.Timeline, func(i, j int) bool { return d.Timeline[i].At < d.Timeline[j].At })
	o := MergeOptions{DeleteBranchDefault: repo.DeleteAfter, AutoMergeAllowed: true, CanMerge: repo.Permissions == nil || repo.Permissions.Push}
	if repo.AllowMerge {
		o.Methods = append(o.Methods, "merge")
	}
	if repo.AllowSquash {
		o.Methods = append(o.Methods, "squash")
	}
	if repo.AllowRebase {
		o.Methods = append(o.Methods, "rebase")
	}
	if len(o.Methods) == 0 {
		o.Methods = []string{"merge"}
	}
	o.Default = o.Methods[0]
	if contains(o.Methods, repo.DefaultMS) {
		o.Default = repo.DefaultMS
	}
	d.Merge = o
	entries := []StackEntry{}
	found := false
	for _, x := range open {
		found = found || x.Number == p.Number
		entries = append(entries, StackEntry{Number: x.Number, Title: x.Title, Head: x.Head.Ref, Base: x.Base.Ref, URL: x.HTMLURL, State: x.State})
	}
	if !found {
		entries = append(entries, StackEntry{Number: p.Number, Title: p.Title, Head: p.Head.Ref, Base: p.Base.Ref, URL: p.HTMLURL, State: d.State})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	d.Stack = BuildStack(entries, p.Number)
	return d, nil
}

func gtReactionCounts(in []gtReaction) []Reaction {
	counts := map[string]int{}
	for _, r := range in {
		counts[r.Content]++
	}
	var out []Reaction
	for _, e := range Emojis {
		if counts[e] > 0 {
			out = append(out, Reaction{Emoji: EmojiGlyph[e], Count: counts[e]})
		}
	}
	return out
}

func (g *Gitea) Issue(ctx context.Context, n int) (*IssueDetail, error) {
	var i gtIssue
	if err := g.c.do(ctx, "GET", fmt.Sprintf("%s/issues/%d", g.repo(), n), nil, &i); err != nil {
		return nil, err
	}
	var comments []gtComment
	_ = g.c.do(ctx, "GET", fmt.Sprintf("%s/issues/%d/comments", g.repo(), n), nil, &comments)
	var reactions []gtReaction
	_ = g.c.do(ctx, "GET", fmt.Sprintf("%s/issues/%d/reactions", g.repo(), n), nil, &reactions)
	d := &IssueDetail{Item: g.issueItem(i), Body: i.Body, CreatedAt: i.CreatedAt, Children: []Item{}, Transitions: []Transition{},
		Timeline: []Event{}, Reactions: gtReactionCounts(reactions), BranchName: BranchName(strconv.Itoa(i.Number), i.Title)}
	for _, c := range comments {
		d.Timeline = append(d.Timeline, Event{ID: strconv.FormatInt(c.ID, 10), Kind: "comment", Author: c.User.Login, Body: c.Body, At: c.CreatedAt, URL: c.HTMLURL})
	}
	return d, nil
}

func (g *Gitea) JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error) {
	for _, c := range pr.CheckRuns {
		if c.ID == checkID {
			return "", fmt.Errorf("%s reports only a status; open its log on the CI site: %s", c.Name, c.URL)
		}
	}
	return "", fmt.Errorf("no such check on this pull request")
}

var gtMergeStyle = map[string]string{"merge": "merge", "squash": "squash", "rebase": "rebase"}

// Merge merges now or, with Auto, when checks succeed. head_commit_id makes
// Gitea refuse if the branch moved after the person confirmed.
func (g *Gitea) Merge(ctx context.Context, n int, m MergeRequest) (string, error) {
	style := gtMergeStyle[m.Method]
	if style == "" {
		return "", fmt.Errorf("merge method must be merge, squash or rebase")
	}
	body := map[string]any{"Do": style, "delete_branch_after_merge": m.DeleteBranch}
	if m.HeadSHA != "" {
		body["head_commit_id"] = m.HeadSHA
	}
	if m.Auto {
		body["merge_when_checks_succeed"] = true
	}
	if err := g.c.do(ctx, "POST", fmt.Sprintf("%s/pulls/%d/merge", g.repo(), n), body, nil); err != nil {
		return "", err
	}
	return "merged", nil
}

func (g *Gitea) DisableAutoMerge(ctx context.Context, n int) error {
	return g.c.do(ctx, "DELETE", fmt.Sprintf("%s/pulls/%d/merge", g.repo(), n), nil, nil)
}

func (g *Gitea) EditReviewers(ctx context.Context, n int, add, remove []string) error {
	path := fmt.Sprintf("%s/pulls/%d/requested_reviewers", g.repo(), n)
	if len(add) > 0 {
		if err := g.c.do(ctx, "POST", path, map[string]any{"reviewers": add}, nil); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		return g.c.do(ctx, "DELETE", path, map[string]any{"reviewers": remove}, nil)
	}
	return nil
}

// EditLabels resolves names to the repository's label ids, which is what
// Gitea's label calls take.
func (g *Gitea) EditLabels(ctx context.Context, kind string, n int, add, remove []string) error {
	var labels []gtLabel
	if err := g.c.do(ctx, "GET", g.repo()+"/labels?limit=100", nil, &labels); err != nil {
		return err
	}
	id := map[string]int64{}
	for _, l := range labels {
		id[strings.ToLower(l.Name)] = l.ID
	}
	var ids []int64
	for _, a := range add {
		v, ok := id[strings.ToLower(a)]
		if !ok {
			return fmt.Errorf("no label %q in this repository", a)
		}
		ids = append(ids, v)
	}
	path := fmt.Sprintf("%s/issues/%d/labels", g.repo(), n)
	if len(ids) > 0 {
		if err := g.c.do(ctx, "POST", path, map[string]any{"labels": ids}, nil); err != nil {
			return err
		}
	}
	for _, r := range remove {
		if v, ok := id[strings.ToLower(r)]; ok {
			if err := g.c.do(ctx, "DELETE", fmt.Sprintf("%s/%d", path, v), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *Gitea) Comment(ctx context.Context, kind string, n int, body string) error {
	return g.c.do(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", g.repo(), n), map[string]any{"body": body}, nil)
}

func (g *Gitea) SetState(ctx context.Context, kind string, n int, open bool) error {
	state := "closed"
	if open {
		state = "open"
	}
	sub := "issues"
	if kind == "pr" {
		sub = "pulls"
	}
	return g.c.do(ctx, "PATCH", fmt.Sprintf("%s/%s/%d", g.repo(), sub, n), map[string]any{"state": state}, nil)
}

func (g *Gitea) Labels(ctx context.Context) ([]Label, error) {
	var labels []gtLabel
	if err := g.c.do(ctx, "GET", g.repo()+"/labels?limit=100", nil, &labels); err != nil {
		return nil, err
	}
	return gtLabels(labels), nil
}

func (g *Gitea) Users(ctx context.Context) ([]User, error) {
	var users []gtUser
	if err := g.c.do(ctx, "GET", g.repo()+"/assignees", nil, &users); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(users))
	for _, u := range users {
		out = append(out, User{Login: u.Login, Name: u.FullName})
	}
	return out, nil
}

func (g *Gitea) FetchRefs(pr *PRDetail) (string, string) {
	return "refs/heads/" + pr.Base, "refs/pull/" + pr.ID + "/head"
}

// React adds a reaction to the issue or pull request, or to a comment.
// Gitea names reactions exactly as GitHub does.
func (g *Gitea) React(ctx context.Context, kind string, n int, subject, emoji string) error {
	if !ValidEmoji(emoji) {
		return fmt.Errorf("unknown reaction %q", emoji)
	}
	path := fmt.Sprintf("%s/issues/%d/reactions", g.repo(), n)
	if subject != "" {
		if _, err := strconv.ParseInt(subject, 10, 64); err != nil {
			return fmt.Errorf("bad comment id %q", subject)
		}
		path = g.repo() + "/issues/comments/" + subject + "/reactions"
	}
	return g.c.do(ctx, "POST", path, map[string]any{"content": emoji}, nil)
}
