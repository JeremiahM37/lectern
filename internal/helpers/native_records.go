package helpers

// Port of internal/nativeidentity/native_records.py: the visible-message
// decoder shared by native history, structured chat and native search.

import (
	"os"
	"regexp"
	"strings"
)

var nativeUUID = regexp.MustCompile(`^[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}$`)

const nativeLimit = 2 * 1024 * 1024

// noLimit is native_record's limit=None.
const noLimit = -1

func strIn(v any, options ...string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, o := range options {
		if s == o {
			return true
		}
	}
	return false
}

// nativeContent is content(value).
func nativeContent(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	list, ok := value.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, item := range list {
		block, ok := item.(*nPyDict)
		if !ok {
			continue
		}
		kind := block.getOr("type", "")
		switch {
		case strIn(kind, "text", "input_text", "output_text"):
			if text, ok := block.getOr("text", "").(string); ok {
				out = append(out, text)
			}
		case strIn(kind, "tool_use"):
			out = append(out, "Tool: "+nPyStr(block.getOr("name", ""))+"\n"+jsonDumps(block.getOr("input", newDict()), false))
		case strIn(kind, "tool_result"):
			out = append(out, "Tool result:\n"+nativeContent(block.getOr("content", "")))
		case strIn(kind, "image", "input_image"):
			out = append(out, "[Image attachment]")
		}
	}
	return strings.Join(out, "\n")
}

// nativeRecord is native_record(row, agent, limit); nil is None.
func nativeRecord(row *nPyDict, agent string, limit int) *nPyDict {
	var role any
	var body string
	if agent == "codex" {
		p, ok := row.getOr("payload", newDict()).(*nPyDict)
		if !ok {
			return nil
		}
		if !strIn(row.getOr("type", nil), "response_item") {
			return nil
		}
		kind := p.getOr("type", nil)
		if channel := p.getOr("channel", nil); channel != nil && !strIn(channel, "", "final", "commentary") {
			return nil
		}
		switch {
		case strIn(kind, "message"):
			role, body = p.getOr("role", nil), nativeContent(p.getOr("content", nil))
		case strIn(kind, "function_call", "custom_tool_call"):
			role = "tool"
			body = nPyStr(p.getOr("name", "Tool")) + "\n" + nPyStr(p.getOr("arguments", p.getOr("input", "")))
		case strIn(kind, "function_call_output", "custom_tool_call_output"):
			role, body = "tool", nPyStr(p.getOr("output", ""))
		default:
			return nil
		}
	} else {
		if !strIn(row.getOr("type", nil), "user", "assistant") {
			return nil
		}
		if truthy(row.getOr("isSidechain", nil)) {
			return nil
		}
		msg, ok := row.getOr("message", newDict()).(*nPyDict)
		if !ok {
			return nil
		}
		role, body = msg.getOr("role", nil), nativeContent(msg.getOr("content", nil))
		if list, ok := msg.getOr("content", nil).([]any); ok && len(list) > 0 {
			all := true
			for _, item := range list {
				if b, ok := item.(*nPyDict); ok && !strIn(b.getOr("type", nil), "tool_result") {
					all = false
					break
				}
			}
			if all {
				role = "tool"
			}
		}
	}
	// System/developer context and private reasoning are not conversation prose.
	if !strIn(role, "user", "assistant", "tool") || body == "" {
		return nil
	}
	text, truncated := body, false
	if limit != noLimit {
		text, truncated = runePrefix(body, limit), runeLen(body) > limit
	}
	return dict("role", role, "text", text, "truncated", truncated, "timestamp", row.getOr("timestamp", ""))
}

func textBlock(role any, kind string, text any, limit int, timestamp any) *nPyDict {
	s, ok := text.(string)
	if !ok || nPyStrip(s) == "" {
		return nil
	}
	return dict("role", role, "kind", kind, "text", runePrefix(s, limit), "truncated", runeLen(s) > limit, "timestamp", timestamp)
}

