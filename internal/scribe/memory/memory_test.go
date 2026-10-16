package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// newStores opens real project and global databases in temp dirs.
func newStores(t *testing.T) (*Stores, string) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

func mustAdd(t *testing.T, d *DB, r Record) Record {
	t.Helper()
	got, _, err := d.Add(r)
	if err != nil {
		t.Fatalf("Add(%q): %v", r.Title, err)
	}
	return got
}

func TestAddDedupesByTitleAndBody(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project

	first := mustAdd(t, d, Record{Category: CatDecision, Title: "Use SQLite for state", Body: "We store all scribe state in SQLite with WAL mode so sessions can share it."})
	if first.ID == "" || first.Scope != ScopeProject {
		t.Fatalf("first record not stored properly: %+v", first)
	}

	// Same normalized title, different body: merged into the first.
	again, dup, err := d.Add(Record{Category: CatDecision, Title: "  use   sqlite for STATE ", Body: "Revised detail about the WAL choice."})
	if err != nil {
		t.Fatal(err)
	}
	if !dup || again.ID != first.ID {
		t.Fatalf("same title should dedupe to %s, got dup=%v id=%s", first.ID, dup, again.ID)
	}

	// Different title, body nearly identical (token Jaccard >= 0.8): merged.
	near := "Revised detail about the WAL choice today."
	_, dup, err = d.Add(Record{Category: CatFact, Title: "Storage note", Body: near})
	if err != nil {
		t.Fatal(err)
	}
	if !dup {
		t.Fatalf("near-identical body should dedupe")
	}

	// Unrelated record is a new row.
	_, dup, err = d.Add(Record{Category: CatBug, Title: "Flaky ticker", Body: "The status ticker drops frames when the terminal resizes quickly."})
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatalf("unrelated record must not dedupe")
	}

	list, err := d.List(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 records after dedupe, got %d", len(list))
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	s, _ := newStores(t)
	if _, _, err := s.Project.Add(Record{Title: "", Body: "x"}); err == nil {
		t.Fatal("empty title should be rejected")
	}
	if _, _, err := s.Project.Add(Record{Title: "t", Body: " "}); err == nil {
		t.Fatal("blank body should be rejected")
	}
	if _, _, err := s.Project.Add(Record{Title: "t", Body: "b", Category: "gossip"}); err == nil {
		t.Fatal("unknown category should be rejected")
	}
	rec := mustAdd(t, s.Project, Record{Title: "t", Body: "b"})
	if rec.Category != CatFact {
		t.Fatalf("empty category should default to fact, got %q", rec.Category)
	}
}

