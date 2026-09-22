package agents

import (
	"encoding/json"
	"strings"
)

// Event is one normalised timeline entry, whatever agent produced it.
type Event struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// NoiseLines is output an agent prints that is not an event and not worth a
// 'raw' card on the timeline (codex announces its stdin handling first).
var NoiseLines = map[string]bool{"Reading additional input from stdin...": true}

// ParseStreamLines turns whatever an agent wrote into events, returning the
// trailing partial line so the caller can resume mid-record next poll.
func ParseStreamLines(agent, buf string) ([]Event, string) {
	switch agent {
	case "codex":
		return parseJSONL(buf, normalizeCodex)
	case "gemini":
		return parsePlaintext(buf)
	default: // claude, and anything unknown
		return parseJSONL(buf, NormalizeClaude)
	}
}

// ParseTaskStreamLines applies a configured task output mode. Custom CLIs do
// not have an adapter-specific parser: plain output is one text event per line,
// while JSONL accepts the common Lectern event envelope and maps other useful
// message-shaped records to text.
func ParseTaskStreamLines(agent, mode, buf string) ([]Event, string) {
	if mode == "jsonl" {
		return parseJSONL(buf, NormalizeGeneric)
	}
	return parsePlaintext(buf)
}

// NormalizeGeneric preserves the stable Lectern event types when a CLI
// emits them, and gives ordinary JSONL responses a useful text timeline.
func NormalizeGeneric(raw map[string]any) []Event {
	t, _ := raw["type"].(string)
	switch t {
	case "init", "text", "tool_use", "tool_result", "result":
		return []Event{{Type: t, Payload: raw}}
	}
	for _, key := range []string{"text", "message", "content", "output"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return []Event{{Type: "text", Payload: map[string]any{"text": clip(value, 2000)}}}
		}
	}
	return []Event{{Type: "raw", Payload: map[string]any{"line": clip(stringValue(raw), 2000)}}}
}

func stringValue(raw map[string]any) string {
	b, _ := json.Marshal(raw)
	return string(b)
}

func parseJSONL(buf string, normalize func(map[string]any) []Event) ([]Event, string) {
	idx := strings.LastIndex(buf, "\n")
	if idx < 0 {
		return nil, buf
	}
	complete, remainder := buf[:idx], buf[idx+1:]
	events := []Event{}
	for _, line := range strings.Split(complete, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || NoiseLines[line] {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			events = append(events, Event{"raw", map[string]any{"line": clip(line, 2000)}})
			continue
		}
		events = append(events, normalize(raw)...)
	}
	return events, remainder
}

// parsePlaintext maps gemini's plain output: every line becomes a timeline line.
func parsePlaintext(buf string) ([]Event, string) {
	idx := strings.LastIndex(buf, "\n")
	if idx < 0 {
		return nil, buf
	}
	complete, remainder := buf[:idx], buf[idx+1:]
	events := []Event{}
	for _, line := range strings.Split(complete, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		events = append(events, Event{"text",
			map[string]any{"text": clip(strings.TrimRight(line, " \t\r"), 2000)}})
	}
	return events, remainder
}

// NormalizeClaude maps one `claude --output-format stream-json` record.
func NormalizeClaude(raw map[string]any) []Event {
	t, _ := raw["type"].(string)

	// housekeeping noise (rate-limit ticks, non-init system chatter) — the
	// timeline is for work, not for protocol bookkeeping
	if inner, ok := raw["data"].(map[string]any); ok {
		if it, _ := inner["type"].(string); it == "rate_limit_event" {
			return nil
		}
	}
	if t == "rate_limit_event" {
		return nil
	}
	if t == "system" {
		if sub, _ := raw["subtype"].(string); sub != "init" {
			return nil
		}
		tools, _ := raw["tools"].([]any)
		if len(tools) > 40 {
			tools = tools[:40]
		}
		return []Event{{"init", map[string]any{
			"session_id": str(raw["session_id"]),
			"model":      str(raw["model"]),
			"tools":      tools,
		}}}
	}
	if t == "assistant" {
		out := []Event{}
		for _, b := range contentBlocks(raw) {
			switch bt, _ := b["type"].(string); bt {
			case "text":
				if text := str(b["text"]); strings.TrimSpace(text) != "" {
					out = append(out, Event{"text", map[string]any{"text": text}})
				}
			case "tool_use":
				out = append(out, Event{"tool_use", map[string]any{
					"id": str(b["id"]), "name": str(b["name"]),
					"input": truncate(b["input"], 2000)}})
			}
		}
		return out
	}
	if t == "user" {
		out := []Event{}
		for _, b := range contentBlocks(raw) {
			if bt, _ := b["type"].(string); bt != "tool_result" {
				continue
			}
			content := ""
			switch c := b["content"].(type) {
			case string:
				content = c
			case []any:
				var sb strings.Builder
				for i, part := range c {
					if pm, ok := part.(map[string]any); ok {
						if i > 0 {
							sb.WriteString(" ")
						}
						sb.WriteString(str(pm["text"]))
					}
				}
				content = sb.String()
			}
			isErr, _ := b["is_error"].(bool)
			out = append(out, Event{"tool_result", map[string]any{
				"tool_use_id": str(b["tool_use_id"]),
				"content":     clip(content, 2000),
				"is_error":    isErr}})
		}
		return out
	}
	if t == "result" {
		return []Event{{"result", map[string]any{
			"subtype":     str(raw["subtype"]),
			"cost_usd":    raw["total_cost_usd"],
			"duration_ms": raw["duration_ms"],
			"num_turns":   raw["num_turns"],
			"result":      clip(str(raw["result"]), 4000),
			"session_id":  str(raw["session_id"]),
		}}}
	}
	return []Event{{"raw", map[string]any{"data": truncate(raw, 2000)}}}
}

