package trackers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHost answers "METHOD /path" (query ignored) with canned JSON and keeps
// every request — a recorded API with no network behind it.
type fakeHost struct {
	mu     sync.Mutex
	srv    *httptest.Server
	routes map[string]string
	status map[string]int
	reqs   []fakeReq
}

type fakeReq struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

func newFakeHost(t *testing.T, routes map[string]string) *fakeHost {
	t.Helper()
	h := &fakeHost{routes: routes, status: map[string]int{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := fakeReq{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &req.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, req)
		h.mu.Unlock()
		key := r.Method + " " + req.Path
		body, ok := h.routes[key]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"no route `+key+`"}`)
			return
		}
		if code := h.status[key]; code != 0 {
			w.WriteHeader(code)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHost) find(method, path string) *fakeReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.reqs) - 1; i >= 0; i-- {
		if h.reqs[i].Method == method && strings.HasPrefix(h.reqs[i].Path, path) {
			return &h.reqs[i]
		}
	}
	return nil
}

// ---- Gitea / Forgejo -------------------------------------------------------

func TestGiteaPullRequests(t *testing.T) {
	r := "/api/v1/repos/team/app"
	h := newFakeHost(t, map[string]string{
		"GET /api/v1/user": `{"login":"me"}`,
		"GET " + r + "/pulls": `[{"number":4,"title":"Add cache","html_url":"u4","state":"open","mergeable":true,"user":{"login":"me"},
		  "labels":[{"id":1,"name":"perf","color":"#00ff00"}],"head":{"ref":"cache","sha":"abc1234"},"base":{"ref":"main"},"updated_at":"2026-09-27T10:00:00Z"},
		  {"number":5,"title":"Stacked","html_url":"u5","state":"open","mergeable":false,"user":{"login":"jo"},"head":{"ref":"cache-2","sha":"def"},"base":{"ref":"cache"}}]`,
		"GET " + r + "/pulls/4": `{"number":4,"title":"Add cache","body":"Speeds it up.","html_url":"u4","state":"open","mergeable":true,
		  "user":{"login":"me"},"labels":[],"requested_reviewers":[{"login":"riley"}],"additions":10,"deletions":2,"changed_files":3,
		  "head":{"ref":"cache","sha":"abc1234","repo":{"full_name":"team/app"}},"base":{"ref":"main"},"created_at":"2026-09-26T09:00:00Z"}`,
		"GET " + r + "/pulls/4/reviews":               `[{"id":1,"user":{"login":"jo"},"state":"REQUEST_CHANGES","body":"needs a test","submitted_at":"2026-09-26T11:00:00Z"}]`,
		"GET " + r + "/issues/4/comments":             `[{"id":77,"user":{"login":"jo"},"body":"see review","created_at":"2026-09-26T12:00:00Z"}]`,
		"GET " + r + "/pulls/4/commits":               `[{"sha":"abc1234","commit":{"message":"Add cache\n\nbody","author":{"name":"Me","date":"2026-09-26T10:00:00Z"}},"author":{"login":"me"}}]`,
		"GET " + r:                                    `{"allow_merge_commits":false,"allow_squash_merge":true,"allow_rebase":true,"default_delete_branch_after_merge":true,"default_merge_style":"squash","permissions":{"push":true}}`,
		"GET " + r + "/issues/4/reactions":            `[{"content":"+1"},{"content":"+1"},{"content":"rocket"}]`,
		"GET " + r + "/commits/abc1234/status":        `{"statuses":[{"context":"ci/woodpecker","status":"failure","target_url":"https://ci/1"}]}`,
		"GET " + r + "/labels":                        `[{"id":1,"name":"perf"},{"id":2,"name":"bug"}]`,
		"POST " + r + "/pulls/4/merge":                `{}`,
		"POST " + r + "/issues/4/labels":              `[]`,
		"DELETE " + r + "/issues/4/labels/1":          ``,
		"POST " + r + "/issues/comments/77/reactions": `{}`,
		"POST " + r + "/pulls/4/requested_reviewers":  `[]`,
	})
	g := NewGitea(RepoRef{Kind: "gitea", Host: "gitea.example", Path: "team/app"}, Creds{BaseURL: h.srv.URL + "/api/v1", Token: "gt-token"}, h.srv.Client())
	ctx := context.Background()
	items, err := g.List(ctx, "pr", Filter{Mine: "authored"})
	if err != nil || len(items) != 1 || items[0].ID != "4" || items[0].Labels[0].Color != "00ff00" {
		t.Fatalf("mine = %+v %v", items, err)
	}
	if h.reqs[0].Auth != "token gt-token" {
		t.Fatalf("auth = %q", h.reqs[0].Auth)
	}
	d, err := g.PR(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if d.Review != "changes_requested" || d.Checks != "fail" || len(d.Reviewers) != 2 || d.Reviewers[0].Login != "riley" ||
		d.Merge.Default != "squash" || strings.Join(d.Merge.Methods, ",") != "squash,rebase" || !d.Merge.DeleteBranchDefault {
		t.Fatalf("detail = %+v", d)
	}
	if len(d.Timeline) != 3 || d.Timeline[0].Kind != "commit" || d.Timeline[2].ID != "77" {
		t.Fatalf("timeline = %+v", d.Timeline)
	}
	if len(d.Reactions) != 2 || d.Reactions[0].Count != 2 || len(d.Stack) != 2 {
		t.Fatalf("reactions %+v stack %+v", d.Reactions, d.Stack)
	}
	if _, err := g.Merge(ctx, 4, MergeRequest{Method: "squash", DeleteBranch: true, Auto: true, HeadSHA: "abc1234"}); err != nil {
		t.Fatal(err)
	}
	m := h.find("POST", r+"/pulls/4/merge").Body
	if m["Do"] != "squash" || m["head_commit_id"] != "abc1234" || m["merge_when_checks_succeed"] != true || m["delete_branch_after_merge"] != true {
		t.Fatalf("merge body = %v", m)
	}
	if err := g.EditLabels(ctx, "pr", 4, []string{"bug"}, []string{"perf"}); err != nil {
		t.Fatal(err)
	}
	if ids := h.find("POST", r+"/issues/4/labels").Body["labels"].([]any); len(ids) != 1 || ids[0] != float64(2) {
		t.Fatalf("label ids = %v", ids)
	}
	if h.find("DELETE", r+"/issues/4/labels/1") == nil {
		t.Fatal("label not removed by id")
	}
	if err := g.EditLabels(ctx, "pr", 4, []string{"nope"}, nil); err == nil {
		t.Fatal("an unknown label was accepted")
	}
	if err := g.React(ctx, "pr", 4, "77", "heart"); err != nil || h.find("POST", r+"/issues/comments/77/reactions").Body["content"] != "heart" {
		t.Fatalf("react: %v", err)
	}
	if err := g.EditReviewers(ctx, 4, []string{"sam"}, nil); err != nil {
		t.Fatal(err)
	}
	if base, head := g.FetchRefs(d); base != "refs/heads/main" || head != "refs/pull/4/head" {
		t.Fatalf("refs %s %s", base, head)
	}
	if _, err := g.JobLog(ctx, d, d.CheckRuns[0].ID); err == nil || !strings.Contains(err.Error(), "https://ci/1") {
		t.Fatalf("log = %v", err)
	}
	h.routes["GET /api/v1/user"] = `{"message":"token is required"}`
	h.status["GET /api/v1/user"] = 401
	if _, err := g.List(ctx, "pr", Filter{Mine: "assigned"}); err == nil {
		t.Fatal("expected auth error")
	} else if ce, ok := err.(*CLIError); !ok || !ce.Auth {
		t.Fatalf("auth error = %#v", err)
	}
}

// ---- Bitbucket Cloud -------------------------------------------------------

const bbCloudPR = `{"id":7,"title":"Retry budget","description":"Caps retries.","state":"OPEN","author":{"display_name":"Sam O","nickname":"sam","uuid":"{s}"},
 "source":{"branch":{"name":"feature/retry"},"commit":{"hash":"a1b2c3d4e5f6"},"repository":{"full_name":"acme/app"}},
 "destination":{"branch":{"name":"main","merge_strategies":["merge_commit","squash","fast_forward"],"default_merge_strategy":"squash"}},
 "reviewers":[{"display_name":"Riley C","nickname":"riley","uuid":"{r}"},{"display_name":"Jo P","nickname":"jo","uuid":"{j}"}],
 "participants":[{"user":{"nickname":"jo","uuid":"{j}"},"role":"REVIEWER","approved":true,"state":"approved"}],
 "close_source_branch":true,"created_on":"2026-09-26T08:00:00Z","updated_on":"2026-09-27T08:00:00Z","links":{"html":{"href":"https://bitbucket.org/acme/app/pull-requests/7"}}}`

func TestBitbucketCloud(t *testing.T) {
	p := "/2.0/repositories/acme/app/pullrequests"
	h := newFakeHost(t, map[string]string{
		"GET /2.0/user":   `{"uuid":"{me}","nickname":"me"}`,
		"GET " + p:        `{"values":[` + bbCloudPR + `]}`,
		"GET " + p + "/7": bbCloudPR,
		"GET " + p + "/7/comments": `{"values":[{"id":1,"content":{"raw":"LGTM"},"user":{"nickname":"jo"},"created_on":"2026-09-26T09:00:00Z"},
		  {"id":2,"content":{"raw":"nit"},"user":{"nickname":"riley"},"created_on":"2026-09-26T10:00:00Z","inline":{"path":"a.go"}},
		  {"id":3,"deleted":true,"content":{"raw":""},"user":{"nickname":"x"},"created_on":"2026-09-26T11:00:00Z"}]}`,
		"GET " + p + "/7/commits":          `{"values":[{"hash":"a1b2c3d4e5f6","message":"Add budget\n","date":"2026-09-26T07:00:00Z","author":{"raw":"Sam <s@x>","user":{"nickname":"sam"}}}]}`,
		"GET " + p + "/7/statuses":         `{"values":[{"key":"build","name":"Pipelines","state":"FAILED","url":"https://bb/p/1"}]}`,
		"GET " + p + "/7/diffstat":         `{"values":[{"lines_added":5,"lines_removed":1},{"lines_added":2,"lines_removed":0}]}`,
		"POST " + p + "/7/merge":           `{"id":7,"state":"MERGED"}`,
		"PUT " + p + "/7":                  bbCloudPR,
		"GET /2.0/workspaces/acme/members": `{"values":[{"user":{"nickname":"devon","display_name":"Devon H","uuid":"{d}"}}]}`,
		"POST " + p + "/7/decline":         `{}`,
	})
	b := NewBitbucket(RepoRef{Kind: "bitbucket", Host: "bitbucket.org", Path: "acme/app", Flavor: "cloud"},
		Creds{BaseURL: h.srv.URL + "/2.0", Username: "me@example.com", Token: "ATBB-token"}, h.srv.Client())
	ctx := context.Background()
	items, err := b.List(ctx, "pr", Filter{Mine: "review", Query: `retry "x"`})
	if err != nil || len(items) != 1 || items[0].Review != "approved" {
		t.Fatalf("list = %+v %v", items, err)
	}
	lr := h.find("GET", p)
	if !strings.Contains(lr.Query, "reviewers.uuid") || !strings.Contains(lr.Query, "state%3D%22OPEN%22") || !strings.Contains(lr.Query, "%5C%22x%5C%22") {
		t.Fatalf("list query = %s", lr.Query)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@example.com:ATBB-token")); lr.Auth != want {
		t.Fatalf("auth = %q", lr.Auth)
	}
	d, err := b.PR(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Checks != "fail" || d.Additions != 7 || d.Deletions != 1 || d.ChangedFiles != 2 || d.Merge.Default != "squash" ||
		strings.Join(d.Merge.Methods, ",") != "merge,squash,rebase" || d.Merge.AutoMergeAllowed {
		t.Fatalf("detail = %+v", d)
	}
	var revs []string
	for _, r := range d.Reviewers {
		revs = append(revs, r.Login+":"+r.State)
	}
	if strings.Join(revs, ",") != "riley:requested,jo:approved" {
		t.Fatalf("reviewers = %v", revs)
	}
	if len(d.Timeline) != 3 || !strings.HasPrefix(d.Timeline[2].Body, "`a.go`: ") {
		t.Fatalf("timeline = %+v", d.Timeline)
	}
	if _, err := b.Merge(ctx, 7, MergeRequest{Method: "squash", HeadSHA: "ffff"}); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("stale merge: %v", err)
	}
	if h.find("POST", p+"/7/merge") != nil {
		t.Fatal("merged a moved branch")
	}
	// the page confirms a full 40-character id; Bitbucket reports 12
	if st, err := b.Merge(ctx, 7, MergeRequest{Method: "squash", DeleteBranch: true, HeadSHA: "a1b2c3d4e5f6a7b8c9d0a1b2c3d4e5f6a7b8c9d0"}); err != nil || st != "merged" {
		t.Fatalf("merge = %q %v", st, err)
	}
	mb := h.find("POST", p+"/7/merge").Body
	if mb["merge_strategy"] != "squash" || mb["close_source_branch"] != true {
		t.Fatalf("merge body = %v", mb)
	}
	if _, err := b.Merge(ctx, 7, MergeRequest{Method: "merge", Auto: true}); err == nil {
		t.Fatal("auto-merge offered on Bitbucket Cloud")
	}
	if err := b.EditReviewers(ctx, 7, []string{"devon"}, []string{"riley"}); err != nil {
		t.Fatal(err)
	}
	put := h.find("PUT", p+"/7").Body["reviewers"].([]any)
	if len(put) != 2 || put[0].(map[string]any)["uuid"] != "{j}" || put[1].(map[string]any)["uuid"] != "{d}" {
		t.Fatalf("reviewers put = %v", put)
	}
	if err := b.EditReviewers(ctx, 7, []string{"stranger"}, nil); err == nil {
		t.Fatal("a non-member became a reviewer")
	}
	if err := b.SetState(ctx, "pr", 7, true); err == nil {
		t.Fatal("reopen offered on Cloud")
	}
	if err := b.React(ctx, "pr", 7, "", "+1"); err == nil {
		t.Fatal("reactions offered on Bitbucket")
	}
	if _, err := b.List(ctx, "issue", Filter{}); err == nil || !strings.Contains(err.Error(), "issue tracker") {
		t.Fatalf("issues without a tracker: %v", err)
	}
	if base, head := b.FetchRefs(d); base != "refs/heads/main" || head != "refs/heads/feature/retry" {
		t.Fatalf("refs %s %s", base, head)
	}
}

// ---- Bitbucket Data Center -------------------------------------------------

const bbServerPR = `{"id":12,"version":3,"title":"Tools cleanup","description":"Removes dead scripts.","state":"OPEN",
 "author":{"user":{"name":"sam","displayName":"Sam O"}},
 "reviewers":[{"user":{"name":"jo"},"status":"NEEDS_WORK"},{"user":{"name":"riley"},"status":"UNAPPROVED"}],
 "fromRef":{"id":"refs/heads/cleanup","displayId":"cleanup","latestCommit":"0123456789abcdef0123456789abcdef01234567","repository":{"slug":"tools","project":{"key":"OPS"}}},
 "toRef":{"id":"refs/heads/master","displayId":"master","repository":{"slug":"tools","project":{"key":"OPS"}}},
 "createdDate":1790400000000,"updatedDate":1790486400000,"properties":{"mergeResult":{"outcome":"CONFLICTED"}},
 "links":{"self":[{"href":"https://bb.corp/projects/OPS/repos/tools/pull-requests/12"}]}}`

func TestBitbucketDataCenter(t *testing.T) {
	r := "/rest/api/1.0/projects/OPS/repos/tools/pull-requests"
	h := newFakeHost(t, map[string]string{
		"GET " + r:         `{"values":[` + bbServerPR + `]}`,
		"GET " + r + "/12": bbServerPR,
		"GET " + r + "/12/activities": `{"values":[{"id":1,"action":"COMMENTED","createdDate":1790401000000,"user":{"name":"jo"},"comment":{"id":9,"text":"please split","author":{"name":"jo"}}},
		  {"id":2,"action":"REVIEWED","createdDate":1790402000000,"user":{"name":"jo"}}]}`,
		"GET " + r + "/12/changes": `{"values":[{},{},{}]}`,
		"GET /rest/build-status/1.0/commits/0123456789abcdef0123456789abcdef01234567": `{"values":[{"key":"b1","name":"Bamboo","state":"SUCCESSFUL","url":"https://ci/b1"}]}`,
		"POST " + r + "/12/merge": `{"id":12,"state":"MERGED"}`,
		"DELETE /rest/branch-utils/1.0/projects/OPS/repos/tools/branches": ``,
		"PUT " + r + "/12":          bbServerPR,
		"POST " + r + "/12/decline": `{}`,
	})
	b := NewBitbucket(RepoRef{Kind: "bitbucket", Host: "bb.corp", Path: "OPS/tools", Flavor: "server"}, Creds{BaseURL: h.srv.URL, Token: "http-access-token"}, h.srv.Client())
	ctx := context.Background()
	items, err := b.List(ctx, "pr", Filter{Query: "tools"})
	if err != nil || len(items) != 1 || !items[0].Conflicts || items[0].Review != "changes_requested" || items[0].UpdatedAt == "" {
		t.Fatalf("list = %+v %v", items, err)
	}
	if lr := h.find("GET", r); lr.Auth != "Bearer http-access-token" || !strings.Contains(lr.Query, "filterText=tools") || !strings.Contains(lr.Query, "state=OPEN") {
		t.Fatalf("list request = %+v", lr)
	}
	d, err := b.PR(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	if d.Mergeable != "conflicting" || d.Checks != "pass" || d.ChangedFiles != 3 || len(d.Timeline) != 2 || d.Timeline[1].State != "changes_requested" {
		t.Fatalf("detail = %+v", d)
	}
	if st, err := b.Merge(ctx, 12, MergeRequest{Method: "squash", DeleteBranch: true, HeadSHA: "0123456789ab"}); err != nil || st != "merged" {
		t.Fatalf("merge = %q %v", st, err)
	}
	m := h.find("POST", r+"/12/merge")
	if m.Query != "version=3" || m.Body["strategyId"] != "squash" {
		t.Fatalf("merge request = %+v", m)
	}
	if del := h.find("DELETE", "/rest/branch-utils/"); del == nil || del.Body["name"] != "refs/heads/cleanup" {
		t.Fatalf("branch delete = %+v", del)
	}
	if _, err := b.Merge(ctx, 12, MergeRequest{Method: "merge", HeadSHA: "ffffffff"}); err == nil {
		t.Fatal("a moved branch was merged")
	}
	if err := b.EditReviewers(ctx, 12, []string{"devon"}, []string{"riley"}); err != nil {
		t.Fatal(err)
	}
	put := h.find("PUT", r+"/12").Body
	if put["version"] != float64(3) || len(put["reviewers"].([]any)) != 2 {
		t.Fatalf("reviewers put = %v", put)
	}
	if err := b.SetState(ctx, "pr", 12, false); err != nil || h.find("POST", r+"/12/decline").Query != "version=3" {
		t.Fatalf("decline: %v", err)
	}
	if _, err := b.List(ctx, "issue", Filter{}); err == nil {
		t.Fatal("Data Center has no issues")
	}
	if _, head := b.FetchRefs(d); head != "refs/pull-requests/12/from" {
		t.Fatalf("head ref = %s", head)
	}
}

// ---- Azure DevOps ----------------------------------------------------------

const azPRJSON = `{"pullRequestId":31,"title":"Checkout redesign","description":"New flow.","status":"active","isDraft":false,
 "createdBy":{"displayName":"Sam O","uniqueName":"sam@acme.test","id":"u-sam"},"creationDate":"2026-09-26T08:00:00Z",
 "sourceRefName":"refs/heads/checkout","targetRefName":"refs/heads/main","mergeStatus":"conflicts",
 "lastMergeSourceCommit":{"commitId":"9f8e7d6c5b4a"},
 "reviewers":[{"displayName":"Jo P","uniqueName":"jo@acme.test","id":"u-jo","vote":10},{"displayName":"Riley C","uniqueName":"riley@acme.test","id":"u-riley","vote":-10},{"displayName":"Web Team","uniqueName":"[Shop]\\Web Team","id":"g-web","vote":0,"isContainer":true}],
 "labels":[{"name":"ux"}]}`

func TestAzureDevOps(t *testing.T) {
	repo := "/acme/Web%20Shop/_apis/git/repositories/storefront"
	pr := repo + "/pullrequests/31"
	h := newFakeHost(t, map[string]string{
		"GET " + repo + "/pullrequests": `{"value":[` + azPRJSON + `]}`,
		"GET " + pr:                     azPRJSON,
		"GET " + pr + "/threads": `{"value":[{"id":1,"comments":[{"id":1,"author":{"displayName":"Jo P","uniqueName":"jo@acme.test"},"content":"Nice","publishedDate":"2026-09-26T09:00:00Z","commentType":"text"}]},
		  {"id":2,"comments":[{"id":1,"author":{"displayName":"Microsoft.VisualStudio.Services.TFS"},"content":"Jo P voted 10","publishedDate":"2026-09-26T09:30:00Z","commentType":"system"}]}]}`,
		"GET " + pr + "/statuses":                               `{"value":[{"state":"failed","context":{"name":"build","genre":"ci"},"targetUrl":"https://dev.azure.com/acme/_build/1"}]}`,
		"GET " + pr + "/commits":                                `{"value":[{"commitId":"9f8e7d6c5b4a","comment":"Redesign","author":{"name":"Sam O","date":"2026-09-26T07:00:00Z"}}]}`,
		"PATCH " + pr:                                           `{"pullRequestId":31,"status":"completed"}`,
		"GET /acme/_apis/connectionData":                        `{"authenticatedUser":{"id":"u-me"}}`,
		"GET /acme/_apis/identities":                            `{"value":[{"id":"u-devon"}]}`,
		"PUT " + repo + "/pullRequests/31/reviewers/u-devon":    `{}`,
		"DELETE " + repo + "/pullRequests/31/reviewers/u-riley": ``,
		"POST " + repo + "/pullRequests/31/threads":             `{}`,
		"POST /acme/Web%20Shop/_apis/wit/wiql":                  `{"workItems":[{"id":501},{"id":502}]}`,
		"GET /acme/Web%20Shop/_apis/wit/workitems":              `{"value":[{"id":501,"fields":{"System.Title":"Card declined message","System.State":"Active","System.ChangedDate":"2026-09-27T01:00:00Z","System.Tags":"payments; ux","System.AssignedTo":{"displayName":"Jo P"}}},{"id":502,"fields":{"System.Title":"Old","System.State":"New"}}]}`,
		"GET /acme/Web%20Shop/_apis/wit/workitems/501":          `{"id":501,"fields":{"System.Title":"Card declined message","System.State":"Active","System.Description":"<div>Show <b>why</b>.</div><ul><li>network</li><li>funds</li></ul>","System.CreatedBy":{"displayName":"Sam O"}}}`,
		"GET /acme/Web%20Shop/_apis/wit/workItems/501/comments": `{"comments":[{"text":"<p>Repro on staging</p>","createdBy":{"displayName":"Riley C"},"createdDate":"2026-09-26T10:00:00Z"}]}`,
	})
	a := NewAzure(RepoRef{Kind: "azure", Host: "dev.azure.com", Path: "acme/Web%20Shop/storefront"}, Creds{BaseURL: h.srv.URL, Token: "pat"}, h.srv.Client())
	ctx := context.Background()
	items, err := a.List(ctx, "pr", Filter{Mine: "review"})
	if err != nil || len(items) != 1 || !items[0].Conflicts || items[0].Review != "changes_requested" || items[0].Head != "checkout" {
		t.Fatalf("list = %+v %v", items, err)
	}
	lr := h.find("GET", repo+"/pullrequests")
	if !strings.Contains(lr.Query, "searchCriteria.reviewerId=u-me") || !strings.Contains(lr.Query, "api-version=7.1") {
		t.Fatalf("list query = %s", lr.Query)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte(":pat")); lr.Auth != want {
		t.Fatalf("auth = %q", lr.Auth)
	}
	d, err := a.PR(ctx, 31)
	if err != nil {
		t.Fatal(err)
	}
	var revs []string
	for _, r := range d.Reviewers {
		revs = append(revs, r.Login+":"+r.State)
	}
	if strings.Join(revs, ",") != `jo@acme.test:approved,riley@acme.test:changes_requested,[Shop]\Web Team:requested` {
		t.Fatalf("reviewers = %v", revs)
	}
	if d.Mergeable != "conflicting" || d.Checks != "fail" || len(d.Timeline) != 3 || d.Timeline[2].Kind != "event" ||
		d.URL != h.srv.URL+"/acme/Web%20Shop/_git/storefront/pullrequest/31" {
		t.Fatalf("detail = %+v", d)
	}
	if st, err := a.Merge(ctx, 31, MergeRequest{Method: "squash", DeleteBranch: true, HeadSHA: "9f8e7d6c5b4a"}); err != nil || st != "merged" {
		t.Fatalf("merge = %q %v", st, err)
	}
	mb := h.find("PATCH", pr).Body
	if mb["status"] != "completed" || mb["lastMergeSourceCommit"].(map[string]any)["commitId"] != "9f8e7d6c5b4a" ||
		mb["completionOptions"].(map[string]any)["mergeStrategy"] != "squash" {
		t.Fatalf("merge body = %v", mb)
	}
	if _, err := a.Merge(ctx, 31, MergeRequest{Method: "merge", Auto: true, HeadSHA: "9f8e7d6c5b4a"}); err != nil {
		t.Fatal(err)
	}
	if ab := h.find("PATCH", pr).Body; ab["autoCompleteSetBy"].(map[string]any)["id"] != "u-me" || ab["status"] != nil {
		t.Fatalf("auto-complete body = %v", ab)
	}
	if err := a.EditReviewers(ctx, 31, []string{"devon@acme.test"}, []string{"riley@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if h.find("PUT", repo+"/pullRequests/31/reviewers/u-devon") == nil || h.find("DELETE", repo+"/pullRequests/31/reviewers/u-riley") == nil {
		t.Fatal("reviewer calls missing")
	}
	if err := a.Comment(ctx, "pr", 31, "Ship it"); err != nil {
		t.Fatal(err)
	}
	if c := h.find("POST", repo+"/pullRequests/31/threads").Body["comments"].([]any)[0].(map[string]any); c["content"] != "Ship it" {
		t.Fatalf("comment = %v", c)
	}
	wi, err := a.List(ctx, "issue", Filter{Mine: "assigned", Query: "card's"})
	if err != nil || len(wi) != 2 || wi[0].ID != "501" || len(wi[0].Labels) != 2 || wi[0].StatusType != "indeterminate" {
		t.Fatalf("work items = %+v %v", wi, err)
	}
	q := h.find("POST", "/acme/Web%20Shop/_apis/wit/wiql").Body["query"].(string)
	if !strings.Contains(q, "[System.AssignedTo] = @me") || !strings.Contains(q, "CONTAINS 'card''s'") {
		t.Fatalf("wiql = %s", q)
	}
	issue, err := a.Issue(ctx, 501)
	if err != nil || issue.Body != "Show why.\n- network\n- funds" || issue.Timeline[0].Body != "Repro on staging" {
		t.Fatalf("work item = %+v %v", issue, err)
	}
}
