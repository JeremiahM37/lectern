package repomap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

var fixture = map[string]string{
	"go.mod": "module example.com/fix\n\ngo 1.25\n",
	"internal/util/util.go": `package util

import "strings"

// Clean is documented here and must not count as a reference.
func Clean(s string) string {
	return strings.TrimSpace(s) // a brace in a comment } is ignored
}

// Brace returns a string containing braces.
func Brace() string {
	return "}{ not a block end"
}

type Set struct {
	items map[string]bool
}

const Version = "1.0"
`,
	"internal/api/server.go": `package api

import (
	"context"

	"example.com/fix/internal/util"
)

type Server struct {
	name string
}

func NewServer(name string) *Server {
	return &Server{name: util.Clean(name)}
}

func (s *Server) Start(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	_ = util.Brace()
	return nil
}
`,
	"internal/worker/worker.go": `package worker

import "example.com/fix/internal/util"

func Run(input string) string {
	return util.Clean(input)
}
`,
	"internal/worker/extra.go": `package worker

import "example.com/fix/internal/util"

func Extra() string {
	return util.Clean("x") + util.Brace()
}
`,
	"cmd/app/main.go": `package main

import (
	"fmt"

	"example.com/fix/internal/util"
)

func main() {
	fmt.Println(util.Clean(" hi "))
}
`,
	"internal/dup/a.go": `package dup

func A(x int) int {
	y := x * 2
	y++
	return y
}
`,
	"internal/dup/b.go": `package dup

func B(x int) int {
	y := x * 2
	y++
	return y
}
`,
	"web/src/lib/format.ts": `export function format(v: string): string {
  return v.trim();
}
`,
	"web/src/app.ts": `import { format } from "./lib/format";

export class Widget {
  render(): string {
    const open = ` + "`}`" + `;
    return open + format("x");
  }
}

export const helper = (x: number) => x * 2;
`,
	"app/core/helpers.py": `def hash_it(s):
    return str(len(s))
`,
	"app/service.py": `from app.core.helpers import hash_it


class Service:
    @property
    def name(self):
        return "}"

    def run(self, x):
        y = hash_it(x)
        return y


def top():
    return 1
`,
	"src/lib.rs": `mod engine;

pub use crate::engine::Engine;
`,
	"src/engine.rs": `pub struct Engine {
    n: u32,
}

impl Engine {
    pub fn run(&self) -> u32 {
        let open = '{';
        self.n + helper(open as u32)
    }
}

pub trait Runner {
    fn go(&self);
}

fn helper(x: u32) -> u32 {
    x
}
`,
	".gitignore":         "ignored/\n",
	"ignored/ignored.go": "package ignored\n",
}

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root,
		"-c", "user.name=test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newFixtureRepo writes the fixture into a temp dir, makes it a git repo with
// some co-change history, and returns the root.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	writeFixture(t, root, fixture)
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "initial")
	for i := 0; i < 2; i++ {
		p := filepath.Join(root, "internal/api/server.go")
		b, _ := os.ReadFile(p)
		os.WriteFile(p, append(b, []byte("// change\n")...), 0o644)
		p = filepath.Join(root, "internal/util/util.go")
		b, _ = os.ReadFile(p)
		os.WriteFile(p, append(b, []byte("// change\n")...), 0o644)
		gitRun(t, root, "commit", "-q", "-am", "touch api and util")
	}
	return root
}

