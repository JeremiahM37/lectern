package mcp

// enable_remote_control: make a running claude session reachable from
// claude.ai/code and the Claude app as well (docs/remote-control.md).

import "fmt"

func init() {
	tools = append(tools, tool{
		Name: "enable_remote_control",
		Description: "Turn on Claude Remote Control for an ALREADY-RUNNING claude session, so the owner can also " +
			"open it from claude.ai/code or the Code tab of the Claude mobile app. It stays a normal Lectern session: " +
			"send_to_session, approvals and restore keep working, and restores, forks and switches keep it on. " +
			"`session` is an id or exact/unique name (see list_sessions). Only for the built-in claude agent with a " +
			"claude.ai login; refused while the agent is mid-turn or waiting on a prompt (try again when it is " +
			"waiting for input), and the refusal says why. To start a new session with it on, pass " +
			"remote_control:true to start_session instead.",
		Schema: obj(map[string]any{
			"session": str("session id, or its exact/unique name"),
		}, "session"),
		Run: func(s *Server, args map[string]any) (any, error) {
			sess, err := s.resolveSessionRef(argStr(args, "session"))
			if err != nil {
				return nil, err
			}
			id := int64(sess["id"].(float64))
			raw, err := s.api("POST", fmt.Sprintf("/sessions/%d/remote-control", id), map[string]any{})
			if err != nil {
				return nil, err
			}
			row, _ := raw.(map[string]any)
			out := map[string]any{"session_id": id, "name": row["name"], "remote_control": row["remote_control"]}
			if url, _ := row["remote_control_url"].(string); url != "" {
				out["url"] = url
			} else {
				out["note"] = "connected; the claude.ai link appears in list_sessions once Lectern sees it on the screen"
			}
			return out, nil
		},
	})
}
