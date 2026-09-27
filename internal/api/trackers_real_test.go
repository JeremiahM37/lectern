package api_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGHScript answers the gh calls the pull request page and its agents
// make for one conflicting PR, #5 on acme/app, and logs every call.
const fakeGHScript = `#!/bin/bash
echo "$*" >> "$FAKE_GH_LOG"
case "$*" in
  "pr view 5 -R acme/app --json "*)
    cat <<JSON
{"number":5,"title":"Feature change","body":"Changes a.txt","url":"https://github.com/acme/app/pull/5","state":"OPEN",
 "isDraft":false,"author":{"login":"sam"},"headRefName":"feature","headRefOid":"$FEATURE_SHA","baseRefName":"main",
 "isCrossRepository":false,"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","reviewDecision":"","reviewRequests":[],
 "latestReviews":[],"reviews":[],"labels":[],"assignees":[],"comments":[],"commits":[],"additions":1,"deletions":1,
 "changedFiles":2,"statusCheckRollup":[],"autoMergeRequest":null,"reactionGroups":[],"createdAt":"2026-09-26T00:00:00Z",
 "updatedAt":"2026-09-26T00:00:00Z","mergedAt":null,"closedAt":null}
JSON
    ;;
  "repo view acme/app --json "*)
    echo '{"mergeCommitAllowed":true,"squashMergeAllowed":true,"rebaseMergeAllowed":false,"deleteBranchOnMerge":false,"viewerPermission":"WRITE","viewerDefaultMergeMethod":"MERGE"}' ;;
  "pr list -R acme/app "*)
    echo '[{"number":5,"title":"Feature change","url":"u","state":"OPEN","headRefName":"feature","baseRefName":"main"}]' ;;
  "api graphql "*)
    echo '{"data":{"repository":{"autoMergeAllowed":false,"mergeQueue":null}}}' ;;
  "pr merge 5 -R acme/app --merge --match-head-commit $FEATURE_SHA")
    echo "✓ Merged" ;;
  *)
    echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

// TestResolveWithAgentRealGitAndFakeGH runs "Resolve with agent" end to end
// on a real local target: the PR is read through a fake gh, the conflicting
// files are worked out by git merge-tree in the real clone, the PR's head is
// fetched from a real (bare, local) origin, a real worktree is cut from it,
// and the agent really starts there with the resolve instructions as its
// opening prompt. Then the confirmed merge goes through the same fake gh.
func TestResolveWithAgentRealGitAndFakeGH(t *testing.T) {
	requireRealTools(t)
	isolateTmux(t)
	h := newHarness(t, realLocal)
	root := t.TempDir()
	origin, clone, bin := filepath.Join(root, "origin.git"), filepath.Join(root, "clone"), filepath.Join(root, "bin")
	setup := `set -e
g() { git -c user.name=t -c user.email=t@example.invalid -c init.defaultBranch=main "$@"; }
g init -q --bare "$ORIGIN"; g init -q "$CLONE"; cd "$CLONE"; g remote add origin "$ORIGIN"
printf 'one\ntwo\n' > a.txt; printf 'keep\n' > b.txt; g add .; g commit -qm base; g push -q origin HEAD:refs/heads/main
g checkout -qb feature; printf 'one\nFEATURE\n' > a.txt; g commit -qam feature; g push -q origin feature
g --git-dir "$ORIGIN" update-ref refs/pull/5/head "$(g rev-parse HEAD)"; g rev-parse HEAD > "$ROOT/feature.sha"
g checkout -q main; printf 'one\nMAIN\n' > a.txt; g commit -qam main; g push -q origin main
`
	cmd := exec.Command("bash", "-c", setup)
	cmd.Env = append(os.Environ(), "ORIGIN="+origin, "CLONE="+clone, "ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "feature.sha"))
	featureSHA := strings.TrimSpace(string(raw))
	os.Mkdir(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGHScript), 0o755)
	agent := filepath.Join(bin, "capture-agent")
	os.WriteFile(agent, []byte("#!/bin/bash\nprintf '%s' \"$1\" > .lectern-prime\nexec sleep 600\n"), 0o755)
	ghLog := filepath.Join(root, "gh.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_LOG", ghLog)
	t.Setenv("FEATURE_SHA", featureSHA)

	tid := h.localTarget(t)
	p := h.post("/api/projects", obj{"name": "acme", "target_id": tid, "repo_path": clone}, 201)
	h.post(fmt.Sprintf("/api/projects/%d/trackers", p.id()), obj{"kind": "github", "config": obj{"repo": "acme/app"}}, 201)
	h.decode("PUT", "/api/agents", []obj{{"name": "capture", "command": agent, "prompt_arg": true}}, 200, nil)

	pr := h.get(fmt.Sprintf("/api/projects/%d/forge/prs/5", p.id()))
	if pr.str("mergeable") != "conflicting" || pr.str("head_sha") != featureSHA {
		t.Fatalf("pr = %v", pr)
	}
	got := h.post(fmt.Sprintf("/api/projects/%d/forge/prs/5/resolve", p.id()), obj{"agent": "capture"}, 202)
	if files := got.sub("conflicts")["files"].([]any); len(files) != 1 || files[0] != "a.txt" {
		t.Fatalf("conflicts = %v", got["conflicts"])
	}
	sid := got.sub("session").id()
	var prime []byte
	var path string
	h.waitUntil("the agent to start in the PR worktree with its instructions", func() bool {
		path = h.get(fmt.Sprintf("/api/sessions/%d", sid)).sub("workspace").str("path")
		if path == "" {
			return false
		}
		prime, _ = os.ReadFile(filepath.Join(path, ".lectern-prime"))
		return len(prime) > 0
	})
	for _, want := range []string{"- a.txt", "git merge origin/main", "git push origin HEAD:feature", "#5 \"Feature change\""} {
		if !strings.Contains(string(prime), want) {
			t.Errorf("prime lacks %q:\n%s", want, prime)
		}
	}
	head, _ := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if strings.TrimSpace(string(head)) != featureSHA {
		t.Fatalf("worktree is at %s, not the PR head %s", head, featureSHA)
	}
	if st, _ := exec.Command("git", "-C", clone, "status", "--porcelain").Output(); len(st) != 0 {
		t.Fatalf("the project's own checkout was touched: %s", st)
	}

	merged := h.post(fmt.Sprintf("/api/projects/%d/forge/prs/5/merge", p.id()),
		obj{"method": "merge", "confirm": true, "head_sha": featureSHA}, 200)
	if merged["ok"] != true {
		t.Fatalf("merge = %v", merged)
	}
	log, _ := os.ReadFile(ghLog)
	if !strings.Contains(string(log), "pr merge 5 -R acme/app --merge --match-head-commit "+featureSHA) {
		t.Fatalf("gh calls:\n%s", log)
	}
}
