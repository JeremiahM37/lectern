package api_test

// The structured, incrementally-pollable chat stream behind the phone's tool
// cards (internal/nativeidentity/conversation_live.py). Mirrors
// native_conversations_test.go's synthetic-JSONL approach: real python3, real
// files on disk, no mocked history reader.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// liveItem is the wire shape of one entry in the live conversation response,
// permissive enough to read either agent's turns.
type liveItem struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Kind       string         `json:"kind"`
	Text       string         `json:"text"`
	ToolName   string         `json:"tool_name"`
	ToolUseID  string         `json:"tool_use_id"`
	Input      map[string]any `json:"input"`
	Output     string         `json:"output"`
	IsError    bool           `json:"is_error"`
}

type livePage struct {
	ConversationID string     `json:"conversation_id"`
	Items          []liveItem `json:"items"`
	Cursor         int64      `json:"cursor"`
	Truncated      bool       `json:"truncated"`
}

// nativeHistoryFixture sets up one target+session (local executor, real
// python3) with the given agent's history directory ready to write into.
// Returns the session, its workdir and the directory JSONL files belong in.
func nativeHistoryFixture(t *testing.T, h *harness, agent string) (*store.Session, string, string) {
	t.Helper()
	requireRealTools(t)
	root := t.TempDir()
	home := t.TempDir()
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "native-live-" + agent, Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "native-live", Agent: agent, Workdir: root, TmuxSession: "not-needed", Status: "dead"})
	if err != nil {
		t.Fatal(err)
	}
	envName, dir := "CODEX_HOME", filepath.Join(home, "sessions")
	if agent == "claude" {
		envName = "CLAUDE_CONFIG_DIR"
		var slug strings.Builder
		for _, c := range root {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				slug.WriteRune(c)
			} else {
				slug.WriteByte('-')
			}
		}
		dir = filepath.Join(home, "projects", slug.String())
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{{"name": agent, "command": agent, "env": obj{envName: home}}}, 200, nil)
	return session, root, dir
}

