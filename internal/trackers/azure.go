package trackers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Azure is a repository in Azure DevOps (Services, or Server with BaseURL
// set to its collection root's parent), reached over the REST API with a
// personal access token. Its issues are Boards work items.
type Azure struct {
	c                      rest
	ref                    RepoRef
	root                   string
	org, project, repoName string
	identities             string
}

const azureAPI = "api-version=7.1"

// NewAzure builds the Azure DevOps forge. ref.Path is org/project/repo.
func NewAzure(ref RepoRef, cr Creds, hc *http.Client) *Azure {
	parts := strings.SplitN(ref.Path, "/", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	root := strings.TrimRight(cr.BaseURL, "/")
	identities := root + "/" + parts[0] + "/_apis/identities"
	if root == "" {
		root = "https://dev.azure.com"
		identities = "https://vssps.dev.azure.com/" + parts[0] + "/_apis/identities"
	}
	a := &Azure{ref: ref, root: root, org: parts[0], project: parts[1], repoName: parts[2], identities: identities}
	a.c = rest{name: "Azure DevOps", base: root, auth: basicAuth("", cr.Token), http: hc}
	return a
}

func (a *Azure) Kind() string  { return "azure" }
func (a *Azure) Repo() RepoRef { return a.ref }

func (a *Azure) projectURL() string { return a.root + "/" + a.org + "/" + a.project }
func (a *Azure) repoURL() string    { return a.projectURL() + "/_apis/git/repositories/" + a.repoName }
func (a *Azure) webPR(id int) string {
	return fmt.Sprintf("%s/_git/%s/pullrequest/%d", a.projectURL(), a.repoName, id)
}

func withAPI(u string) string {
	if strings.Contains(u, "?") {
		return u + "&" + azureAPI
	}
	return u + "?" + azureAPI
}

type azIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

func (i azIdentity) login() string { return firstNonEmpty(i.UniqueName, i.DisplayName) }

type azReviewer struct {
	azIdentity
	Vote        int  `json:"vote"` // 10 approved, 5 approved with suggestions, 0 none, -5 waiting, -10 rejected
	IsRequired  bool `json:"isRequired"`
	IsContainer bool `json:"isContainer"`
}

type azPR struct {
	ID                    int        `json:"pullRequestId"`
	Title                 string     `json:"title"`
	Description           string     `json:"description"`
	Status                string     `json:"status"` // active | completed | abandoned
	IsDraft               bool       `json:"isDraft"`
	CreatedBy             azIdentity `json:"createdBy"`
	CreationDate          string     `json:"creationDate"`
	ClosedDate            string     `json:"closedDate"`
	SourceRefName         string     `json:"sourceRefName"`
	TargetRefName         string     `json:"targetRefName"`
	MergeStatus           string     `json:"mergeStatus"`
	LastMergeSourceCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	Reviewers []azReviewer `json:"reviewers"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	AutoCompleteSetBy *azIdentity `json:"autoCompleteSetBy"`
	CompletionOptions *struct {
		MergeStrategy      string `json:"mergeStrategy"`
		DeleteSourceBranch bool   `json:"deleteSourceBranch"`
	} `json:"completionOptions"`
	ForkSource *struct{} `json:"forkSource"`
}

func azState(s string) string {
	switch s {
	case "active":
		return "open"
	case "completed":
		return "merged"
	}
	return "closed"
}

func branchName(ref string) string { return strings.TrimPrefix(ref, "refs/heads/") }

func (a *Azure) prItem(p azPR) Item {
	it := Item{Source: "azure", Kind: "pr", ID: strconv.Itoa(p.ID), Title: p.Title, URL: a.webPR(p.ID), State: azState(p.Status),
		Author: p.CreatedBy.login(), Assignees: []string{}, Labels: []Label{}, UpdatedAt: firstNonEmpty(p.ClosedDate, p.CreationDate),
		Draft: p.IsDraft, Conflicts: p.MergeStatus == "conflicts", Head: branchName(p.SourceRefName), Base: branchName(p.TargetRefName)}
	for _, l := range p.Labels {
		it.Labels = append(it.Labels, Label{Name: l.Name})
	}
	for _, r := range p.Reviewers {
		switch {
		case r.Vote <= -10:
			it.Review = "changes_requested"
		case r.Vote >= 5 && it.Review == "":
			it.Review = "approved"
		}
	}
	return it
}

func (a *Azure) me(ctx context.Context) (string, error) {
	var cd struct {
		AuthenticatedUser struct {
			ID string `json:"id"`
		} `json:"authenticatedUser"`
	}
	if err := a.c.do(ctx, "GET", withAPI(a.root+"/"+a.org+"/_apis/connectionData"), nil, &cd); err != nil {
		return "", err
	}
	return cd.AuthenticatedUser.ID, nil
}

func (a *Azure) List(ctx context.Context, kind string, f Filter) ([]Item, error) {
	if kind != "pr" {
		return a.workItems(ctx, f)
	}
	q := url.Values{"$top": {strconv.Itoa(f.limit())}}
	switch f.State {
	case "", "open":
		q.Set("searchCriteria.status", "active")
	case "merged":
		q.Set("searchCriteria.status", "completed")
	case "closed":
		q.Set("searchCriteria.status", "all")
	case "all":
		q.Set("searchCriteria.status", "all")
	default:
		return nil, fmt.Errorf("unknown state %q", f.State)
	}
	if f.Mine != "" {
		id, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if f.Mine == "review" {
			q.Set("searchCriteria.reviewerId", id)
		} else {
			q.Set("searchCriteria.creatorId", id)
		}
	}
	var res struct {
		Value []azPR `json:"value"`
	}
	if err := a.c.do(ctx, "GET", withAPI(a.repoURL()+"/pullrequests?"+q.Encode()), nil, &res); err != nil {
		return nil, err
	}
	out := []Item{}
	for _, p := range res.Value {
		if f.State == "closed" && p.Status == "active" {
			continue
		}
		it := a.prItem(p)
		if matchesQuery(it, f.Query) {
			out = append(out, it)
		}
	}
	return out, nil
}

var azVoteState = func(v int) string {
	switch {
	case v >= 5:
		return "approved"
	case v <= -10:
		return "changes_requested"
	case v == -5:
		return "commented"
	}
	return "requested"
}

func (a *Azure) PR(ctx context.Context, n int) (*PRDetail, error) {
	base := fmt.Sprintf("%s/pullrequests/%d", a.repoURL(), n)
	var (
		p       azPR
		prErr   error
		threads struct {
			Value []struct {
				ID        int  `json:"id"`
				IsDeleted bool `json:"isDeleted"`
				Comments  []struct {
					ID            int        `json:"id"`
					Author        azIdentity `json:"author"`
					Content       string     `json:"content"`
					PublishedDate string     `json:"publishedDate"`
					CommentType   string     `json:"commentType"`
				} `json:"comments"`
			} `json:"value"`
		}
		statuses struct {
			Value []struct {
				State   string `json:"state"`
				Context struct {
					Name  string `json:"name"`
					Genre string `json:"genre"`
				} `json:"context"`
				TargetURL string `json:"targetUrl"`
			} `json:"value"`
		}
		commits struct {
			Value []struct {
				CommitID string `json:"commitId"`
				Comment  string `json:"comment"`
				Author   struct {
					Name string `json:"name"`
					Date string `json:"date"`
				} `json:"author"`
			} `json:"value"`
		}
		open struct {
			Value []azPR `json:"value"`
		}
		wg sync.WaitGroup
	)
	wg.Add(5)
	go func() { defer wg.Done(); prErr = a.c.do(ctx, "GET", withAPI(base), nil, &p) }()
	go func() { defer wg.Done(); _ = a.c.do(ctx, "GET", withAPI(base+"/threads"), nil, &threads) }()
	go func() { defer wg.Done(); _ = a.c.do(ctx, "GET", withAPI(base+"/statuses"), nil, &statuses) }()
	go func() { defer wg.Done(); _ = a.c.do(ctx, "GET", withAPI(base+"/commits?$top=50"), nil, &commits) }()
	go func() {
		defer wg.Done()
		_ = a.c.do(ctx, "GET", withAPI(a.repoURL()+"/pullrequests?searchCriteria.status=active&$top=100"), nil, &open)
	}()
	wg.Wait()
	if prErr != nil {
		return nil, prErr
	}
	d := &PRDetail{Item: a.prItem(p), Body: p.Description, HeadSHA: p.LastMergeSourceCommit.CommitID, CreatedAt: p.CreationDate,
		CrossRepo: p.ForkSource != nil, CheckRuns: []Check{}, Reviewers: []Reviewer{}, Timeline: []Event{}}
	switch p.Status {
	case "completed":
		d.MergedAt = p.ClosedDate
	case "abandoned":
		d.ClosedAt = p.ClosedDate
	}
	d.Mergeable, d.MergeState = "unknown", "unknown"
	switch p.MergeStatus {
	case "conflicts":
		d.Mergeable, d.MergeState = "conflicting", "dirty"
	case "succeeded":
		d.Mergeable, d.MergeState = "mergeable", "clean"
	}
	if p.AutoCompleteSetBy != nil && p.AutoCompleteSetBy.ID != "" {
		d.AutoMerge = &AutoMerge{EnabledBy: p.AutoCompleteSetBy.login()}
		if p.CompletionOptions != nil {
			d.AutoMerge.Method = azMethod[p.CompletionOptions.MergeStrategy]
		}
	}
	for _, r := range p.Reviewers {
		d.Reviewers = append(d.Reviewers, Reviewer{Login: r.login(), State: azVoteState(r.Vote), Team: r.IsContainer})
	}
	for _, s := range statuses.Value {
		name := s.Context.Name
		if s.Context.Genre != "" {
			name = s.Context.Genre + "/" + name
		}
		c := Check{ID: "status:" + name, Name: name, URL: s.TargetURL}
		switch s.State {
		case "succeeded":
			c.Status = "pass"
		case "failed", "error":
			c.Status = "fail"
		case "notApplicable":
			c.Status = "skipping"
		default:
			c.Status = "pending"
		}
		d.CheckRuns = append(d.CheckRuns, c)
	}
	d.Checks = RollupChecks(d.CheckRuns)
	for _, t := range threads.Value {
		if t.IsDeleted {
			continue
		}
		for _, c := range t.Comments {
			kind := "comment"
			if c.CommentType == "system" {
				kind = "event"
			}
			d.Timeline = append(d.Timeline, Event{Kind: kind, Author: c.Author.login(), Body: c.Content, At: c.PublishedDate})
		}
	}
	for _, c := range commits.Value {
		headline, _, _ := strings.Cut(c.Comment, "\n")
		d.Timeline = append(d.Timeline, Event{Kind: "commit", Author: c.Author.Name, Body: headline, State: shortSHA(c.CommitID), At: c.Author.Date})
	}
	sort.SliceStable(d.Timeline, func(i, j int) bool { return d.Timeline[i].At < d.Timeline[j].At })
	d.Merge = MergeOptions{Methods: []string{"merge", "squash", "rebase"}, Default: "merge", AutoMergeAllowed: true, CanMerge: true}
	entries := []StackEntry{}
	found := false
	for _, x := range open.Value {
		found = found || x.ID == p.ID
		entries = append(entries, StackEntry{Number: x.ID, Title: x.Title, Head: branchName(x.SourceRefName), Base: branchName(x.TargetRefName), URL: a.webPR(x.ID), State: azState(x.Status)})
	}
	if !found {
		entries = append(entries, StackEntry{Number: p.ID, Title: p.Title, Head: d.Head, Base: d.Base, URL: d.URL, State: d.State})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	d.Stack = BuildStack(entries, p.ID)
	return d, nil
}

var azMethod = map[string]string{"noFastForward": "merge", "squash": "squash", "rebase": "rebase", "rebaseMerge": "rebase"}
var azStrategy = map[string]string{"merge": "noFastForward", "squash": "squash", "rebase": "rebase"}

func (a *Azure) JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error) {
	for _, c := range pr.CheckRuns {
		if c.ID == checkID {
			return "", fmt.Errorf("%s reports only a status; open its log on the pipeline's page: %s", c.Name, c.URL)
		}
	}
	return "", fmt.Errorf("no such check on this pull request")
}

// Merge completes the pull request now, or sets auto-complete. Both name the
// head commit the person confirmed (lastMergeSourceCommit), and Azure DevOps
// refuses when the branch has moved.
func (a *Azure) Merge(ctx context.Context, n int, m MergeRequest) (string, error) {
	strategy := azStrategy[m.Method]
	if strategy == "" {
		return "", fmt.Errorf("merge method must be merge, squash or rebase")
	}
	body := map[string]any{"completionOptions": map[string]any{"mergeStrategy": strategy, "deleteSourceBranch": m.DeleteBranch}}
	if m.HeadSHA != "" {
		body["lastMergeSourceCommit"] = map[string]string{"commitId": m.HeadSHA}
	}
	if m.Auto {
		id, err := a.me(ctx)
		if err != nil {
			return "", err
		}
		body["autoCompleteSetBy"] = map[string]string{"id": id}
	} else {
		body["status"] = "completed"
	}
	var res azPR
	if err := a.c.do(ctx, "PATCH", withAPI(fmt.Sprintf("%s/pullrequests/%d", a.repoURL(), n)), body, &res); err != nil {
		return "", err
	}
	return azState(res.Status), nil
}

func (a *Azure) DisableAutoMerge(ctx context.Context, n int) error {
	return a.c.do(ctx, "PATCH", withAPI(fmt.Sprintf("%s/pullrequests/%d", a.repoURL(), n)),
		map[string]any{"autoCompleteSetBy": map[string]string{"id": "00000000-0000-0000-0000-000000000000"}}, nil)
}

// identity resolves a name or email to an Azure DevOps identity id.
func (a *Azure) identity(ctx context.Context, name string) (string, error) {
	var res struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	u := a.identities + "?searchFilter=General&filterValue=" + url.QueryEscape(name) + "&" + azureAPI
	if err := a.c.do(ctx, "GET", u, nil, &res); err != nil {
		return "", err
	}
	if len(res.Value) == 0 {
		return "", fmt.Errorf("no Azure DevOps user %q", name)
	}
	return res.Value[0].ID, nil
}

func (a *Azure) EditReviewers(ctx context.Context, n int, add, remove []string) error {
	base := fmt.Sprintf("%s/pullRequests/%d/reviewers", a.repoURL(), n)
	if len(remove) > 0 {
		var p azPR
		if err := a.c.do(ctx, "GET", withAPI(fmt.Sprintf("%s/pullrequests/%d", a.repoURL(), n)), nil, &p); err != nil {
			return err
		}
		for _, r := range p.Reviewers {
			if containsFold(remove, r.login()) || containsFold(remove, r.DisplayName) {
				if err := a.c.do(ctx, "DELETE", withAPI(base+"/"+url.PathEscape(r.ID)), nil, nil); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range add {
		id, err := a.identity(ctx, name)
		if err != nil {
			return err
		}
		if err := a.c.do(ctx, "PUT", withAPI(base+"/"+url.PathEscape(id)), map[string]any{"vote": 0}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (a *Azure) EditLabels(ctx context.Context, kind string, n int, add, remove []string) error {
	if kind != "pr" {
		return unsupported("Azure Boards through Lectern", "tag editing")
	}
	base := fmt.Sprintf("%s/pullRequests/%d/labels", a.repoURL(), n)
	for _, l := range add {
		if err := a.c.do(ctx, "POST", withAPI(base), map[string]any{"name": l}, nil); err != nil {
			return err
		}
	}
	for _, l := range remove {
		if err := a.c.do(ctx, "DELETE", withAPI(base+"/"+url.PathEscape(l)), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func (a *Azure) Comment(ctx context.Context, kind string, n int, body string) error {
	if kind != "pr" {
		return a.c.do(ctx, "POST", fmt.Sprintf("%s/_apis/wit/workItems/%d/comments?api-version=7.1-preview.4", a.projectURL(), n),
			map[string]any{"text": body}, nil)
	}
	return a.c.do(ctx, "POST", withAPI(fmt.Sprintf("%s/pullRequests/%d/threads", a.repoURL(), n)),
		map[string]any{"comments": []map[string]any{{"parentCommentId": 0, "content": body, "commentType": 1}}, "status": 1}, nil)
}

func (a *Azure) SetState(ctx context.Context, kind string, n int, open bool) error {
	if kind != "pr" {
		return unsupported("Lectern for Azure Boards", "state changes (each process names its states differently)")
	}
	status := "abandoned"
	if open {
		status = "active"
	}
	return a.c.do(ctx, "PATCH", withAPI(fmt.Sprintf("%s/pullrequests/%d", a.repoURL(), n)), map[string]any{"status": status}, nil)
}

func (a *Azure) Labels(ctx context.Context) ([]Label, error) { return []Label{}, nil }

func (a *Azure) Users(ctx context.Context) ([]User, error) {
	var res struct {
		Value []struct {
			Identity azIdentity `json:"identity"`
		} `json:"value"`
	}
	team := url.PathEscape(a.projectName() + " Team")
	if err := a.c.do(ctx, "GET", withAPI(a.root+"/"+a.org+"/_apis/projects/"+a.project+"/teams/"+team+"/members"), nil, &res); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(res.Value))
	for _, m := range res.Value {
		out = append(out, User{Login: m.Identity.login(), Name: m.Identity.DisplayName})
	}
	return out, nil
}

func (a *Azure) projectName() string {
	if n, err := url.PathUnescape(a.project); err == nil {
		return n
	}
	return a.project
}

func (a *Azure) FetchRefs(pr *PRDetail) (string, string) {
	return "refs/heads/" + pr.Base, "refs/heads/" + pr.Head
}

func (a *Azure) React(ctx context.Context, kind string, n int, subject, emoji string) error {
	return unsupported("Azure DevOps", "emoji reactions")
}

// ---- work items ------------------------------------------------------------

type azWorkItem struct {
	ID     int `json:"id"`
	Fields struct {
		Title       string      `json:"System.Title"`
		State       string      `json:"System.State"`
		Type        string      `json:"System.WorkItemType"`
		Description string      `json:"System.Description"`
		AssignedTo  *azIdentity `json:"System.AssignedTo"`
		CreatedBy   *azIdentity `json:"System.CreatedBy"`
		Tags        string      `json:"System.Tags"`
		Created     string      `json:"System.CreatedDate"`
		Changed     string      `json:"System.ChangedDate"`
		Category    string      `json:"System.StateCategory"`
		Parent      int         `json:"System.Parent"`
	} `json:"fields"`
	Relations []struct {
		Rel string `json:"rel"`
		URL string `json:"url"`
	} `json:"relations"`
}

func (a *Azure) workItemItem(w azWorkItem) Item {
	it := Item{Source: "azure", Kind: "issue", ID: strconv.Itoa(w.ID), Title: w.Fields.Title, State: w.Fields.State,
		UpdatedAt: w.Fields.Changed, Assignees: []string{}, Labels: []Label{},
		URL: fmt.Sprintf("%s/_workitems/edit/%d", a.projectURL(), w.ID)}
	switch w.Fields.State {
	case "Closed", "Done", "Removed", "Resolved":
		it.StatusType = "done"
	case "New", "To Do", "Proposed":
		it.StatusType = "new"
	default:
		it.StatusType = "indeterminate"
	}
	if w.Fields.AssignedTo != nil {
		it.Assignees = []string{w.Fields.AssignedTo.DisplayName}
	}
	if w.Fields.CreatedBy != nil {
		it.Author = w.Fields.CreatedBy.DisplayName
	}
	for _, t := range strings.Split(w.Fields.Tags, ";") {
		if t = strings.TrimSpace(t); t != "" {
			it.Labels = append(it.Labels, Label{Name: t})
		}
	}
	if w.Fields.Parent != 0 {
		it.Parent = strconv.Itoa(w.Fields.Parent)
	}
	return it
}

func wiqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (a *Azure) workItems(ctx context.Context, f Filter) ([]Item, error) {
	where := []string{"[System.TeamProject] = @project"}
	switch f.State {
	case "", "open":
		where = append(where, "[System.State] NOT IN ('Closed', 'Done', 'Removed', 'Resolved')")
	case "closed", "merged":
		where = append(where, "[System.State] IN ('Closed', 'Done', 'Resolved')")
	}
	switch f.Mine {
	case "assigned":
		where = append(where, "[System.AssignedTo] = @me")
	case "authored":
		where = append(where, "[System.CreatedBy] = @me")
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		where = append(where, "[System.Title] CONTAINS "+wiqlString(s))
	}
	query := "SELECT [System.Id] FROM WorkItems WHERE " + strings.Join(where, " AND ") + " ORDER BY [System.ChangedDate] DESC"
	var res struct {
		WorkItems []struct {
			ID int `json:"id"`
		} `json:"workItems"`
	}
	if err := a.c.do(ctx, "POST", withAPI(fmt.Sprintf("%s/_apis/wit/wiql?$top=%d", a.projectURL(), f.limit())), map[string]any{"query": query}, &res); err != nil {
		return nil, err
	}
	if len(res.WorkItems) == 0 {
		return []Item{}, nil
	}
	ids := make([]string, 0, len(res.WorkItems))
	for i, w := range res.WorkItems {
		if i == f.limit() {
			break
		}
		ids = append(ids, strconv.Itoa(w.ID))
	}
	var batch struct {
		Value []azWorkItem `json:"value"`
	}
	if err := a.c.do(ctx, "GET", withAPI(a.projectURL()+"/_apis/wit/workitems?ids="+strings.Join(ids, ",")), nil, &batch); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(batch.Value))
	for _, w := range batch.Value {
		out = append(out, a.workItemItem(w))
	}
	return out, nil
}

var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// htmlText flattens a work item's HTML description to readable text.
func htmlText(h string) string {
	h = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>`).ReplaceAllString(h, "\n")
	h = regexp.MustCompile(`(?i)<li[^>]*>`).ReplaceAllString(h, "- ")
	h = htmlTagRe.ReplaceAllString(h, "")
	h = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(h)
	return strings.TrimSpace(collapseBlankLines(h))
}

func (a *Azure) Issue(ctx context.Context, n int) (*IssueDetail, error) {
	var w azWorkItem
	if err := a.c.do(ctx, "GET", withAPI(fmt.Sprintf("%s/_apis/wit/workitems/%d?$expand=relations", a.projectURL(), n)), nil, &w); err != nil {
		return nil, err
	}
	d := &IssueDetail{Item: a.workItemItem(w), Body: htmlText(w.Fields.Description), CreatedAt: w.Fields.Created,
		Children: []Item{}, Transitions: []Transition{}, Timeline: []Event{}, BranchName: BranchName(strconv.Itoa(w.ID), w.Fields.Title)}
	var comments struct {
		Comments []struct {
			Text        string     `json:"text"`
			CreatedBy   azIdentity `json:"createdBy"`
			CreatedDate string     `json:"createdDate"`
		} `json:"comments"`
	}
	_ = a.c.do(ctx, "GET", fmt.Sprintf("%s/_apis/wit/workItems/%d/comments?api-version=7.1-preview.4", a.projectURL(), n), nil, &comments)
	for _, c := range comments.Comments {
		d.Timeline = append(d.Timeline, Event{Kind: "comment", Author: c.CreatedBy.DisplayName, Body: htmlText(c.Text), At: c.CreatedDate})
	}
	sort.SliceStable(d.Timeline, func(i, j int) bool { return d.Timeline[i].At < d.Timeline[j].At })
	return d, nil
}
