package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// buildMap opens and builds an in-memory code map for root and makes it the
// session's map.
func buildMap(t *testing.T, env *tools.Env, root string) *repomap.Map {
	t.Helper()
	m, err := repomap.Open(root, repomap.Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	env.Services = map[string]any{"repomap": m}
	return m
}

func TestReadSeveralRangesInOneCall(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "l1\nl2\nl3\nl4\nl5\nl6\n")
	r := NewRead()
	for _, lines := range []any{"1-2,5", []string{"1-2", "5"}, "5,1-2"} {
		got := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": lines, "force": true}))
		if !strings.Contains(got.Output, "── lines 1-2 ──\n     1  l1\n     2  l2\n") || !strings.Contains(got.Output, "── lines 5-5 ──\n     5  l5") {
			t.Fatalf("lines=%v: %q", lines, got.Output)
		}
		if strings.Contains(got.Output, "l3") {
			t.Fatalf("lines=%v showed a line outside the ranges: %q", lines, got.Output)
		}
	}
	objs := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "force": true, "ranges": []map[string]any{{"start": 3, "end": 4}, {"start": 6}}}))
	if !strings.Contains(objs.Output, "     3  l3") || !strings.Contains(objs.Output, "     6  l6") || strings.Contains(objs.Output, "l5") {
		t.Fatalf("ranges objects: %q", objs.Output)
	}
	// Overlapping ranges merge into one window with no header.
	merged := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "1-3,2-4", "force": true}))
	if strings.Contains(merged.Output, "── lines") || !strings.Contains(merged.Output, "     4  l4") {
		t.Fatalf("overlap not merged: %q", merged.Output)
	}
	mustErr(t, call(t, r, env, map[string]any{"path": "a.txt", "ranges": []map[string]any{{"start": 5, "end": 2}}}), "ranges need start")
}

func TestReadSymbolByKindAndName(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "s.go", goSymbolsSrc)
	r := NewRead()
	typ := mustOK(t, call(t, r, env, map[string]any{"path": "s.go", "target": "type", "name": "Server"}))
	if !strings.HasPrefix(typ.Output, "Server (type), lines 3-5:") {
		t.Fatalf("target type: %q", typ.Output)
	}
	// A struct is a type in Go.
	mustOK(t, call(t, r, env, map[string]any{"path": "s.go", "target": "struct", "name": "Server"}))
	// Two definitions share the name Run (the method and the function): the
	// kind decides, and without it the reply lists both.
	mustErr(t, call(t, r, env, map[string]any{"path": "s.go", "name": "Run"}), "2 definitions match")
	meth := mustOK(t, call(t, r, env, map[string]any{"path": "s.go", "target": "method", "name": "Server.Run"}))
	if !strings.HasPrefix(meth.Output, "Run (func), lines 7-9:") {
		t.Fatalf("target method Server.Run: %q", meth.Output)
	}
	// function covers methods too, so it is still two here.
	mustErr(t, call(t, r, env, map[string]any{"path": "s.go", "target": "function", "name": "Run"}), "2 definitions match")
	// Near miss: the kind merged into symbol. A unique name resolves.
	merged := mustOK(t, call(t, r, env, map[string]any{"path": "s.go", "symbol": "type Server", "force": true}))
	if !strings.HasPrefix(merged.Output, "Server (type), lines 3-5:") {
		t.Fatalf("symbol with kind word: %q", merged.Output)
	}
	// A name of the wrong kind says what it is.
	mustErr(t, call(t, r, env, map[string]any{"path": "s.go", "target": "class", "name": "Server"}), "is not a class. It is a type")
	mustErr(t, call(t, r, env, map[string]any{"path": "s.go", "target": "function"}), "needs a name")
	mustErr(t, call(t, r, env, map[string]any{"path": "s.go", "target": "type", "name": "Server", "lines": "1-2"}), "not both")
}

func TestReadSymbolKindUsesCodeMap(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "k.go", "package p\n\ntype Run struct{}\n\nfunc Run() {}\n")
	buildMap(t, env, root)
	fn := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "k.go", "target": "function", "name": "Run"}))
	if !strings.HasPrefix(fn.Output, "Run (function), lines 5-5:") {
		t.Fatalf("map-backed function kind: %q", fn.Output)
	}
}

