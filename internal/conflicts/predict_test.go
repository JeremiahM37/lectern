package conflicts

import (
	"strings"
	"testing"
)

// Synthetic diffs for the prediction table. Each pair is a realistic shape of
// the problem: one side changes a function, the other calls it or changes it.

const goOtherParseEdit = `diff --git a/internal/y/other.go b/internal/y/other.go
--- a/internal/y/other.go
+++ b/internal/y/other.go
@@ -1,3 +1,4 @@ func Load(path string) error {
+	if err := Parse(src); err != nil {
+		return err
+	}
`

const goUnrelatedEdit = `diff --git a/internal/z/unrelated.go b/internal/z/unrelated.go
--- a/internal/z/unrelated.go
+++ b/internal/z/unrelated.go
@@ -1,3 +1,4 @@ func Shout(s string) string {
+	return strings.ToUpper(s)
`

const goGenericErrDiff = `diff --git a/internal/q/errs.go b/internal/q/errs.go
--- a/internal/q/errs.go
+++ b/internal/q/errs.go
@@ -1,3 +1,4 @@ func handler(ctx context.Context) error {
+	if err != nil {
+		return err
+	}
+	New()
`

const goGenericNewDiff = `diff --git a/internal/r/new.go b/internal/r/new.go
--- a/internal/r/new.go
+++ b/internal/r/new.go
@@ -1,2 +1,3 @@ func Make() {
+	x := New()
`

const goChangesErrDefDiff = `diff --git a/internal/s/err.go b/internal/s/err.go
--- a/internal/s/err.go
+++ b/internal/s/err.go
@@ -1,2 +1,3 @@ func ctxHelper() {
+func err() {}
`

const goCloseMethodOther = `diff --git a/internal/t/close.go b/internal/t/close.go
--- a/internal/t/close.go
+++ b/internal/t/close.go
@@ -1,2 +1,3 @@ func (f *File) Close() error {
+	f.done = true
`

const goCloseMethodDiff = `diff --git a/internal/t/conn.go b/internal/t/conn.go
--- a/internal/t/conn.go
+++ b/internal/t/conn.go
@@ -1,2 +1,3 @@ func (c *Conn) Close() error {
+	c.closed = true
`

const goSharedFileDiffA = `diff --git a/internal/shared/a.go b/internal/shared/a.go
--- a/internal/shared/a.go
+++ b/internal/shared/a.go
@@ -1,2 +1,3 @@ func alpha() {
+	x := 1
`

const goSharedFileDiffB = `diff --git a/internal/shared/a.go b/internal/shared/a.go
--- a/internal/shared/a.go
+++ b/internal/shared/a.go
@@ -50,2 +50,3 @@ func beta() {
+	y := 2
`

const pyCallerDiff = `diff --git a/svc/report.py b/svc/report.py
--- a/svc/report.py
+++ b/svc/report.py
@@ -1,2 +1,3 @@ def build():
+    text = render_table(rows)
`

const tsCallerNoLink = `diff --git a/web/src/other.ts b/web/src/other.ts
--- a/web/src/other.ts
+++ b/web/src/other.ts
@@ -1,2 +1,3 @@ export function unrelatedThing() {
+const k = 3;
`

func fp(diff string) Footprint { return ParseFootprint(diff) }

