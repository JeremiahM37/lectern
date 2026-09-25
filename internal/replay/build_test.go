package replay

import (
	"strings"
	"testing"
)

func TestPreFilterSkipsTooLarge(t *testing.T) {
	pr := PR{Number: 1, Title: "Huge refactor", Files: []FileStat{{Path: "a.go", Additions: 300, Deletions: 200}}}
	skip, _, _ := PreFilter(pr, "make check", Options{MaxChangedLines: 400})
	if skip == "" || !strings.Contains(skip, "too large") {
		t.Fatalf("expected a 'too large' skip, got %q", skip)
	}
}

func TestPreFilterUsesDefaultCapWhenUnset(t *testing.T) {
	pr := PR{Files: []FileStat{{Path: "a.go", Additions: 401}}}
	skip, _, _ := PreFilter(pr, "make check", Options{})
	if skip == "" {
		t.Fatal("expected the default 400-line cap to skip this PR")
	}
	pr2 := PR{Files: []FileStat{{Path: "a.go", Additions: 400}}}
	skip2, _, _ := PreFilter(pr2, "make check", Options{})
	if skip2 != "" {
		t.Fatalf("exactly the cap should be accepted, got skip %q", skip2)
	}
}

func TestPreFilterSkipsDocsOnly(t *testing.T) {
	pr := PR{Files: []FileStat{{Path: "docs/guide.md", Additions: 5, Deletions: 1}}}
	skip, _, _ := PreFilter(pr, "make check", Options{})
	if skip == "" || !strings.Contains(skip, "docs-only") {
		t.Fatalf("expected a docs-only skip, got %q", skip)
	}
}

func TestPreFilterSkipsWhenNoRunnableCheck(t *testing.T) {
	// no project verify command AND no recognizable test files touched
	pr := PR{Files: []FileStat{{Path: "internal/app.go", Additions: 10, Deletions: 2}}}
	skip, _, _ := PreFilter(pr, "", Options{})
	if skip == "" || !strings.Contains(skip, "no runnable check") {
		t.Fatalf("expected a 'no runnable check' skip, got %q", skip)
	}
}

func TestPreFilterAcceptsWithOnlyAProjectCheck(t *testing.T) {
	// no test files, but the project itself has a verify command — that
	// alone is a runnable check.
	pr := PR{Files: []FileStat{{Path: "internal/app.go", Additions: 10, Deletions: 2}}}
	skip, lang, files := PreFilter(pr, "make check", Options{})
	if skip != "" {
		t.Fatalf("should be accepted with a project check: %q", skip)
	}
	if lang != "" || files != nil {
		t.Fatalf("no test files should have been detected: lang=%q files=%v", lang, files)
	}
}

func TestPreFilterAcceptsWithOnlyTestFiles(t *testing.T) {
	pr := PR{Files: []FileStat{{Path: "internal/app_test.go", Additions: 10}}}
	skip, lang, _ := PreFilter(pr, "", Options{})
	if skip != "" {
		t.Fatalf("should be accepted from test files alone: %q", skip)
	}
	if lang != "go" {
		t.Fatalf("lang: %q", lang)
	}
}

func TestBuildCaseCombinesProjectCheckAndTestCommand(t *testing.T) {
	pr := PR{Number: 9, Title: "Fix the parser"}
	diff := "--- a/internal/p_test.go\n+++ b/internal/p_test.go\n@@ -0,0 +1,1 @@\n+func TestParsesX(t *testing.T) {}\n"
	c := BuildCase(pr, "make check", "go", []string{"internal/p_test.go"}, "parentsha", diff)
	if !strings.HasPrefix(c.CheckCommand, "make check && go test") {
		t.Fatalf("expected project check + test command, got %q", c.CheckCommand)
	}
}

func TestBuildCaseWithoutTestFilesLeavesCheckCommandEmpty(t *testing.T) {
	// per docs/evals.md, an empty case check_command already falls back to
	// the project's own verify command — BuildCase must not duplicate it.
	pr := PR{Number: 9, Title: "Tweak a comment"}
	c := BuildCase(pr, "make check", "", nil, "parentsha", "")
	if c.CheckCommand != "" {
		t.Fatalf("expected empty check_command (inherit project check), got %q", c.CheckCommand)
	}
}

func TestBuildCaseNameIncludesPRNumberAndTitle(t *testing.T) {
	pr := PR{Number: 55, Title: "Add retries to the HTTP client"}
	c := BuildCase(pr, "", "", nil, "sha", "")
	if !strings.Contains(c.Name, "#55") || !strings.Contains(c.Name, "Add retries to the HTTP client") {
		t.Fatalf("case name: %q", c.Name)
	}
}