func TestReadIfHashSkipsUnchangedWindow(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "one\ntwo\nthree\n")
	r := NewRead()
	shown := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "2-3"}))
	hash := shown.Output[strings.LastIndex(shown.Output, "\nhash:")+len("\nhash:"):]
	skip := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "2-3", "ifHash": hash}))
	if skip.Summary != "unchanged" || !strings.HasPrefix(skip.Output, "unchanged:"+hash+" (2 lines)") || strings.Contains(skip.Output, "two") {
		t.Fatalf("ifHash match should skip content: %q", skip.Output)
	}
	// A different hash sends the content.
	sent := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "2-3", "ifHash": "000000000000", "force": true}))
	if !strings.Contains(sent.Output, "     2  two") {
		t.Fatalf("mismatched ifHash should send content: %q", sent.Output)
	}
}

func TestReadIfHashOnWholeFile(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "one\ntwo\n")
	r := NewRead()
	mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	again := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	// The unchanged notice carries the file hash, which ifHash accepts too.
	fields := strings.Fields(again.Output)
	var fileHash string
	for i, f := range fields {
		if f == "(hash" && i+1 < len(fields) {
			fileHash = strings.TrimSuffix(fields[i+1], ",")
		}
	}
	if fileHash == "" {
		t.Fatalf("no hash in notice: %q", again.Output)
	}
	fresh := newEnvSameRoot(t, env, root)
	skip := mustOK(t, call(t, r, fresh, map[string]any{"path": "a.txt", "ifHash": fileHash}))
	if skip.Summary != "unchanged" {
		t.Fatalf("file hash should skip the whole file: %q", skip.Output)
	}
}

// newEnvSameRoot is a fresh session over the same files, so its read state is
// empty and only ifHash can explain an unchanged reply.
func newEnvSameRoot(t *testing.T, env *tools.Env, root string) *tools.Env {
	t.Helper()
	return &tools.Env{Workdir: root, Host: env.Host, Files: newTestFiles()}
}

func TestReadCols(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "wide.txt", strings.Repeat("x", 300)+"\nshort\n")
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "wide.txt", "cols": 50}))
	if !strings.Contains(got.Output, "were longer than cols=50") || strings.Contains(got.Output, strings.Repeat("x", 60)) {
		t.Fatalf("cols should cut wide lines and say so: %q", got.Output)
	}
	// Images carry their display width for the TUI.
	os.WriteFile(filepath.Join(root, "pic.png"), []byte("png"), 0o644)
	def := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "pic.png"}))
	if def.Meta["cols"] != 120 {
		t.Fatalf("default image cols = %v", def.Meta["cols"])
	}
	wide := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "pic.png", "cols": 500}))
	if wide.Meta["cols"] != 200 {
		t.Fatalf("image cols should cap at 200, got %v", wide.Meta["cols"])
	}
}

func TestReadWebSourceRefusesPrivateAddressesAndDenial(t *testing.T) {
	env, _, _, host := newEnv(t)
	// Loopback is refused by the fetch guard before any approval is asked.
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "http://127.0.0.1:9/x"}), "private")
	// A public address with the user declining: refused, and the reason says so.
	// A literal public IP keeps the test off DNS, so it runs without a network:
	// the guard passes it and the approval is asked before anything is dialled.
	host.decision = tools.Deny
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "https://93.184.215.14/"}), "denied")
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "https://93.184.215.14/a.png"}), "not fetch")
}

func TestReadSmartTruncationOutlinesBeyondCut(t *testing.T) {
	env, root, _, _ := newEnv(t)
	var sb strings.Builder
	sb.WriteString("package p\n\n")
	for i := 0; i < 2100; i++ {
		sb.WriteString("// filler\n")
	}
	sb.WriteString("func Late() {\n\t_ = 1\n}\n")
	write(t, root, "big.go", sb.String())
	buildMap(t, env, root)
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "big.go"}))
	if !strings.Contains(got.Output, "… 105 more lines; call read with offset=2001 to continue.") {
		t.Fatalf("continuation hint missing: %q", got.Output[len(got.Output)-200:])
	}
	if !strings.Contains(got.Output, "Symbols below:\n  :2103-2105 function Late") {
		t.Fatalf("outline should name the symbol below the cut: %q", got.Output[len(got.Output)-300:])
	}
}

