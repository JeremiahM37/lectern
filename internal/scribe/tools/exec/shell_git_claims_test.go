package exec

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// reachFor returns, for each git call in command, its resolved paths and
// whole-tree flag, skipping calls that change nothing.
func reachFor(command, dir string) []gitReach {
	var out []gitReach
	for _, c := range shellGitCalls(command, dir) {
		if r, ok := c.reach(); ok {
			out = append(out, r)
		}
	}
	return out
}

func TestShellGitReach(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "proj")
	p := func(rel ...string) []string {
		var out []string
		for _, r := range rel {
			out = append(out, filepath.Join(dir, r))
		}
		return out
	}
	cases := []struct {
		name    string
		command string
		want    []gitReach
	}{
		{"commit with message reaches the whole tree", `git commit -m "fix"`, []gitReach{{all: true}}},
		{"commit -a is whole tree", `git commit -am fix`, []gitReach{{all: true}}},
		{"add -A is whole tree", `git add -A && git commit -m x`, []gitReach{{all: true}, {all: true}}},
		{"add . is whole tree", `git add .`, []gitReach{{all: true}}},
		{"add named file", `git add main.go`, []gitReach{{paths: p("main.go")}}},
		{"add with no paths is whole tree", `git add`, []gitReach{{all: true}}},
		{"checkout -- file names the file only", `git checkout -- main.go`, []gitReach{{paths: p("main.go")}}},
		{"checkout branch is whole tree", `git checkout feature`, []gitReach{{all: true}}},
		{"restore file", `git restore internal/x.go`, []gitReach{{paths: p("internal/x.go")}}},
		{"restore staged file", `git restore --staged a.go`, []gitReach{{paths: p("a.go")}}},
		{"restore with no paths is whole tree", `git restore --staged`, []gitReach{{all: true}}},
		{"reset --hard is whole tree", `git reset --hard HEAD~1`, []gitReach{{all: true}}},
		{"reset -- file names the file only", `git reset -- a.go`, []gitReach{{paths: p("a.go")}}},
		{"stash is whole tree", `git stash push -m wip`, []gitReach{{all: true}}},
		{"stash list changes nothing", `git stash list`, nil},
		{"stash show changes nothing", `git stash show -p`, nil},
		{"rm names the file", `git rm old.go`, []gitReach{{paths: p("old.go")}}},
		{"clean -n changes nothing", `git clean -fdn`, nil},
		{"clean -f is whole tree", `git clean -fd`, []gitReach{{all: true}}},
		{"apply --check changes nothing", `git apply --check p.diff`, nil},
		{"read-only git is ignored", `git status && git diff`, nil},
		{"push does not change files", `git push origin main`, nil},
		{"not a git command", `ls -la && echo git commit`, nil},
		{"-C moves the directory", `git -C sub add x.go`, []gitReach{{paths: p("sub/x.go")}}},
		{"cd then git uses the new directory", `cd sub && git checkout -- x.go`, []gitReach{{paths: p("sub/x.go")}}},
		{"env prefix and quotes", `env GIT_EDITOR=true git commit -m "a && b"`, []gitReach{{all: true}}},
		{"pipeline segments are each read", `echo hi | git add a.go; git commit -m x`, []gitReach{{paths: p("a.go")}, {all: true}}},
		{"git -c option is skipped", `git -c user.name=x commit -m y`, []gitReach{{all: true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reachFor(c.command, dir)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("reachFor(%q)\n got %+v\nwant %+v", c.command, got, c.want)
			}
		})
	}
}

func TestShellSegmentsKeepsQuotedText(t *testing.T) {
	got := shellSegments(`git commit -m "a && b" ; echo 'x|y'`)
	want := [][]string{
		{"git", "commit", "-m", "a && b"},
		{"echo", "x|y"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shellSegments = %q, want %q", got, want)
	}
}

func TestShellGitWarnsAboutOtherTabsClaims(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "other.go"), "package other\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	writeFile(t, filepath.Join(dir, "main.go"), "package main // edited\n")
	writeFile(t, filepath.Join(dir, "other.go"), "package other // edited\n")

	env := newTestEnv(dir, &fakeHost{})
	env.Claims = fakeClaims{filepath.Join(dir, "main.go"): "tab two"}
	reg, _ := newTestRegistry(t, Options{})

	// A whole-tree commit reaches main.go through the changed files. The
	// command still runs: the warning is advisory, not a refusal.
	res := runTool(t, reg, "shell", env, map[string]any{"command": "git add -A && git commit -q -m x"})
	if !strings.HasPrefix(res.Output, "WARNING:") || !strings.Contains(res.Output, "main.go (claimed by tab two)") {
		t.Fatalf("expected a warning naming main.go first, got %q", res.Output)
	}
	if !strings.Contains(res.Summary, "another tab") {
		t.Fatalf("the row summary should carry the warning, got %q", res.Summary)
	}
	if strings.Contains(res.Output, "other.go") {
		t.Fatalf("unclaimed other.go should not be named: %q", res.Output)
	}
	if subject := gitIn(t, dir, "log", "-1", "--format=%s"); subject != "x\n" {
		t.Fatalf("the commit should still run, last subject %q", subject)
	}

	// A named path that is not claimed produces no warning.
	env.Claims = fakeClaims{filepath.Join(dir, "main.go"): "tab two"}
	if w := shellClaimWarning(env, dir, "git checkout -- other.go"); w != "" {
		t.Fatalf("unclaimed path should not warn, got %q", w)
	}
	// A named claimed path warns even when the tree is clean.
	if w := shellClaimWarning(env, dir, "git checkout -- main.go"); !strings.Contains(w, "main.go (claimed by tab two)") {
		t.Fatalf("named claimed path should warn, got %q", w)
	}

	// Read-only git never warns, even with a claim on a changed file.
	if res := runTool(t, reg, "shell", env, map[string]any{"command": "git status && git stash list"}); strings.Contains(res.Output, "WARNING") {
		t.Fatalf("read-only git should not warn: %q", res.Output)
	}
}

func TestShellClaimWarningNeedsClaims(t *testing.T) {
	dir := t.TempDir()
	if w := shellClaimWarning(nil, dir, "git commit -m x"); w != "" {
		t.Fatalf("nil env should not warn, got %q", w)
	}
	env := newTestEnv(dir, nil)
	if w := shellClaimWarning(env, dir, "git commit -m x"); w != "" {
		t.Fatalf("no claim source should not warn, got %q", w)
	}
	env.Claims = fakeClaims{}
	if w := shellClaimWarning(env, dir, "git commit -m x"); w != "" {
		t.Fatalf("empty claims should not warn, got %q", w)
	}
}
