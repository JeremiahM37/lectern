package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/delegation"
	"github.com/JeremiahM37/lectern/v2/internal/mediapost"
)

// localGitCommonDir runs git LOCALLY, on the machine this MCP process is
// itself running on (unlike everything else in this package, which only
// ever talks to lectern's own HTTP API) — it is the one way the
// `active_work` tool can resolve a repo_path argument to the same
// git-common-dir string internal/awareness.ResolveRepoKey computes
// server-side, without lectern's control plane needing to know anything
// about this filesystem. See GET /api/peers?common_dir=... and
// awareness.PeersForCommonDir for the matching server-side half.
func localGitCommonDir(repoPath string) (string, bool) {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", false
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return "", false
	}
	return commonDir, true
}

// tool is one MCP tool: its schema, and what it does.
type tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         func(s *Server, args map[string]any) (any, error)
}

func obj(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

func argStr(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func argInt(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func argBool(args map[string]any, key string, def bool) bool {
	if b, ok := args[key].(bool); ok {
		return b
	}
	return def
}

// tools is the whole surface an MCP client sees.
var tools = []tool{
	{
		Name: "post_media",
		Description: "Show the operator something in Lectern's Media feed: a screen recording or demo video, " +
			"a screenshot, a generated file or report (HTML renders in place), or a link to a site or dev server " +
			"you are running. Use it to prove work with evidence instead of describing it. Give exactly one of " +
			"path (a local file; it is copied, so it outlives your worktree) or url. The post is attached to " +
			"your own session automatically.",
		Schema: obj(map[string]any{
			"path":       str("absolute path of a local file to post: mp4/webm video, image, pdf, html, log, any file"),
			"url":        str("http(s) address to post instead of a file, e.g. the dev server you started"),
			"title":      str("short headline, e.g. 'Split view working end to end'"),
			"note":       str("what this shows and what to look for"),
			"session_id": num("Lectern session to attach to; omit to use the session you are running in"),
		}, "title"),
		Run: func(s *Server, args map[string]any) (any, error) {
			raw, err := mediapost.Send(s.API, s.Token, mediapost.Post{Path: argStr(args, "path"),
				URL: argStr(args, "url"), Title: argStr(args, "title"), Note: argStr(args, "note"),
				SessionID: argInt(args, "session_id"), Source: "mcp"})
			if err != nil {
				return nil, err
			}
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				return nil, err
			}
			return row, nil
		},
	},
	{
		Name: "open_live_view",
		Description: "Give the operator a live view of a real desktop on the machine you are running on, shown in " +
			"Lectern's Media view. Use it when they want to WATCH something happen rather than read about it: a " +
			"browser automation in headed mode, a GUI app, a visual test. It starts a private virtual display and " +
			"returns its DISPLAY value; run your tool with that in its environment (for example " +
			"`DISPLAY=:91 your-tool --headed`) and it draws where the operator is watching. Pass url to also open a " +
			"browser there — that browser sees this machine's own localhost, so it is also how the operator reaches a " +
			"web app that only listens on 127.0.0.1. The view closes when your session ends.",
		Schema: obj(map[string]any{
			"title":      str("what the operator is about to watch, e.g. 'Quotation replay in headed mode'"),
			"url":        str("optional http(s) address to open in a browser on the desktop, e.g. http://127.0.0.1:18080"),
			"session_id": num("Lectern session to attach to; omit to use the session you are running in"),
		}, "title"),
		Run: func(s *Server, args map[string]any) (any, error) {
			body := map[string]any{"title": argStr(args, "title"), "url": argStr(args, "url"),
				"session_id": argInt(args, "session_id")}
			if argInt(args, "session_id") == 0 {
				body["hint_session_id"] = mediapost.SessionID()
				body["tmux_session"] = mediapost.TmuxSession()
			}
			raw, err := s.api("POST", "/live/desktops", body)
			if err != nil {
				return nil, err
			}
			view, _ := raw.(map[string]any)
			detail, _ := view["detail"].(map[string]any)
			display, _ := detail["display"].(string)
			view["how_to_use"] = "Run anything that opens a window with DISPLAY=" + display +
				" in its environment. The operator is watching that display in Lectern → Media."
			return view, nil
		},
	},
	{
		Name: "active_work",
		Description: "See what OTHER agents (Claude Code, Codex, or anything else running as a Lectern " +
			"session or task) are doing RIGHT NOW in the repository you are working in: their name, agent, " +
			"branch, current state, last prompt, and files they edited recently. Call this before starting " +
			"any non-trivial change so you don't duplicate or collide with work another agent already has in " +
			"flight — two agents independently building the same feature is exactly what this is for. With no " +
			"arguments it looks at the repository of the session you are running in; pass repo_path to check a " +
			"different checkout (for an agent with no Lectern session context of its own).",
		Schema: obj(map[string]any{
			"repo_path": str("absolute path inside a git checkout to inspect instead of your own session's repository"),
		}),
		Run: func(s *Server, args map[string]any) (any, error) {
			repoPath := argStr(args, "repo_path")
			var result map[string]any
			var err error
			if repoPath != "" {
				commonDir, ok := localGitCommonDir(repoPath)
				if !ok {
					return nil, fmt.Errorf("%s does not look like a git checkout", repoPath)
				}
				result, err = s.object("/peers?common_dir=" + url.QueryEscape(commonDir))
			} else {
				sid := mediapost.SessionID()
				if sid == 0 {
					return nil, fmt.Errorf("no LECTERN_SESSION_ID in this environment; pass repo_path instead")
				}
				result, err = s.object(fmt.Sprintf("/sessions/%d/peers", sid))
			}
			if err != nil {
				return nil, err
			}
			peers, _ := result["peers"].([]any)
			if len(peers) == 0 {
				return map[string]any{"peers": []any{}, "summary": "No other agents are currently working in this repository."}, nil
			}
			return result, nil
		},
	},
	{
		Name: "claim_work",
		Description: "Claim a piece of work in this repository so other agents (Claude Code, Codex, ACP " +
			"agents) — and humans — see you're on it instead of duplicating it. scope_kind is `task` " +
			"(scope is a task id you're picking up), `paths` (scope is one or more path globs, e.g. " +
			"'frontend/src/sessions/**'), or `topic` (scope is a short free-text description, e.g. " +
			"'rename button in session card'). Call active_work / list_claims first to check nobody is " +
			"already on it. The claim expires in ttl_minutes (default 120) unless you keep working — " +
			"activity renews it automatically — and release_work ends it early.",
		Schema: obj(map[string]any{
			"scope_kind":  str("task | paths | topic"),
			"scope":       str("a task id (scope_kind=task) or a short topic (scope_kind=topic); omit for scope_kind=paths"),
			"paths":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "one or more path globs, only for scope_kind=paths, e.g. ['frontend/src/sessions/**']"},
			"intent":      str("what you're doing, shown to anyone who sees this claim"),
			"ttl_minutes": num("minutes before this claim auto-expires if you go idle (default 120)"),
		}, "scope_kind"),
		Run: func(s *Server, args map[string]any) (any, error) {
			sid := mediapost.SessionID()
			if sid == 0 {
				return nil, fmt.Errorf("no LECTERN_SESSION_ID in this environment; claim_work needs a Lectern session to attribute the claim to")
			}
			body := map[string]any{
				"session_id": sid, "scope_kind": argStr(args, "scope_kind"),
				"scope": argStr(args, "scope"), "intent": argStr(args, "intent"),
				"ttl_minutes": argInt(args, "ttl_minutes"),
			}
			if raw, ok := args["paths"].([]any); ok {
				paths := make([]string, 0, len(raw))
				for _, p := range raw {
					if str, ok := p.(string); ok {
						paths = append(paths, str)
					}
				}
				body["paths"] = paths
			}
			return s.api("POST", "/claims", body)
		},
	},
	{
		Name: "release_work",
		Description: "Release a claim you made with claim_work — call this as soon as you're done with " +
			"that work, don't wait for it to expire. Give claim_id for one specific claim, or all:true to " +
			"release every claim your session currently holds.",
		Schema: obj(map[string]any{
			"claim_id": num("id of the claim to release (from claim_work's result or list_claims)"),
			"all":      flag("release every claim this session holds instead of one"),
		}),
		Run: func(s *Server, args map[string]any) (any, error) {
			if argBool(args, "all", false) {
				sid := mediapost.SessionID()
				if sid == 0 {
					return nil, fmt.Errorf("no LECTERN_SESSION_ID in this environment; pass claim_id instead")
				}
				mine, err := s.list(fmt.Sprintf("/claims?session_id=%d", sid))
				if err != nil {
					return nil, err
				}
				released := 0
				for _, c := range mine {
					id, _ := c["id"].(float64)
					if id == 0 {
						continue
					}
					if _, err := s.api("DELETE", fmt.Sprintf("/claims/%d", int64(id)), nil); err == nil {
						released++
					}
				}
				return map[string]any{"released": released}, nil
			}
			id := argInt(args, "claim_id")
			if id == 0 {
				return nil, fmt.Errorf("give claim_id, or all:true to release every claim this session holds")
			}
			return s.api("DELETE", fmt.Sprintf("/claims/%d", id), nil)
		},
	},
	{
		Name: "list_claims",
		Description: "List active claims — what other agents (and humans) have already claimed. With no " +
			"arguments, lists claims in the repository of the session you're running in; pass repo (a " +
			"repo_key, from active_work's peer data) to check a different one, or omit both to see every " +
			"active claim on the whole board.",
		Schema: obj(map[string]any{
			"repo": str("a repo_key to filter to, e.g. from active_work's result; omit to use your own session's repo"),
		}),
		Run: func(s *Server, args map[string]any) (any, error) {
			path := "/claims"
			repo := argStr(args, "repo")
			if repo == "" {
				if sid := mediapost.SessionID(); sid != 0 {
					if sess, err := s.object(fmt.Sprintf("/sessions/%d", sid)); err == nil {
						repo, _ = sess["repo_key"].(string)
					}
				}
			}
			if repo != "" {
				path += "?repo_key=" + url.QueryEscape(repo)
			}
			rows, err := s.list(path)
			if err != nil {
				return nil, err
			}
			return rows, nil
		},
	},
	{
		Name:        "board_summary",
		Description: "Current board state: task counts per column and the pending approval count.",
		Schema:      obj(map[string]any{}),
		Run: func(s *Server, _ map[string]any) (any, error) {
			return s.object("/health")
		},
	},
	{
		Name:        "list_projects",
		Description: "Projects that tasks can be filed against (name, target, repo).",
		Schema:      obj(map[string]any{}),
		Run: func(s *Server, _ map[string]any) (any, error) {
			rows, err := s.list("/projects")
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(rows))
			for _, p := range rows {
				out = append(out, map[string]any{"id": p["id"], "name": p["name"],
					"target": p["target_name"], "repo": p["repo_path"]})
			}
			return out, nil
		},
	},
	{
		Name: "list_tasks",
		Description: "List tasks, optionally filtered by status " +
			"(backlog|queued|running|review|done|failed|cancelled).",
		Schema: obj(map[string]any{"status": str("status to filter by")}),
		Run: func(s *Server, args map[string]any) (any, error) {
			path := "/tasks"
			if status := argStr(args, "status"); status != "" {
				path += "?status=" + url.QueryEscape(status)
			}
			rows, err := s.list(path)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(rows))
			for _, t := range rows {
				out = append(out, map[string]any{"id": t["id"], "title": t["title"],
					"status": t["status"], "project": t["project_name"],
					"target": t["target_name"], "created_by": t["created_by"]})
			}
			return out, nil
		},
	},
	{
		Name: "create_task",
		Description: "File a coding task. `project` is a project NAME (see list_projects). " +
			"dispatch=true starts an agent immediately; false parks it in the backlog. " +
			"orchestrate=true makes it an orchestrated build: a lead plans the prompt, hands the " +
			"implementation to the delegated-build worker, reviews and integrates it (needs Delegated builds ON).",
		Schema: obj(map[string]any{
			"project":         str("project name"),
			"title":           str("short card title"),
			"prompt":          str("what the agent should do"),
			"dispatch":        flag("start an agent now (default true)"),
			"orchestrate":     flag("run it as an orchestrated build: lead plans, worker builds, lead reviews (default false)"),
			"model":           str("model override, e.g. sonnet"),
			"agent":           str("agent name from the registry (default: the project's)"),
			"permission_mode": str("default|acceptEdits|plan|bypassPermissions"),
		}, "project", "title", "prompt"),
		Run: func(s *Server, args map[string]any) (any, error) {
			name := argStr(args, "project")
			projects, err := s.list("/projects")
			if err != nil {
				return nil, err
			}
			var match map[string]any
			var names []string
			for _, p := range projects {
				pn, _ := p["name"].(string)
				names = append(names, pn)
				if pn == name {
					match = p
				}
			}
			if match == nil {
				return nil, fmt.Errorf("no project named %q — have: %s",
					name, strings.Join(names, ", "))
			}
			mode := argStr(args, "permission_mode")
			if mode == "" {
				mode = "acceptEdits"
			}
			body := map[string]any{
				"project_id": match["id"], "title": argStr(args, "title"),
				"prompt": argStr(args, "prompt"), "model": argStr(args, "model"),
				"permission_mode": mode}
			if on, _ := args["orchestrate"].(bool); on {
				body["orchestrate"] = true
			}
			if agent := argStr(args, "agent"); agent != "" {
				body["agent"] = agent
			}
			created, err := s.api("POST", "/tasks", body)
			if err != nil {
				return nil, err
			}
			task, _ := created.(map[string]any)
			id := int64(task["id"].(float64))
			if argBool(args, "dispatch", true) {
				if _, err := s.api("POST", fmt.Sprintf("/tasks/%d/dispatch", id),
					map[string]any{}); err != nil {
					return nil, err
				}
				if fresh, err := s.object(fmt.Sprintf("/tasks/%d", id)); err == nil {
					task = fresh
				}
			}
			return map[string]any{"task_id": id, "status": task["status"],
				"url": fmt.Sprintf("%s/#task/%d", s.API, id)}, nil
		},
	},
	{
		Name:        "task_status",
		Description: "Status, attempt result, verify outcome and diff stats for a task.",
		Schema: obj(map[string]any{
			"task_id":        num("task id"),
			"include_events": flag("append the last 15 timeline events"),
		}, "task_id"),
		Run: func(s *Server, args map[string]any) (any, error) {
			id := argInt(args, "task_id")
			t, err := s.object(fmt.Sprintf("/tasks/%d", id))
			if err != nil {
				return nil, err
			}
			out := map[string]any{"id": t["id"], "title": t["title"],
				"status": t["status"], "attempt": t["attempt"]}
			if argBool(args, "include_events", false) {
				evs, err := s.list(fmt.Sprintf("/tasks/%d/events", id))
				if err != nil {
					return nil, err
				}
				if len(evs) > 15 {
					evs = evs[len(evs)-15:]
				}
				tail := make([]map[string]any, 0, len(evs))
				for _, e := range evs {
					tail = append(tail, map[string]any{"type": e["type"], "payload": e["payload"]})
				}
				out["events_tail"] = tail
			}
			return out, nil
		},
	},
	{
		Name:        "task_diff",
		Description: "The unified diff a finished task produced, per file.",
		Schema:      obj(map[string]any{"task_id": num("task id")}, "task_id"),
		Run: func(s *Server, args map[string]any) (any, error) {
			return s.object(fmt.Sprintf("/tasks/%d/diff", argInt(args, "task_id")))
		},
	},
	{
		Name:        "pending_approvals",
		Description: "Approvals waiting on a human — tool name, input, and owning task.",
		Schema:      obj(map[string]any{}),
		Run: func(s *Server, _ map[string]any) (any, error) {
			return s.list("/approvals?status=pending")
		},
	},
	{
		Name:        "decide_approval",
		Description: "Approve or deny a pending approval. decision: approved|denied.",
		Schema: obj(map[string]any{
			"approval_id": num("approval id"),
			"decision":    str("approved or denied"),
			"note":        str("reason, fed back to the agent on a denial"),
		}, "approval_id", "decision"),
		Run: func(s *Server, args map[string]any) (any, error) {
			return s.api("POST",
				fmt.Sprintf("/approvals/%d/decision", argInt(args, "approval_id")),
				map[string]any{"decision": argStr(args, "decision"), "note": argStr(args, "note")})
		},
	},
	{
		Name:        "complete_task",
		Description: "Mark a reviewed task as done.",
		Schema:      obj(map[string]any{"task_id": num("task id")}, "task_id"),
		Run: func(s *Server, args map[string]any) (any, error) {
			return s.api("POST", fmt.Sprintf("/tasks/%d/complete", argInt(args, "task_id")), nil)
		},
	},
	{
		Name:        "request_changes",
		Description: "Send a reviewed task back for another attempt with feedback.",
		Schema: obj(map[string]any{
			"task_id":  num("task id"),
			"feedback": str("what should change"),
		}, "task_id", "feedback"),
		Run: func(s *Server, args map[string]any) (any, error) {
			return s.api("POST", fmt.Sprintf("/tasks/%d/followup", argInt(args, "task_id")),
				map[string]any{"feedback": argStr(args, "feedback")})
		},
	},
	{
		Name: "delegate_build",
		Description: "Delegated builds: hand an implementation brief to the configured worker agent " +
			"(a cheaper model) as a task in its own worktree, and wait for it. You stay the lead: " +
			"you write the brief, then review the diff and the report, then accept_build or " +
			"request_changes. Refused when Delegated builds is off in Settings. `project` is a " +
			"project NAME; `brief` is the complete task brief (contracts, file scope, acceptance " +
			"criteria, verification commands). Returns the task id, the worker's completion " +
			"report, branch and diff stats once the worker finishes, or done:false at the timeout " +
			"(then call wait_build).",
		Schema: obj(map[string]any{
			"project":   str("project name (see list_projects); may be omitted when `workdir` is given"),
			"workdir":   str("absolute path of your checkout; the project registered at that path is used"),
			"title":     str("short card title"),
			"brief":     str("the full brief for the worker"),
			"timeout_s": num("seconds to wait before returning done:false (default 1500, max 3300)"),
		}, "title", "brief"),
		Run: func(s *Server, args map[string]any) (any, error) {
			cfg, err := s.object("/delegation")
			if err != nil {
				return nil, err
			}
			settings, _ := cfg["settings"].(map[string]any)
			if on, _ := settings["enabled"].(bool); !on {
				return nil, fmt.Errorf("Delegated builds is OFF. Turn it on in Settings (the big card at the top) and choose a worker; or do the implementation yourself")
			}
			if ready, _ := cfg["worker_ready"].(bool); !ready {
				return nil, fmt.Errorf("Delegated builds is on but the worker is not runnable: %v", cfg["worker_problem"])
			}
			name := argStr(args, "project")
			workdir := strings.TrimRight(argStr(args, "workdir"), "/")
			if name == "" && workdir == "" {
				return nil, fmt.Errorf("give `project` (a name) or `workdir` (your checkout's absolute path)")
			}
			projects, err := s.list("/projects")
			if err != nil {
				return nil, err
			}
			var match map[string]any
			var names []string
			for _, p := range projects {
				pn, _ := p["name"].(string)
				pr, _ := p["repo"].(string)
				if pr == "" {
					pr, _ = p["repo_path"].(string)
				}
				names = append(names, pn)
				if (name != "" && pn == name) || (name == "" && workdir != "" && strings.TrimRight(pr, "/") == workdir) {
					match = p
				}
			}
			if match == nil && name == "" {
				// A lead running as a task (an orchestrated build) sits in a
				// task worktree, not at the project's registered path; the
				// worktree still says which project it belongs to.
				match = s.projectOfWorktree(projects, workdir)
			}
			if match == nil {
				if name != "" {
					return nil, fmt.Errorf("no project named %q — have: %s", name, strings.Join(names, ", "))
				}
				return nil, fmt.Errorf("no project is registered at %s — have: %s", workdir, strings.Join(names, ", "))
			}
			agent, _ := settings["worker_agent"].(string)
			model, _ := settings["worker_model"].(string)
			mode, _ := settings["permission_mode"].(string)
			created, err := s.api("POST", "/tasks", map[string]any{
				"project_id": match["id"], "title": argStr(args, "title"),
				"prompt": delegation.WorkerPrompt(argStr(args, "brief")), "model": model,
				"agent": agent, "permission_mode": mode, "labels": []string{"delegated-build"}})
			if err != nil {
				return nil, err
			}
			task, _ := created.(map[string]any)
			id := int64(task["id"].(float64))
			if _, err := s.api("POST", fmt.Sprintf("/tasks/%d/dispatch", id), map[string]any{}); err != nil {
				return nil, err
			}
			return s.waitBuild(id, argInt(args, "timeout_s"))
		},
	},
	{
		Name: "wait_build",
		Description: "Wait for a delegated build (or any task) to leave queued/running. One call, " +
			"not polling: nothing is reported in between. Returns done:false at the timeout so you " +
			"can call it again; a timeout alone is not evidence the worker is stuck.",
		Schema: obj(map[string]any{
			"task_id":   num("task id"),
			"timeout_s": num("seconds to wait (default 1500, max 3300)"),
		}, "task_id"),
		Run: func(s *Server, args map[string]any) (any, error) {
			return s.waitBuild(argInt(args, "task_id"), argInt(args, "timeout_s"))
		},
	},
	{
		Name: "accept_build",
		Description: "Accept a reviewed delegated build: marks the task done and, when `workdir` is " +
			"given, brings the worker's branch into that working tree (your own checkout) as " +
			"uncommitted changes (mode apply, default) or as a merge commit (mode merge). A conflict " +
			"is reported and nothing is half-applied. Review task_diff before calling this.",
		Schema: obj(map[string]any{
			"task_id": num("task id"),
			"workdir": str("absolute path of the checkout to integrate into (omit to only mark done)"),
			"mode":    str("apply (default, uncommitted) or merge (merge commit)"),
		}, "task_id"),
		Run: func(s *Server, args map[string]any) (any, error) {
			id := argInt(args, "task_id")
			out := map[string]any{"task_id": id}
			if wd := argStr(args, "workdir"); wd != "" {
				merged, err := s.api("POST", fmt.Sprintf("/tasks/%d/integrate", id), map[string]any{"workdir": wd, "mode": argStr(args, "mode")})
				if err != nil {
					return nil, err
				}
				out["integration"] = merged
			}
			done, err := s.api("POST", fmt.Sprintf("/tasks/%d/complete", id), nil)
			if err != nil {
				return nil, err
			}
			if t, ok := done.(map[string]any); ok {
				out["status"] = t["status"]
			}
			return out, nil
		},
	},
}

