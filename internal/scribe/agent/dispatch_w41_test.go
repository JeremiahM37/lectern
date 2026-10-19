package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeCodeMap is a code map with fixed symbols and importers.
type fakeCodeMap struct {
	syms      map[string][]repomap.Symbol
	importers map[string][]string
}

func (m fakeCodeMap) FileSymbols(file string) ([]repomap.Symbol, bool) {
	s, ok := m.syms[file]
	return s, ok
}

func (m fakeCodeMap) Impact(file string) (repomap.Impact, error) {
	if _, ok := m.syms[file]; !ok {
		return repomap.Impact{}, os.ErrNotExist
	}
	im := repomap.Impact{File: file}
	for _, p := range m.importers[file] {
		im.Dependents = append(im.Dependents, repomap.Dependent{Path: p, Level: 1})
	}
	return im, nil
}

func TestCheckTargetsKeepsFilesTheMapKnows(t *testing.T) {
	dir := t.TempDir() // no files on disk at all
	m := fakeCodeMap{syms: map[string][]repomap.Symbol{"pkg/a.go": {{Name: "A", Kind: "function", Line: 1}}}}
	tasks := []subTask{{Role: "code", Files: []string{"pkg/a.go:1-5", "pkg/ghost.go"}}}
	notes := checkTargetsIn(tasks, dir, m)
	if len(tasks[0].Files) != 1 || tasks[0].Files[0] != "pkg/a.go:1-5" {
		t.Fatalf("kept %v, want only the mapped file with its range", tasks[0].Files)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "pkg/ghost.go") {
		t.Fatalf("notes = %v, want one drop note for pkg/ghost.go", notes)
	}
	// Without a map the disk alone decides, so the mapped file is dropped.
	plain := []subTask{{Role: "code", Files: []string{"pkg/a.go"}}}
	checkTargets(plain, dir)
	if len(plain[0].Files) != 0 {
		t.Fatalf("with no map and no file on disk, kept %v", plain[0].Files)
	}
}

func TestDependentNotesListImportersOutsideTheDispatch(t *testing.T) {
	m := fakeCodeMap{
		syms: map[string][]repomap.Symbol{"core.go": {{Name: "Core", Kind: "function", Line: 3}}},
		importers: map[string][]string{"core.go": {
			"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go",
		}},
	}
	tasks := []subTask{
		{Role: "code", Files: []string{"core.go"}},
		{Role: "code", Files: []string{"a.go"}},
	}
	notes := dependentNotes(tasks, m)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want one", notes)
	}
	// a.go is a target of this dispatch, so it is not listed; five others are.
	if strings.Contains(notes[0], "a.go") || !strings.Contains(notes[0], "b.go, c.go, d.go, e.go, f.go") || strings.Contains(notes[0], "g.go") {
		t.Fatalf("note = %q, want b.go to f.go and no a.go or g.go", notes[0])
	}
	if got := dependentNotes(tasks, nil); got != nil {
		t.Fatalf("no map gave %v", got)
	}
}

func TestTargetSymbolLinesAppendRangesAndCapCount(t *testing.T) {
	var syms []repomap.Symbol
	for i := 1; i <= 14; i++ {
		syms = append(syms, repomap.Symbol{Name: "F", Kind: "function", Line: i * 10, EndLine: i*10 + 4})
	}
	m := fakeCodeMap{syms: map[string][]repomap.Symbol{"big.go": syms}}
	block := targetSymbolLines([]string{"big.go:1-9"}, m)
	if !strings.HasPrefix(block, "Target files:\n- big.go: function F lines 10-14; ") {
		t.Fatalf("block = %q", block)
	}
	if !strings.HasSuffix(block, "; 2 more") {
		t.Fatalf("block does not cap the list: %q", block)
	}
	if targetSymbolLines([]string{"none.go"}, m) != "" {
		t.Fatal("a file the map does not know gets no block")
	}
}

