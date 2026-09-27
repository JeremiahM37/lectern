package api_test

// The Tasks hub API (docs/trackers.md) against the mock target's scripted
// GitHub repository (internal/executor/mock_forge.go): the hub list, the
// pull request page, merge behind its confirmation, reviewer/label/comment
// edits, starting sessions and tasks from items, and the two PR agents.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// launchedWorkspace waits for a background-setup session's worktree plan.
// The mock target cannot create worktrees, so the plan it was asked for is
// what matters here; trackers_real_test.go runs the whole launch for real.
func (h *harness) launchedWorkspace(session obj) obj {
	h.t.Helper()
	var ws obj
	h.waitUntil("the session's worktree plan", func() bool {
		ws = h.get(fmt.Sprintf("/api/sessions/%d", session.id())).sub("workspace")
		return ws.str("branch") != ""
	})
	return ws
}

func itemIDs(items []obj, kind string) []string {
	var out []string
	for _, i := range items {
		if i.str("kind") == kind {
			out = append(out, i.str("id"))
		}
	}
	return out
}

func TestTrackerHubListsTheProjectsForge(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	tr := h.get(fmt.Sprintf("/api/projects/%d/trackers", pid))
	if f := tr.sub("forge"); f.str("kind") != "github" || f.str("repo") != "mock/repo" || f.str("source") != "remote" {
		t.Fatalf("forge = %v", f)
	}
	work := h.get(fmt.Sprintf("/api/projects/%d/work", pid))
	items := work.list("items")
	if got := strings.Join(itemIDs(items, "pr"), ","); got != "14,12,15,17" {
		t.Fatalf("PRs newest first = %s", got)
	}
	if got := strings.Join(itemIDs(items, "issue"), ","); got != "3,8" {
		t.Fatalf("issues = %s", got)
	}
	for _, s := range work.list("sources") {
		if s["ok"] != true {
			t.Fatalf("source failed: %v", s)
		}
	}
	for _, i := range items {
		if i.str("id") == "14" && (i.str("checks") != "fail" || i.str("base") != "feature/retry-budget") {
			t.Fatalf("PR 14 row = %v", i)
		}
		if i.str("id") == "15" && i["conflicts"] != true {
			t.Fatalf("PR 15 row = %v", i)
		}
	}
	if review := h.get(fmt.Sprintf("/api/projects/%d/work?state=open&mine=review", pid)).list("items"); len(itemIDs(review, "issue")) != 0 {
		t.Fatalf("review requested listed issues: %v", review)
	}
	if !h.cmdLogHas("--search review-requested:@me") {
		t.Fatal("review filter did not reach gh")
	}
	only := h.get(fmt.Sprintf("/api/projects/%d/work?kind=pr&q=config", pid)).list("items")
	if len(only) != 1 || only[0].str("id") != "15" {
		t.Fatalf("filtered = %v", only)
	}
}

func TestPRPageChecksStackAndConflicts(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	pr := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/14", pid))
	var stack []string
	for _, e := range pr.list("stack") {
		stack = append(stack, fmt.Sprint(e.num("number")))
	}
	if strings.Join(stack, ",") != "12,14" || pr.str("checks") != "fail" || pr.sub("merge").str("default") != "squash" {
		t.Fatalf("pr 14 = stack %v, %v", stack, pr)
	}
	var failing obj
	for _, c := range pr.list("check_runs") {
		if c.str("status") == "fail" {
			failing = c
		}
	}
	log := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/14/log?check=%s", pid, failing.str("id")))
	text := log.str("log")
	if !strings.Contains(text, "AssertionError: expected 3 of 5") || strings.Contains(text, "2026-09-26T18:04") || strings.Contains(text, "\ufeff") {
		t.Fatalf("check log not cleaned: %q", text)
	}
	if code := h.status("GET", fmt.Sprintf("/api/projects/%d/forge/prs/14/log?check=https://elsewhere/actions/runs/1/job/2", pid), nil); code != 502 {
		t.Fatalf("log for a check not on the PR: %d", code)
	}
	c := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/15/conflicts", pid))
	files := c.sub("conflicts")["files"].([]any)
	if c.str("mergeable") != "conflicting" || len(files) != 2 || files[0] != "src/config.py" {
		t.Fatalf("conflicts = %v", c)
	}
	if h.get(fmt.Sprintf("/api/projects/%d/forge/prs/12/conflicts", pid)).sub("conflicts")["checked"] != true {
		t.Fatal("clean PR conflict check did not run")
	}
}

