package anthropic

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// isolate keeps any stray HOME lookups out of the real home directory.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// bodyOf builds the request body and decodes it.
func bodyOf(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	raw, err := buildBody(req)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return m
}

// toolTypes lists the "type" of each tool in the body, "" for client tools.
func toolTypes(m map[string]any) []string {
	tools, _ := m["tools"].([]any)
	var out []string
	for _, t := range tools {
		tm := t.(map[string]any)
		s, _ := tm["type"].(string)
		out = append(out, s)
	}
	return out
}

// sentTo sends req through a capturing server and returns what it received.
func sentTo(t *testing.T, req llm.Request) *captured {
	t.Helper()
	p, c := captureServer(t)
	mustStream(t, p, req)
	c.await(t)
	return c
}

var clientTool = llm.ToolSpec{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}

// ---- (a) code execution ----

func TestCodeExecutionOnAddsToolAndBeta(t *testing.T) {
	isolate(t)
	req := llm.Request{Model: "claude-sonnet-4-5", CodeExecution: true, Tools: []llm.ToolSpec{clientTool}}

	m := bodyOf(t, req)
	tools := toolTypes(m)
	if len(tools) != 2 || tools[1] != "code_execution_20250825" {
		t.Fatalf("tools = %v, want client tool then code_execution_20250825", tools)
	}
	last := m["tools"].([]any)[1].(map[string]any)
	if last["name"] != "code_execution" {
		t.Fatalf("server tool name = %v, want code_execution", last["name"])
	}

	c := sentTo(t, req)
	if got := c.headers.Get("anthropic-beta"); got != codeExecutionBeta {
		t.Fatalf("anthropic-beta = %q, want %q", got, codeExecutionBeta)
	}
}

func TestCodeExecutionOffLeavesRequestUnchanged(t *testing.T) {
	isolate(t)
	req := llm.Request{Model: "claude-sonnet-4-5", Tools: []llm.ToolSpec{clientTool}}

	m := bodyOf(t, req)
	for _, typ := range toolTypes(m) {
		if typ != "" {
			t.Fatalf("server tool %q sent with code execution off", typ)
		}
	}
	c := sentTo(t, req)
	if got := c.headers.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta = %q with code execution off, want none", got)
	}
}

func TestCodeExecutionAloneHasNoClientTools(t *testing.T) {
	isolate(t)
	m := bodyOf(t, llm.Request{Model: "claude-sonnet-4-5", CodeExecution: true})
	if got := toolTypes(m); len(got) != 1 || got[0] != "code_execution_20250825" {
		t.Fatalf("tools = %v, want just the code execution tool", got)
	}
}

// ---- (b) computer use ----

func TestComputerUseOnAddsToolAndBeta(t *testing.T) {
	isolate(t)
	req := llm.Request{Model: "claude-opus-4-5", ComputerUse: true}

	m := bodyOf(t, req)
	tools := m["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want one computer tool", tools)
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "computer_20251124" || tool["name"] != "computer" {
		t.Fatalf("computer tool = %v", tool)
	}
	if tool["display_width_px"] != float64(computerDisplayW) || tool["display_height_px"] != float64(computerDisplayH) {
		t.Fatalf("display size = %v x %v", tool["display_width_px"], tool["display_height_px"])
	}

	c := sentTo(t, req)
	if got := c.headers.Get("anthropic-beta"); got != computerUseBeta {
		t.Fatalf("anthropic-beta = %q, want %q", got, computerUseBeta)
	}
}

func TestComputerUseOffLeavesRequestUnchanged(t *testing.T) {
	isolate(t)
	req := llm.Request{Model: "claude-opus-4-5", Tools: []llm.ToolSpec{clientTool}}

	m := bodyOf(t, req)
	for _, typ := range toolTypes(m) {
		if typ == "computer_20251124" {
			t.Fatal("computer tool sent with computer use off")
		}
	}
	c := sentTo(t, req)
	if got := c.headers.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta = %q with computer use off, want none", got)
	}
}

