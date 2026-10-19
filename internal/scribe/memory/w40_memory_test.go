package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// w40Stores opens real stores under temp dirs, so nothing touches the home.
func w40Stores(t *testing.T) (*Stores, string) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

func TestFileHintNamesTheRecordForThatFile(t *testing.T) {
	s, _ := w40Stores(t)
	rec, _, err := s.Project.Add(Record{Category: CatBug, Title: "Parser drops trailing commas", Body: "Fails on trailing commas.", Files: []string{"internal/parse/parse.go"}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.FileHint("internal/parse/parse.go")
	if !strings.Contains(got, "Parser drops trailing commas") || !strings.Contains(got, shortID(rec.ID)) {
		t.Fatalf("hint for the file = %q, want the record's title and id", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("hint must be one line: %q", got)
	}
}

func TestFileHintIsEmptyForUnrelatedFileOrNoStore(t *testing.T) {
	s, _ := w40Stores(t)
	if _, _, err := s.Project.Add(Record{Category: CatBug, Title: "Parser bug", Body: "x", Files: []string{"internal/parse/parse.go"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.FileHint("docs/unrelated.md"); got != "" {
		t.Fatalf("unrelated file got hint %q", got)
	}
	if got := s.FileHint(""); got != "" {
		t.Fatalf("empty path got hint %q", got)
	}
	var none *Stores
	if got := none.FileHint("internal/parse/parse.go"); got != "" {
		t.Fatalf("nil stores got hint %q", got)
	}
}

func TestFileHintIsSuppressedAfterMemorySearchUntilNextTurn(t *testing.T) {
	s, _ := w40Stores(t)
	if _, _, err := s.Project.Add(Record{Category: CatBug, Title: "Parser bug", Body: "x", Files: []string{"internal/parse/parse.go"}}); err != nil {
		t.Fatal(err)
	}
	tool := &memoryTool{stores: s}
	if got := s.FileHint("internal/parse/parse.go"); got == "" {
		t.Fatal("no hint before any search")
	}
	// A search, a list and a get each count; the model has the memory now.
	for _, raw := range []string{
		`{"action":"search","query":"parser"}`,
		`{"action":"list"}`,
	} {
		tool.Run(context.Background(), nil, json.RawMessage(raw))
		if got := s.FileHint("internal/parse/parse.go"); got != "" {
			t.Fatalf("hint after %s = %q, want suppressed", raw, got)
		}
	}
	s.BeginTurn()
	if got := s.FileHint("internal/parse/parse.go"); got == "" {
		t.Fatal("hint did not return on the next turn")
	}
}

func TestMemorySaveDoesNotSuppressFileHint(t *testing.T) {
	s, _ := w40Stores(t)
	if _, _, err := s.Project.Add(Record{Category: CatBug, Title: "Parser bug", Body: "x", Files: []string{"internal/parse/parse.go"}}); err != nil {
		t.Fatal(err)
	}
	tool := &memoryTool{stores: s}
	tool.Run(context.Background(), nil, json.RawMessage(`{"action":"save","category":"fact","title":"Other thing","body":"y"}`))
	if got := s.FileHint("internal/parse/parse.go"); got == "" {
		t.Fatal("a save suppressed the hint; only search, list and get should")
	}
}

func TestSaveCompactionWritesDiaryWithFilesThatExist(t *testing.T) {
	s, root := w40Stores(t)
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "parse.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary := "Task: fix trailing commas.\nKey Decisions: keep the lexer.\nEdited internal/parse.go and missing/gone.go. e.g. this is prose."
	rec, dup, err := s.SaveCompaction(summary, root)
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("first diary entry reported as a duplicate")
	}
	if !strings.HasPrefix(rec.Title, "Session diary: Task: fix trailing commas.") {
		t.Fatalf("title = %q", rec.Title)
	}
	if len(rec.Files) != 1 || rec.Files[0] != "internal/parse.go" {
		t.Fatalf("files = %v, want only the file that exists", rec.Files)
	}
	if rec.Source != SourceAgent || !strings.Contains(rec.Body, "keep the lexer") {
		t.Fatalf("record = %+v", rec)
	}
}

func TestSaveCompactionEmptySummaryWritesNothing(t *testing.T) {
	s, root := w40Stores(t)
	if _, _, err := s.SaveCompaction("   \n", root); err != nil {
		t.Fatal(err)
	}
	if recs, _ := s.Project.List(ListOptions{Limit: 10}); len(recs) != 0 {
		t.Fatalf("empty summary wrote %d records", len(recs))
	}
}

func TestSaveCompactionCapsFilesAndBody(t *testing.T) {
	s, root := w40Stores(t)
	var names []string
	for i := 0; i < 14; i++ {
		name := filepath.Join("pkg", strings.Repeat("a", i+1)+".go")
		if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rec, _, err := s.SaveCompaction(strings.Join(names, " ")+"\n"+strings.Repeat("z", 5000), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Files) != diaryMaxFiles {
		t.Fatalf("files = %d, want %d", len(rec.Files), diaryMaxFiles)
	}
	if n := len([]rune(rec.Body)); n > diaryBodyChars {
		t.Fatalf("body = %d runes, want <= %d", n, diaryBodyChars)
	}
}
