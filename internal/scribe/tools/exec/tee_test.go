package exec

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// useTeeDir points tee files at a temp dir for the rest of the test.
func useTeeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := TeeDir
	TeeDir = dir
	t.Cleanup(func() { TeeDir = prev })
	return dir
}

// teePathIn returns the tee file path named in an output's "[full output: …]" note.
func teePathIn(t *testing.T, output string) string {
	t.Helper()
	m := regexp.MustCompile(`\[full output: (.+?)\]`).FindStringSubmatch(output)
	if m == nil {
		t.Fatalf("no full output note in %q", output)
	}
	return m[1]
}

func TestTeePrunesToTwentyFiles(t *testing.T) {
	dir := useTeeDir(t)
	for i := 0; i < 25; i++ {
		if _, err := writeTee("run", strings.Repeat("x", 10)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != teeMaxFiles {
		t.Fatalf("tee dir holds %d files, want %d", len(entries), teeMaxFiles)
	}
}

func TestTeeCapsLargeTextKeepingHeadAndTail(t *testing.T) {
	useTeeDir(t)
	big := "HEAD" + strings.Repeat("m", teeMaxBytes*2) + "TAIL"
	path, err := writeTee("big", big)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > teeMaxBytes+200 {
		t.Fatalf("tee file is %d bytes, want at most about %d", len(data), teeMaxBytes)
	}
	if !strings.HasPrefix(string(data), "HEAD") || !strings.HasSuffix(string(data), "TAIL") {
		t.Fatalf("tee file lost its head or tail")
	}
}

func TestSpillLeavesShortOutputAlone(t *testing.T) {
	dir := useTeeDir(t)
	body, omitted := spill("short", 100, "x")
	if body != "short" || omitted != 0 {
		t.Fatalf("short output changed: %q (%d)", body, omitted)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a tee file was written for short output")
	}
	if got := filepath.Base(sanitizeLabel("go test/./x")); got != "go-test---x" {
		t.Fatalf("sanitizeLabel = %q", got)
	}
}