func TestReadFilesArrayPerFileOptions(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "a1\na2\na3\n")
	write(t, root, "b.txt", "b1\nb2\nb3\n")
	got := mustOK(t, call(t, NewRead(), env, map[string]any{
		"files": []any{"a.txt", map[string]any{"path": "b.txt", "lines": "2"}},
	}))
	if !strings.Contains(got.Output, "── a.txt ──\n     1  a1") || !strings.Contains(got.Output, "── b.txt ──\n     2  b2") || strings.Contains(got.Output, "b1") {
		t.Fatalf("files array: %q", got.Output)
	}
	mustErr(t, call(t, NewRead(), env, map[string]any{"files": []any{map[string]any{"lines": "1"}}}), "needs a path")
}

func TestGrepForceAndDependencyScope(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "src/app.js", "const x = needle;\n")
	write(t, root, "node_modules/pkg/index.js", "module.exports = needle;\n")
	write(t, root, "node_modules/pkg/package.json", "{}\n")
	write(t, root, "vendor/lib/lib.go", "package lib\n// needle\n")
	for _, forceGo := range []bool{true, false} {
		g := newGrep(forceGo)
		plain := mustOK(t, call(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files"}))
		// .gitignore is the rule here, and node_modules is always skipped.
		if strings.Contains(plain.Output, "node_modules") || !strings.Contains(plain.Output, "src/app.js") {
			t.Fatalf("default grep should skip node_modules: %q", plain.Output)
		}
		forced := mustOK(t, call(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files", "force": true}))
		if forced.Output != plain.Output {
			t.Fatalf("force changed the answer: %q vs %q", forced.Output, plain.Output)
		}
		dep := mustOK(t, call(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files", "dep": "pkg"}))
		if !strings.Contains(dep.Output, "node_modules/pkg/index.js") || strings.Contains(dep.Output, "src/app.js") {
			t.Fatalf("dep=pkg: %q", dep.Output)
		}
		vend := mustOK(t, call(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files", "dep": "lib"}))
		if !strings.Contains(vend.Output, "vendor/lib/lib.go") {
			t.Fatalf("dep=lib should fall back to vendor/: %q", vend.Output)
		}
		all := mustOK(t, call(t, g, env, map[string]any{"pattern": "needle", "output_mode": "files", "dep": "true"}))
		if !strings.Contains(all.Output, "src/app.js") || !strings.Contains(all.Output, "node_modules/pkg/index.js") {
			t.Fatalf("dep=true: %q", all.Output)
		}
		r := call(t, g, env, map[string]any{"pattern": "needle", "dep": "missing"})
		mustErr(t, r, "No dependency named \"missing\"")
	}
}

func TestGrepNamesEnclosingSymbolWhenMapKnowsFile(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "e.go", "package p\n\nfunc Outer() {\n\tneedle()\n}\n\nfunc Other() {}\n")
	buildMap(t, env, root)
	got := mustOK(t, call(t, newGrep(true), env, map[string]any{"pattern": "needle"}))
	if !strings.Contains(got.Output, "e.go:4:\tneedle()  [in function Outer]") {
		t.Fatalf("enclosing symbol missing: %q", got.Output)
	}
	// Without a map, the line is unchanged.
	plain := mustOK(t, call(t, newGrep(true), &tools.Env{Workdir: root}, map[string]any{"pattern": "needle"}))
	if strings.Contains(plain.Output, "[in ") {
		t.Fatalf("no map, no annotation: %q", plain.Output)
	}
}

func TestGlobAndListDirShowMapSymbolCounts(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "m.go", "package m\n\nfunc A() {}\n\nfunc B() {}\n")
	write(t, root, "plain.txt", "no code\n")
	buildMap(t, env, root)
	g := mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "*.*"}))
	if !strings.Contains(g.Output, "m.go  · 2 symbols") || !strings.Contains(g.Output, "plain.txt\n") && !strings.HasSuffix(g.Output, "plain.txt") {
		t.Fatalf("glob annotation: %q", g.Output)
	}
	l := mustOK(t, call(t, NewListDir(), env, map[string]any{}))
	if !strings.Contains(l.Output, "m.go") || !strings.Contains(l.Output, "· 2 symbols") {
		t.Fatalf("list_dir annotation: %q", l.Output)
	}
	if strings.Contains(l.Output, "plain.txt  ") && strings.Contains(strings.Split(l.Output, "plain.txt")[1], "symbols") {
		t.Fatalf("file without symbols should carry no count: %q", l.Output)
	}
}
