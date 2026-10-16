package fs

import (
	"strings"
	"testing"
)

func TestCreationPreviewShowsHeadAndTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "line"+string(rune('a'+i-1)))
	}
	got := creationPreview(strings.Join(lines, "\n") + "\n")
	want := "\n  linea\n  lineb\n  linec\n  ... (5 lines not shown)\n  linei\n  linej"
	if got != want {
		t.Fatalf("preview:\n%q\nwant:\n%q", got, want)
	}
	if short := creationPreview("one\ntwo\n"); short != "\n  one\n  two" {
		t.Fatalf("short file should be shown whole, got %q", short)
	}
	if creationPreview("") != "" {
		t.Fatal("empty content should add nothing")
	}
}