func buildMap(t *testing.T, root string, opts Options) *Map {
	t.Helper()
	m, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSymbolsAndLineRanges(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})

	api := fixture["internal/api/server.go"]
	syms, ok := m.FileSymbols("internal/api/server.go")
	if !ok {
		t.Fatal("api/server.go not indexed")
	}
	by := map[string]Symbol{}
	for _, s := range syms {
		by[s.Name] = s
	}
	start := by["Start"]
	if start.Kind != "method" || start.Parent != "Server" || !start.Exported {
		t.Errorf("Start = %+v", start)
	}
	if start.Line != findLine(t, api, "func (s *Server) Start") || start.EndLine != endLine(t, api, start.Line, "}") {
		t.Errorf("Start lines = %d-%d", start.Line, start.EndLine)
	}
	if by["Server"].Kind != "struct" || by["NewServer"].Kind != "function" {
		t.Errorf("types/functions wrong: %+v %+v", by["Server"], by["NewServer"])
	}

	util := fixture["internal/util/util.go"]
	us, _ := m.FileSymbols("internal/util/util.go")
	ub := map[string]Symbol{}
	for _, s := range us {
		ub[s.Name] = s
	}
	brace := ub["Brace"]
	if brace.Line != findLine(t, util, "func Brace()") || brace.EndLine != endLine(t, util, brace.Line, "}") {
		t.Errorf("Brace must end at its real closing brace, not the '}' in its string: %d-%d", brace.Line, brace.EndLine)
	}
	if ub["Version"].Kind != "const" || ub["Set"].Kind != "struct" {
		t.Errorf("const/struct kinds wrong: %+v %+v", ub["Version"], ub["Set"])
	}

	ts := fixture["web/src/app.ts"]
	ws, _ := m.FileSymbols("web/src/app.ts")
	wb := map[string]Symbol{}
	for _, s := range ws {
		wb[s.Name] = s
	}
	widget := wb["Widget"]
	if widget.Kind != "class" || widget.EndLine != endLine(t, ts, widget.Line, "}") {
		t.Errorf("Widget = %+v (template literal '}' must not close the class)", widget)
	}
	if wb["render"].Parent != "Widget" || wb["render"].Kind != "method" {
		t.Errorf("render = %+v", wb["render"])
	}
	if wb["helper"].Kind != "function" || !wb["helper"].Exported {
		t.Errorf("arrow const not recognized: %+v", wb["helper"])
	}

	py := fixture["app/service.py"]
	ps, _ := m.FileSymbols("app/service.py")
	pb := map[string]Symbol{}
	for _, s := range ps {
		pb[s.Name] = s
	}
	name := pb["name"]
	if name.Parent != "Service" || name.Line != findLine(t, py, "@property") || name.EndLine != findLine(t, py, `return "}"`) {
		t.Errorf("decorated property lines = %d-%d parent=%q", name.Line, name.EndLine, name.Parent)
	}
	if pb["run"].EndLine != findLine(t, py, "return y") {
		t.Errorf("run ends at %d", pb["run"].EndLine)
	}
	if pb["top"].Kind != "function" || pb["top"].EndLine != findLine(t, py, "return 1") {
		t.Errorf("top = %+v", pb["top"])
	}

	rs, _ := m.FileSymbols("src/engine.rs")
	rb := map[string]Symbol{}
	for _, s := range rs {
		rb[s.Name] = s
	}
	run := rb["run"]
	if run.Parent != "Engine" || run.Kind != "method" || run.EndLine != endLine(t, fixture["src/engine.rs"], run.Line, "    }") {
		t.Errorf("run = %+v (char literal '{' must not unbalance braces)", run)
	}
	if rb["Runner"].Kind != "trait" || rb["go"].Parent != "Runner" || rb["go"].EndLine != rb["go"].Line {
		t.Errorf("trait wrong: %+v / %+v", rb["Runner"], rb["go"])
	}
	if _, ok := rb["Engine"]; !ok || rb["Engine"].Kind != "struct" {
		t.Errorf("struct Engine missing or wrong kind")
	}
}