func TestMergeNeedsConfirmationAndTheConfirmedCommit(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	path := fmt.Sprintf("/api/projects/%d/forge/prs/12/merge", pid)
	pr := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/12", pid))
	sha := pr.str("head_sha")
	if code := h.status("POST", path, obj{"method": "squash", "head_sha": sha}); code != 428 {
		t.Fatalf("merge without confirm: %d", code)
	}
	if code := h.status("POST", path, obj{"method": "squash", "confirm": true}); code != 428 {
		t.Fatalf("merge without the confirmed head: %d", code)
	}
	if code := h.status("POST", path, obj{"method": "octopus", "confirm": true, "head_sha": sha}); code != 422 {
		t.Fatalf("bad method: %d", code)
	}
	code, raw := h.request("POST", path, obj{"method": "squash", "confirm": true, "head_sha": "0000000"}, nil)
	if code != 502 || !strings.Contains(string(raw), "Head branch was modified") {
		t.Fatalf("stale head merged: %d %s", code, raw)
	}
	got := h.post(path, obj{"method": "squash", "confirm": true, "head_sha": sha, "delete_branch": true}, 200)
	if got.sub("pr").str("state") != "merged" {
		t.Fatalf("after merge = %v", got)
	}
	if !h.cmdLogHas("gh pr merge 12 -R mock/repo --squash --delete-branch --match-head-commit " + sha) {
		t.Fatal("merge did not pin the confirmed commit")
	}
	// auto-merge on another PR, then cancel it
	pr17 := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/17", pid))
	h.post(fmt.Sprintf("/api/projects/%d/forge/prs/17/merge", pid), obj{"method": "merge", "auto": true, "confirm": true, "head_sha": pr17.str("head_sha")}, 200)
	if a := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/17", pid)).sub("auto_merge"); a.str("method") != "merge" {
		t.Fatalf("auto merge = %v", a)
	}
	h.decode("DELETE", fmt.Sprintf("/api/projects/%d/forge/prs/17/auto-merge", pid), nil, 200, nil)
	if a := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/17", pid))["auto_merge"]; a != nil {
		t.Fatalf("auto merge still armed: %v", a)
	}
}

func TestPREditsReviewersLabelsCommentsAndState(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	base := fmt.Sprintf("/api/projects/%d/forge", pid)
	h.post(base+"/prs/14/reviewers", obj{"add": []string{"@devon", "jo"}}, 200)
	h.post(base+"/prs/14/reviewers", obj{"remove": []string{"jo"}}, 200)
	h.post(base+"/prs/14/labels", obj{"add": []string{"needs-review"}, "remove": []string{"enhancement"}}, 200)
	h.post(base+"/prs/14/comments", obj{"body": "Looking now."}, 200)
	if code := h.status("POST", base+"/prs/14/comments", obj{"body": "  "}); code != 422 {
		t.Fatalf("empty comment: %d", code)
	}
	pr := h.get(base + "/prs/14")
	var reviewers, labels []string
	for _, r := range pr.list("reviewers") {
		reviewers = append(reviewers, r.str("login")+":"+r.str("state"))
	}
	for _, l := range pr.list("labels") {
		labels = append(labels, l.str("name"))
	}
	if strings.Join(reviewers, ",") != "devon:requested" || strings.Join(labels, ",") != "needs-review" {
		t.Fatalf("reviewers %v labels %v", reviewers, labels)
	}
	tl := pr.list("timeline")
	if last := tl[len(tl)-1]; last.str("body") != "Looking now." {
		t.Fatalf("comment missing from timeline: %v", last)
	}
	h.post(base+"/issues/8/state", obj{"open": false}, 200)
	if iss := h.get(base + "/issues/8"); iss.str("state") != "closed" {
		t.Fatalf("issue 8 = %v", iss)
	}
	if code := h.status("POST", base+"/issues/8/state", obj{}); code != 422 {
		t.Fatalf("state without open: %d", code)
	}
	meta := h.get(base + "/meta")
	if len(meta.list("users")) != 4 || len(meta.list("labels")) != 5 {
		t.Fatalf("meta = %v", meta)
	}
}

