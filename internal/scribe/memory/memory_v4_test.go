package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func callMem(t *testing.T, s *Stores, args map[string]any) tools.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	return (&memoryTool{stores: s}).Run(context.Background(), &tools.Env{}, raw)
}

// A database written before versioning (user_version 0, no source, no
// superseded_by, no trigram index) must upgrade in place and keep its rows.
func TestMigratesUnversionedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// The original version-1 layout, exactly as it was before migrations.
	for _, stmt := range splitStatements(schema) {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO memories (id, category, title, body, norm_title, tags, created, updated, uses, last_used, pinned)
		VALUES ('old-1', 'fact', 'Old note', 'Migrations run on startup', 'old note', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 0, '2026-01-01T00:00:00Z', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO memories_fts (id, title, body, tags) VALUES ('old-1', 'Old note', 'Migrations run on startup', '')"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := OpenDB(path, ScopeProject)
	if err != nil {
		t.Fatalf("OpenDB on old file: %v", err)
	}
	defer d.Close()
	var v int
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion() {
		t.Fatalf("user_version = %d (%v), want %d", v, err, schemaVersion())
	}
	got, err := d.Get("old-1")
	if err != nil || got.Source != SourceUser {
		t.Fatalf("old row after upgrade: %+v, %v", got, err)
	}
	// The trigram index was back-filled, so a mid-word query finds it.
	hits, err := d.Search("ration", 5)
	if err != nil || len(hits) != 1 || hits[0].ID != "old-1" {
		t.Fatalf("trigram back-fill: hits=%v err=%v", hits, err)
	}
	// A migrated database reopens without rerunning anything.
	d.Close()
	d2, err := OpenDB(path, ScopeProject)
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	d2.Close()
}

func TestOpeningNewerSchemaFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	d, err := OpenDB(path, ScopeProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := OpenDB(path, ScopeProject); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("want a newer-schema error, got %v", err)
	}
}

func TestTrigramMatchesInsideIdentifiers(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	mustAdd(t, d, Record{Title: "Handler detail", Body: "The handler calls getUserName before rendering."})
	hits, err := d.Search("username", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("substring of an identifier should match, got %d hits", len(hits))
	}
	// Words shorter than three characters cannot use the trigram index and
	// must not be sent to it.
	if q := triQuery("go db"); q != "" {
		t.Fatalf("two-letter tokens should not build a trigram query, got %q", q)
	}
}

func TestValidationCaps(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	long := strings.Repeat("x", MaxBody+1)
	if _, _, err := d.Add(Record{Title: "t", Body: long}); err == nil || !strings.Contains(err.Error(), "body is longer") {
		t.Fatalf("want body cap error, got %v", err)
	}
	nine := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if _, _, err := d.Add(Record{Title: "t", Body: "b", Tags: nine}); err == nil {
		t.Fatal("nine tags should be rejected")
	}
	if _, _, err := d.Add(Record{Title: "t", Body: "b", Tags: []string{strings.Repeat("t", MaxTagLen+1)}}); err == nil {
		t.Fatal("over-long tag should be rejected")
	}
	var files []string
	for i := 0; i <= MaxFiles; i++ {
		files = append(files, "f"+string(rune('a'+i))+".go")
	}
	if _, _, err := d.Add(Record{Title: "t", Body: "b", Files: files}); err == nil {
		t.Fatal("17 files should be rejected")
	}
	// A title of 200 multi-byte characters is within the limit.
	title := strings.Repeat("é", MaxTitle)
	if _, _, err := d.Add(Record{Title: title, Body: "b"}); err != nil {
		t.Fatalf("200-character title should pass: %v", err)
	}
}

