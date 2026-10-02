package api

import "testing"

func TestSuggestedCommitMessage(t *testing.T) {
	files := []any{map[string]any{"path": "a/NOTES.md"}, map[string]any{"path": "b.go"}}
	for _, c := range []struct {
		prompt string
		files  any
		want   string
	}{
		{"make a file\nwith details", files, "Make a file"},
		{"", files, "Update NOTES.md, b.go"},
		{"", []any{map[string]any{"path": "1"}, map[string]any{"path": "2"}, map[string]any{"path": "3"}, map[string]any{"path": "4"}}, "Update 1, 2, 3 and 1 more"},
		{"", nil, ""},
	} {
		if got := suggestCommitMessage(c.prompt, c.files); got != c.want {
			t.Errorf("%q: got %q want %q", c.prompt, got, c.want)
		}
	}
}
