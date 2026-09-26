package mcp

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// restore_session is end_session's way back: it reopens a session that was
// ended, archived, exited, or interrupted by a restart, through the same
// POST /sessions/{id}/reopen the Restore list uses. Without a session it lists
// what can be restored, so a chat can ask which one.
func init() {
	tools = append(tools, tool{
		Name: "restore_session",
		Description: "Bring back a Lectern session that was ended (including by end_session), archived, exited, or " +
			"interrupted by a restart. Omit `session` to list restorable sessions, newest first, with what reopening " +
			"each would do. `session` is an id or the exact name of a restorable session. Lectern picks the right " +
			"way back: it resumes the saved conversation when one is bound, tracks a still-running terminal again, or " +
			"starts fresh in the same folder and says so. Give `agent` and/or `model` to continue it in a different " +
			"agent instead, primed with its last handoff or the end of its conversation.",
		Schema: obj(map[string]any{
			"session": str("id or exact name of a restorable session; omit to list them"),
			"agent":   str("optional: continue in this agent instead (e.g. codex)"),
			"model":   str("optional: continue with this model instead"),
		}),
		Run: func(s *Server, args map[string]any) (any, error) {
			ref := strings.TrimSpace(argStr(args, "session"))
			raw, err := s.api("GET", "/sessions/restorable?limit=100", nil)
			if err != nil {
				return nil, err
			}
			rows, _ := raw.([]any)
			brief := func(row map[string]any) map[string]any {
				return map[string]any{"id": row["id"], "name": row["name"], "project": row["project_name"],
					"agent": row["agent"], "why": row["reason_label"], "reopen_does": row["action_label"],
					"note": row["note"], "last_message": row["preview"]}
			}
			if ref == "" {
				list := []map[string]any{}
				for i, r := range rows {
					if i == 20 {
						break
					}
					if row, ok := r.(map[string]any); ok {
						list = append(list, brief(row))
					}
				}
				return map[string]any{"restorable": list}, nil
			}
			var match map[string]any
			var named []string
			for _, r := range rows {
				row, ok := r.(map[string]any)
				if !ok {
					continue
				}
				id := strconv.FormatInt(int64(idNum(row["id"])), 10)
				name, _ := row["name"].(string)
				if id == strings.TrimPrefix(ref, "#") {
					match = row
					break
				}
				if strings.EqualFold(name, ref) {
					named = append(named, "#"+id)
					if match == nil {
						match = row
					}
				}
			}
			if len(named) > 1 {
				return nil, fmt.Errorf("%d restorable sessions are named %q (%s) — pass the id", len(named), ref, strings.Join(named, ", "))
			}
			if match == nil {
				return nil, fmt.Errorf("no restorable session has id or exact name %q; call restore_session without a session to list them", ref)
			}
			body := map[string]any{}
			if agent := strings.TrimSpace(argStr(args, "agent")); agent != "" {
				body["agent"] = agent
			}
			if model := strings.TrimSpace(argStr(args, "model")); model != "" {
				body["model"] = model
			}
			id := strconv.FormatInt(int64(idNum(match["id"])), 10)
			out, err := s.api("POST", "/sessions/"+url.PathEscape(id)+"/reopen", body)
			if err != nil {
				return nil, err
			}
			result, _ := out.(map[string]any)
			session, _ := result["session"].(map[string]any)
			return map[string]any{"restored": true, "from": brief(match), "session_id": session["id"],
				"name": session["name"], "status": session["status"], "message": result["message"]}, nil
		},
	})
}

func idNum(v any) float64 {
	f, _ := v.(float64)
	return f
}
