package api_test

// The MCP surface is how the Discord bot, other agents and claude.ai file work
// onto the board. It talks to the same HTTP API, so it can never bypass a rule
// the API enforces — these tests prove that end to end.

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/mcp"
)

// mcpCall drives one JSON-RPC exchange over the stdio transport.
func mcpCall(t *testing.T, h *harness, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out bytes.Buffer
	if err := mcp.New(h.URL, h.App.Cfg.AuthToken).Serve(in, &out); err != nil {
		t.Fatalf("mcp serve: %v", err)
	}
	var frames []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var f map[string]any
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		frames = append(frames, f)
	}
	return frames
}

// toolText is the text payload of a tools/call result.
func toolText(t *testing.T, frame map[string]any) string {
	t.Helper()
	result, _ := frame["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %v", frame)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}

func TestMCPHandshakeAndToolList(t *testing.T) {
	h := newHarness(t)
	frames := mcpCall(t,
		h,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	// the notification gets no reply, so two frames come back for three messages
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d: %v", len(frames), frames)
	}
	info := frames[0]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "lectern" {
		t.Errorf("serverInfo: %v", info)
	}
	toolList, _ := frames[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range toolList {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"board_summary", "list_projects", "list_tasks",
		"create_task", "task_status", "task_diff", "pending_approvals",
		"decide_approval", "complete_task", "request_changes"} {
		if !names[want] {
			t.Errorf("tool %q is missing", want)
		}
	}
}

func TestMCPCreateTaskFilesRealWork(t *testing.T) {
	h := newHarness(t)
	frames := mcpCall(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_task",`+
			`"arguments":{"project":"demo-app","title":"filed by mcp","prompt":"do it",`+
			`"dispatch":false}}}`)
	var created map[string]any
	if err := json.Unmarshal([]byte(toolText(t, frames[0])), &created); err != nil {
		t.Fatal(err)
	}
	if created["status"] != "backlog" {
		t.Fatalf("dispatch=false must park the card: %v", created)
	}
	found := false
	for _, task := range h.getList("/api/tasks") {
		if task.str("title") == "filed by mcp" {
			found = true
		}
	}
	if !found {
		t.Fatal("the card never reached the board")
	}
}

// An unknown project must come back as a readable error the model can act on,
// not a silent failure or a dead RPC.
func TestMCPUnknownProjectIsAReadableError(t *testing.T) {
	h := newHarness(t)
	frames := mcpCall(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_task",`+
			`"arguments":{"project":"nope","title":"x","prompt":"y"}}}`)
	result := frames[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("expected an error result: %v", result)
	}
	text := toolText(t, frames[0])
	if !strings.Contains(text, "no project named") || !strings.Contains(text, "demo-app") {
		t.Fatalf("the error should name the valid projects: %q", text)
	}
}

func TestMCPUnknownToolAndMethod(t *testing.T) {
	h := newHarness(t)
	frames := mcpCall(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"launch_missiles","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"telepathy"}`)
	if frames[0]["result"].(map[string]any)["isError"] != true {
		t.Errorf("unknown tool: %v", frames[0])
	}
	if frames[1]["error"] == nil {
		t.Errorf("unknown method must be a JSON-RPC error: %v", frames[1])
	}
}

// In token mode the MCP client is a HUMAN surface, so it carries the bearer —
// unlike an agent, which only ever holds its per-attempt hook token.
func TestMCPCarriesTheBearerInTokenMode(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AuthToken = "humanonly" })
	frames := mcpCall(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"board_summary","arguments":{}}}`)
	if frames[0]["result"].(map[string]any)["isError"] == true {
		t.Fatalf("the MCP client was locked out of its own board: %s", toolText(t, frames[0]))
	}
	if !strings.Contains(toolText(t, frames[0]), `"ok": true`) {
		t.Fatalf("board summary: %s", toolText(t, frames[0]))
	}
}
