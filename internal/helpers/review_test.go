package helpers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reviewFixture is a repository with every kind of change the review views
// show: modified (two hunks), staged, renamed, deleted, untracked,
// intent-to-add, an untracked symlink, binary and non-UTF-8 names.
func reviewFixture(t *testing.T, root string) string {
	t.Helper()
	repo := filepath.Join(root, "repo")
	lines := func(n int, word string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(word + " line\n")
		}
		return b.String()
	}
	gitFixture(t, repo, map[string]string{
		"a.txt":       lines(30, "a"),
		"b.txt":       "b\n",
		"dir/c.txt":   lines(5, "c"),
		"del.txt":     "gone soon\n",
		"bin.dat":     "\x00\x01binary",
		"Upper.txt":   "u\n",
		"crlf.txt":    "one\r\ntwo\r\n",
		"dir/sub/e.x": "e\n",
	})
	os.Symlink("a.txt", filepath.Join(repo, "tracked-link"))
	gitIn(t, repo, "add", "tracked-link")
	gitIn(t, repo, "commit", "-qm", "link")
	a := strings.Split(lines(30, "a"), "\n")
	a[1], a[25] = "changed early", "changed late"
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte(strings.Join(a, "\n")), 0o644)
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b staged\n"), 0o644)
	gitIn(t, repo, "add", "b.txt")
	gitIn(t, repo, "mv", "dir/c.txt", "dir/renamed.txt")
	os.Remove(filepath.Join(repo, "del.txt"))
	os.WriteFile(filepath.Join(repo, "new.txt"), []byte("brand\nnew\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "ita.txt"), []byte("intent\n"), 0o755)
	gitIn(t, repo, "add", "-N", "ita.txt")
	os.Symlink("/etc/hostname", filepath.Join(repo, "ulink"))
	os.WriteFile(filepath.Join(repo, "bin.dat"), []byte("\x00\x02changed"), 0o644)
	os.WriteFile(filepath.Join(repo, "é-name.txt"), []byte("accent\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "bad\xffname.txt"), []byte("x\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "crlf.txt"), []byte("one\r\ntwo changed\r\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "dir/sub/e.x"), []byte("e changed\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "empty-new"), nil, 0o644)
	return repo
}

var gitEnv = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.invalid",
	"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z"}

func TestReviewParity(t *testing.T) {
	script := pythonFile(t, "api/scripts/review.py")
	steps := func(r string) [][]string {
		repo := r + "/repo"
		return [][]string{
			{repo, "working", ""}, {repo, "staged", ""}, {repo, "working", "a.txt"}, {repo, "working", "new.txt"},
			{repo, "working", "ulink"}, {repo, "staged", "dir/renamed.txt"}, {repo, "working", "bin.dat"},
			{repo, "working", "crlf.txt"}, {repo, "working", "nope.txt"}, {repo, "staged", "new.txt"},
			{repo + "/dir", "working", ""}, {repo + "/dir", "staged", "renamed.txt"}, {repo, "both", ""},
			{r + "/plain", "working", ""}, {r + "/missing", "working", ""}, {repo, "working"},
			{repo + "/", "working", "é-name.txt"},
		}
	}
	twinSteps(t, script, "review", nil, func(t *testing.T, root string) (runSpec, [][]string) {
		reviewFixture(t, root)
		os.MkdirAll(root+"/plain", 0o755)
		return runSpec{dir: root, env: gitEnv}, steps(root)
	})
}

// reviewGitStep runs one action in both fixtures; params may depend on what
// the last status step printed (hunk fingerprints).
type reviewGitStep struct {
	action  string
	params  func(prev map[string]any) any
	prepare func(root string)
	raw     []string // exact arguments after WORKSPACE, instead of action/params
}

