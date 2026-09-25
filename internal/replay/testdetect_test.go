package replay

import (
	"strings"
	"testing"
)

func TestTestFilesDetectsGo(t *testing.T) {
	lang, matched := TestFiles([]FileStat{
		{Path: "internal/foo/foo.go"},
		{Path: "internal/foo/foo_test.go"},
		{Path: "internal/bar/bar_test.go"},
	})
	if lang != "go" || len(matched) != 2 {
		t.Fatalf("TestFiles: lang=%q matched=%v", lang, matched)
	}
}

func TestTestFilesDetectsPytest(t *testing.T) {
	lang, matched := TestFiles([]FileStat{{Path: "app.py"}, {Path: "tests/test_app.py"}})
	if lang != "python" || len(matched) != 1 || matched[0] != "tests/test_app.py" {
		t.Fatalf("TestFiles: lang=%q matched=%v", lang, matched)
	}
	lang, matched = TestFiles([]FileStat{{Path: "app_test.py"}})
	if lang != "python" || len(matched) != 1 {
		t.Fatalf("TestFiles (suffix form): lang=%q matched=%v", lang, matched)
	}
}

func TestTestFilesDetectsJS(t *testing.T) {
	lang, matched := TestFiles([]FileStat{{Path: "src/app.tsx"}, {Path: "src/app.test.tsx"}})
	if lang != "js" || len(matched) != 1 {
		t.Fatalf("TestFiles: lang=%q matched=%v", lang, matched)
	}
}

func TestTestFilesNoneWhenNothingTestShaped(t *testing.T) {
	lang, matched := TestFiles([]FileStat{{Path: "README.md"}, {Path: "app.py"}})
	if lang != "" || matched != nil {
		t.Fatalf("expected no detection, got lang=%q matched=%v", lang, matched)
	}
}

func TestGoTestCommandNamesExactFunctionsFromTheDiff(t *testing.T) {
	diff := "--- a/internal/foo/foo_test.go\n+++ b/internal/foo/foo_test.go\n" +
		"@@ -0,0 +1,6 @@\n+func TestFooDoesX(t *testing.T) {}\n+func TestFooDoesY(t *testing.T) {}\n" +
		"+func helper() {}\n" // not a Test func — must not appear in -run
	cmd := TestCommand("go", []string{"internal/foo/foo_test.go"}, diff)
	if !strings.Contains(cmd, "go test ./internal/foo/...") {
		t.Fatalf("command missing package dir: %q", cmd)
	}
	if !strings.Contains(cmd, "TestFooDoesX") || !strings.Contains(cmd, "TestFooDoesY") {
		t.Fatalf("command missing added test names: %q", cmd)
	}
	if strings.Contains(cmd, "helper") {
		t.Fatalf("command should only name Test funcs, got: %q", cmd)
	}
}

func TestGoTestCommandWithoutADiffStillScopesToPackages(t *testing.T) {
	cmd := TestCommand("go", []string{"internal/foo/foo_test.go", "internal/bar/bar_test.go"}, "")
	if !strings.Contains(cmd, "./internal/bar/...") || !strings.Contains(cmd, "./internal/foo/...") {
		t.Fatalf("command missing a package dir: %q", cmd)
	}
	if strings.Contains(cmd, "-run") {
		t.Fatalf("no test names should mean no -run filter: %q", cmd)
	}
}

func TestPytestCommandRunsExactlyTheMatchedFiles(t *testing.T) {
	cmd := TestCommand("python", []string{"tests/test_a.py", "tests/test_b.py"}, "")
	if cmd != "pytest 'tests/test_a.py' 'tests/test_b.py'" {
		t.Fatalf("unexpected pytest command: %q", cmd)
	}
}

func TestJestCommandRunsExactlyTheMatchedFiles(t *testing.T) {
	cmd := TestCommand("js", []string{"src/app.test.tsx"}, "")
	if cmd != "npx jest 'src/app.test.tsx'" {
		t.Fatalf("unexpected jest command: %q", cmd)
	}
}

func TestTestCommandQuotesFilesWithSpecialCharacters(t *testing.T) {
	cmd := TestCommand("python", []string{"tests/it's a test.py"}, "")
	if !strings.Contains(cmd, `'tests/it'\''s a test.py'`) {
		t.Fatalf("expected shell-escaped path, got %q", cmd)
	}
}
