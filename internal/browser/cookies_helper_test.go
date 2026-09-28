package browser

import (
	"context"
	"strings"
	"testing"
)

func TestCookieImportRunsTheLecternHelperForAFile(t *testing.T) {
	proc := &Process{Dir: "/b", Port: 9222, Path: "/devtools/browser/x"}
	for _, c := range []struct {
		kind, source, lectern, want string
	}{
		{"file", "/b/import.cookies", "", "python3 -c "},
		{"file", "/b/import.cookies", "/opt/lectern/bin/lectern",
			"/opt/lectern/bin/lectern helper browser-cookies 9222 /devtools/browser/x file /b/import.cookies a.example,b.example 2>&1 | tail -n 1\n"},
		// A Chrome profile is SQLite: the Python script reads it.
		{"chrome", "auto", "/opt/lectern/bin/lectern", "python3 -c "},
	} {
		var script string
		run := func(ctx context.Context, s string) (string, error) { script = s; return `{"imported":0}`, nil }
		if _, err := ImportCookiesWith(context.Background(), run, c.lectern, proc, c.kind, c.source, []string{"a.example", "b.example"}); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(script, "# lectern-browser-cookies\n") || !strings.Contains(script, c.want) {
			t.Fatalf("%s with lectern %q ran %q", c.kind, c.lectern, script)
		}
	}
}
