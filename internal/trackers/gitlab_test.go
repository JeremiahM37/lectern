package trackers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Recorded GitLab REST shapes (merge request, notes, approvals, pipeline
// jobs and bridges, project settings), served through a fake `glab api`.
const glMRJSON = `{"iid": 7, "title": "Rename loader", "description": "Moves it.", "web_url": "https://gitlab.com/grp/sub/app/-/merge_requests/7",
 "state": "opened", "draft": false, "author": {"id": 1, "username": "jo"}, "assignees": [], "labels": ["refactor"],
 "reviewers": [{"id": 5, "username": "sam"}, {"id": 6, "username": "riley"}], "source_branch": "refactor/loader", "target_branch": "main",
 "sha": "c3d4e5f60718", "has_conflicts": true, "detailed_merge_status": "conflict", "merge_when_pipeline_succeeds": false,
 "head_pipeline": {"id": 900, "status": "failed", "web_url": "p"}, "source_project_id": 40, "target_project_id": 40,
 "upvotes": 2, "downvotes": 0, "created_at": "2026-09-25T08:00:00Z", "updated_at": "2026-09-26T10:00:00Z", "changes_count": "4"}`

func gitlabFake() *fakeExec {
	return (&fakeExec{}).
		on("merge_requests/7/notes", `[{"body":"please rebase","author":{"username":"sam"},"created_at":"2026-09-26T09:00:00Z","system":false},
		  {"body":"added 1 commit","author":{"username":"jo"},"created_at":"2026-09-26T09:30:00Z","system":true}]`).
		on("merge_requests/7/approvals", `{"approved_by":[{"user":{"username":"sam"}},{"user":{"username":"lee"}}]}`).
		on("pipelines/900/jobs", `[{"id":31,"name":"lint","stage":"test","status":"success","web_url":"j31"},{"id":32,"name":"unit","stage":"test","status":"failed","web_url":"j32"}]`).
		on("pipelines/900/bridges", `[{"name":"docs","stage":"deploy","status":"failed","web_url":"b1","downstream_pipeline":{"id":901,"project_id":77}}]`).
		on("projects/77/pipelines/901/jobs", `[{"id":41,"name":"build-docs","stage":"build","status":"failed","web_url":"j41"}]`).
		on("merge_requests?state=opened", `[{"iid":7,"title":"Rename loader","web_url":"u7","state":"opened","source_branch":"refactor/loader","target_branch":"main"}]`).
		exact("glab api projects/grp%2Fsub%2Fapp/merge_requests/7", glMRJSON).
		exact("glab api projects/grp%2Fsub%2Fapp", `{"merge_method":"merge","squash_option":"default_on","remove_source_branch_after_merge":true,
		  "merge_trains_enabled":false,"permissions":{"project_access":{"access_level":30},"group_access":null}}`)
}

func TestGitLabMRDetail(t *testing.T) {
	f := gitlabFake()
	g := NewGitLab(f, RepoRef{Kind: "gitlab", Host: "gitlab.com", Path: "grp/sub/app"})
	d, err := g.PR(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "gitlab" || d.State != "open" || d.Mergeable != "conflicting" || d.MergeState != "dirty" || d.Item.Checks != "fail" {
		t.Fatalf("header = %+v", d)
	}
	got := []string{}
	for _, r := range d.Reviewers {
		got = append(got, r.Login+":"+r.State)
	}
	if strings.Join(got, ",") != "sam:approved,riley:requested,lee:approved" {
		t.Fatalf("reviewers = %v", got)
	}
	names := []string{}
	for _, c := range d.CheckRuns {
		names = append(names, c.ID+"="+c.Status)
	}
	if strings.Join(names, ",") != "self:31=pass,self:32=fail,bridge:docs=fail,77:41=fail" {
		t.Fatalf("checks = %v", names)
	}
	if len(d.Timeline) != 2 || d.Timeline[1].Kind != "event" {
		t.Fatalf("timeline = %+v", d.Timeline)
	}
	if strings.Join(d.Merge.Methods, ",") != "squash,merge" || !d.Merge.CanMerge || !d.Merge.DeleteBranchDefault {
		t.Fatalf("merge = %+v", d.Merge)
	}
	if base, head := g.FetchRefs(d); base != "refs/heads/main" || head != "refs/merge-requests/7/head" {
		t.Fatalf("refs = %s %s", base, head)
	}
	f.on("projects/77/jobs/41/trace", "docs failed\n")
	if log, err := g.JobLog(context.Background(), d, "77:41"); err != nil || log != "docs failed\n" {
		t.Fatalf("child job log = %q %v", log, err)
	}
	if _, err := g.JobLog(context.Background(), d, "bridge:docs"); err == nil {
		t.Fatal("a bridge job has no log of its own")
	}
}

func TestGitLabWritesSendJSONBodies(t *testing.T) {
	f := (&fakeExec{}).
		on("users?username=lee", `[{"id":9,"username":"lee"}]`).
		on("-X PUT projects/grp%2Fapp/merge_requests/7/merge", `{"state":"merged"}`).
		on("-X PUT projects/grp%2Fapp/merge_requests/7", `{}`).
		exact("glab api --hostname gitlab.example.com projects/grp%2Fapp/merge_requests/7", `{"iid":7,"reviewers":[{"id":5,"username":"sam"},{"id":6,"username":"riley"}]}`)
	g := NewGitLab(f, RepoRef{Kind: "gitlab", Host: "gitlab.example.com", Path: "grp/app"})
	ctx := context.Background()
	if _, err := g.Merge(ctx, 7, MergeRequest{Method: "rebase"}); err == nil {
		t.Fatal("rebase accepted on GitLab")
	}
	state, err := g.Merge(ctx, 7, MergeRequest{Method: "squash", DeleteBranch: true, Auto: true, HeadSHA: "abc"})
	if err != nil || state != "merged" {
		t.Fatalf("merge = %q %v", state, err)
	}
	cmd := f.ran("/merge")
	if !strings.Contains(cmd, "--hostname gitlab.example.com") || !strings.Contains(cmd, "--input -") {
		t.Fatalf("merge command = %s", cmd)
	}
	body := jsonBody(t, cmd)
	if body["squash"] != true || body["should_remove_source_branch"] != true || body["merge_when_pipeline_succeeds"] != true || body["sha"] != "abc" {
		t.Fatalf("merge body = %v", body)
	}
	if err := g.EditReviewers(ctx, 7, []string{"lee", "sam"}, []string{"riley"}); err != nil {
		t.Fatal(err)
	}
	put := f.ran("-X PUT projects/grp%2Fapp/merge_requests/7 -H")
	ids, _ := jsonBody(t, put)["reviewer_ids"].([]any)
	if len(ids) != 2 || ids[0] != float64(5) || ids[1] != float64(9) {
		t.Fatalf("reviewer ids = %v (%s)", ids, put)
	}
}

// jsonBody pulls the JSON a `printf '%s' '<json>' | glab api ...` command sends.
func jsonBody(t *testing.T, cmd string) map[string]any {
	t.Helper()
	start := strings.Index(cmd, "printf '%s' '")
	end := strings.Index(cmd, "' | glab")
	if start < 0 || end < 0 {
		t.Fatalf("no body in %s", cmd)
	}
	raw := strings.ReplaceAll(cmd[start+len("printf '%s' '"):end], `'\''`, `'`)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("body %q: %v", raw, err)
	}
	return out
}
