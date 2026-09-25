package replay

import "testing"

func TestChangedLinesSumsAdditionsAndDeletions(t *testing.T) {
	files := []FileStat{{Path: "a.go", Additions: 10, Deletions: 3}, {Path: "b.go", Additions: 0, Deletions: 5}}
	if got := ChangedLines(files); got != 18 {
		t.Fatalf("ChangedLines: got %d, want 18", got)
	}
	if got := ChangedLines(nil); got != 0 {
		t.Fatalf("ChangedLines(nil): got %d, want 0", got)
	}
}

func TestIsDocsOnly(t *testing.T) {
	cases := []struct {
		name  string
		files []FileStat
		want  bool
	}{
		{"empty file list is not docs-only", nil, false},
		{"single markdown file", []FileStat{{Path: "docs/guide.md"}}, true},
		{"under a docs directory with no extension hint", []FileStat{{Path: "docs/adr/0001-decision"}}, true},
		{"bare LICENSE", []FileStat{{Path: "LICENSE"}}, true},
		{"CHANGELOG.md plus README", []FileStat{{Path: "CHANGELOG.md"}, {Path: "README.md"}}, true},
		{"mixed docs and code", []FileStat{{Path: "README.md"}, {Path: "internal/app.go"}}, false},
		{"code only", []FileStat{{Path: "internal/app.go"}}, false},
		{"file that merely CONTAINS 'readme' in its name", []FileStat{{Path: "internal/readmeparser.go"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDocsOnly(c.files); got != c.want {
				t.Errorf("IsDocsOnly(%v) = %v, want %v", c.files, got, c.want)
			}
		})
	}
}