func TestPredict(t *testing.T) {
	cases := []struct {
		name      string
		a, b      string
		wantLevel string
		// wantReason is a substring that must appear in some reason, "" to skip.
		wantReason string
	}{
		{
			name:       "A changes Parse, B's new code calls it, different files",
			a:          goParseDiff,
			b:          goOtherParseEdit,
			wantLevel:  LevelSemantic,
			wantReason: "A changes Parse (internal/x/parse.go); B's new code uses Parse",
		},
		{
			name:       "new caller in another file is found in either direction",
			a:          goNewCallerDiff,
			b:          goParseDiff,
			wantLevel:  LevelSemantic,
			wantReason: "B changes Parse (internal/x/parse.go); A's new code uses Parse",
		},
		{
			name: "both sides change the same symbol",
			a:    goParseDiff,
			b: `diff --git a/internal/m/p.go b/internal/m/p.go
--- a/internal/m/p.go
+++ b/internal/m/p.go
@@ -1,2 +1,3 @@ func Parse(s string) {
+	_ = s
`,
			wantLevel:  LevelSemantic,
			wantReason: "A and B both change Parse",
		},
		{
			name:       "python caller uses a function defined on the other side",
			a:          pyDiff,
			b:          pyCallerDiff,
			wantLevel:  LevelSemantic,
			wantReason: "render_table",
		},
		{
			name:       "ts function signature change reaches a caller",
			a:          tsDiff,
			b:          tsCallerDiff,
			wantLevel:  LevelSemantic,
			wantReason: "formatAge",
		},
		{
			name:       "method-qualified symbols match on the bare method name",
			a:          goCloseMethodDiff,
			b:          goOtherParseEdit,
			wantLevel:  LevelNone,
			wantReason: "",
		},
		{
			name:       "same file no symbol link is textual",
			a:          goSharedFileDiffA,
			b:          goSharedFileDiffB,
			wantLevel:  LevelTextual,
			wantReason: "both change internal/shared/a.go",
		},
		{
			name:      "unrelated files are none",
			a:         goParseDiff,
			b:         goUnrelatedEdit,
			wantLevel: LevelNone,
		},
		{
			name:      "generic err and ctx do not trigger",
			a:         goParseDiff,
			b:         goGenericErrDiff,
			wantLevel: LevelNone,
		},
		{
			name: "common name New alone does not trigger",
			a: `diff --git a/internal/n/n.go b/internal/n/n.go
--- a/internal/n/n.go
+++ b/internal/n/n.go
@@ -1,2 +1,3 @@ func New(cfg Config) *Thing {
+	return nil
`,
			b:         goGenericNewDiff,
			wantLevel: LevelNone,
		},
		{
			name:      "short symbol err defined on one side does not trigger",
			a:         goChangesErrDefDiff,
			b:         goGenericErrDiff,
			wantLevel: LevelNone,
		},
		{
			name: "short bare name Close on a function does not link",
			a: `diff --git a/internal/k/k.go b/internal/k/k.go
--- a/internal/k/k.go
+++ b/internal/k/k.go
@@ -1,2 +1,3 @@ func Shut() {
+func Run() {}
`,
			b: `diff --git a/internal/l/l.go b/internal/l/l.go
--- a/internal/l/l.go
+++ b/internal/l/l.go
@@ -1,2 +1,3 @@ func Other() {
+	Run()
`,
			wantLevel: LevelNone,
		},
		{
			name: "common longer name like run is not a link on its own",
			a: `diff --git a/x/a.go b/x/a.go
--- a/x/a.go
+++ b/x/a.go
@@ -1,2 +1,3 @@ func run() {
+	x := 1
`,
			b: `diff --git a/x/b.go b/x/b.go
--- a/x/b.go
+++ b/x/b.go
@@ -1,2 +1,3 @@ func other() {
+	run()
`,
			wantLevel: LevelNone,
		},
		{
			name:      "empty diffs are none",
			a:         "",
			b:         "",
			wantLevel: LevelNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Predict(fp(c.a), fp(c.b))
			if got.Level != c.wantLevel {
				t.Fatalf("level = %s, want %s (reasons %v)", got.Level, c.wantLevel, got.Reasons)
			}
			if c.wantReason == "" {
				return
			}
			found := false
			for _, r := range got.Reasons {
				if strings.Contains(r, c.wantReason) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no reason contains %q; reasons = %v", c.wantReason, got.Reasons)
			}
		})
	}
}

func TestPredictNamedUsesSessionLabels(t *testing.T) {
	got := PredictNamed("Session #12", "Session #14", fp(goParseDiff), fp(goOtherParseEdit))
	if got.Level != LevelSemantic || len(got.Reasons) == 0 {
		t.Fatalf("got %+v", got)
	}
	want := "Session #12 changes Parse (internal/x/parse.go); Session #14's new code uses Parse"
	if got.Reasons[0] != want {
		t.Fatalf("reason = %q, want %q", got.Reasons[0], want)
	}
}

func TestPredictIsSymmetricInLevel(t *testing.T) {
	pairs := [][2]string{
		{goParseDiff, goOtherParseEdit},
		{goSharedFileDiffA, goSharedFileDiffB},
		{goParseDiff, goUnrelatedEdit},
	}
	for _, p := range pairs {
		ab := Predict(fp(p[0]), fp(p[1])).Level
		ba := Predict(fp(p[1]), fp(p[0])).Level
		if ab != ba {
			t.Errorf("asymmetric level %s vs %s", ab, ba)
		}
	}
}
