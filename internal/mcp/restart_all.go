package mcp

// restart_all_sessions: restart every session in place with the same
// conversation (docs/sessions-restart.md). Not on the web connector
// (webExcluded).

func init() {
	tools = append(tools, tool{
		Name: "restart_all_sessions",
		Description: "Restart every Lectern session in place: each agent is stopped and started again resuming the " +
			"SAME conversation, with its name, project, worktree and launch settings (claude sessions come back " +
			"with Remote Control on when they can use it). Idle sessions restart one at a time in the background; a " +
			"session that is working, asking a question or has text typed at its prompt is restarted once it waits " +
			"for input, never mid-turn. Shells and sessions whose conversation cannot be resumed are skipped with " +
			"the reason. dry_run:true only reports what would happen. Call list_sessions or this tool with " +
			"dry_run:true afterwards to follow it.",
		Schema: obj(map[string]any{
			"dry_run": flag("only report what would happen (default false)"),
		}),
		Run: func(s *Server, args map[string]any) (any, error) {
			if argBool(args, "dry_run", false) {
				return s.api("GET", "/restart-all", nil)
			}
			return s.api("POST", "/restart-all", map[string]any{})
		},
	})
}
