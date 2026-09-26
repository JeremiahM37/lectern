package mcp

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// end_session is the clean-up half of start_session: a chat that started a
// session can also finish it. It archives with stop=true — the terminal ends,
// but the record and its terminal snapshot stay, and the session can be
// restored from Lectern — so nothing is deleted outright from a remote client.
func init() {
	tools = append(tools, tool{
		Name: "end_session",
		Description: "End an interactive Lectern session you no longer need — e.g. the test or build session a chat " +
			"started. `session` is a session id or its exact/unique name (see list_sessions); an ambiguous or " +
			"unknown name errors with the candidates instead of guessing. This stops the agent's terminal and " +
			"ARCHIVES the session: its record and final terminal output are kept and it can be restored from " +
			"Lectern, so nothing is permanently deleted. Confirm with the user which session before calling it.",
		Schema: obj(map[string]any{
			"session": str("session id, or its exact/unique name"),
		}, "session"),
		Run: func(s *Server, args map[string]any) (any, error) {
			ref := strings.TrimSpace(argStr(args, "session"))
			sess, err := s.resolveSessionRef(ref)
			if err != nil {
				return nil, err
			}
			// resolveSessionRef also accepts a unique partial name, which is
			// fine for sending a message but too loose for ending a session:
			// "chat" must not quietly end "chat-test". Require the id or the
			// exact name.
			if _, numeric := strconv.ParseInt(ref, 10, 64); numeric != nil {
				if name, _ := sess["name"].(string); !strings.EqualFold(name, ref) {
					return nil, fmt.Errorf("%q only partly matches session %q — to end it, pass its exact name or id", ref, name)
				}
			}
			id := fmt.Sprint(sess["id"])
			if f, ok := sess["id"].(float64); ok {
				id = fmt.Sprintf("%d", int64(f))
			}
			out, err := s.api("POST", "/sessions/"+url.PathEscape(id)+"/archive", map[string]any{"stop": true})
			if err != nil {
				return nil, err
			}
			return map[string]any{"ended": true, "archived": true, "id": id, "name": sess["name"],
				"note": "terminal stopped; record and final output kept — restore it from Lectern if needed",
				"result": out}, nil
		},
	})
}
