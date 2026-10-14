package conflicts

import (
	"reflect"
	"sort"
	"testing"
)

// Synthetic diffs only. They are shaped like git output but contain no real
// project code.
const goParseDiff = `diff --git a/internal/x/parse.go b/internal/x/parse.go
index 1111111..2222222 100644
--- a/internal/x/parse.go
+++ b/internal/x/parse.go
@@ -10,7 +10,7 @@ func Parse(input string) (Node, error) {
 	if input == "" {
-		return nil, errEmpty
+		return nil, ErrEmptyInput
 	}
`

const goMethodDiff = `diff --git a/internal/store/db.go b/internal/store/db.go
--- a/internal/store/db.go
+++ b/internal/store/db.go
@@ -40,3 +40,4 @@ func (db *DB) Close() error {
+	db.closed = true
`

const goNewCallerDiff = `diff --git a/internal/api/handler.go b/internal/api/handler.go
--- a/internal/api/handler.go
+++ b/internal/api/handler.go
@@ -5,6 +5,8 @@ func serve(w http.ResponseWriter, r *http.Request) {
+	node, err := Parse(r.FormValue("q"))
+	if err != nil {
+		return
+	}
`

const pyDiff = `diff --git a/app/tools.py b/app/tools.py
--- a/app/tools.py
+++ b/app/tools.py
@@ -1,3 +1,6 @@ class Renderer:
+def render_table(rows):
+    return format_rows(rows)
`

const tsDiff = `diff --git a/web/src/format.ts b/web/src/format.ts
--- a/web/src/format.ts
+++ b/web/src/format.ts
@@ -3,4 +3,4 @@ export const formatDuration =
-export function formatAge(ms: number) {
+export function formatAge(ms: number, now: number) {
`

const tsCallerDiff = `diff --git a/web/src/card.tsx b/web/src/card.tsx
--- a/web/src/card.tsx
+++ b/web/src/card.tsx
@@ -1,2 +1,3 @@
+const label = formatAge(started, Date.now());
`

const deletedFileDiff = `diff --git a/old/gone.go b/old/gone.go
deleted file mode 100644
--- a/old/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-func Retired() {}
`

func TestParseFootprintFiles(t *testing.T) {
	fp := ParseFootprint(goParseDiff)
	if want := []string{"internal/x/parse.go"}; !reflect.DeepEqual(fp.Files, want) {
		t.Fatalf("files = %v, want %v", fp.Files, want)
	}
	fp = ParseFootprint(deletedFileDiff)
	if want := []string{"old/gone.go"}; !reflect.DeepEqual(fp.Files, want) {
		t.Fatalf("deleted file name = %v, want %v", fp.Files, want)
	}
}

func TestParseFootprintSymbols(t *testing.T) {
	cases := []struct {
		name string
		diff string
		file string
		want []string
	}{
		{"go func from hunk context", goParseDiff, "internal/x/parse.go", []string{"Parse"}},
		{"go method qualified by receiver", goMethodDiff, "internal/store/db.go", []string{"DB.Close"}},
		{"go caller adds no definition", goNewCallerDiff, "internal/api/handler.go", []string{"serve"}},
		{"python def and class context", pyDiff, "app/tools.py", []string{"Renderer", "render_table"}},
		{"ts export const and function", tsDiff, "web/src/format.ts", []string{"formatAge", "formatDuration"}},
		{"deleted go func from removed line", deletedFileDiff, "old/gone.go", []string{"Retired"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fp := ParseFootprint(c.diff)
			got := append([]string(nil), fp.Defined[c.file]...)
			sort.Strings(got)
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Defined[%s] = %v, want %v", c.file, got, want)
			}
		})
	}
}

func TestParseFootprintReferencedFiltersKeywords(t *testing.T) {
	fp := ParseFootprint(goNewCallerDiff)
	for _, id := range []string{"Parse", "node", "err", "r"} {
		if !fp.Referenced[id] {
			t.Errorf("Referenced missing %q", id)
		}
	}
	for _, kw := range []string{"if", "return", "func", "nil"} {
		if fp.Referenced[kw] {
			t.Errorf("Referenced contains keyword %q", kw)
		}
	}
}

func TestParseFootprintEmpty(t *testing.T) {
	fp := ParseFootprint("")
	if len(fp.Files) != 0 || len(fp.Defined) != 0 || len(fp.Referenced) != 0 {
		t.Fatalf("empty diff produced %+v", fp)
	}
}

func TestParseFootprintUnknownExtensionHasNoSymbols(t *testing.T) {
	diff := `diff --git a/notes/readme.md b/notes/readme.md
--- a/notes/readme.md
+++ b/notes/readme.md
@@ -1 +1 @@ func Parse(
-func Parse(x)
+func Parse(y)
`
	fp := ParseFootprint(diff)
	if len(fp.Defined) != 0 {
		t.Fatalf("unknown extension should define no symbols, got %v", fp.Defined)
	}
}
