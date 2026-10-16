package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// setup returns a store for a fresh project root, with the global index
// pointed at a temp directory.
func setup(t *testing.T) (*Store, string) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
	root := t.TempDir()
	return NewStore(root), root
}

func newSession(t *testing.T, s *Store, title string) Meta {
	t.Helper()
	m := Meta{Title: title, Model: "anthropic/test", Mode: "default"}
	if err := s.Create(&m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return m
}

func TestCreateOpenPrefix(t *testing.T) {
	s, root := setup(t)
	m := newSession(t, s, "first")
	if len(m.ID) != 36 {
		t.Fatalf("expected a UUID, got %q", m.ID)
	}
	if m.Workdir != root {
		t.Fatalf("Workdir = %q, want %q", m.Workdir, root)
	}
	got, err := s.Open(m.ID)
	if err != nil || got.Title != "first" {
		t.Fatalf("Open full id: %+v, %v", got, err)
	}
	got, err = s.Open(strings.ToUpper(m.ID[:8]))
	if err != nil || got.ID != m.ID {
		t.Fatalf("Open unique prefix (case-insensitive): %+v, %v", got, err)
	}
	if _, err := s.Open("zzzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open missing: want ErrNotFound, got %v", err)
	}
	if _, err := s.Open(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open empty: want ErrNotFound, got %v", err)
	}
	if err := s.Create(&Meta{ID: m.ID}); err == nil {
		t.Fatal("creating an existing id should fail")
	}
}

