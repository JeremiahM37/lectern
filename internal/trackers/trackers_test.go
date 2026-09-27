package trackers

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// fakeExec answers commands from a table of (substring → result) pairs,
// first match wins, and records every command — recorded CLI output with no
// network and no real gh/glab.
type fakeExec struct {
	mu      sync.Mutex
	answers []fakeAnswer
	log     []string
}

type fakeAnswer struct {
	match string
	exact bool
	res   executor.Result
}

// exact answers only the command that is exactly cmd, ahead of every
// substring answer.
func (f *fakeExec) exact(cmd, stdout string) *fakeExec {
	f.answers = append([]fakeAnswer{{cmd, true, executor.Result{Stdout: stdout}}}, f.answers...)
	return f
}

func (f *fakeExec) on(match, stdout string) *fakeExec {
	f.answers = append(f.answers, fakeAnswer{match: match, res: executor.Result{Stdout: stdout}})
	return f
}

func (f *fakeExec) fail(match string, rc int, stderr string) *fakeExec {
	f.answers = append(f.answers, fakeAnswer{match: match, res: executor.Result{RC: rc, Stderr: stderr}})
	return f
}

func (f *fakeExec) Run(_ context.Context, cmd string, _ executor.RunOpts) (executor.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, cmd)
	for _, a := range f.answers {
		if (a.exact && cmd == a.match) || (!a.exact && strings.Contains(cmd, a.match)) {
			return a.res, nil
		}
	}
	return executor.Result{RC: 1, Stderr: "fake: unexpected command: " + cmd}, nil
}

func (f *fakeExec) ReadFile(context.Context, string, int64) ([]byte, error) { return nil, nil }
func (f *fakeExec) WriteFile(context.Context, string, []byte) error       { return nil }
func (f *fakeExec) Close() error                                          { return nil }

func (f *fakeExec) ran(sub string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.log {
		if strings.Contains(c, sub) {
			return c
		}
	}
	return ""
}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in, hint string
		want     RepoRef
		err      bool
	}{
		{"https://github.com/acme/widgets.git", "", RepoRef{"github", "github.com", "acme/widgets"}, false},
		{"git@github.com:acme/widgets.git", "", RepoRef{"github", "github.com", "acme/widgets"}, false},
		{"ssh://git@github.example.com:22/acme/widgets", "", RepoRef{"github", "github.example.com", "acme/widgets"}, false},
		{"https://gitlab.com/group/sub/proj.git", "", RepoRef{"gitlab", "gitlab.com", "group/sub/proj"}, false},
		{"git@code.internal:team/app.git", "gitlab", RepoRef{"gitlab", "code.internal", "team/app"}, false},
		{"git@code.internal:team/app.git", "", RepoRef{}, true},
		{"https://github.com/a/b/c", "", RepoRef{}, true},
		{"/srv/git/app.git", "", RepoRef{}, true},
	}
	for _, c := range cases {
		got, err := ParseRemote(c.in, c.hint)
		if (err != nil) != c.err || (!c.err && got != c.want) {
			t.Errorf("ParseRemote(%q,%q) = %+v, %v", c.in, c.hint, got, err)
		}
	}
	if s := (RepoRef{"github", "ghe.corp", "a/b"}).Slug(); s != "ghe.corp/a/b" {
		t.Errorf("enterprise slug = %q", s)
	}
}