func TestImportResolution(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	m.mu.RLock()
	defer m.mu.RUnlock()
	cases := map[string]string{
		"internal/util/util.go": "internal/api/server.go", // go module path -> package dir
		"web/src/lib/format.ts": "web/src/app.ts",         // relative TS import with extension inference
		"app/core/helpers.py":   "app/service.py",         // dotted python module
		"src/engine.rs":         "src/lib.rs",             // mod engine; and crate:: use
	}
	for target, importer := range cases {
		found := false
		for _, p := range m.in[target] {
			if p == importer {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should be imported by %s; importers = %v", target, importer, m.in[target])
		}
	}
	if len(m.in["internal/util/util.go"]) != 4 {
		t.Errorf("util importers = %v, want 4", m.in["internal/util/util.go"])
	}
}

func TestPageRankRanksHeavilyImportedFirst(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	m.mu.RLock()
	util := m.fileRank("internal/util/util.go")
	for _, p := range m.paths {
		if p != "internal/util/util.go" && m.fileRank(p) >= util {
			t.Errorf("%s (%.4f) should rank below util (%.4f)", p, m.fileRank(p), util)
		}
	}
	total := 0.0
	for _, r := range m.rank {
		total += r
	}
	m.mu.RUnlock()
	if total < 0.999 || total > 1.001 {
		t.Errorf("ranks sum to %f, want 1", total)
	}
	out := m.Render(2500, nil)
	if !strings.Contains(out, "internal/util/util.go (→4)") {
		t.Errorf("render should lead with util; got:\n%s", out)
	}
	if !strings.HasPrefix(strings.SplitN(out, "\n", 2)[1], "internal/util/util.go") {
		t.Errorf("util is not the first block:\n%s", out)
	}
}

func TestRenderRespectsBudget(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	for _, budget := range []int{60, 150, 400, 2500} {
		out := m.Render(budget, nil)
		if out == "" {
			t.Fatalf("budget %d rendered nothing", budget)
		}
		if est := estTokens(out); est > budget {
			t.Errorf("budget %d: output estimates %d tokens", budget, est)
		}
	}
	full := m.Render(2500, nil)
	if !strings.Contains(full, "  +func (s *Server) Start(ctx context.Context) error :") {
		t.Errorf("signature line missing:\n%s", full)
	}
	focused := m.Render(2500, []string{"web/src/app.ts"})
	if !strings.Contains(focused, "web/src/app.ts") {
		t.Errorf("focus file dropped from the map")
	}
}

func TestIncrementalUpdate(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	p := filepath.Join(root, "internal/worker/worker.go")
	src := fixture["internal/worker/worker.go"] + "\nfunc Fresh() int { return 7 }\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update([]string{p}); err != nil {
		t.Fatal(err)
	}
	if m.LastParsed() != 1 {
		t.Errorf("Update parsed %d files, want 1", m.LastParsed())
	}
	if d := m.Definition("Fresh"); len(d) != 1 || d[0].File != "internal/worker/worker.go" {
		t.Errorf("new symbol not visible after Update: %+v", d)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := m.Update([]string{"internal/worker/worker.go"}); err != nil {
		t.Fatal(err)
	}
	if d := m.Definition("Fresh"); len(d) != 0 {
		t.Errorf("deleted file still defines Fresh: %+v", d)
	}
}

func TestCacheReuseAcrossBuilds(t *testing.T) {
	root := newFixtureRepo(t)
	first := buildMap(t, root, Options{})
	if first.LastParsed() == 0 {
		t.Fatal("first build parsed nothing")
	}
	if _, err := os.Stat(filepath.Join(root, ".scribe", "cache", "map.db")); err != nil {
		t.Fatalf("cache db not written: %v", err)
	}
	first.Close()

	second := buildMap(t, root, Options{})
	if n := second.LastParsed(); n != 0 {
		t.Errorf("second build parsed %d files, want 0", n)
	}
	if got := second.Stats().Symbols; got != first.Stats().Symbols {
		t.Errorf("cached symbols %d != fresh %d", got, first.Stats().Symbols)
	}

	// Same content, new mtime: the hash matches, so nothing is reparsed.
	p := filepath.Join(root, "internal/worker/extra.go")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	third := buildMap(t, root, Options{})
	if n := third.LastParsed(); n != 0 {
		t.Errorf("touch-only change parsed %d files, want 0", n)
	}
}

func TestSearchSymbolsRanking(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	hits := m.SearchSymbols("clean", 5)
	if len(hits) == 0 || hits[0].Name != "Clean" {
		t.Fatalf("exact (case-insensitive) match should lead: %+v", hits)
	}
	// "serv": Server and Service are prefix matches; NewServer is only a
	// substring match, so it must come after both.
	hits = m.SearchSymbols("serv", 10)
	pos := map[string]int{}
	for i, h := range hits {
		pos[h.Name] = i
	}
	if _, ok := pos["NewServer"]; !ok {
		t.Fatalf("substring match missing: %+v", hits)
	}
	if !(pos["Server"] < pos["NewServer"] && pos["Service"] < pos["NewServer"]) {
		t.Fatalf("prefix matches should beat substring NewServer: %+v", hits)
	}
	hits = m.SearchSymbols("srv", 10)
	found := false
	for _, h := range hits {
		if h.Name == "Server" {
			found = true
		}
	}
	if !found {
		t.Errorf("subsequence 'srv' should match Server: %+v", hits)
	}
	if got := m.FindFile("worker", 5); len(got) == 0 || !strings.HasPrefix(got[0].Path, "internal/worker/") {
		t.Errorf("FindFile(worker) = %+v", got)
	}
}

func TestCoChangeFromGitHistory(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	pairs := m.CoChange("internal/api/server.go")
	found := false
	for _, p := range pairs {
		if p.File == "internal/util/util.go" {
			found = true
			if p.Count < 2 {
				t.Errorf("co-change count = %d", p.Count)
			}
		}
	}
	if !found {
		t.Errorf("util.go should co-change with server.go: %+v", pairs)
	}
	if got := m.CoChange("web/src/app.ts"); len(got) != 0 {
		t.Errorf("unrelated file has co-changes: %+v", got)
	}
}

func TestReferencesSkipStringsAndComments(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	refs, err := m.References("Clean")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, r := range refs {
		files[r.File] = true
		if strings.Contains(r.Text, "must not count") {
			t.Errorf("comment counted as reference: %+v", r)
		}
	}
	for _, want := range []string{"internal/api/server.go", "internal/worker/worker.go", "cmd/app/main.go"} {
		if !files[want] {
			t.Errorf("no reference to Clean in %s: %+v", want, refs)
		}
	}
}

func TestImpactAndDuplicates(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	im, err := m.Impact("internal/util/util.go")
	if err != nil {
		t.Fatal(err)
	}
	if im.Direct != 4 || !strings.HasPrefix(im.Risk, "medium") {
		t.Errorf("impact = direct %d risk %q", im.Direct, im.Risk)
	}
	if len(im.CoChanged) == 0 || im.CoChanged[0].File != "internal/api/server.go" {
		t.Errorf("impact co-changes = %+v", im.CoChanged)
	}
	if _, err := m.Impact("nope.go"); err == nil {
		t.Error("impact of an unknown file should fail")
	}

	dups := m.Duplicates()
	found := false
	for _, g := range dups {
		names := map[string]bool{}
		for _, s := range g.Symbols {
			names[s.Name] = true
		}
		if names["A"] && names["B"] {
			found = true
		}
	}
	if !found {
		t.Errorf("A and B have identical bodies but were not grouped: %+v", dups)
	}
}

func TestWalkHonoursGitignoreWithoutGit(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		".gitignore":            "build-out/\n*.tmp.go\n!keep.tmp.go\n",
		"a.go":                  "package a\n",
		"build-out/b.go":        "package b\n",
		"x.tmp.go":              "package x\n",
		"keep.tmp.go":           "package keep\n",
		"node_modules/pkg/c.js": "module.exports = 1\n",
		"sub/d.go":              "package sub\n",
	})
	got := map[string]bool{}
	walked, _ := walkFiles(root)
	for _, f := range walked {
		got[f] = true
	}
	for _, want := range []string{"a.go", "sub/d.go", "keep.tmp.go"} {
		if !got[want] {
			t.Errorf("%s should be listed: %v", want, got)
		}
	}
	for _, bad := range []string{"build-out/b.go", "x.tmp.go", "node_modules/pkg/c.js"} {
		if got[bad] {
			t.Errorf("%s should be ignored", bad)
		}
	}
}