func TestAmbiguousPrefix(t *testing.T) {
	s, _ := setup(t)
	for _, id := range []string{"abc111-one", "abc999-two", "xyz000-three"} {
		if err := s.Create(&Meta{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Open("abc")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
	if got, err := s.Open("abc1"); err != nil || got.ID != "abc111-one" {
		t.Fatalf("unique extension should resolve: %+v, %v", got, err)
	}
	if got, err := s.Open("abc111-one"); err != nil || got.ID != "abc111-one" {
		t.Fatalf("exact id should win: %+v, %v", got, err)
	}
}

func TestListOrder(t *testing.T) {
	s, _ := setup(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"old", "mid", "new"} {
		m := Meta{ID: id, Updated: base.Add(time.Duration(i) * time.Hour)}
		if err := s.Create(&m); err != nil {
			t.Fatal(err)
		}
	}
	// A corrupt entry must be skipped, not fail the listing.
	bad := filepath.Join(s.Dir("broken"), "meta.json")
	if err := os.MkdirAll(filepath.Dir(bad), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "new,mid,old" {
		t.Fatalf("List order = %v", ids)
	}
}

func TestMessagesAppendReplaceLoad(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "")
	msgs := []llm.Message{
		llm.UserText("fix the build"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartText, Text: "looking"},
			{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c1", ToolName: "read", Result: "package a"}}},
	}
	if err := s.AppendMessages(m.ID, msgs[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessages(m.ID, msgs[1:]); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadMessages(m.ID)
	if err != nil || len(got) != 3 {
		t.Fatalf("LoadMessages: %d msgs, %v", len(got), err)
	}
	if got[1].ToolCalls()[0].ToolName != "read" || got[2].Parts[0].Result != "package a" {
		t.Fatalf("round trip lost data: %+v", got)
	}

	// Title is derived from the first user message.
	if after, _ := s.Open(m.ID); after.Title != "fix the build" {
		t.Fatalf("derived title = %q", after.Title)
	}

	// A write cut short by a crash must not hide later messages.
	f, err := os.OpenFile(filepath.Join(s.Dir(m.ID), "messages.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":"assis`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := s.AppendMessages(m.ID, []llm.Message{llm.UserText("after the crash")}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LoadMessages(m.ID)
	if len(got) != 4 || got[3].Text() != "after the crash" {
		t.Fatalf("message after a truncated line was lost: %d msgs", len(got))
	}

	if err := s.ReplaceMessages(m.ID, msgs[:1]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LoadMessages(m.ID)
	if len(got) != 1 || got[0].Text() != "fix the build" {
		t.Fatalf("ReplaceMessages: %+v", got)
	}
}

func TestUIRecords(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "")
	recs := []json.RawMessage{
		json.RawMessage(`{"kind":"user","text":"hi"}`),
		json.RawMessage("{\n  \"kind\": \"tool\",\n  \"args\": {\"a\": 1}\n}"),
	}
	if err := s.AppendUI(m.ID, recs...); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUI(m.ID, json.RawMessage(`not json`)); err == nil {
		t.Fatal("invalid UI record should be rejected")
	}
	got, err := s.LoadUI(m.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("LoadUI: %d, %v", len(got), err)
	}
	if string(got[0]) != `{"kind":"user","text":"hi"}` {
		t.Fatalf("record 0 = %s", got[0])
	}
	if string(got[1]) != `{"kind":"tool","args":{"a":1}}` {
		t.Fatalf("multi-line record should be stored compacted, got %s", got[1])
	}
}

func TestForkCopiesPrefix(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "original")
	msgs := []llm.Message{llm.UserText("one"), llm.AssistantText("two"), llm.UserText("three")}
	if err := s.AppendMessages(m.ID, msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUI(m.ID, json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(m.ID, json.RawMessage(`{"steps":[]}`)); err != nil {
		t.Fatal(err)
	}

	partial, err := s.Fork(m.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	pm, _ := s.Open(partial)
	if pm.ParentID != m.ID || pm.Title != "original (fork)" {
		t.Fatalf("fork meta: %+v", pm)
	}
	pmsgs, _ := s.LoadMessages(partial)
	if len(pmsgs) != 2 || pmsgs[1].Text() != "two" {
		t.Fatalf("partial fork messages: %+v", pmsgs)
	}
	if ui, _ := s.LoadUI(partial); len(ui) != 0 {
		t.Fatalf("UI must not be copied for a partial fork, got %d", len(ui))
	}

	full, err := s.Fork(m.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ui, _ := s.LoadUI(full); len(ui) != 1 {
		t.Fatalf("full fork should copy UI, got %d", len(ui))
	}
	if _, err := s.LoadPlan(full); err != nil {
		t.Fatalf("plan not copied: %v", err)
	}
	if _, err := s.Fork(m.ID, 4); err == nil {
		t.Fatal("fork beyond the last message should fail")
	}
}

func TestDeleteRemovesSession(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "gone")
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still opens: %v", err)
	}
	if _, _, err := FindGlobal(m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still in global index: %v", err)
	}
}

func TestExportMarkdownAndJSON(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "export me")
	msgs := []llm.Message{
		llm.UserText("run the tests"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "tests first"},
			{Type: llm.PartText, Text: "Running them."},
			{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "shell", Args: json.RawMessage(`{"cmd":"go test ./..."}`)},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c1", ToolName: "shell", Result: "FAIL pkg\nlots more output", IsError: true}}},
	}
	if err := s.AppendMessages(m.ID, msgs); err != nil {
		t.Fatal(err)
	}
	md, err := s.Export(m.ID, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	text := string(md)
	for _, want := range []string{"# export me", "## User", "run the tests", "Running them.", "tool `shell`", `go test ./...`, "result error", "FAIL pkg"} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "lots more output") {
		t.Errorf("markdown should summarise tool output to its first line:\n%s", text)
	}

	js, err := s.Export(m.ID, "json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Meta     Meta          `json:"meta"`
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(js, &doc); err != nil || len(doc.Messages) != 3 || doc.Meta.ID != m.ID {
		t.Fatalf("json export: %v %+v", err, doc)
	}
	if _, err := s.Export(m.ID, "pdf"); err == nil {
		t.Fatal("unknown format should fail")
	}
}

func TestSearchTitleAndFirstUser(t *testing.T) {
	s, _ := setup(t)
	a := newSession(t, s, "Refactor the parser")
	b := newSession(t, s, "")
	if err := s.AppendMessages(b.ID, []llm.Message{llm.UserText("why is the Lexer slow")}); err != nil {
		t.Fatal(err)
	}
	c := newSession(t, s, "unrelated")
	_ = c
	if got := s.Search("parser"); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("title search: %+v", got)
	}
	if got := s.Search("LEXER"); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("first-user search: %+v", got)
	}
	if got := s.Search(""); len(got) != 3 {
		t.Fatalf("empty query should list all, got %d", len(got))
	}
}

func TestGlobalIndexResumesFromAnyProject(t *testing.T) {
	s1, _ := setup(t)
	other := NewStore(t.TempDir())
	a := newSession(t, s1, "in project one")
	b, err := func() (Meta, error) {
		m := Meta{Title: "in project two"}
		err := other.Create(&m)
		return m, err
	}()
	if err != nil {
		t.Fatal(err)
	}

	st, m, err := FindGlobal(a.ID[:6])
	if err != nil || m.ID != a.ID || st.Root != s1.Root {
		t.Fatalf("FindGlobal prefix: %+v %v", m, err)
	}
	if _, m, err := FindGlobal(b.ID); err != nil || m.Title != "in project two" {
		t.Fatalf("FindGlobal across projects: %+v %v", m, err)
	}
	if all := ListAll(); len(all) != 2 {
		t.Fatalf("ListAll = %d, want 2", len(all))
	}
}

func TestDeriveTitle(t *testing.T) {
	if got := DeriveTitle("   "); got != "Empty session" {
		t.Errorf("blank = %q", got)
	}
	if got := DeriveTitle("first line\nsecond"); got != "first line" {
		t.Errorf("multi-line = %q", got)
	}
	long := strings.Repeat("x", 80)
	if got := DeriveTitle(long); len([]rune(got)) != 60 || !strings.HasSuffix(got, "...") {
		t.Errorf("long = %q (%d)", got, len([]rune(got)))
	}
}