func codexReasoningText(payload *nPyDict) string {
	// Codex rollouts carry a reasoning item's visible text as a list of
	// summary blocks; fall back to a raw content list on older shapes.
	for _, key := range []string{"summary", "content"} {
		blocks, ok := payload.getOr(key, nil).([]any)
		if !ok {
			continue
		}
		var parts []string
		for _, item := range blocks {
			if b, ok := item.(*nPyDict); ok {
				if text, ok := b.getOr("text", nil).(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// asInput is _as_input: tool input as an object the client renders field by
// field.
func asInput(raw any) any {
	if d, ok := raw.(*nPyDict); ok {
		return d
	}
	if s, ok := raw.(string); ok {
		if parsed, err := jsonLoadsStr(s); err == nil {
			if d, ok := parsed.(*nPyDict); ok {
				return d
			}
		}
	}
	if raw == nil || raw == "" {
		return newDict()
	}
	return dict("value", raw)
}

// structuredRecords is structured_records(row, agent, limit).
func structuredRecords(row *nPyDict, agent string, limit int) []*nPyDict {
	var out []*nPyDict
	add := func(item *nPyDict) {
		if item != nil {
			out = append(out, item)
		}
	}
	ts := row.getOr("timestamp", "")
	if agent == "codex" {
		p, ok := row.getOr("payload", newDict()).(*nPyDict)
		if !ok || !strIn(row.getOr("type", nil), "response_item") {
			return out
		}
		kind := p.getOr("type", nil)
		channel := p.getOr("channel", nil)
		if strIn(kind, "reasoning") {
			add(textBlock("assistant", "thinking", codexReasoningText(p), limit, ts))
			return out
		}
		if channel != nil && !strIn(channel, "", "final", "commentary") {
			return out
		}
		switch {
		case strIn(kind, "message"):
			role := p.getOr("role", nil)
			blocks, _ := p.getOr("content", nil).([]any)
			for _, item := range blocks {
				block, ok := item.(*nPyDict)
				if !ok {
					continue
				}
				if strIn(block.getOr("type", nil), "text", "input_text", "output_text") {
					add(textBlock(role, "text", block.getOr("text", ""), limit, ts))
				}
			}
		case strIn(kind, "function_call", "custom_tool_call"):
			raw := p.getOr("arguments", p.getOr("input", newDict()))
			out = append(out, dict("role", "assistant", "kind", "tool_use", "timestamp", ts,
				"tool_name", nPyStr(p.getOr("name", "Tool")), "tool_use_id", nPyStr(p.getOr("call_id", p.getOr("id", ""))),
				"input", asInput(raw)))
		case strIn(kind, "function_call_output", "custom_tool_call_output"):
			text := nPyStr(p.getOr("output", ""))
			out = append(out, dict("role", "tool", "kind", "tool_result", "timestamp", ts,
				"tool_use_id", nPyStr(p.getOr("call_id", p.getOr("id", ""))),
				"output", runePrefix(text, limit), "truncated", runeLen(text) > limit, "is_error", truthy(p.getOr("is_error", nil))))
		}
		return out
	}
	if !strIn(row.getOr("type", nil), "user", "assistant") || truthy(row.getOr("isSidechain", nil)) {
		return out
	}
	msg, ok := row.getOr("message", newDict()).(*nPyDict)
	if !ok {
		return out
	}
	role, body := msg.getOr("role", nil), msg.getOr("content", nil)
	switch b := body.(type) {
	case string:
		add(textBlock(role, "text", b, limit, ts))
	case []any:
		for _, item := range b {
			block, ok := item.(*nPyDict)
			if !ok {
				continue
			}
			bkind := block.getOr("type", nil)
			switch {
			case strIn(bkind, "text", "input_text", "output_text"):
				add(textBlock(role, "text", block.getOr("text", ""), limit, ts))
			case strIn(bkind, "thinking"):
				add(textBlock("assistant", "thinking", block.getOr("thinking", block.getOr("text", "")), limit, ts))
			case strIn(bkind, "tool_use"):
				out = append(out, dict("role", "assistant", "kind", "tool_use", "timestamp", ts,
					"tool_name", nPyStr(block.getOr("name", "Tool")), "tool_use_id", nPyStr(block.getOr("id", "")),
					"input", asInput(block.getOr("input", newDict()))))
			case strIn(bkind, "tool_result"):
				text := nativeContent(block.getOr("content", ""))
				out = append(out, dict("role", "tool", "kind", "tool_result", "timestamp", ts,
					"tool_use_id", nPyStr(block.getOr("tool_use_id", "")),
					"output", runePrefix(text, limit), "truncated", runeLen(text) > limit, "is_error", truthy(block.getOr("is_error", nil))))
			case strIn(bkind, "image", "input_image"):
				add(textBlock(role, "text", "[Image attachment]", limit, ts))
			}
		}
	}
	return out
}

// nativeMetadata is native_metadata(file, agent, workspace, max_bytes);
// workspace nil is None. A nil dict with a nil error is None.
func nativeMetadata(file, agent string, workspace *string, maxBytes int64) (*nPyDict, error) {
	f, err := openPy(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cid, cwd any = "", ""
	title := ""
	codexHeaderSeen := false
	for n := 0; n < 80; n++ {
		if f.pos >= maxBytes {
			break
		}
		line, err := f.readline(maxBytes + 1)
		if err != nil {
			return nil, err
		}
		if len(line) == 0 || int64(len(line)) > maxBytes {
			break
		}
		v, err := jsonLoadsBytes(line)
		if err != nil {
			continue
		}
		row, ok := v.(*nPyDict)
		if !ok {
			continue
		}
		if agent == "codex" && strIn(row.getOr("type", nil), "session_meta") && !codexHeaderSeen {
			// Forks copy the parent's header into their history. The first
			// native header belongs to this file; later headers are context.
			codexHeaderSeen = true
			p, ok := row.getOr("payload", newDict()).(*nPyDict)
			if !ok {
				continue
			}
			cid, cwd = p.getOr("id", p.getOr("session_id", "")), p.getOr("cwd", "")
		} else if agent == "claude" {
			if !truthy(cid) {
				cid = row.getOr("sessionId", "")
			}
			if !truthy(cwd) {
				cwd = row.getOr("cwd", "")
			}
		}
		item := nativeRecord(row, agent, 64000)
		if item != nil && strIn(item.vals["role"], "user") && title == "" {
			title = runePrefix(strings.ReplaceAll(nPyStrip(item.vals["text"].(string)), "\n", " "), 160)
		}
		if truthy(cid) && truthy(cwd) && title != "" {
			break
		}
	}
	id, ok := cid.(string)
	dir, dirOK := cwd.(string)
	if !ok || !nativeUUID.MatchString(id) || !dirOK || dir == "" {
		return nil, nil
	}
	dir = realpathLoose(dir)
	if workspace != nil && dir != *workspace {
		return nil, nil
	}
	fi, err := os.Stat(file)
	if err != nil {
		return nil, osError(err, file)
	}
	if title == "" {
		title = "Saved conversation"
	}
	return dict("id", id, "title", title, "modified", statMtime(fi), "agent", agent, "cwd", dir), nil
}