func TestToolsAreReadOnlyAndWork(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	reg := tools.NewRegistry()
	Register(reg, m)
	env := &tools.Env{Workdir: root}
	call := func(name, args string) tools.Result {
		t.Helper()
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		if !tl.ReadOnly() {
			t.Errorf("%s must be read-only", name)
		}
		return tl.Run(context.Background(), env, []byte(args))
	}
	cases := []struct{ name, args, want string }{
		{"code_map", `{"query":"clean","budget":800}`, "internal/util/util.go"},
		{"find_symbol", `{"query":"Start"}`, "internal/api/server.go:"},
		{"symbol_refs", `{"name":"Clean"}`, "internal/worker/worker.go:"},
		{"code_impact", `{"file":"internal/util/util.go"}`, "Direct importers (4)"},
		{"file_outline", `{"path":"internal/api/server.go"}`, "    L"},
	}
	for _, c := range cases {
		res := call(c.name, c.args)
		if res.IsError {
			t.Errorf("%s returned error: %s", c.name, res.Output)
		}
		if !strings.Contains(res.Output, c.want) {
			t.Errorf("%s output missing %q:\n%s", c.name, c.want, res.Output)
		}
	}
	if res := call("file_outline", `{"path":"nope.go"}`); !res.IsError {
		t.Errorf("unknown path should be an error result")
	}
	if res := call("find_symbol", `{}`); !res.IsError {
		t.Errorf("empty query should be an error result")
	}
}

// TestBuildThisRepository measures a full build of a real Go repository
// (Lectern itself). Skipped with -short or when the checkout is absent.
func TestBuildThisRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	const repo = "/mnt/bulk/lectern-forge"
	if _, err := os.Stat(repo); err != nil {
		t.Skip("repository not present")
	}
	m, err := Open(repo, Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	start := time.Now()
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	st := m.Stats()
	t.Logf("built %d files, %d symbols, %d edges in %s (%d Go files)", st.Files, st.Symbols, st.Edges, elapsed, st.Languages["go"])
	if st.Languages["go"] < 100 || st.Symbols == 0 {
		t.Errorf("implausible stats: %+v", st)
	}
	if elapsed > 30*time.Second {
		t.Errorf("build took %s; target is under 10s", elapsed)
	}
}