func TestRecallFileOverlapSurfacesMemory(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	target := mustAdd(t, d, Record{
		Category: CatConvention,
		Title:    "Retry policy lives in client",
		Body:     "Retries use exponential backoff with jitter. Do not add a second retry layer.",
		Files:    []string{"internal/net/client.go"},
	})
	mustAdd(t, d, Record{Category: CatFact, Title: "Logo colour", Body: "The brand accent is teal."})

	// No text in the query: only the file link can find it.
	got, err := d.Recall("", []string{"internal/net/client.go"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != target.ID {
		t.Fatalf("file-passed recall = %+v, want only %s", got, target.ID)
	}
	if !contains(got[0].Why, "file") {
		t.Fatalf("expected file signal, got %v", got[0].Why)
	}

	// A bare basename also matches a stored relative path.
	got, err = d.Recall("", []string{"client.go"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != target.ID {
		t.Fatalf("basename recall = %+v", got)
	}

	// The unrelated file recalls nothing.
	got, err = d.Recall("", []string{"docs/readme.md"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unrelated file should recall nothing, got %+v", got)
	}
}

func TestRecallRanksTextMatchFirst(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	mustAdd(t, d, Record{Category: CatBug, Title: "Terminal resize glitch", Body: "The pane redraws twice on resize."})
	migr := mustAdd(t, d, Record{Category: CatDecision, Title: "SQLite migration order", Body: "Run schema migrations in a transaction before opening WAL readers."})
	mustAdd(t, d, Record{Category: CatTodo, Title: "Write docs", Body: "Document the onboarding flow."})

	got, err := d.Recall("how do sqlite migrations run", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != migr.ID {
		t.Fatalf("sqlite migration memory should rank first, got %+v", got)
	}
	if !contains(got[0].Why, "text") {
		t.Fatalf("expected text signal, got %v", got[0].Why)
	}
}

func TestEmbeddingIsNormalisedAndSeparatesTopics(t *testing.T) {
	v := embed("retry the failed upload with backoff")
	if n := dot(v, v); n < 0.999 || n > 1.001 {
		t.Fatalf("embedding not unit length: %v", n)
	}
	if dot(embed("   "), embed("   ")) != 0 {
		t.Fatal("empty text should embed to the zero vector")
	}
	near := dot(embed("upload retry with backoff"), v)
	far := dot(embed("colour palette for the logo"), v)
	if near <= far {
		t.Fatalf("paraphrase cosine %.3f should beat unrelated %.3f", near, far)
	}
}

func TestSearchFTS(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	hit := mustAdd(t, d, Record{Category: CatFact, Title: "Gateway port", Body: "The homelab gateway listens on 9105.", Tags: []string{"network"}})
	mustAdd(t, d, Record{Category: CatFact, Title: "Unrelated", Body: "Nothing to see."})

	got, err := d.Search("gateway", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != hit.ID {
		t.Fatalf("Search(gateway) = %+v", got)
	}
	// Prefix matching: "gate" finds "gateway".
	got, err = d.Search("gate", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("prefix search failed: %v %+v", err, got)
	}
	// Tags are indexed too.
	got, err = d.Search("network", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("tag search failed: %v %+v", err, got)
	}
	got, err = d.Search("zzqqxx", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("no-hit search = %+v, %v", got, err)
	}
}

func TestPinnedAlwaysIncluded(t *testing.T) {
	s, _ := newStores(t)
	d := s.Global
	pinned := mustAdd(t, d, Record{Category: CatPreference, Title: "Always use tabs", Body: "Indent Go with tabs, gofmt decides."})
	if err := d.Pin(pinned.ID, true); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, d, Record{Category: CatFact, Title: "Other", Body: "Unrelated fact about lighthouses."})

	got, err := d.Recall("quantum chromodynamics lattice", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != pinned.ID || !contains(got[0].Why, "pinned") {
		t.Fatalf("pinned memory should always be recalled, got %+v", got)
	}
	if err := d.Pin(pinned.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = d.Recall("quantum chromodynamics lattice", nil, 3)
	if len(got) != 0 {
		t.Fatalf("unpinned unrelated memory should not recall, got %+v", got)
	}
}

func TestPruneRemovesStaleUnpinnedOnly(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	stale := mustAdd(t, d, Record{Category: CatTodo, Title: "Old todo", Body: "Maybe look at this someday."})
	keep := mustAdd(t, d, Record{Category: CatDecision, Title: "Pinned old", Body: "Keep this forever."})
	if err := d.Pin(keep.ID, true); err != nil {
		t.Fatal(err)
	}
	// Advance the clock 90 days: both are now stale by last use.
	base := d.now
	d.now = func() time.Time { return base().Add(90 * 24 * time.Hour) }
	defer func() { d.now = base }()

	n, err := d.Prune(30*24*time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("Prune removed %d, want 1", n)
	}
	if _, err := d.Get(stale.ID); err != ErrNotFound {
		t.Fatalf("stale record should be gone, err=%v", err)
	}
	if _, err := d.Get(keep.ID); err != nil {
		t.Fatalf("pinned record must survive prune: %v", err)
	}
}

func TestInjectFormat(t *testing.T) {
	s, _ := newStores(t)
	long := strings.Repeat("abcdefghij ", 80) // > 400 chars
	mustAdd(t, s.Project, Record{Category: CatDecision, Title: "Trim the injected body", Body: long, Files: []string{"a/b.go"}})

	out := s.Inject("trim injected body", nil)
	if !strings.HasPrefix(out, "<recalled_memories>\n") || !strings.HasSuffix(out, "</recalled_memories>") {
		t.Fatalf("bad block framing:\n%s", out)
	}
	if !strings.Contains(out, "[decision] Trim the injected body (id ") {
		t.Fatalf("missing header line:\n%s", out)
	}
	bodyLine := strings.Split(out, "\n")[2]
	if n := len([]rune(bodyLine)); n > injectBodyChars || !strings.HasSuffix(bodyLine, "…") {
		t.Fatalf("body not trimmed to %d runes with ellipsis; got %d runes", injectBodyChars, n)
	}

	// Inject bumps usage.
	got, _ := s.Project.List(ListOptions{})
	if got[0].Uses != 1 {
		t.Fatalf("Inject should count one use, got %d", got[0].Uses)
	}

	if out := s.Inject("lighthouse colour palette", nil); out != "" {
		t.Fatalf("nothing relevant should inject empty, got %q", out)
	}
}

func TestStoresScopesAndDedupeAcrossCalls(t *testing.T) {
	s, _ := newStores(t)
	mustAdd(t, s.Global, Record{Category: CatConvention, Title: "Commit style", Body: "Imperative mood, under 72 columns."})
	mustAdd(t, s.Project, Record{Category: CatConvention, Title: "Project tests", Body: "Run go test -race before pushing.", Files: []string{"go.mod"}})

	got, err := s.Recall("commit style", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Scope != ScopeGlobal {
		t.Fatalf("global commit memory expected, got %+v", got)
	}
	got, _ = s.Recall("", []string{"go.mod"}, 5)
	if len(got) != 1 || got[0].Scope != ScopeProject {
		t.Fatalf("project file memory expected, got %+v", got)
	}
	if _, err := s.DB("nope"); err == nil {
		t.Fatal("unknown scope should error")
	}
}

func TestGetPrefixUpdateDelete(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	r := mustAdd(t, d, Record{Category: CatFact, Title: "Editable", Body: "first body", Tags: []string{"a"}})

	got, err := d.Get(r.ID[:8])
	if err != nil || got.ID != r.ID {
		t.Fatalf("prefix Get failed: %v", err)
	}
	r.Body = "second body"
	r.Files = []string{"x.go"}
	if err := d.Update(r); err != nil {
		t.Fatal(err)
	}
	got, _ = d.Get(r.ID)
	if got.Body != "second body" || len(got.Files) != 1 {
		t.Fatalf("Update not applied: %+v", got)
	}
	if hits, _ := d.Search("second", 3); len(hits) != 1 {
		t.Fatal("FTS index not updated after Update")
	}
	if err := d.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := d.Search("second", 3); len(hits) != 0 {
		t.Fatal("FTS row not removed on Delete")
	}
	if err := d.Delete(r.ID); err != ErrNotFound {
		t.Fatalf("second delete should be ErrNotFound, got %v", err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	s, _ := newStores(t)
	mustAdd(t, s.Project, Record{Category: CatDecision, Title: "Round trip", Body: "Exported then imported.", Files: []string{"f.go"}, Tags: []string{"io"}})
	var buf bytes.Buffer
	if err := s.Project.Export(&buf); err != nil {
		t.Fatal(err)
	}
	var recs []Record
	if err := json.Unmarshal(buf.Bytes(), &recs); err != nil || len(recs) != 1 {
		t.Fatalf("export not a 1-element array: %v %s", err, buf.String())
	}

	s2, _ := newStores(t)
	added, merged, err := s2.Project.Import(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || merged != 0 {
		t.Fatalf("import added=%d merged=%d", added, merged)
	}
	got, _ := s2.Project.List(ListOptions{})
	if len(got) != 1 || got[0].Title != "Round trip" || len(got[0].Files) != 1 || got[0].Tags[0] != "io" {
		t.Fatalf("imported record wrong: %+v", got)
	}
}

// ---- tool ----

func runTool(t *testing.T, r *tools.Registry, args string) tools.Result {
	t.Helper()
	tool, ok := r.Get("memory")
	if !ok {
		t.Fatal("memory tool not registered")
	}
	return tool.Run(context.Background(), &tools.Env{}, json.RawMessage(args))
}

func TestMemoryToolActions(t *testing.T) {
	s, _ := newStores(t)
	reg := tools.NewRegistry()
	Register(reg, s)
	tool, _ := reg.Get("memory")
	if !tool.ReadOnly() {
		t.Fatal("memory tool must report ReadOnly()=true")
	}

	res := runTool(t, reg, `{"action":"save","category":"bug","title":"Resize flicker","body":"Redraw twice on resize.","files":["ui/term.go"],"tags":["tui"]}`)
	if res.IsError || !strings.Contains(res.Output, "Saved memory") {
		t.Fatalf("save failed: %+v", res)
	}
	id := strings.Fields(res.Output)[2]

	// Saving the same title again merges.
	res = runTool(t, reg, `{"action":"save","title":"resize FLICKER","body":"Different wording about the same problem."}`)
	if res.IsError || !strings.Contains(res.Output, "Updated existing memory") {
		t.Fatalf("dedupe save: %+v", res)
	}

	res = runTool(t, reg, `{"action":"search","query":"resize redraw"}`)
	if res.IsError || !strings.Contains(strings.ToLower(res.Output), "resize flicker") {
		t.Fatalf("search: %+v", res)
	}
	res = runTool(t, reg, `{"action":"search","query":"lighthouse"}`)
	if res.IsError || !strings.Contains(res.Output, "No memories match") {
		t.Fatalf("empty search: %+v", res)
	}

	res = runTool(t, reg, `{"action":"list","scope":"project","category":"bug"}`)
	if res.IsError || !strings.Contains(strings.ToLower(res.Output), "resize flicker") {
		t.Fatalf("list: %+v", res)
	}

	res = runTool(t, reg, `{"action":"pin","id":"`+id+`"}`)
	if res.IsError || !strings.Contains(res.Output, "Pinned") {
		t.Fatalf("pin: %+v", res)
	}
	res = runTool(t, reg, `{"action":"list","pinned":true}`)
	if !strings.Contains(res.Output, "[pinned]") {
		t.Fatalf("pinned list: %+v", res)
	}
	res = runTool(t, reg, `{"action":"pin","id":"`+id+`","pinned":false}`)
	if res.IsError || !strings.Contains(res.Output, "Unpinned") {
		t.Fatalf("unpin: %+v", res)
	}

	res = runTool(t, reg, `{"action":"delete","id":"`+id+`"}`)
	if res.IsError || !strings.Contains(res.Output, "Deleted memory") {
		t.Fatalf("delete: %+v", res)
	}
	res = runTool(t, reg, `{"action":"delete","id":"`+id+`"}`)
	if !res.IsError || !strings.Contains(res.Output, "already deleted") {
		t.Fatalf("second delete should error with a hint: %+v", res)
	}
	// delete is soft: restore brings the record back into recall and list.
	if res := runTool(t, reg, `{"action":"restore","id":"`+id+`"}`); res.IsError || !strings.Contains(res.Output, "Restored") {
		t.Fatalf("restore: %+v", res)
	}
	if res := runTool(t, reg, `{"action":"forget","id":"`+id+`"}`); res.IsError || !strings.Contains(res.Output, "Deleted memory") {
		t.Fatalf("near-miss action forget should delete: %+v", res)
	}
	if res := runTool(t, reg, `{"action":"restore","id":"`+id+`"}`); res.IsError {
		t.Fatalf("restore after soft delete: %+v", res)
	}

	// Error paths tell the model what to do next.
	if res := runTool(t, reg, `{"action":"save","title":"","body":"x"}`); !res.IsError {
		t.Fatal("save without title should error")
	}
	if res := runTool(t, reg, `{"action":"search"}`); !res.IsError {
		t.Fatal("search without query should error")
	}
	if res := runTool(t, reg, `{"action":"explode"}`); !res.IsError || !strings.Contains(res.Output, "unknown action") {
		t.Fatalf("unknown action: %+v", res)
	}
	if res := runTool(t, reg, `not json`); !res.IsError {
		t.Fatal("bad json should error")
	}
}

func TestMemoryToolWithoutProjectStore(t *testing.T) {
	s, _ := newStores(t)
	s.Project = nil
	reg := tools.NewRegistry()
	Register(reg, s)
	// Save falls back to global when there is no project.
	res := runTool(t, reg, `{"action":"save","title":"Global only","body":"Saved with no project."}`)
	if res.IsError || !strings.Contains(res.Output, "(global") {
		t.Fatalf("save without project should use global: %+v", res)
	}
	if res := runTool(t, reg, `{"action":"save","scope":"project","title":"x","body":"y"}`); !res.IsError {
		t.Fatal("explicit project scope with no project store should error")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
