package mcp

// read_session: what a coding agent in a running session has been saying and
// doing, as compact structured turns. Reads the agent's saved conversation
// (Claude/Codex) through the same endpoint the Chat view uses and falls back
// to the terminal screen for every other agent.

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
)

const (
	readDefaultTurns = 30
	readMaxTurns     = 200
	readDefaultChars = 16000 // about 4k tokens
	readTextChars    = 1500  // one user/assistant message
	readToolChars    = 300   // one tool call or tool result
	readScreenLines  = 40
)

func init() {
	tools = append(tools, tool{
		Name: "read_session",
		Description: "Read what the agent in a running Lectern session has been saying and doing: recent turns " +
			"(user, assistant, and one-line tool calls/results) plus status (working / idle / ended / " +
			"waiting_for_approval), project, agent and branch. `session` is an id or exact/unique name (see " +
			"list_sessions). Returns the newest `limit` turns (default 30, within about 4k tokens) oldest-first; " +
			"long text and tool output are truncated and secrets redacted. To page back in time pass `skip` = " +
			"the `next_skip` of the previous call. Agents without a saved transcript fall back to the last " +
			"lines of the terminal screen (`source: screen`). Read-only.",
		Schema: obj(map[string]any{
			"session":   str("session id, or its exact/unique name"),
			"limit":     num("how many recent turns to return (default 30, max 200)"),
			"skip":      num("turns to skip from the newest end, for paging back (default 0)"),
			"max_chars": num("total character budget for the turns (default 16000)"),
			"screen":    flag("also include the last ~40 lines of the terminal screen (default false; always used as fallback)"),
		}, "session"),
		Run: func(s *Server, args map[string]any) (any, error) { return s.readSession(args) },
	})
}

func clip(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]) + " …", true
}

func (s *Server) readSession(args map[string]any) (any, error) {
	sess, err := s.resolveSessionRef(argStr(args, "session"))
	if err != nil {
		return nil, err
	}
	id := int64(sess["id"].(float64))
	limit := int(argInt(args, "limit"))
	if limit <= 0 {
		limit = readDefaultTurns
	}
	if limit > readMaxTurns {
		limit = readMaxTurns
	}
	skip := int(argInt(args, "skip"))
	if skip < 0 {
		skip = 0
	}
	budget := int(argInt(args, "max_chars"))
	if budget <= 0 {
		budget = readDefaultChars
	}

	ended := sess["ended_at"] != nil || sess["status"] == "dead"
	out := map[string]any{
		"session_id": id, "name": sess["name"], "agent": sess["agent"],
		"project_id": sess["project_id"], "workdir": sess["workdir"],
	}
	if g, err := s.object(fmt.Sprintf("/sessions/%d/git", id)); err == nil && g != nil {
		out["branch"] = g["branch"]
	}

	screen := ""
	if !ended {
		if r, err := s.object(fmt.Sprintf("/sessions/%d/reader", id)); err == nil && r != nil {
			screen, _ = r["text"].(string)
		}
	} else if t, _ := sess["pane_tail"].(string); t != "" {
		screen = t
	}
	screenTail := ciloop.Redact(lastLines(screen, readScreenLines))
	out["status"] = sessionStatus(sess, ended, screenTail)
	if ended {
		out["note"] = "session has ended; showing its saved conversation if any"
	}

	items := []map[string]any(nil)
	if raw, err := s.object(fmt.Sprintf("/sessions/%d/conversation/live", id)); err == nil {
		if arr, ok := raw["items"].([]any); ok {
			for _, it := range arr {
				if m, ok := it.(map[string]any); ok {
					items = append(items, m)
				}
			}
		}
	}

	if len(items) == 0 {
		out["source"] = "screen"
		out["screen"] = screenTail
		return out, nil
	}

	turns := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if t := turnOf(it); t != nil {
			turns = append(turns, t)
		}
	}
	total := len(turns)
	end := total - skip
	if end < 0 {
		end = 0
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	// Spend the character budget from the newest turn backwards.
	used := 0
	for i := end - 1; i >= start; i-- {
		used += len(fmt.Sprint(turns[i]["text"])) + 20
		if used > budget && i < end-1 {
			start = i + 1
			break
		}
	}
	page := turns[start:end]
	out["source"] = "conversation"
	out["turns"] = page
	out["turn_count"] = len(page)
	if start > 0 {
		out["has_more"] = true
		out["next_skip"] = total - start
	}
	if argBool(args, "screen", false) {
		out["screen"] = screenTail
	}
	return out, nil
}

// turnOf compresses one structured conversation item to {role, text}.
func turnOf(it map[string]any) map[string]any {
	role, _ := it["role"].(string)
	kind, _ := it["kind"].(string)
	var text string
	limit := readTextChars
	switch kind {
	case "tool_use":
		role = "tool_call"
		name, _ := it["tool_name"].(string)
		text = name + " " + briefInput(it["input"])
		limit = readToolChars
	case "tool_result":
		role = "tool_result"
		text, _ = it["output"].(string)
		limit = readToolChars
	default:
		text, _ = it["text"].(string)
	}
	text = strings.TrimSpace(ciloop.Redact(text))
	if text == "" {
		return nil
	}
	text, _ = clip(text, limit)
	t := map[string]any{"role": role, "text": text}
	if ts, _ := it["timestamp"].(string); ts != "" {
		t["at"] = ts
	}
	return t
}

func briefInput(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprint(v)
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url", "description", "prompt"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return fmt.Sprint(m)
}

func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n ")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// sessionStatus maps Lectern's state onto working / idle / ended /
// waiting_for_approval. Approval prompts are only visible on the screen.
func sessionStatus(sess map[string]any, ended bool, screen string) string {
	if ended {
		return "ended"
	}
	low := strings.ToLower(screen)
	if strings.Contains(low, "do you want to") || strings.Contains(low, "❯ 1. yes") ||
		strings.Contains(low, "allow this") || strings.Contains(low, "(y/n)") {
		return "waiting_for_approval"
	}
	if st, _ := sess["state"].(string); st != "" {
		return st
	}
	if st, _ := sess["status"].(string); st != "" {
		return st
	}
	return "unknown"
}
