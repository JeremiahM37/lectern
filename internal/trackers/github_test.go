package trackers

import (
	"context"
	"strings"
	"testing"
)

// Recorded `gh ... --json` output, trimmed to the fields the adapter reads
// (the shapes are gh's own: statusCheckRollup mixes CheckRun and
// StatusContext, reviewRequests mixes users and teams).
const ghPRViewJSON = `{
 "number": 14, "title": "Stream sync progress", "body": "Builds on #12.", "url": "https://github.com/acme/app/pull/14",
 "state": "OPEN", "isDraft": false, "author": {"login": "sam", "is_bot": false},
 "headRefName": "feature/progress", "headRefOid": "b2c3d4e5f60718293a4b5c6d7e8f901234567891", "baseRefName": "feature/retry",
 "isCrossRepository": false, "mergeable": "CONFLICTING", "mergeStateStatus": "DIRTY", "reviewDecision": "CHANGES_REQUESTED",
 "reviewRequests": [{"__typename": "User", "login": "riley"}, {"__typename": "Team", "name": "Core", "slug": "core"}],
 "latestReviews": [{"author": {"login": "jo"}, "state": "CHANGES_REQUESTED", "body": "", "submittedAt": "2026-09-26T12:00:00Z"}],
 "reviews": [{"author": {"login": "jo"}, "state": "COMMENTED", "body": "first pass", "submittedAt": "2026-09-26T10:00:00Z"},
             {"author": {"login": "jo"}, "state": "CHANGES_REQUESTED", "body": "needs a test", "submittedAt": "2026-09-26T12:00:00Z"},
             {"author": {"login": "me"}, "state": "PENDING", "body": "draft", "submittedAt": ""}],
 "labels": [{"id": "L1", "name": "enhancement", "description": "", "color": "a2eeef"}],
 "assignees": [{"login": "sam"}],
 "comments": [{"author": {"login": "riley"}, "body": "CI is red", "createdAt": "2026-09-26T11:00:00Z", "url": "https://github.com/acme/app/pull/14#c1",
               "reactionGroups": [{"content": "THUMBS_UP", "users": {"totalCount": 2}}, {"content": "EYES", "users": {"totalCount": 0}}]}],
 "commits": [{"oid": "b2c3d4e5f60718293a4b5c6d7e8f901234567891", "messageHeadline": "Report progress", "committedDate": "2026-09-26T09:00:00Z", "authors": [{"login": "sam", "name": "Sam"}]}],
 "additions": 40, "deletions": 3, "changedFiles": 2,
 "statusCheckRollup": [
  {"__typename": "CheckRun", "name": "unit tests", "workflowName": "CI", "status": "COMPLETED", "conclusion": "FAILURE", "detailsUrl": "https://github.com/acme/app/actions/runs/55/job/66"},
  {"__typename": "CheckRun", "name": "lint", "workflowName": "CI", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "https://github.com/acme/app/actions/runs/55/job/67"},
  {"__typename": "CheckRun", "name": "e2e", "workflowName": "CI", "status": "IN_PROGRESS", "conclusion": "", "detailsUrl": "https://github.com/acme/app/actions/runs/55/job/68"},
  {"__typename": "StatusContext", "context": "ci/buildkite", "state": "SUCCESS", "targetUrl": "https://buildkite.com/acme/app/builds/9"}],
 "autoMergeRequest": {"mergeMethod": "SQUASH", "enabledBy": {"login": "sam"}},
 "reactionGroups": [{"content": "HOORAY", "users": {"totalCount": 1}}],
 "createdAt": "2026-09-25T08:00:00Z", "updatedAt": "2026-09-26T12:00:00Z", "mergedAt": null, "closedAt": null}`

const ghRepoJSON = `{"mergeCommitAllowed": false, "squashMergeAllowed": true, "rebaseMergeAllowed": true,
 "deleteBranchOnMerge": true, "viewerPermission": "WRITE", "viewerDefaultMergeMethod": "MERGE"}`