// normalizeCodex maps one `codex exec --json` thread event.
func normalizeCodex(raw map[string]any) []Event {
	t, _ := raw["type"].(string)
	switch t {
	case "thread.started":
		return []Event{{"init", map[string]any{
			"session_id": str(raw["thread_id"]), "model": "codex", "tools": []any{}}}}
	case "turn.started", "thread.completed":
		return nil
	case "turn.completed":
		usage, _ := raw["usage"].(map[string]any)
		var tokens any
		if usage != nil {
			tokens = usage["output_tokens"]
		}
		return []Event{{"result", map[string]any{
			"subtype": "success", "cost_usd": nil, "num_turns": nil,
			"duration_ms": nil, "result": "", "session_id": "", "tokens": tokens}}}
	case "item.started", "item.completed":
		item, _ := raw["item"].(map[string]any)
		if item == nil {
			return nil
		}
		// item.started is what makes the timeline live: codex emits it when a
		// command begins, so the card shows work in flight instead of only after
		// it lands. Its exit_code is null until then — reading that as failure
		// once marked every in-flight command red.
		started := t == "item.started"
		switch it, _ := item["type"].(string); it {
		case "command_execution":
			if started {
				return []Event{{"tool_use", map[string]any{
					"id": str(item["id"]), "name": "Bash",
					"input": map[string]any{"command": str(item["command"])}}}}
			}
			return []Event{{"tool_result", map[string]any{
				"tool_use_id": str(item["id"]),
				"content":     clip(str(item["aggregated_output"]), 2000),
				"is_error":    numOr(item["exit_code"], 0) != 0}}}
		case "file_change":
			changes, _ := item["changes"].([]any)
			paths := make([]string, 0, len(changes))
			for _, c := range changes {
				if cm, ok := c.(map[string]any); ok {
					p := str(cm["path"])
					if p == "" {
						p = "?"
					}
					paths = append(paths, p)
				}
			}
			files := strings.Join(paths, ", ")
			if started {
				return []Event{{"tool_use", map[string]any{
					"id": str(item["id"]), "name": "Edit",
					"input": map[string]any{"file_path": files}}}}
			}
			return []Event{{"tool_result", map[string]any{
				"tool_use_id": str(item["id"]), "content": "changed: " + files,
				"is_error": str(item["status"]) == "failed"}}}
		case "agent_message":
			if started {
				return nil
			}
			if text := str(item["text"]); strings.TrimSpace(text) != "" {
				return []Event{{"text", map[string]any{"text": text}}}
			}
			return nil
		case "reasoning", "todo_list":
			return nil
		}
		return nil
	}
	return []Event{{"raw", map[string]any{"data": raw}}}
}

func contentBlocks(raw map[string]any) []map[string]any {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	list, _ := msg["content"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, b := range list {
		if bm, ok := b.(map[string]any); ok {
			out = append(out, bm)
		}
	}
	return out
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func numOr(v any, def float64) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return def
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// truncate keeps a payload bounded: an oversized blob is replaced by a marker
// rather than bloating every event row and SSE frame.
func truncate(obj any, limit int) any {
	b, err := json.Marshal(obj)
	if err != nil || len(b) <= limit {
		if obj == nil {
			return map[string]any{}
		}
		return obj
	}
	return map[string]any{"_truncated": string(b[:limit])}
}
