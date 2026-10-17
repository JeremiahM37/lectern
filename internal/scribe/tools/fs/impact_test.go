package fs

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestFormatImpactWordsTheImporters(t *testing.T) {
	cases := map[string]struct {
		in   []string
		want string
	}{
		"none":  {nil, ""},
		"one":   {[]string{"a.go"}, "imported by a.go"},
		"three": {[]string{"a.go", "b.go", "c.go"}, "imported by 3 files: a.go, b.go, c.go"},
		"five": {
			[]string{"a.go", "b.go", "c.go", "d.go", "e.go"},
			"imported by 5 files: a.go, b.go, c.go, and 2 more",
		},
	}
	for name, c := range cases {
		if got := formatImpact(c.in); got != c.want {
			t.Errorf("%s: formatImpact = %q, want %q", name, got, c.want)
		}
	}
}

// hub.ts is imported by two files, so editing it should name both.
func impactFixture(t *testing.T) (*tools.Env, string) {
	t.Helper()
	env, root, _, _ := newEnv(t)
	write(t, root, "hub.ts", "export function hub(): number {\n  return 1;\n}\n")
	write(t, root, "a.ts", "import { hub } from \"./hub\";\nexport const a = hub();\n")
	write(t, root, "b.ts", "import { hub } from \"./hub\";\nexport const b = hub() + 1;\n")
	write(t, root, "solo.ts", "export const solo = 2;\n")
	buildMap(t, env, root)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "hub.ts"}))
	return env, root
}

func TestEditNamesImportersFromTheMap(t *testing.T) {
	env, _ := impactFixture(t)
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "hub.ts", "old_string": "return 1;", "new_string": "return 2;",
	}))
	got, _ := r.Meta["impact"].(string)
	if !strings.HasPrefix(got, "imported by 2 files: ") || !strings.Contains(got, "a.ts") || !strings.Contains(got, "b.ts") {
		t.Fatalf("impact = %q, want the two importers named", got)
	}
	if strings.Contains(r.Output, "imported by") {
		t.Fatalf("impact is for the user; it must not reach the model's output: %q", r.Output)
	}
}

func TestEditOfUnimportedFileHasNoImpact(t *testing.T) {
	env, _ := impactFixture(t)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "solo.ts"}))
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "solo.ts", "old_string": "= 2", "new_string": "= 3",
	}))
	if _, ok := r.Meta["impact"]; ok {
		t.Fatalf("nothing imports solo.ts, so there should be no impact: %v", r.Meta)
	}
}

func TestEditWithoutMapStillSucceeds(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "hub.ts", "export const x = 1;\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "hub.ts"}))
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "hub.ts", "old_string": "= 1", "new_string": "= 2",
	}))
	if _, ok := r.Meta["impact"]; ok || r.Summary != "+1 -1" {
		t.Fatalf("no map should mean no impact and a normal edit: meta=%v summary=%q", r.Meta, r.Summary)
	}
}

func TestNewFileHasNoImpact(t *testing.T) {
	env, _ := impactFixture(t)
	r := mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": "fresh.ts", "content": "export {};\n"}))
	if _, ok := r.Meta["impact"]; ok {
		t.Fatalf("a new file has no edit impact: %v", r.Meta)
	}
}

// multi_edit keeps its joined before/after and also names the importers.
func TestMultiEditKeepsJoinedMetaAndImpact(t *testing.T) {
	env, _ := impactFixture(t)
	r := mustOK(t, call(t, NewMultiEdit(nil), env, map[string]any{
		"path": "hub.ts",
		"edits": []map[string]any{
			{"old_string": "return 1;", "new_string": "return 2;"},
			{"old_string": "hub()", "new_string": "hub2()"},
		},
	}))
	before, okB := r.Meta["before"].(string)
	after, okA := r.Meta["after"].(string)
	if !okB || !okA || !strings.Contains(before, "return 1;") || !strings.Contains(after, "return 2;") {
		t.Fatalf("joined before/after missing: %v", r.Meta)
	}
	if got, _ := r.Meta["impact"].(string); !strings.HasPrefix(got, "imported by 2 files") {
		t.Fatalf("impact = %q", got)
	}
}

func TestFormatImpactCapsTheNamesShown(t *testing.T) {
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, fmt.Sprintf("f%02d.go", i))
	}
	got := formatImpact(many)
	if strings.Count(got, ".go") != impactNames || !strings.HasSuffix(got, "and 17 more") {
		t.Fatalf("names not capped: %q", got)
	}
}