func TestFilesEditedSectionFlagsSharedFiles(t *testing.T) {
	results := []taskResult{
		{edited: []string{"a.go", "b.go"}},
		{edited: []string{"b.go"}},
	}
	out := filesEditedSection(results)
	if !strings.Contains(out, "## Files Edited\n- a.go (edited by agents 1)\n") {
		t.Fatalf("a.go line missing:\n%s", out)
	}
	if !strings.Contains(out, "- b.go (edited by agents 1, 2) WARNING: more than one agent edited this file") {
		t.Fatalf("shared file not flagged:\n%s", out)
	}
	if filesEditedSection(nil) != "" {
		t.Fatal("no edits gives a section")
	}
}

func TestRelTargetAndDigestAreWorkdirRelative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := relTarget(dir, filepath.Join(dir, "x.txt")); got != "x.txt" {
		t.Fatalf("relTarget = %q, want x.txt", got)
	}
	if got := relTarget(dir, "./x.txt"); got != "x.txt" {
		t.Fatalf("relTarget of ./x.txt = %q", got)
	}
	if digestFile(dir, "x.txt") != digestFile(dir, filepath.Join(dir, "x.txt")) {
		t.Fatal("relative and absolute digests differ")
	}
	if digestFile(dir, "missing.txt") != "" {
		t.Fatal("a missing file must digest to the empty string")
	}
}

func TestAttemptBudgetIsRetriesPlusOne(t *testing.T) {
	env := &tools.Env{}
	if got := newDispatchRun(&Dispatcher{}, env).attemptBudget(); got != 4 {
		t.Fatalf("default budget = %d, want 4", got)
	}
	if got := newDispatchRun(&Dispatcher{MaxRetries: 2}, env).attemptBudget(); got != 3 {
		t.Fatalf("budget with 2 retries = %d, want 3", got)
	}
}

func TestRetryBackoffDoublesFromTheBase(t *testing.T) {
	r := newDispatchRun(&Dispatcher{retryWait: 2 * time.Second}, &tools.Env{})
	for n, lo := range map[int]time.Duration{2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second} {
		got := r.retryBackoff(n)
		if got < lo || got > lo+time.Second {
			t.Fatalf("backoff before run %d = %v, want %v plus up to 1s", n, got, lo)
		}
	}
}

func TestCacheRefCarriesReadsToNextBus(t *testing.T) {
	var ref cacheRef
	first := NewAgentBus()
	first.StoreRead("/w/a.go", "stamp1", "package a")
	ref.keep(first)

	second := NewAgentBus()
	ref.seed(second)
	if out, ok := second.CachedRead("/w/a.go", "stamp1"); !ok || out != "package a" {
		t.Fatalf("seeded bus read = %q, %v; want the kept read", out, ok)
	}
	if _, ok := second.CachedRead("/w/a.go", "stamp2"); ok {
		t.Fatal("a changed file (new stamp) must miss")
	}
	// A bus with no kept reads starts empty.
	var empty cacheRef
	fresh := NewAgentBus()
	empty.seed(fresh)
	if _, ok := fresh.CachedRead("/w/a.go", "stamp1"); ok {
		t.Fatal("seeding from an empty ref added reads")
	}
}

