package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestSlugifyTitleForExportName(t *testing.T) {
	cases := map[string]string{
		"Fix the login bug!": "fix-the-login-bug",
		"  spaced   out  ":   "spaced-out",
		"":                   "",
		"!!!":                "",
		"a/b\\c: d":          "a-b-c-d",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := ""
	for i := 0; i < 80; i++ {
		long += "word "
	}
	if got := slugify(long); len(got) > 60 {
		t.Errorf("slug is %d chars, want at most 60", len(got))
	}
}

func TestParseExtractedToleratesFences(t *testing.T) {
	got := parseExtracted("```json\n[{\"category\":\"bug\",\"title\":\"x\",\"body\":\"y\"}]\n```")
	if len(got) != 1 || got[0].Category != "bug" || got[0].Title != "x" {
		t.Fatalf("parsed %+v", got)
	}
	if got := parseExtracted("nothing to save"); got != nil {
		t.Fatalf("prose parsed as %+v", got)
	}
	if got := parseExtracted("[]"); len(got) != 0 {
		t.Fatalf("empty array parsed as %+v", got)
	}
}

func TestLastAssistantText(t *testing.T) {
	msgs := []llm.Message{llm.UserText("q"), llm.AssistantText("first"), llm.UserText("again"), llm.AssistantText(" second ")}
	if got := lastAssistantText(msgs); got != "second" {
		t.Fatalf("last assistant = %q", got)
	}
}