func TestBranchName(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"42", "Fix login timeout!"}:          "42-fix-login-timeout",
		{"ENG-12", "Add SSO (Okta) support"}:  "ENG-12-add-sso-okta-support",
		{"PROJ-7", ""}:                        "PROJ-7",
		{"", "   "}:                           "work",
		{"9", "Ünïcode — only…"}:              "9-n-code-only",
		{"1", strings.Repeat("word ", 30)}:    "1-word-word-word-word-word-word-word-word-word",
		{"x y", "a..b"}:                       "x-y-a-b",
	} {
		if got := BranchName(in[0], in[1]); got != want {
			t.Errorf("BranchName(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestBuildStack(t *testing.T) {
	all := []StackEntry{
		{Number: 1, Head: "a", Base: "main"},
		{Number: 2, Head: "b", Base: "a"},
		{Number: 3, Head: "c", Base: "b"},
		{Number: 4, Head: "d", Base: "b"},
		{Number: 5, Head: "e", Base: "main"},
	}
	got := BuildStack(all, 2)
	var nums, depths []int
	for _, e := range got {
		nums, depths = append(nums, e.Number), append(depths, e.Depth)
	}
	if !reflect.DeepEqual(nums, []int{1, 2, 3, 4}) || !reflect.DeepEqual(depths, []int{0, 1, 2, 2}) || !got[1].Current {
		t.Fatalf("stack = %+v", got)
	}
	if one := BuildStack(all, 5); len(one) != 1 || !one[0].Current {
		t.Fatalf("lone PR = %+v", one)
	}
	// a cycle (two PRs based on each other's heads) must terminate
	cyc := []StackEntry{{Number: 1, Head: "a", Base: "b"}, {Number: 2, Head: "b", Base: "a"}}
	if s := BuildStack(cyc, 1); len(s) != 2 {
		t.Fatalf("cycle = %+v", s)
	}
}

func TestParseMergeTree(t *testing.T) {
	c := parseMergeTree("From x\n--lectern-merge-tree--\nabc123\nsrc/a.go\nREADME.md\nrc=1\n", "")
	if !c.Checked || !reflect.DeepEqual(c.Files, []string{"src/a.go", "README.md"}) {
		t.Fatalf("conflicts = %+v", c)
	}
	if c := parseMergeTree("--lectern-merge-tree--\nabc123\nrc=0\n", ""); !c.Checked || len(c.Files) != 0 {
		t.Fatalf("clean = %+v", c)
	}
	if c := parseMergeTree("--lectern-merge-tree--\nrc=129\n", "error: unknown option `write-tree'\nusage: git merge-tree"); c.Checked || !strings.Contains(c.Detail, "2.38") {
		t.Fatalf("old git = %+v", c)
	}
	if c := parseMergeTree("", "fatal: couldn't find remote ref"); c.Checked || !strings.Contains(c.Detail, "fetch") {
		t.Fatalf("fetch failure = %+v", c)
	}
}

func TestRichTextADF(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
	 {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Steps"}]},
	 {"type":"paragraph","content":[{"type":"text","text":"Rotate the "},{"type":"text","text":"token","marks":[{"type":"strong"}]},{"type":"hardBreak"},{"type":"text","text":"then sync"}]},
	 {"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"text":"@Jo"}}]}]}]},
	 {"type":"codeBlock","content":[{"type":"text","text":"make test"}]}]}`
	got := richText(json.RawMessage(doc))
	want := "## Steps\n\nRotate the token\nthen sync\n\n- one\n- @Jo\n\n```\nmake test\n```"
	if got != want {
		t.Fatalf("richText =\n%q\nwant\n%q", got, want)
	}
	if richText(json.RawMessage(`"plain *wiki* text"`)) != "plain *wiki* text" || richText(nil) != "" {
		t.Fatal("v2 string or empty body mishandled")
	}
	adf, _ := json.Marshal(textADF("a\nb\n\nc"))
	if richText(adf) != "a\nb\n\nc" {
		t.Fatalf("textADF round trip = %q", richText(adf))
	}
}

func TestListJQL(t *testing.T) {
	got := ListJQL("OPS", "", Filter{Mine: "assigned", Query: `say "hi"`})
	want := `project = "OPS" AND statusCategory != Done AND assignee = currentUser() AND text ~ "say \"hi\"" ORDER BY updated DESC`
	if got != want {
		t.Fatalf("jql = %s", got)
	}
	if got := ListJQL("OPS", "labels = x", Filter{State: "all"}); got != "(labels = x) ORDER BY updated DESC" {
		t.Fatalf("base jql = %s", got)
	}
}

func TestCLIErrorClassification(t *testing.T) {
	f := (&fakeExec{}).fail("pr list", 4, "To get started with GitHub CLI, please run:  gh auth login")
	g := NewGitHub(f, RepoRef{"github", "github.com", "a/b"})
	_, err := g.List(context.Background(), "pr", Filter{})
	ce, ok := err.(*CLIError)
	if !ok || !ce.Auth || !strings.Contains(ce.Msg, "gh auth login") {
		t.Fatalf("err = %#v", err)
	}
	f2 := (&fakeExec{}).fail("pr list", 127, "bash: gh: command not found")
	_, err = NewGitHub(f2, RepoRef{"github", "github.com", "a/b"}).List(context.Background(), "pr", Filter{})
	if ce, ok := err.(*CLIError); !ok || !ce.Auth || !strings.Contains(ce.Msg, "not installed") {
		t.Fatalf("err = %#v", err)
	}
}
