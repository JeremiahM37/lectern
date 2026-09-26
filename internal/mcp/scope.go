package mcp

import "github.com/JeremiahM37/lectern/v2/internal/oauth"

// toolScope maps every advertised tool — except decide_approval, which is
// never reachable over the web connector at all (see http.go) — to the
// OAuth scope a web-connector session needs to call it. Only checked for a
// request that carries OAuth scopes at all (internal/mcp/http.go); stdio and
// a static bearer caller are the existing local-trust surfaces and stay
// unrestricted, exactly as they were before this feature existed.
//
// Table-driven and checked by TestEveryToolHasAScope: a tool that ships
// unclassified here would otherwise be silently unreachable over the web
// connector (present in Tools() but missing from every filtered
// tools/list), which reads as a bug report nobody could reproduce without
// knowing to look here.
var toolScope = map[string]string{
	// Reads: the board, as it stands right now. None of these can change
	// what's on it.
	"list_sessions":     oauth.ScopeRead,
	"list_projects":     oauth.ScopeRead,
	"list_tasks":        oauth.ScopeRead,
	"board_summary":     oauth.ScopeRead,
	"task_status":       oauth.ScopeRead,
	"task_diff":         oauth.ScopeRead,
	"pending_approvals": oauth.ScopeRead,
	"active_work":       oauth.ScopeRead,
	"list_claims":       oauth.ScopeRead,
	"wait_build":        oauth.ScopeRead,

	// Writes: start a session, message into one, file or dispatch work, or
	// move a task/build forward. decide_approval is deliberately absent from
	// this map — it is excluded from the web connector unconditionally, not
	// gated by scope, because an approval decision must come from the human.
	"start_session":   oauth.ScopeWrite,
	"send_to_session": oauth.ScopeWrite,
	"create_task":     oauth.ScopeWrite,
	"delegate_build":  oauth.ScopeWrite,
	"complete_task":   oauth.ScopeWrite,
	"request_changes": oauth.ScopeWrite,
	"accept_build":    oauth.ScopeWrite,
	"claim_work":      oauth.ScopeWrite,
	"release_work":    oauth.ScopeWrite,
	"post_media":      oauth.ScopeWrite,
	"open_live_view":  oauth.ScopeWrite,
}

// webExcluded are tools never exposed over the web connector transport, in
// tools/list or tools/call, no matter what scope a token carries. This is
// the whole point of the split from toolScope: a scope check can only ever
// narrow what a granted token may do, so a tool that must never reach a
// remote LLM — an approval decision, which the contract requires a human
// for — has to be refused before any scope is even consulted.
var webExcluded = map[string]bool{
	"decide_approval": true,
}

// scopeFor reports the scope a tool needs, and whether it is classified at
// all — every entry in Tools() other than webExcluded must be.
func scopeFor(name string) (string, bool) {
	if s, ok := toolScope[name]; ok {
		return s, true
	}
	// Fail closed: a tool nobody classified (a new one, or a typo in this
	// table) needs the write scope rather than slipping past a read-only
	// token. TestEveryToolHasAWebScope keeps the table complete.
	return oauth.ScopeWrite, true
}

// filterToolsByScope keeps only the tools granted covers, after webExcluded
// has already removed the tools no web session may ever see. Used for
// tools/list on a scoped (OAuth) session; nil granted means unrestricted and
// is never passed through this function — see http.go.
func filterToolsByScope(all []map[string]any, granted []string) []map[string]any {
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		name, _ := t["name"].(string)
		need, ok := scopeFor(name)
		if !ok || scopeGrantedLocal(granted, need) {
			out = append(out, t)
		}
	}
	return out
}

// filterWebExcluded drops webExcluded tools from a tools/list result,
// unconditionally — applied to every web-transport request regardless of
// auth kind (static bearer or OAuth), never only to a scoped one.
func filterWebExcluded(all []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		name, _ := t["name"].(string)
		if !webExcluded[name] {
			out = append(out, t)
		}
	}
	return out
}

// scopeGrantedLocal avoids importing oauth's unexported scopeGranted helper
// a second time; trivial enough to keep local rather than exporting it just
// for this one call site.
func scopeGrantedLocal(granted []string, need string) bool {
	for _, g := range granted {
		if g == need {
			return true
		}
	}
	return false
}
