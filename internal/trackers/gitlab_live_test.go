package trackers

import (
	"context"
	"os"
	"strings"
	"testing"
)

// testdata/gitlab holds responses captured from gitlab.com's public API
// (gitlab-org/gitlab-runner, read without a token on 2026-09-27; people's
// names and profile URLs replaced). They pin the adapter to what GitLab
// really returns, including what an unauthenticated or non-member caller
// does NOT get: list rows carry no head_pipeline, and the project omits its
// merge settings and permissions.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/gitlab/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGitLabRecordedShapes(t *testing.T) {
	const api = "glab api projects/gitlab-org%2Fgitlab-runner"
	f := (&fakeExec{}).
		exact(api+"/merge_requests/7480", fixture(t, "mr.json")).
		exact(api, fixture(t, "project.json")).
		on("/merge_requests/7480/notes", `[]`).
		on("/merge_requests/7480/approvals", fixture(t, "approvals.json")).
		on("/pipelines/2886735593/jobs", fixture(t, "jobs.json")).
		on("/pipelines/2886735593/bridges", fixture(t, "bridges.json")).
		on("merge_requests?state=opened&per_page=100", fixture(t, "mr_list.json")).
		on("merge_requests?", fixture(t, "mr_list.json")).
		on("/merge_trains?", fixture(t, "trains.json"))
	g := NewGitLab(f, RepoRef{Kind: "gitlab", Host: "gitlab.com", Path: "gitlab-org/gitlab-runner"})
	ctx := context.Background()

	items, err := g.List(ctx, "pr", Filter{})
	if err != nil || len(items) != 2 || items[0].ID != "7480" || items[0].Checks != "none" {
		t.Fatalf("list = %+v, %v (list rows carry no pipeline)", items, err)
	}
	d, err := g.PR(ctx, 7480)
	if err != nil {
		t.Fatal(err)
	}
	if d.Item.Checks != "pending" || d.ChangedFiles != 11 || d.MergeState != "draft" || d.Mergeable != "mergeable" || d.Base != "main" {
		t.Fatalf("detail = %+v", d.Item)
	}
	// no merge settings or access level for a non-member: offered, GitLab decides
	if !d.Merge.CanMerge || d.Merge.Default != "merge" {
		t.Fatalf("merge options = %+v", d.Merge)
	}
	var st []string
	for _, c := range d.CheckRuns {
		st = append(st, c.Status)
	}
	// manual jobs read as skipped, created ones as pending, bridges with no
	// downstream pipeline yet add no child jobs
	if strings.Join(st, ",") != "skipping,skipping,pending,pending,pending" {
		t.Fatalf("check states = %v", st)
	}
	q, err := g.Queue(ctx, "main")
	if err != nil || len(q) != 2 || q[0].Number != 7306 || q[0].Position != 1 || q[0].Pipeline != "success" || q[0].Status != "merged" {
		t.Fatalf("train = %+v %v", q, err)
	}
	if cmd := f.ran("/merge_trains?"); !strings.Contains(cmd, "scope=active") || !strings.Contains(cmd, "target_branch=main") {
		t.Fatalf("train query = %s", cmd)
	}
	if b, err := g.DefaultBranch(ctx); err != nil || b != "main" {
		t.Fatalf("default branch = %q %v", b, err)
	}
}

func TestGitLabReactAndDequeue(t *testing.T) {
	f := (&fakeExec{}).on("award_emoji", `{}`).on("cancel_merge_when_pipeline_succeeds", `{}`)
	g := NewGitLab(f, RepoRef{Kind: "gitlab", Host: "gitlab.com", Path: "g/app"})
	ctx := context.Background()
	if err := g.React(ctx, "pr", 7, "", "hooray"); err != nil {
		t.Fatal(err)
	}
	if cmd := f.ran("award_emoji"); !strings.Contains(cmd, "projects/g%2Fapp/merge_requests/7/award_emoji") || jsonBody(t, cmd)["name"] != "tada" {
		t.Fatalf("award = %s", cmd)
	}
	if err := g.React(ctx, "issue", 3, "55", "+1"); err != nil {
		t.Fatal(err)
	}
	if cmd := f.log[len(f.log)-1]; !strings.Contains(cmd, "issues/3/notes/55/award_emoji") {
		t.Fatalf("note award = %s", cmd)
	}
	if err := g.React(ctx, "issue", 3, "55; rm", "+1"); err == nil {
		t.Fatal("a non-numeric note id was accepted")
	}
	if err := g.Dequeue(ctx, QueueEntry{Number: 9}); err != nil || f.ran("merge_requests/9/cancel_merge_when_pipeline_succeeds") == "" {
		t.Fatalf("dequeue: %v %v", err, f.log)
	}
}