const ghOpenPRsJSON = `[
 {"number": 12, "title": "Retry budget", "url": "u12", "state": "OPEN", "headRefName": "feature/retry", "baseRefName": "main"},
 {"number": 14, "title": "Stream sync progress", "url": "u14", "state": "OPEN", "headRefName": "feature/progress", "baseRefName": "feature/retry"},
 {"number": 19, "title": "Progress UI", "url": "u19", "state": "OPEN", "headRefName": "feature/progress-ui", "baseRefName": "feature/progress"},
 {"number": 20, "title": "Unrelated", "url": "u20", "state": "OPEN", "headRefName": "x", "baseRefName": "main"}]`

func githubFake() *fakeExec {
	return (&fakeExec{}).
		on("pr view 14 -R acme/app --json", ghPRViewJSON).
		on("repo view acme/app --json mergeCommitAllowed", ghRepoJSON).
		on("pr list -R acme/app --state open --limit 100", ghOpenPRsJSON).
		on("api graphql", `{"data":{"repository":{"autoMergeAllowed":true,"mergeQueue":{"id":"MQ_1"}}}}`)
}

func TestGitHubPRDetail(t *testing.T) {
	f := githubFake()
	g := NewGitHub(f, RepoRef{Kind: "github", Host: "github.com", Path: "acme/app"})
	d, err := g.PR(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "14" || d.State != "open" || d.Mergeable != "conflicting" || !d.Conflicts || d.Review != "changes_requested" {
		t.Fatalf("header = %+v", d.Item)
	}
	if d.CheckRuns[0].Status != "fail" || !d.CheckRuns[0].HasLog || d.CheckRuns[2].Status != "pending" ||
		d.CheckRuns[3].Name != "ci/buildkite" || d.CheckRuns[3].HasLog || d.Item.Checks != "fail" {
		t.Fatalf("checks = %+v", d.CheckRuns)
	}
	wantRev := []Reviewer{{Login: "riley", State: "requested"}, {Login: "core", State: "requested", Team: true}, {Login: "jo", State: "changes_requested"}}
	if len(d.Reviewers) != 3 || d.Reviewers[0] != wantRev[0] || d.Reviewers[1] != wantRev[1] || d.Reviewers[2] != wantRev[2] {
		t.Fatalf("reviewers = %+v", d.Reviewers)
	}
	// timeline: commit, review(commented), comment, review(changes) — pending review dropped
	var kinds []string
	for _, e := range d.Timeline {
		kinds = append(kinds, e.Kind+":"+e.State)
	}
	if strings.Join(kinds, ",") != "commit:b2c3d4e,review:commented,comment:,review:changes_requested" {
		t.Fatalf("timeline = %v", kinds)
	}
	if r := d.Timeline[2].Reactions; len(r) != 1 || r[0].Emoji != "👍" || r[0].Count != 2 {
		t.Fatalf("comment reactions = %+v", r)
	}
	m := d.Merge
	if strings.Join(m.Methods, ",") != "squash,rebase" || m.Default != "squash" || !m.DeleteBranchDefault ||
		!m.CanMerge || !m.AutoMergeAllowed || !m.MergeQueue {
		t.Fatalf("merge options = %+v", m)
	}
	if d.AutoMerge == nil || d.AutoMerge.Method != "squash" || d.AutoMerge.EnabledBy != "sam" {
		t.Fatalf("auto merge = %+v", d.AutoMerge)
	}
	var stack []int
	for _, e := range d.Stack {
		stack = append(stack, e.Number)
	}
	if len(stack) != 3 || stack[0] != 12 || stack[1] != 14 || stack[2] != 19 || !d.Stack[1].Current {
		t.Fatalf("stack = %+v", d.Stack)
	}
	if q := f.ran("api graphql"); !strings.Contains(q, "b=feature/retry") {
		t.Fatalf("merge-queue lookup was not for the base branch: %s", q)
	}
	if base, head := g.FetchRefs(d); base != "refs/heads/feature/retry" || head != "refs/pull/14/head" {
		t.Fatalf("fetch refs = %s %s", base, head)
	}
}

func TestGitHubListFilters(t *testing.T) {
	f := (&fakeExec{}).on("pr list", `[{"number":3,"title":"t","url":"u","state":"OPEN","isDraft":true,"author":{"login":"a"},
	  "headRefName":"h","baseRefName":"main","labels":[],"assignees":[],"reviewDecision":"","updatedAt":"2026-01-01T00:00:00Z",
	  "statusCheckRollup":[],"mergeable":"MERGEABLE"}]`).on("issue list", `[]`)
	g := NewGitHub(f, RepoRef{Kind: "github", Host: "ghe.corp", Path: "acme/app"})
	items, err := g.List(context.Background(), "pr", Filter{Mine: "review", Query: "retry"})
	if err != nil || len(items) != 1 || !items[0].Draft || items[0].Checks != "none" || items[0].Source != "github" {
		t.Fatalf("items = %+v, %v", items, err)
	}
	cmd := f.ran("pr list")
	if !strings.Contains(cmd, "-R ghe.corp/acme/app") || !strings.Contains(cmd, "--search 'retry review-requested:@me'") {
		t.Fatalf("pr list command = %s", cmd)
	}
	if _, err := g.List(context.Background(), "issue", Filter{Mine: "assigned", State: "closed"}); err != nil {
		t.Fatal(err)
	}
	if cmd := f.ran("issue list"); !strings.Contains(cmd, "--assignee @me") || !strings.Contains(cmd, "--state closed") {
		t.Fatalf("issue list command = %s", cmd)
	}
	if _, err := g.List(context.Background(), "pr", Filter{State: "bogus"}); err == nil {
		t.Fatal("unknown state accepted")
	}
}

func TestGitHubActionsCommands(t *testing.T) {
	f := (&fakeExec{}).on("pr merge", "✓ Merged").on("pr edit", "").on("pr comment", "").on("issue close", "")
	g := NewGitHub(f, RepoRef{Kind: "github", Host: "github.com", Path: "acme/app"})
	ctx := context.Background()
	if _, err := g.Merge(ctx, 14, MergeRequest{Method: "squash", DeleteBranch: true, Auto: true, HeadSHA: "abc"}); err != nil {
		t.Fatal(err)
	}
	if cmd := f.ran("pr merge"); cmd != "gh pr merge 14 -R acme/app --squash --delete-branch --auto --match-head-commit abc" {
		t.Fatalf("merge = %s", cmd)
	}
	if _, err := g.Merge(ctx, 14, MergeRequest{Method: "yolo"}); err == nil {
		t.Fatal("bad method accepted")
	}
	if err := g.EditReviewers(ctx, 14, []string{"a", "b"}, []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if cmd := f.ran("pr edit"); cmd != "gh pr edit 14 -R acme/app --add-reviewer a,b --remove-reviewer c" {
		t.Fatalf("reviewers = %s", cmd)
	}
	if err := g.Comment(ctx, "pr", 14, "it's done; $(rm -rf /)"); err != nil {
		t.Fatal(err)
	}
	if cmd := f.ran("pr comment"); cmd != `gh pr comment 14 -R acme/app --body 'it'\''s done; $(rm -rf /)'` {
		t.Fatalf("comment not quoted: %s", cmd)
	}
	if err := g.SetState(ctx, "issue", 3, false); err != nil || f.ran("issue close 3 -R acme/app") == "" {
		t.Fatalf("close: %v %v", err, f.log)
	}
}

func TestGitHubJobLogUsesCILoopFetcher(t *testing.T) {
	f := githubFake().on("run view 55 -R acme/app --log-failed --job 66", "unit tests\tRun\t2026-09-26T11:00:00Z boom\n")
	g := NewGitHub(f, RepoRef{Kind: "github", Host: "github.com", Path: "acme/app"})
	d, err := g.PR(context.Background(), 14)
	if err != nil {
		t.Fatal(err)
	}
	log, err := g.JobLog(context.Background(), d, d.CheckRuns[0].ID)
	if err != nil || !strings.Contains(log, "boom") {
		t.Fatalf("log = %q, %v", log, err)
	}
	if _, err := g.JobLog(context.Background(), d, d.CheckRuns[3].ID); err == nil || !strings.Contains(err.Error(), "not a GitHub Actions job") {
		t.Fatalf("status context log: %v", err)
	}
	if _, err := g.JobLog(context.Background(), d, "https://evil/actions/runs/1/job/2"); err == nil {
		t.Fatal("a check that is not on the PR was read")
	}
}