// ---- (c) server compaction ----

// compactHistory is a conversation whose assistant turn starts with the
// compaction block the server returned earlier.
func compactHistory(withBlock bool) []llm.Message {
	msgs := []llm.Message{llm.UserText("first question")}
	var parts []llm.Part
	if withBlock {
		parts = append(parts, llm.Part{Type: llm.PartCompaction, Text: "SUMMARY-BODY"})
	}
	parts = append(parts, llm.Part{Type: llm.PartText, Text: "answer"})
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Parts: parts})
	msgs = append(msgs, llm.UserText("follow up"))
	return msgs
}

func TestServerCompactionOnCarriesBlockBack(t *testing.T) {
	isolate(t)
	req := llm.Request{
		Model: "claude-opus-5-5", ContextWindow: 1000000, ServerCompaction: true,
		Messages: compactHistory(true),
	}

	m := bodyOf(t, req)
	cm, _ := m["context_management"].(map[string]any)
	edits, _ := cm["edits"].([]any)
	if len(edits) != 1 || edits[0].(map[string]any)["type"] != "compact_20260112" {
		t.Fatalf("context_management = %v, want one compact edit", cm)
	}

	msgs := m["messages"].([]any)
	assistant := msgs[1].(map[string]any)
	blocks := blocksOf(t, assistant)
	if len(blocks) == 0 || blocks[0]["type"] != "compaction" || blocks[0]["content"] != "SUMMARY-BODY" {
		t.Fatalf("assistant blocks = %v, want the compaction block first", blocks)
	}

	c := sentTo(t, req)
	if got := c.headers.Get("anthropic-beta"); got != compactionBeta {
		t.Fatalf("anthropic-beta = %q, want %q", got, compactionBeta)
	}
}

func TestServerCompactionOffUnchanged(t *testing.T) {
	isolate(t)
	withBlock := llm.Request{Model: "claude-opus-5-5", ContextWindow: 1000000, Messages: compactHistory(true)}
	without := llm.Request{Model: "claude-opus-5-5", ContextWindow: 1000000, Messages: compactHistory(false)}

	// Off: no edit, no beta, and the stored block is dropped, so the body is
	// exactly what it was before the block existed.
	m := bodyOf(t, withBlock)
	if _, ok := m["context_management"]; ok {
		t.Fatal("context_management sent with server compaction off")
	}
	if !bytesEqualJSON(t, withBlock, without) {
		t.Fatal("body with a stored compaction block differs from the body without it, with compaction off")
	}
	c := sentTo(t, withBlock)
	if got := c.headers.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta = %q with compaction off, want none", got)
	}
}

func bytesEqualJSON(t *testing.T, a, b llm.Request) bool {
	t.Helper()
	ra, err := buildBody(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := buildBody(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ra) == string(rb)
}

func TestServerCompactionStreamEmitsWholeSummary(t *testing.T) {
	isolate(t)
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":null}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"Part one. "}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"Part two."}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hi"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "claude-opus-5-5"})
	terminalOf(t, evs)

	var summaries []string
	for _, ev := range evs {
		if ev.Type == llm.EvCompaction {
			summaries = append(summaries, ev.Text)
		}
	}
	if !slices.Equal(summaries, []string{"Part one. Part two."}) {
		t.Fatalf("compaction events = %q, want one whole summary", summaries)
	}
	if evs[len(evs)-1].Type != llm.EvFinish {
		t.Fatalf("stream did not finish cleanly: %+v", evs[len(evs)-1])
	}
}

// A server tool result carries an object as content. It must not break the
// parse of the rest of the stream.
func TestServerToolResultDoesNotBreakStream(t *testing.T) {
	isolate(t)
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"4\n","return_code":0}}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "claude-opus-5-5"})
	if fin := terminalOf(t, evs); fin.Type != llm.EvFinish {
		t.Fatalf("terminal = %s (%v), want finish", fin.Type, fin.Err)
	}
}
