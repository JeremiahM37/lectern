package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestBuildPromptIsDeterministic(t *testing.T) {
	req := sampleRequest()
	if a, b := buildPrompt(req), buildPrompt(req); a != b {
		t.Fatalf("prompt differs between two builds of the same request")
	}
}

func TestBuildPromptTranscriptAndTools(t *testing.T) {
	got := buildPrompt(sampleRequest())
	for _, want := range []string{
		"SYSTEM INSTRUCTIONS\nYou are Scribe.",
		"1. USER\nRead go.mod please",
		"2. ASSISTANT\n[tool call id=call_1 name=read_file args={\"path\":\"go.mod\"}]",
		"3. TOOL\n[tool result id=call_1 name=read_file status=ok]\nmodule example",
		"4. USER\n[image omitted: image/png]",
		"TOOLS (request one by its exact name)\n- read_file: Read a file\n  input schema: {\"type\":\"object\"}",
		"TOOL CHOICE: auto.",
		"Do not run commands, edit files or read files yourself.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", want, got)
		}
	}
}

func TestBuildPromptLeavesOutReasoning(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{
		Role: llm.RoleAssistant,
		Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "secret scratch work"},
			{Type: llm.PartText, Text: "visible answer"},
		},
	}}}
	got := buildPrompt(req)
	if strings.Contains(got, "secret scratch work") {
		t.Fatalf("reasoning from an earlier turn was sent: %s", got)
	}
	if !strings.Contains(got, "visible answer") {
		t.Fatalf("answer text missing: %s", got)
	}
}

func TestBuildPromptToolResultError(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{
		Role:  llm.RoleTool,
		Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c9", ToolName: "exec", Result: "exit 2", IsError: true}},
	}}}
	if got := buildPrompt(req); !strings.Contains(got, "[tool result id=c9 name=exec status=error]\nexit 2") {
		t.Fatalf("error result not rendered: %s", got)
	}
}

func TestBuildPromptNoToolsAndChoice(t *testing.T) {
	got := buildPrompt(llm.Request{Messages: []llm.Message{llm.UserText("hi")}, ToolChoice: "required"})
	if !strings.Contains(got, "TOOLS\nNone. Answer with text and do not request a tool.") {
		t.Fatalf("no-tools line missing: %s", got)
	}
	// With no tools the choice is none, whatever the request asked for.
	if !strings.Contains(got, "TOOL CHOICE: none (no tools are available)") {
		t.Fatalf("choice should be none without tools: %s", got)
	}
}

func TestToolChoiceLine(t *testing.T) {
	tools := []llm.ToolSpec{{Name: "t", Schema: json.RawMessage(`{}`)}}
	cases := map[string]string{
		"":         "auto. Request a tool only when it is needed.",
		"auto":     "auto. Request a tool only when it is needed.",
		"none":     "none. Answer directly and do not request any tool.",
		"required": "required. Request at least one tool this turn.",
	}
	for choice, want := range cases {
		if got := toolChoiceLine(llm.Request{Tools: tools, ToolChoice: choice}); got != want {
			t.Errorf("choice %q: got %q want %q", choice, got, want)
		}
	}
}

func TestOutputSchemaIsStrictShape(t *testing.T) {
	if !json.Valid([]byte(outputSchema)) {
		t.Fatal("outputSchema is not valid JSON")
	}
	var s struct {
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(outputSchema), &s); err != nil {
		t.Fatal(err)
	}
	if s.AdditionalProperties || len(s.Required) != 3 {
		t.Fatalf("schema must require all three properties and forbid extras: %+v", s)
	}
}

func sampleRequest() llm.Request {
	return llm.Request{
		System: "You are Scribe.\n",
		Messages: []llm.Message{
			llm.UserText("Read go.mod please"),
			{Role: llm.RoleAssistant, Parts: []llm.Part{{
				Type: llm.PartToolCall, ToolCallID: "call_1", ToolName: "read_file",
				Args: json.RawMessage(`{ "path": "go.mod" }`),
			}}},
			{Role: llm.RoleTool, Parts: []llm.Part{{
				Type: llm.PartToolResult, ToolCallID: "call_1", ToolName: "read_file", Result: "module example\n",
			}}},
			{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartImage, MediaType: "image/png", Data: []byte{1, 2}}}},
		},
		Tools: []llm.ToolSpec{{
			Name:        "read_file",
			Description: "Read a file",
			Schema:      json.RawMessage(`{"type": "object"}`),
		}},
	}
}