func TestDedupeBumpsUseCount(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	first := mustAdd(t, d, Record{Title: "Same thing", Body: "The shared body text stays put."})
	again, dup, err := d.Add(Record{Title: "same   thing", Body: "The shared body text stays put."})
	if err != nil || !dup {
		t.Fatalf("expected dedupe, dup=%v err=%v", dup, err)
	}
	got, _ := d.Get(first.ID)
	if got.Uses != 1 || again.Uses != 1 {
		t.Fatalf("dedupe hit should bump use_count to 1, got stored %d", got.Uses)
	}
}

func TestSupersededRecordIsNotRecalled(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	old := mustAdd(t, d, Record{Title: "Deploy with make", Body: "Deploy by running make deploy in the repo root."})
	neu := mustAdd(t, d, Record{Title: "Deploy with script", Body: "Deploy by running scripts/deploy.sh now."})
	if err := d.Supersede(old.ID, neu.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Supersede(neu.ID, neu.ID); err == nil {
		t.Fatal("a record must not supersede itself")
	}
	if hits, _ := d.Search("deploy", 10); len(hits) != 1 || hits[0].ID != neu.ID {
		t.Fatalf("search should return only the replacement, got %+v", hits)
	}
	if recs, _ := d.List(ListOptions{}); len(recs) != 1 {
		t.Fatalf("list should hide the superseded record, got %d", len(recs))
	}
	if rec, _ := d.Recall("deploy make", nil, 5); len(rec) != 1 || rec[0].ID != neu.ID {
		t.Fatalf("recall should return only the replacement, got %+v", rec)
	}
	// Still on disk for inspection.
	if got, err := d.Get(old.ID); err != nil || got.SupersededBy != neu.ID {
		t.Fatalf("superseded record should remain with its pointer: %+v %v", got, err)
	}
}

func TestSimilarAndStale(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	a := mustAdd(t, d, Record{Title: "Cache invalidation", Body: "Invalidate the cache when the schema changes."})
	mustAdd(t, d, Record{Title: "Logging format", Body: "Use structured logs with request ids."})

	if sims, err := d.Similar(a.ID, 0, 5); err != nil || len(sims) != 1 {
		t.Fatalf("threshold 0 should report the other record only, got %d (%v)", len(sims), err)
	}
	if sims, _ := d.Similar(a.ID, 1.01, 5); len(sims) != 0 {
		t.Fatalf("nothing is above cosine 1.01, got %d", len(sims))
	}

	// 120 days later both records are stale; nothing is deleted.
	later := time.Now().Add(120 * 24 * time.Hour)
	d.now = func() time.Time { return later }
	stale, err := d.StaleCandidates(90*24*time.Hour, 2, 10)
	if err != nil || len(stale) != 2 {
		t.Fatalf("want 2 stale records, got %d (%v)", len(stale), err)
	}
	if n, _ := d.List(ListOptions{}); len(n) != 2 {
		t.Fatalf("stale check must not delete, %d left", len(n))
	}
}

func TestClearByCategory(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	mustAdd(t, d, Record{Category: CatBug, Title: "Bug one", Body: "first bug body text"})
	mustAdd(t, d, Record{Category: CatBug, Title: "Bug two", Body: "second bug body text"})
	mustAdd(t, d, Record{Category: CatFact, Title: "A fact", Body: "fact body text here"})
	n, err := d.Clear(CatBug)
	if err != nil || n != 2 {
		t.Fatalf("Clear(bug) = %d, %v; want 2", n, err)
	}
	if hits, _ := d.Search("bug", 5); len(hits) != 0 {
		t.Fatalf("cleared rows must leave the FTS index too, got %d", len(hits))
	}
	if n, _ := d.Clear(""); n != 1 {
		t.Fatalf("Clear(\"\") should remove the last record, removed %d", n)
	}
}

func TestListTopicFilter(t *testing.T) {
	s, _ := newStores(t)
	d := s.Project
	mustAdd(t, d, Record{Title: "Tagged", Body: "carries the tag", Tags: []string{"Release"}})
	mustAdd(t, d, Record{Title: "Untagged", Body: "does not carry it"})
	got, err := d.List(ListOptions{Topic: "release"})
	if err != nil || len(got) != 1 || got[0].Title != "Tagged" {
		t.Fatalf("topic filter: %+v %v", got, err)
	}
}

func TestToolGetStaleSupersedeAndHints(t *testing.T) {
	s, _ := newStores(t)
	old := callMem(t, s, map[string]any{"action": "save", "title": "Port is 8080", "body": "The dev server listens on port 8080."})
	if old.IsError {
		t.Fatalf("save: %s", old.Output)
	}
	id := strings.Fields(strings.TrimPrefix(old.Output, "Saved memory "))[0]

	res := callMem(t, s, map[string]any{"action": "save", "title": "Port is 9090", "body": "The dev server listens on port 9090 now.", "supersedes": id})
	if res.IsError || !strings.Contains(res.Output, "supersedes") {
		t.Fatalf("supersede save: %q", res.Output)
	}

	if bad := callMem(t, s, map[string]any{"action": "save", "title": "x", "body": "y", "supersedes": "nope"}); !bad.IsError {
		t.Fatal("supersedes an unknown id must fail before saving")
	}

	get := callMem(t, s, map[string]any{"action": "get", "id": id})
	if get.IsError || !strings.Contains(get.Output, "source: agent") {
		t.Fatalf("tool saves should be tagged agent: %q", get.Output)
	}

	if st := callMem(t, s, map[string]any{"action": "stale"}); st.IsError {
		t.Fatalf("stale: %s", st.Output)
	} else if strings.Contains(st.Output, "Deleted") {
		t.Fatal("stale must not delete")
	}
}

func TestToolWritesOffRefusesSaves(t *testing.T) {
	s, _ := newStores(t)
	s.Policy.WriteScope = "none"
	if res := callMem(t, s, map[string]any{"action": "save", "title": "t", "body": "b"}); !res.IsError {
		t.Fatalf("writeScope none should refuse saves: %q", res.Output)
	}
}

func TestPolicyLoadOverridesAndRecallScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, PolicyFile), []byte(`{"writeScope":"global","readScope":"all"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, ".scribe")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Project file overrides readScope and gives an unknown writeScope, which
	// is ignored so the home value stands.
	if err := os.WriteFile(filepath.Join(projDir, PolicyFile), []byte(`{"readScope":"project","writeScope":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := LoadPolicy(root)
	if p.WriteScope != "global" || p.ReadScope != "project" {
		t.Fatalf("policy = %+v", p)
	}

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustAdd(t, s.Global, Record{Title: "Global only fact", Body: "lives in the global store about widgets"})
	mustAdd(t, s.Project, Record{Title: "Project fact", Body: "lives in the project store about widgets"})
	got, _ := s.Recall("widgets", nil, 5)
	for _, m := range got {
		if m.Scope != ScopeProject {
			t.Fatalf("readScope project must not recall %+v", m)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want the project record only, got %d", len(got))
	}
	// Save with no scope goes to the global store per writeScope.
	res := callMem(t, s, map[string]any{"action": "save", "title": "Routed", "body": "goes to global"})
	if res.IsError || !strings.Contains(res.Output, "(global,") {
		t.Fatalf("save should route to global: %q", res.Output)
	}
}

func TestInjectTotalBudget(t *testing.T) {
	s, _ := newStores(t)
	long := strings.Repeat("alpha ", 80) // trimmed to injectBodyChars each
	for i, title := range []string{"alpha one", "alpha two", "alpha three", "alpha four", "alpha five"} {
		mustAdd(t, s.Project, Record{Title: title, Body: long + string(rune('a'+i))})
	}
	block := s.Inject("alpha", nil)
	if block == "" {
		t.Fatal("expected a block")
	}
	if n := strings.Count(block, "(id "); n == 0 || n > 4 {
		t.Fatalf("budget should keep at most about 4 bodies of 400 chars, got %d records", n)
	}
}