func TestGitHubQueueAndReactions(t *testing.T) {
	f := (&fakeExec{}).
		on("pr view 14 -R acme/app --json id", `{"id":"PR_kwDO14"}`).
		on("addReaction", `{"data":{"addReaction":{"reaction":{"content":"ROCKET"}}}}`).
		on("mergeQueue(branch", `{"data":{"repository":{"mergeQueue":{"entries":{"nodes":[
		  {"id":"MQE_2","position":2,"state":"AWAITING_CHECKS","enqueuedAt":"2026-09-27T10:05:00Z","estimatedTimeToMerge":600,"pullRequest":{"number":15,"title":"B","url":"u15","author":{"login":"jo"}}},
		  {"id":"MQE_1","position":1,"state":"MERGEABLE","enqueuedAt":"2026-09-27T10:00:00Z","estimatedTimeToMerge":null,"pullRequest":{"number":12,"title":"A","url":"u12","author":{"login":"sam"}}}]}}}}}`).
		on("dequeuePullRequest", `{"data":{"dequeuePullRequest":{"mergeQueueEntry":{"id":"MQE_1"}}}}`).
		on("repo view acme/app --json defaultBranchRef", `{"defaultBranchRef":{"name":"trunk"}}`)
	g := NewGitHub(f, RepoRef{Kind: "github", Host: "github.com", Path: "acme/app"})
	ctx := context.Background()
	if err := g.React(ctx, "pr", 14, "", "rocket"); err != nil {
		t.Fatal(err)
	}
	if cmd := f.ran("addReaction"); !strings.Contains(cmd, "s=PR_kwDO14") || !strings.Contains(cmd, "c=ROCKET") {
		t.Fatalf("react = %s", cmd)
	}
	if err := g.React(ctx, "pr", 14, "IC_comment9", "eyes"); err != nil || !strings.Contains(f.log[len(f.log)-1], "s=IC_comment9") {
		t.Fatalf("comment react: %v %s", err, f.log[len(f.log)-1])
	}
	if err := g.React(ctx, "pr", 14, "", "thumbsup"); err == nil {
		t.Fatal("an unknown reaction was accepted")
	}
	q, err := g.Queue(ctx, "main")
	if err != nil || len(q) != 2 || q[0].Number != 12 || q[1].ETASeconds != 600 || q[0].Status != "mergeable" {
		t.Fatalf("queue = %+v %v", q, err)
	}
	if err := g.Dequeue(ctx, q[0]); err != nil || !strings.Contains(f.ran("dequeuePullRequest"), "id=MQE_1") {
		t.Fatalf("dequeue: %v %s", err, f.ran("dequeuePullRequest"))
	}
	if b, _ := g.DefaultBranch(ctx); b != "trunk" {
		t.Fatalf("default branch = %q", b)
	}
	f2 := (&fakeExec{}).on("graphql", `{"data":null,"errors":[{"message":"Resource not accessible by integration"}]}`)
	if _, err := NewGitHub(f2, RepoRef{Kind: "github", Host: "github.com", Path: "acme/app"}).Queue(ctx, "main"); err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("graphql error = %v", err)
	}
}

func TestParseRemoteNewHosts(t *testing.T) {
	cases := map[string]RepoRef{
		"https://bitbucket.org/acme/app.git":                         {Kind: "bitbucket", Host: "bitbucket.org", Path: "acme/app", Flavor: "cloud"},
		"git@bitbucket.org:acme/app.git":                             {Kind: "bitbucket", Host: "bitbucket.org", Path: "acme/app", Flavor: "cloud"},
		"https://bitbucket.corp.example/scm/OPS/tools.git":           {Kind: "bitbucket", Host: "bitbucket.corp.example", Path: "OPS/tools", Flavor: "server"},
		"ssh://git@bitbucket.corp.example:7999/ops/tools.git":        {Kind: "bitbucket", Host: "bitbucket.corp.example", Path: "ops/tools", Flavor: "server"},
		"https://codeberg.org/forgejo/forgejo.git":                   {Kind: "gitea", Host: "codeberg.org", Path: "forgejo/forgejo"},
		"https://gitea.example.com/team/app":                         {Kind: "gitea", Host: "gitea.example.com", Path: "team/app"},
		"https://acme@dev.azure.com/acme/Web%20Shop/_git/storefront": {Kind: "azure", Host: "dev.azure.com", Path: "acme/Web%20Shop/storefront"},
		"git@ssh.dev.azure.com:v3/acme/Shop/storefront":              {Kind: "azure", Host: "dev.azure.com", Path: "acme/Shop/storefront"},
		"https://acme.visualstudio.com/Shop/_git/storefront":         {Kind: "azure", Host: "dev.azure.com", Path: "acme/Shop/storefront"},
	}
	for in, want := range cases {
		got, err := ParseRemote(in, "")
		if err != nil || got != want {
			t.Errorf("ParseRemote(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if got, _ := ParseRemote("https://git.internal/team/app", "gitea"); got.Kind != "gitea" {
		t.Errorf("hint ignored: %+v", got)
	}
	if u := (RepoRef{Kind: "azure", Host: "dev.azure.com", Path: "acme/Shop/storefront"}).WebURL(); u != "https://dev.azure.com/acme/Shop/_git/storefront" {
		t.Errorf("azure web url = %s", u)
	}
	if u := (RepoRef{Kind: "bitbucket", Host: "bb.corp", Path: "OPS/tools", Flavor: "server"}).WebURL(); u != "https://bb.corp/projects/OPS/repos/tools" {
		t.Errorf("bitbucket server url = %s", u)
	}
}