func TestDispatchNoEditNoticeForUnchangedContent(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	if err := os.WriteFile(filepath.Join(h.dir, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write := writeThenReport("notes.txt", "hello\n", "rewrote notes", nil)
	h.f.setPick(func(_ int, req recordedReq) reply { return write(req) })
	res := h.run(`{"tasks":[{"task":"CODE-SAME rewrite the notes","role":"code","files":["notes.txt"]}]}`)
	if !strings.Contains(res.Output, "marked as edited but content unchanged: notes.txt") {
		t.Fatalf("no unchanged-content note:\n%s", res.Output)
	}
}

func TestDispatchExploreRetriesProviderFailureWithinBudget(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.Contains(lastUserText(req), "stopped on a provider error") {
			return turn(sseTurn{text: []string{"found it"}})
		}
		return transientError()
	})
	res := h.run(`{"tasks":[{"task":"EXP-FLAKY find the parser","role":"explore"}]}`)
	if res.IsError || !strings.Contains(res.Output, "found it") {
		t.Fatalf("explore task was not retried:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "retried once: the previous attempt stopped on a provider error") {
		t.Fatalf("no retry note:\n%s", res.Output)
	}
}

func TestDispatchStopsAtAttemptBudget(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.d.MaxRetries = 1 // budget of 2 runs; the harness keeps its own in-run retries
	h.f.setPick(func(_ int, req recordedReq) reply { return transientError() })
	res := h.run(`{"tasks":[{"task":"BUDGET-1 look","role":"explore"}]}`)
	// Each run makes its first request and the harness's in-run retries (4 in
	// all), and the budget allows two runs.
	if n := h.f.count(); n != 8 {
		t.Fatalf("%d requests, want 8 (two runs of four requests)", n)
	}
	// Two runs of four provider attempts each (the first and three retries).
	if !strings.Contains(res.Output, "FAILED after 8 attempts") {
		t.Fatalf("output does not report the attempts:\n%s", res.Output)
	}
}

func TestDispatchArchiveLinksListSavedReport(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.d.ArchiveDir = filepath.Join(h.dir, "archive")
	long := strings.Repeat("finding line\n", 400)
	h.f.setPick(func(_ int, req recordedReq) reply {
		return turn(sseTurn{text: []string{long}})
	})
	res := h.run(`{"tasks":[{"task":"LONG-REPORT look","role":"explore"}]}`)
	if !strings.Contains(res.Output, "## Archived reports\n- agent 1: ") {
		t.Fatalf("no archived-reports section:\n%s", res.Output)
	}
	entries, err := os.ReadDir(h.d.ArchiveDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive dir = %v, %v; want one saved report", entries, err)
	}
}

// The project check runs after every dispatch that changed files; only the
// model review waits for verify_edits (a deliberate departure from spec 03
// §3.8 step 4, which gates both).
func TestDispatchReviewRunsOnlyWithVerifyEdits(t *testing.T) {
	run := func(verify bool) string {
		h := newDispatchHarness(t, plainRegistry)
		h.d.Features = &Features{VerifyEdits: verify, TargetFileValidation: true, DispatchCache: true}
		h.f.setPick(func(_ int, req recordedReq) reply {
			if strings.HasPrefix(openingOf(req), "Review the edits") {
				return turn(sseTurn{text: []string{"VERDICT: PASS"}})
			}
			return writeThenReport("notes.txt", "hello\n", "wrote notes", nil)(req)
		})
		return h.run(`{"tasks":[{"task":"VERIFY-GATE add notes","role":"code","files":["notes.txt"]}]}`).Output
	}
	if out := run(false); !strings.Contains(out, "## Verification") || strings.Contains(out, "## Review") {
		t.Fatalf("with verify_edits off the project check must run and the review must not:\n%s", out)
	}
	if out := run(true); !strings.Contains(out, "## Verification") || !strings.Contains(out, "## Review") {
		t.Fatalf("with verify_edits on both the check and the review must run:\n%s", out)
	}
}

func TestDispatchTaskGetsMatchingSkillText(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.d.Skills = fakeSkills{text: []string{"Skill release-notes:\nAlways link the PR."}}
	h.f.setPick(func(_ int, req recordedReq) reply { return turn(sseTurn{text: []string{"done"}}) })
	h.run(`{"tasks":[{"task":"SKILL-TASK update the changelog","role":"explore"}]}`)
	req := requestMentioning(t, h.f, "SKILL-TASK")
	if !strings.Contains(lastUserText(req), "Skills that match this task:") || !strings.Contains(lastUserText(req), "Always link the PR.") {
		t.Fatalf("task text carries no skill:\n%s", lastUserText(req))
	}
}

type fakeSkills struct{ text []string }

func (f fakeSkills) ForTask(string) []string { return f.text }