func writeJSONL(t *testing.T, path string, rows []map[string]any) {
	t.Helper()
	var b strings.Builder
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLiveConversationClaudeStructuredTurns(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	session, root, dir := nativeHistoryFixture(t, h, "claude")
	cid := "11111111-1111-4111-8111-111111111111"
	rows := []map[string]any{
		{"type": "user", "sessionId": cid, "cwd": root, "timestamp": "t1", "message": obj{"role": "user", "content": "please read the file"}},
		{"type": "assistant", "sessionId": cid, "cwd": root, "timestamp": "t2", "message": obj{"role": "assistant", "content": []obj{
			{"type": "thinking", "thinking": "I should read it first"},
			{"type": "text", "text": "Reading now."},
			{"type": "tool_use", "id": "tu1", "name": "Read", "input": obj{"file_path": "/tmp/x.txt"}},
		}}},
		{"type": "user", "sessionId": cid, "cwd": root, "timestamp": "t3", "message": obj{"role": "user", "content": []obj{
			{"type": "tool_result", "tool_use_id": "tu1", "content": "line one\nline two"},
		}}},
	}
	writeJSONL(t, filepath.Join(dir, cid+".jsonl"), rows)

	var page livePage
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/conversation/live?cid=%s", session.ID, cid), nil, 200, &page)
	if page.ConversationID != cid {
		t.Fatalf("conversation_id = %q", page.ConversationID)
	}
	kinds := make([]string, len(page.Items))
	for i, item := range page.Items {
		kinds[i] = item.Kind
	}
	want := []string{"text", "thinking", "text", "tool_use", "tool_result"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	var toolUse, toolResult liveItem
	for _, item := range page.Items {
		if item.Kind == "tool_use" {
			toolUse = item
		}
		if item.Kind == "tool_result" {
			toolResult = item
		}
	}
	if toolUse.ToolName != "Read" || toolUse.Input["file_path"] != "/tmp/x.txt" {
		t.Fatalf("tool_use = %+v", toolUse)
	}
	if toolUse.ToolUseID != "tu1" || toolResult.ToolUseID != "tu1" {
		t.Fatalf("tool_use_id did not correlate: use=%q result=%q", toolUse.ToolUseID, toolResult.ToolUseID)
	}
	if !strings.Contains(toolResult.Output, "line one") {
		t.Fatalf("tool_result output = %q", toolResult.Output)
	}
	if page.Cursor <= 0 {
		t.Fatal("no cursor returned")
	}

	// Polling again with the returned cursor sees nothing new.
	var empty livePage
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/conversation/live?cid=%s&since=%d", session.ID, cid, page.Cursor), nil, 200, &empty)
	if len(empty.Items) != 0 {
		t.Fatalf("expected no new items, got %d", len(empty.Items))
	}
	if empty.Cursor != page.Cursor {
		t.Fatalf("cursor moved with nothing appended: %d -> %d", page.Cursor, empty.Cursor)
	}

	// A message appended after the cursor shows up on the next poll, and
	// only that message.
	appendRow, _ := json.Marshal(map[string]any{"type": "assistant", "sessionId": cid, "cwd": root, "timestamp": "t4",
		"message": obj{"role": "assistant", "content": "All done."}})
	f, err := os.OpenFile(filepath.Join(dir, cid+".jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(appendRow)
	f.Write([]byte("\n"))
	f.Close()

	var next livePage
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/conversation/live?cid=%s&since=%d", session.ID, cid, page.Cursor), nil, 200, &next)
	if len(next.Items) != 1 || next.Items[0].Text != "All done." {
		t.Fatalf("incremental poll = %+v", next.Items)
	}
}

func TestLiveConversationClaudePrivateReasoningIsCollapsedNotDropped(t *testing.T) {
	// native_record()/the plain-text reader drop thinking entirely; the
	// structured stream must still carry it (as kind=thinking, for the
	// client to render collapsed) rather than silently discard it a second
	// time under a different code path.
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	session, root, dir := nativeHistoryFixture(t, h, "claude")
	cid := "22222222-2222-4222-8222-222222222222"
	writeJSONL(t, filepath.Join(dir, cid+".jsonl"), []map[string]any{
		{"type": "assistant", "sessionId": cid, "cwd": root, "message": obj{"role": "assistant", "content": []obj{
			{"type": "thinking", "thinking": "PRIVATE_REASONING"},
		}}},
	})
	var page livePage
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/conversation/live?cid=%s", session.ID, cid), nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].Kind != "thinking" || !strings.Contains(page.Items[0].Text, "PRIVATE_REASONING") {
		t.Fatalf("items = %+v", page.Items)
	}
}

func TestLiveConversationCodexStructuredTurns(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	session, root, dir := nativeHistoryFixture(t, h, "codex")
	cid := "33333333-3333-4333-8333-333333333333"
	rows := []map[string]any{
		{"type": "session_meta", "payload": obj{"id": cid, "cwd": root}},
		{"type": "response_item", "payload": obj{"type": "message", "role": "user", "content": []obj{{"type": "input_text", "text": "run the tests"}}}},
		{"type": "response_item", "payload": obj{"type": "reasoning", "summary": []obj{{"type": "summary_text", "text": "Let me run pytest"}}}},
		{"type": "response_item", "payload": obj{"type": "function_call", "call_id": "call1", "name": "shell", "arguments": `{"command":["pytest"]}`}},
		{"type": "response_item", "payload": obj{"type": "function_call_output", "call_id": "call1", "output": "3 passed"}},
	}
	writeJSONL(t, filepath.Join(dir, cid+".jsonl"), rows)

	var page livePage
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/conversation/live?cid=%s", session.ID, cid), nil, 200, &page)
	kinds := make([]string, len(page.Items))
	for i, item := range page.Items {
		kinds[i] = item.Kind
	}
	want := []string{"text", "thinking", "tool_use", "tool_result"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	var toolUse, toolResult liveItem
	for _, item := range page.Items {
		if item.Kind == "tool_use" {
			toolUse = item
		}
		if item.Kind == "tool_result" {
			toolResult = item
		}
	}
	if toolUse.ToolName != "shell" || toolUse.ToolUseID != "call1" {
		t.Fatalf("tool_use = %+v", toolUse)
	}
	if toolResult.ToolUseID != "call1" || toolResult.Output != "3 passed" {
		t.Fatalf("tool_result = %+v", toolResult)
	}
}

func TestLiveConversationUnknownAgentRejected(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "shell-target", Kind: "local"})
	sess, err := h.App.DB.InsertSession(&store.Session{ProjectID: &pid, TargetID: target.ID, Name: "shell", Agent: "shell", Workdir: t.TempDir(), TmuxSession: "not-needed", Status: "dead"})
	if err != nil {
		t.Fatal(err)
	}
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/conversation/live", sess.ID), nil, 409, nil)
}