func TestStartSessionAndTaskFromAnIssue(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	got := h.post(fmt.Sprintf("/api/projects/%d/work/start", pid), obj{"source": "github", "id": "3"}, 202)
	if got.str("branch") != "3-sync-stalls-when-the-upstream-token-expires" || got.sub("session").str("name") != "3 · Sync stalls when the upstream token expires" {
		t.Fatalf("start = %v", got)
	}
	if ws := h.launchedWorkspace(got.sub("session")); ws.str("branch") != "3-sync-stalls-when-the-upstream-token-expires" || ws.str("base") != "main" {
		t.Fatalf("workspace = %v", ws)
	}
	task := h.post(fmt.Sprintf("/api/projects/%d/work/start", pid), obj{"source": "github", "id": "8", "mode": "task"}, 201).sub("task")
	if task.str("title") != "[8] Document the retry budget" || !strings.Contains(task.str("prompt"), "Explain the window") || task.str("status") != "backlog" {
		t.Fatalf("task = %v", task)
	}
	if code := h.status("POST", fmt.Sprintf("/api/projects/%d/work/start", pid), obj{"source": "github", "id": "3", "branch": "bad..name"}); code != 422 {
		t.Fatalf("bad branch: %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/projects/%d/work/start", pid), obj{"source": "linear", "connection_id": 99, "id": "ENG-1"}); code != 422 {
		t.Fatalf("unknown connection: %d", code)
	}
}

func TestFixChecksHandsThePRToTheCILoop(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	path := fmt.Sprintf("/api/projects/%d/forge/prs/14/fix-checks", pid)
	got := h.post(path, obj{}, 202)
	ci := got.sub("ci")
	if ci.str("state") != "failing" || ci.num("attempts") != 1 || ci.str("pr_url") != "https://github.com/mock/repo/pull/14" {
		t.Fatalf("ci = %v (%v)", ci, got)
	}
	if ws := h.launchedWorkspace(got.sub("session")); ws.str("base") != "origin/feature/sync-progress" || !strings.HasPrefix(ws.str("branch"), "lec/pr-14-fix-") {
		t.Fatalf("workspace = %v", ws)
	}
	if !h.cmdLogHas("fetch --quiet --no-tags origin +refs/heads/feature/retry-budget:refs/remotes/origin/feature/retry-budget +refs/heads/feature/sync-progress:refs/remotes/origin/feature/sync-progress") {
		t.Fatal("the PR's branches were not fetched for the worktree")
	}
	if code := h.status("POST", path, obj{}); code != 409 {
		t.Fatalf("second fix while the loop is working: %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/projects/%d/forge/prs/12/fix-checks", pid), obj{}); code != 409 {
		t.Fatalf("fix on a green PR: %d", code)
	}
	if pr := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/14", pid)); pr.sub("ci").str("state") != "failing" {
		t.Fatalf("PR page does not show the watch: %v", pr["ci"])
	}
}

func TestResolveConflictsSendsTheFilesToASession(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	got := h.post(fmt.Sprintf("/api/projects/%d/forge/prs/15/resolve", pid), obj{}, 202)
	if files := got.sub("conflicts")["files"].([]any); len(files) != 2 {
		t.Fatalf("conflicts = %v", got)
	}
	if !strings.Contains(got.sub("session").str("name"), "resolve conflicts") {
		t.Fatalf("session = %v", got.sub("session"))
	}
	if ws := h.launchedWorkspace(got.sub("session")); ws.str("base") != "origin/refactor/config-loader" {
		t.Fatalf("workspace = %v", ws)
	}
}

