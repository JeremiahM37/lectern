package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShellWordsSplitsLikeAShell(t *testing.T) {
	cases := map[string][]string{
		"nvim":                     {"nvim"},
		"  code   --wait ":         {"code", "--wait"},
		`sh -c 'exit 3'`:           {"sh", "-c", "exit 3"},
		`emacs -nw "my file"`:      {"emacs", "-nw", "my file"},
		`a "q\"uote" b\ c`:         {"a", `q"uote`, "b c"},
		`''`:                       {""},
		"":                         nil,
		`vim 'unclosed rest of it`: {"vim", "unclosed rest of it"},
	}
	for in, want := range cases {
		if got := shellWords(in); !reflect.DeepEqual(got, want) {
			t.Errorf("shellWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// $EDITOR with a quoted argument reaches the editor as one argument, and an
// unset editor setting lets $EDITOR decide (QA wave 40, item 7).
func TestEditorFromEnvironmentKeepsQuotedArguments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "myed")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SCRIBE_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", `myed --opt 'two words'`)
	setEditorCommand("", nil)
	argv, err := editorCommandAt("f.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"myed", "--opt", "two words", "f.txt"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}
