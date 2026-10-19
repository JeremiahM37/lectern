package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// outputSchema is the --output-schema codex enforces on the final message.
// Every property is required and additionalProperties is false because strict
// mode needs both. A tool's input is a JSON string, not an object, so the
// model can put any arguments text there and we decode it ourselves.
const outputSchema = `{"type":"object","additionalProperties":false,"required":["finishReason","text","toolCalls"],"properties":{"finishReason":{"type":"string","enum":["stop","tool-calls"]},"text":{"type":"string"},"toolCalls":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["toolName","input"],"properties":{"toolName":{"type":"string"},"input":{"type":"string"}}}}}}`

// answerRules tells the model how to reply. codex exec has no tool calling of
// its own here, so the reply shape is the only way a tool request reaches us.
// The last rule matters most: codex has shell and file tools, and the client
// is the one that must run the agent's tools, inside its own permissions.
const answerRules = `ANSWER RULES
- Reply with one JSON object that matches the output schema: finishReason, text, toolCalls.
- To use tools, set finishReason to "tool-calls" and add one toolCalls entry per call. Each entry has the exact tool name and an input string that holds the arguments as a JSON object, for example {"path":"go.mod"}.
- When the answer is complete, set finishReason to "stop", leave toolCalls empty and put the answer in text.
- Do not wrap the JSON in markdown, and add nothing before or after it.
- Do not run commands, edit files or read files yourself. The client runs tools; request them.
`

// buildPrompt renders one model call as the text codex exec reads on stdin.
// codex has no chat API we can drive directly, so the whole conversation goes
// into one prompt: system text, the numbered transcript, the tool list, the
// tool-choice rule and the answer rules. For the same request the text is the
// same every time, which keeps tests and cache hits predictable.
func buildPrompt(req llm.Request) string {
	var b strings.Builder
	if sys := strings.TrimSpace(req.System); sys != "" {
		b.WriteString("SYSTEM INSTRUCTIONS\n")
		b.WriteString(sys)
		b.WriteString("\n\n")
	}
	b.WriteString("CONVERSATION (oldest first)\n")
	if len(req.Messages) == 0 {
		b.WriteString("(empty)\n")
	}
	for i, m := range req.Messages {
		fmt.Fprintf(&b, "%d. %s\n", i+1, roleLabel(m.Role))
		writeParts(&b, m.Parts)
	}
	b.WriteString("\n")
	writeTools(&b, req.Tools)
	b.WriteString("\n")
	fmt.Fprintf(&b, "TOOL CHOICE: %s\n\n", toolChoiceLine(req))
	b.WriteString(answerRules)
	return b.String()
}

// roleLabel names a message's role the way the transcript shows it.
func roleLabel(r llm.Role) string {
	switch r {
	case llm.RoleUser:
		return "USER"
	case llm.RoleAssistant:
		return "ASSISTANT"
	case llm.RoleTool:
		return "TOOL"
	}
	return strings.ToUpper(string(r))
}

// writeParts writes one message's parts. Reasoning is left out: it is the
// model's own scratch work from earlier turns, and replaying it would only
// spend input tokens. Images are not sent here, so they become a placeholder
// line that tells the model something was there.
func writeParts(b *strings.Builder, parts []llm.Part) {
	for _, p := range parts {
		switch p.Type {
		case llm.PartText:
			writeLine(b, p.Text)
		case llm.PartToolCall:
			fmt.Fprintf(b, "[tool call id=%s name=%s args=%s]\n", p.ToolCallID, p.ToolName, argsText(p.Args))
		case llm.PartToolResult:
			status := "ok"
			if p.IsError {
				status = "error"
			}
			fmt.Fprintf(b, "[tool result id=%s name=%s status=%s]\n", p.ToolCallID, p.ToolName, status)
			writeLine(b, p.Result)
		case llm.PartImage:
			media := p.MediaType
			if media == "" {
				media = "image"
			}
			fmt.Fprintf(b, "[image omitted: %s]\n", media)
		}
	}
}

// writeLine writes s and a newline unless s is empty.
func writeLine(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteString(s)
	if !strings.HasSuffix(s, "\n") {
		b.WriteString("\n")
	}
}

// argsText returns tool arguments as compact JSON, so a call reads on one line.
// Empty arguments are {}, and anything that is not valid JSON is kept as-is.
func argsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// writeTools lists the tools the model may request, with each schema on the
// line below its name.
func writeTools(b *strings.Builder, tools []llm.ToolSpec) {
	if len(tools) == 0 {
		b.WriteString("TOOLS\nNone. Answer with text and do not request a tool.\n")
		return
	}
	b.WriteString("TOOLS (request one by its exact name)\n")
	for _, t := range tools {
		fmt.Fprintf(b, "- %s: %s\n  input schema: %s\n", t.Name, strings.TrimSpace(t.Description), argsText(t.Schema))
	}
}

// toolChoiceLine states the request's tool-choice rule in words. With no
// tools, the answer is always none, whatever the request asked for.
func toolChoiceLine(req llm.Request) string {
	if len(req.Tools) == 0 {
		return "none (no tools are available)"
	}
	switch req.ToolChoice {
	case "none":
		return "none. Answer directly and do not request any tool."
	case "required":
		return "required. Request at least one tool this turn."
	}
	return "auto. Request a tool only when it is needed."
}
