package ctxbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

func anyContains(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func TestCollectReadsFilesAndGlobs(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "CLAUDE.md"), "house rules")
	write(t, filepath.Join(dir, "docs", "a.md"), "aaa")
	write(t, filepath.Join(dir, "docs", "b.md"), "bbb")

	files, notes := Collect([]string{
		filepath.Join(dir, "CLAUDE.md"), filepath.Join(dir, "docs", "*.md")})
	if got := names(files); strings.Join(got, ",") != "CLAUDE.md,a.md,b.md" {
		t.Fatalf("staged names: %v", got)
	}
	if string(files[0].Data) != "house rules" {
		t.Errorf("content: %q", files[0].Data)
	}
	if len(notes) != 0 {
		t.Errorf("nothing should have been skipped: %v", notes)
	}
}

func TestMissingPathsAreReportedNotSwallowed(t *testing.T) {
	dir := t.TempDir()
	files, notes := Collect([]string{filepath.Join(dir, "nope.md")})
	if len(files) != 0 {
		t.Fatalf("nothing should be staged: %v", names(files))
	}
	if !anyContains(notes, "no such file") {
		t.Fatalf("notes: %v", notes)
	}
	// and the agent is TOLD, rather than quietly running with less
	if !strings.Contains(PromptPrefix(files, notes), "nope.md") {
		t.Error("the prompt must name what could not be staged")
	}
}

func TestDuplicateBasenamesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "one", "CLAUDE.md"), "one")
	write(t, filepath.Join(dir, "two", "CLAUDE.md"), "two")
	files, _ := Collect([]string{
		filepath.Join(dir, "one", "CLAUDE.md"), filepath.Join(dir, "two", "CLAUDE.md")})
	got := names(files)
	if len(got) != 2 || got[0] != "CLAUDE.md" || got[1] != "two_CLAUDE.md" {
		t.Fatalf("staged names: %v", got)
	}
}

func TestSamePathTwiceIsStagedOnce(t *testing.T) {
	dir := t.TempDir()
	p := write(t, filepath.Join(dir, "CLAUDE.md"), "x")
	files, _ := Collect([]string{p, p})
	if len(files) != 1 {
		t.Fatalf("expected one staged file, got %v", names(files))
	}
}

func TestOversizedFileTruncatesLoudly(t *testing.T) {
	dir := t.TempDir()
	old := MaxFileBytes
	MaxFileBytes = 100
	defer func() { MaxFileBytes = old }()
	write(t, filepath.Join(dir, "big.md"), strings.Repeat("x", 500))
	files, notes := Collect([]string{filepath.Join(dir, "big.md")})
	if !strings.Contains(string(files[0].Data), "truncated by lectern") {
		t.Error("truncation must be visible in the staged file")
	}
	if !anyContains(notes, "truncated") {
		t.Errorf("notes: %v", notes)
	}
}

func TestTotalCapStopsTheBundleWithANote(t *testing.T) {
	dir := t.TempDir()
	old := MaxTotalBytes
	MaxTotalBytes = 10
	defer func() { MaxTotalBytes = old }()
	write(t, filepath.Join(dir, "a.md"), "12345")
	write(t, filepath.Join(dir, "b.md"), "67890123")
	files, notes := Collect([]string{
		filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")})
	if got := names(files); len(got) != 1 || got[0] != "a.md" {
		t.Fatalf("staged: %v", got)
	}
	if !anyContains(notes, "cap") {
		t.Errorf("notes: %v", notes)
	}
}

func TestPromptPrefixEmptyWhenNothingConfigured(t *testing.T) {
	if PromptPrefix(nil, nil) != "" {
		t.Fatal("no context configured must leave the prompt untouched")
	}
}

func TestIndexListsSourcePaths(t *testing.T) {
	dir := t.TempDir()
	p := write(t, filepath.Join(dir, "CLAUDE.md"), "hi")
	files, notes := Collect([]string{p})
	if !strings.Contains(IndexMarkdown(files, notes), p) {
		t.Error("the index must record where each file came from")
	}
}