func TestTrackerConnectionsNeverReturnSecrets(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	path := fmt.Sprintf("/api/projects/%d/trackers", pid)
	code, raw := h.request("POST", path, obj{"kind": "jira", "config": obj{"base_url": "https://jira.corp", "project_key": "OPS"},
		"secrets": obj{"token": "super-secret-pat"}}, nil)
	if code != 201 || strings.Contains(string(raw), "super-secret-pat") {
		t.Fatalf("create = %d %s", code, raw)
	}
	var c obj
	_ = json.Unmarshal(raw, &c)
	if c.sub("secrets")["token"] != true || c.sub("config").str("project_key") != "OPS" {
		t.Fatalf("view = %v", c)
	}
	_, raw = h.request("GET", path, nil, nil)
	if strings.Contains(string(raw), "super-secret-pat") {
		t.Fatal("list leaked the token")
	}
	// an empty secrets patch keeps the stored token
	h.patch(fmt.Sprintf("/api/trackers/%d", c.id()), obj{"name": "Ops Jira", "secrets": obj{}}, 200)
	if got := h.get(path).list("connections")[0]; got.str("name") != "Ops Jira" || got.sub("secrets")["token"] != true {
		t.Fatalf("after patch = %v", got)
	}
	if code := h.status("POST", path, obj{"kind": "jira", "config": obj{"base_url": "not a url"}}); code != 400 {
		t.Fatalf("bad jira url: %d", code)
	}
	if code := h.status("POST", path, obj{"kind": "trello"}); code != 400 {
		t.Fatalf("unknown kind: %d", code)
	}
	h.decode("DELETE", fmt.Sprintf("/api/trackers/%d", c.id()), nil, 204, nil)
}

// fakeLinearAPI answers the hub's Linear queries.
func fakeLinearAPI(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("Authorization") != "lin_api_hub" {
			w.WriteHeader(401)
			return
		}
		q := body.Query
		switch {
		case strings.Contains(q, "issues(filter"):
			fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"u9","identifier":"ENG-9","title":"Speed up search","url":"https://linear.app/x/issue/ENG-9",
			  "updatedAt":"2026-09-27T09:00:00Z","state":{"id":"s1","name":"Todo","type":"unstarted"},"labels":{"nodes":[]}}]}}}`)
		case strings.Contains(q, "workflowStates"):
			fmt.Fprint(w, `{"data":{"workflowStates":{"nodes":[{"id":"s1","name":"Todo","type":"unstarted"},{"id":"s2","name":"Done","type":"completed"}]}}}`)
		case strings.Contains(q, "issue(id"):
			fmt.Fprint(w, `{"data":{"issue":{"id":"u9","identifier":"ENG-9","title":"Speed up search","description":"Index the titles.",
			  "url":"https://linear.app/x/issue/ENG-9","branchName":"eng-9-speed-up-search","team":{"id":"t","key":"ENG"},
			  "state":{"id":"s1","name":"Todo","type":"unstarted"},"labels":{"nodes":[]},"children":{"nodes":[]},"comments":{"nodes":[]}}}}`)
		case strings.Contains(q, "issueUpdate"):
			fmt.Fprint(w, `{"data":{"issueUpdate":{"success":true}}}`)
		default:
			fmt.Fprint(w, `{"errors":[{"message":"unexpected"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLinearConnectionInTheHub(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	srv := fakeLinearAPI(t)
	c := h.post(fmt.Sprintf("/api/projects/%d/trackers", pid), obj{"kind": "linear",
		"config": obj{"team_key": "ENG", "api_url": srv.URL}, "secrets": obj{"api_key": "lin_api_hub"}}, 201)
	items := h.get(fmt.Sprintf("/api/projects/%d/work?source=linear", pid)).list("items")
	if len(items) != 1 || items[0].str("id") != "ENG-9" || items[0].num("connection_id") != float64(c.id()) {
		t.Fatalf("linear items = %v", items)
	}
	d := h.get(fmt.Sprintf("/api/trackers/%d/issues/ENG-9", c.id()))
	if d.str("branch_name") != "eng-9-speed-up-search" || len(d.list("transitions")) != 2 {
		t.Fatalf("detail = %v", d)
	}
	h.post(fmt.Sprintf("/api/trackers/%d/issues/ENG-9/status", c.id()), obj{"id": "s2"}, 200)
	if code := h.status("POST", fmt.Sprintf("/api/trackers/%d/issues/ENG-9/status", c.id()), obj{"id": "not-a-state"}); code != 422 {
		t.Fatalf("foreign state id: %d", code)
	}
	got := h.post(fmt.Sprintf("/api/projects/%d/work/start", pid), obj{"source": "linear", "connection_id": c.id(), "id": "ENG-9"}, 202)
	if got.str("branch") != "eng-9-speed-up-search" {
		t.Fatalf("linear start = %v", got)
	}
}