// waitBuild blocks on the server-side wait and, once the task has stopped,
// attaches the worker's report and diff stats so the lead reads one result.
func (s *Server) waitBuild(id, timeout int64) (any, error) {
	if timeout <= 0 {
		timeout = 1500
	}
	if timeout > 3300 {
		timeout = 3300
	}
	raw, err := s.apiLong("GET", fmt.Sprintf("/tasks/%d/wait?timeout=%d", id, timeout), nil, time.Duration(timeout)*time.Second)
	if err != nil {
		return nil, err
	}
	res, _ := raw.(map[string]any)
	task, _ := res["task"].(map[string]any)
	out := map[string]any{"task_id": id, "done": res["done"], "status": task["status"]}
	if done, _ := res["done"].(bool); !done {
		out["next"] = "call wait_build again with the same task_id"
		return out, nil
	}
	report, err := s.object(fmt.Sprintf("/tasks/%d/report", id))
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"report", "branch", "worktree_path", "diff_stat", "verify", "exit_code", "attempt"} {
		out[k] = report[k]
	}
	// The patch itself, when it is small enough to read here: one result to
	// review instead of a second round trip, which is one fewer sample of the
	// lead's whole context.
	if diff, err := s.object(fmt.Sprintf("/tasks/%d/diff", id)); err == nil {
		if files, ok := diff["files"].([]any); ok {
			size := 0
			for _, f := range files {
				if m, ok := f.(map[string]any); ok {
					p, _ := m["patch"].(string)
					size += len(p)
				}
			}
			if size > 0 && size <= 48*1024 {
				out["diff"] = files
				out["next"] = "review `diff` and `report`, then accept_build (with workdir) or request_changes for one correction cycle"
				return out, nil
			}
		}
	}
	out["next"] = "read task_diff, then accept_build (with workdir to merge) or request_changes for one correction cycle"
	return out, nil
}

func toolSchemas() []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
	}
	return out
}

func (s *Server) call(name string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	for _, t := range tools {
		if t.Name == name {
			return t.Run(s, args)
		}
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// projectOfWorktree finds the project whose task owns the worktree at
// workdir, for a lead that is itself a task. Only the latest attempt of each
// task is consulted; an older attempt's tree is gone or about to be.
func (s *Server) projectOfWorktree(projects []map[string]any, workdir string) map[string]any {
	if workdir == "" {
		return nil
	}
	tasks, err := s.list("/tasks")
	if err != nil {
		return nil
	}
	for _, t := range tasks {
		att, _ := t["attempt"].(map[string]any)
		if att == nil {
			continue
		}
		if wt, _ := att["worktree_path"].(string); strings.TrimRight(wt, "/") != workdir {
			continue
		}
		pid, _ := t["project_id"].(float64)
		for _, p := range projects {
			if id, _ := p["id"].(float64); id == pid {
				return p
			}
		}
	}
	return nil
}