func TestReviewGitParity(t *testing.T) {
	script := pythonFile(t, "api/scripts/review_git.py")
	resolveFile := fmt.Sprintf("/tmp/lectern-resolve-%d-1", os.Getpid())
	fixed := func(p any) func(map[string]any) any { return func(map[string]any) any { return p } }
	hunkOf := func(path, scope string, index int, extra map[string]any) func(map[string]any) any {
		return func(prev map[string]any) any {
			p := map[string]any{"path": path, "index": index, "fingerprint": "none"}
			files, _ := prev["files"].([]any)
			for _, f := range files {
				f := f.(map[string]any)
				if f["path"] == path {
					if hunks := f[scope+"_hunks"].([]any); index < len(hunks) {
						p["fingerprint"] = hunks[index]
					}
				}
			}
			for k, v := range extra {
				p[k] = v
			}
			return p
		}
	}
	status := reviewGitStep{action: "status", params: fixed(map[string]any{})}
	cases := []struct {
		name  string
		setup func(t *testing.T, repo string)
		steps []reviewGitStep
	}{
		{name: "status and staging", steps: []reviewGitStep{
			status,
			{action: "stage", params: fixed(map[string]any{"paths": []string{"new.txt", "a.txt"}})},
			status,
			{action: "unstage", params: fixed(map[string]any{"paths": []string{"new.txt", "b.txt"}})},
			{action: "stage", params: fixed(map[string]any{"paths": []string{"../outside"}})},
			{action: "stage", params: fixed(map[string]any{"paths": []string{}})},
			{action: "stage", params: fixed(map[string]any{"paths": []any{""}})},
			{action: "stage", params: fixed(map[string]any{"paths": []any{3}})},
			{action: "stage", params: fixed(map[string]any{"paths": "ab"})},
			{action: "stage", params: fixed(map[string]any{"paths": []string{"linkdir/x"}})},
			status,
		}},
		{name: "hunks", steps: []reviewGitStep{
			status,
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 1, map[string]any{"op": "stage"})},
			status,
			{action: "hunk", params: hunkOf("a.txt", "staged", 0, map[string]any{"op": "unstage"})},
			status,
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 0, map[string]any{"op": "discard", "lines": []int{1}})},
			status,
			{action: "hunk", params: hunkOf("new.txt", "unstaged", 0, map[string]any{"op": "stage", "lines": []int{0}})},
			status,
			{action: "hunk", params: hunkOf("ita.txt", "unstaged", 0, map[string]any{"op": "discard"})},
			status,
			{action: "hunk", params: hunkOf("crlf.txt", "unstaged", 0, map[string]any{"op": "stage", "lines": []int{1, 2}})},
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 0, map[string]any{"op": "stage", "fingerprint": "stale"})},
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 9, map[string]any{"op": "stage"})},
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 0, map[string]any{"op": "stage", "lines": []int{}})},
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 0, map[string]any{"op": "stage", "lines": []int{0}})},
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 0, map[string]any{"op": "stage", "lines": []any{map[string]any{}}})},
			{action: "hunk", params: hunkOf("bin.dat", "unstaged", 0, map[string]any{"op": "stage"})},
			{action: "hunk", params: hunkOf("a.txt", "unstaged", 0, map[string]any{"op": "frob"})},
			{action: "hunk", params: hunkOf("clean.txt", "unstaged", 0, map[string]any{"op": "stage"})},
		}},
		{name: "discard", steps: []reviewGitStep{
			{action: "discard", params: fixed(map[string]any{"paths": []string{"new.txt", "a.txt", "del.txt", "ita.txt", "ulink"}})},
			status,
			{action: "discard", params: fixed(map[string]any{"paths": []string{"b.txt"}})},
			{action: "discard", params: fixed(map[string]any{"paths": []string{"untracked-dir"}})},
			status,
		}, setup: func(t *testing.T, repo string) {
			os.MkdirAll(repo+"/untracked-dir", 0o755)
			os.WriteFile(repo+"/untracked-dir/f", nil, 0o644)
		}},
		{name: "conflicts", setup: conflictFixture, steps: []reviewGitStep{
			{action: "conflicts", params: fixed(map[string]any{})},
			{action: "conflicts", params: fixed(map[string]any{"path": "a.txt"})},
			{action: "conflicts", params: fixed(map[string]any{"path": "b.txt"})},
			{action: "resolve", params: fixed(map[string]any{"path": "a.txt", "staged_file": "/etc/passwd"})},
			{action: "resolve", params: fixed(map[string]any{"path": "a.txt", "staged_file": resolveFile}),
				prepare: func(string) { os.WriteFile(resolveFile, []byte("<<<<<<< x\nstill\n"), 0o600) }},
			{action: "resolve", params: fixed(map[string]any{"path": "a.txt", "staged_file": resolveFile}),
				prepare: func(string) { os.WriteFile(resolveFile, []byte("resolved\n"), 0o600) }},
			{action: "resolve", params: fixed(map[string]any{"path": "b.txt", "take": "theirs"})},
			{action: "resolve", params: fixed(map[string]any{"path": "gone.txt", "take": "ours"})},
			{action: "status", params: fixed(map[string]any{})},
			{action: "abort", params: fixed(map[string]any{})},
			{action: "abort", params: fixed(map[string]any{})},
			{action: "status", params: fixed(map[string]any{})},
		}},
		{name: "blobs and blame", steps: []reviewGitStep{
			{action: "blob", params: fixed(map[string]any{"path": "a.txt"})},
			{action: "blob", params: fixed(map[string]any{"path": "a.txt", "side": "old"})},
			{action: "blob", params: fixed(map[string]any{"path": "new.txt", "side": "old"})},
			{action: "blob", params: fixed(map[string]any{"path": "a.txt", "side": "old", "ref": "HEAD~1"})},
			{action: "blob", params: fixed(map[string]any{"path": "ulink"})},
			{action: "blob", params: fixed(map[string]any{"path": "/etc/passwd"})},
			{action: "blame", params: fixed(map[string]any{"base": "HEAD~2", "hashes": true,
				"files": map[string]any{"a.txt": [][]int{{1, 3}, {20, 22}}, "new.txt": nil, "nope": [][]int{{1, 1}}, "crlf.txt": [][]int{}}})},
			{action: "blame", params: fixed(map[string]any{"files": map[string]any{"a.txt": [][]any{{"1", 2}}}})},
			{action: "frobnicate", params: fixed(map[string]any{})},
			{raw: []string{"status"}},
			{raw: []string{"status", "e30="}},
			{raw: []string{"status", ""}},
			{raw: []string{"status", "!!!"}},
		}, setup: func(t *testing.T, repo string) {
			os.WriteFile(repo+"/agent.txt", []byte("x\n"), 0o644)
			gitIn(t, repo, "add", "agent.txt")
			gitIn(t, repo, "commit", "-qm", "agent work\n\nCo-Authored-By: Claude <noreply@anthropic.com>", "--author", "Claude <c@anthropic.com>")
			gitIn(t, repo, "commit", "-qm", "human work", "--allow-empty")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			requirePython(t)
			pyRoot, goRoot := t.TempDir(), t.TempDir()
			for _, root := range []string{pyRoot, goRoot} {
				repo := reviewFixture(t, root)
				os.MkdirAll(root+"/outside", 0o755)
				os.Symlink(root+"/outside", repo+"/linkdir")
				if c.setup != nil {
					c.setup(t, repo)
				}
			}
			prev := map[string]any{}
			for i, step := range c.steps {
				args := step.raw
				if args == nil {
					raw, _ := json.Marshal(step.params(prev))
					args = []string{step.action, base64.StdEncoding.EncodeToString(raw)}
				}
				run := func(root string, fn func(runSpec, ...string) runResult) runResult {
					if step.prepare != nil {
						step.prepare(root)
					}
					res := fn(runSpec{dir: root, env: gitEnv}, append([]string{root + "/repo"}, args...)...)
					res.stdout = strings.ReplaceAll(res.stdout, root, "$ROOT")
					return res
				}
				py := run(pyRoot, func(s runSpec, a ...string) runResult { return runPy(t, s, script, a...) })
				goRes := run(goRoot, func(s runSpec, a ...string) runResult { return runGo(t, s, "review-git", a...) })
				sameResult(t, py, goRes)
				t.Logf("step %d %s: rc %d %.300s", i, step.action, goRes.rc, goRes.stdout)
				if a, b := treeOf(t, pyRoot), treeOf(t, goRoot); strings.ReplaceAll(a, pyRoot, "$ROOT") != strings.ReplaceAll(b, goRoot, "$ROOT") {
					t.Fatalf("after step %d trees differ\npython:\n%s\ngo:\n%s", i, a, b)
				}
				if a, b := gitIn(t, pyRoot+"/repo", "status", "--porcelain=v1"), gitIn(t, goRoot+"/repo", "status", "--porcelain=v1"); a != b {
					t.Fatalf("after step %d git state differs\npython:\n%s\ngo:\n%s", i, a, b)
				}
				if step.action == "status" {
					prev = map[string]any{}
					json.Unmarshal([]byte(goRes.stdout), &prev)
				}
			}
		})
	}
}

// conflictFixture leaves a merge stopped on conflicts in a.txt (both
// changed), b.txt (deleted on one side) and gone.txt (not conflicted).
func conflictFixture(t *testing.T, repo string) {
	gitIn(t, repo, "reset", "-q", "--hard")
	gitIn(t, repo, "clean", "-qfd")
	gitIn(t, repo, "checkout", "-q", "-b", "other")
	os.WriteFile(repo+"/a.txt", []byte("theirs\n"), 0o644)
	os.WriteFile(repo+"/b.txt", []byte("b theirs\n"), 0o644)
	gitIn(t, repo, "commit", "-qam", "theirs")
	gitIn(t, repo, "checkout", "-q", "main")
	os.WriteFile(repo+"/a.txt", []byte("ours\n"), 0o644)
	gitIn(t, repo, "rm", "-q", "b.txt")
	gitIn(t, repo, "commit", "-qam", "ours")
	// The merge stops on the conflicts, which is the point.
	c := exec.Command("git", "-C", repo, "merge", "-q", "other")
	c.Env = append(os.Environ(), gitEnv...)
	c.Run()
}
