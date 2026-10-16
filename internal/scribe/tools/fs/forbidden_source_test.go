package fs

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Patterns is the single source both the file tools and the shell read. It
// merges the project file and the session list, and MatchCommand applies the
// same anchoring the file tools use.
func TestPatternsMergesProjectFileAndSession(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	write(t, root, ".scribe/forbidden.json", `{"patterns": ["*.log"]}`)
	env := &tools.Env{Workdir: root, Services: map[string]any{"forbidden": []string{"notes/**"}}}
	pats, err := Patterns(env)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"*.log": true, "notes/**": true}
	if len(pats) != 2 || !want[pats[0]] || !want[pats[1]] {
		t.Fatalf("Patterns = %v, want the project file and the session pattern", pats)
	}
}

func TestPatternsNilEnvIsEmpty(t *testing.T) {
	isolateHome(t)
	pats, err := Patterns(nil)
	if err != nil || len(pats) != 0 {
		t.Fatalf("Patterns(nil) = %v, %v", pats, err)
	}
}

func TestPatternsMalformedFileIsAnError(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	write(t, root, ".scribe/forbidden.json", `{not json`)
	if _, err := Patterns(&tools.Env{Workdir: root}); err == nil {
		t.Fatal("a malformed forbidden.json must fail closed")
	}
}

func TestMatchCommandFindsPathWords(t *testing.T) {
	cases := []struct {
		name, command string
		pats          []string
		hit           bool
		path          string
	}{
		{"read a matching file", "cat config/.env", []string{".env"}, true, "config/.env"},
		{"redirect into a match", "echo hi > keys/key.pem", []string{"*.pem"}, true, "keys/key.pem"},
		{"directory glob", "ls secrets/prod/db.txt", []string{"secrets/**"}, true, "secrets/prod/db.txt"},
		{"segment boundary", "cat notsecrets/x", []string{"secrets/**"}, false, ""},
		{"flags are skipped", "rm -f .env.bak", []string{"-f"}, false, ""},
		{"no patterns", "cat .env", nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit, found, err := MatchCommand(c.pats, c.command)
			if err != nil {
				t.Fatal(err)
			}
			if found != c.hit {
				t.Fatalf("found = %v (hit %+v), want %v", found, hit, c.hit)
			}
			if found && hit.Path != c.path {
				t.Fatalf("path = %q, want %q", hit.Path, c.path)
			}
		})
	}
}
