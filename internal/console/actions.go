package console

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func (u *UI) item(kind, id string, row map[string]any) error {
	path := "/" + kind + "/" + id
	for {
		b, _ := json.Marshal(row)
		u.show(b)
		options := map[string]string{
			"sessions":  "attach · shell · read · send · interrupt · upload · files · rename (edit) · handoff · wraps · promote · restore (tracking) · delete",
			"tasks":     "attach · messages · send · upload · events · diff · dispatch · takeover · followup · complete · cancel · commit · cleanup · edit · delete",
			"routines":  "run · enable · disable · edit · delete (running tasks appear in Tasks; choose takeover there)",
			"projects":  "attach · shell · brief · notes · wraps · capability · MCP settings (add / edit / remove) · skills (attach / detach) · workflows (Spec Kit / Maestro) · edit · delete",
			"targets":   "check · edit · delete",
			"approvals": "allow · deny",
		}
		u.say("\n%s\nb: back", options[kind])
		pick, e := u.ask("Action", "")
		if e != nil {
			return e
		}
		if pick == "b" || pick == "" {
			return nil
		}
		switch pick {
		case "edit", "rename":
			e = u.edit("PATCH", path)
		case "mcp":
			e = u.mcpSettings(path)
		case "skills":
			e = u.skillsSettings(path, row)
		case "workflows":
			if kind == "projects" {
				e = u.workflowSettings(path, row)
			}
		case "attach", "shell":
			terminalKind := strings.TrimSuffix(kind, "s")
			terminalID := id
			if kind == "tasks" {
				data, err := u.Client.JSON("GET", path, nil)
				if err != nil {
					return err
				}
				var task map[string]any
				_ = json.Unmarshal(data, &task)
				attempt, _ := task["latest_attempt"].(map[string]any)
				if attempt == nil {
					attempt, _ = task["attempt"].(map[string]any)
				}
				if attempt == nil {
					return fmt.Errorf("task has no attempt to attach")
				}
				terminalID = fmt.Sprintf("%.0f", attempt["id"])
				terminalKind = "attempt"
			}
			if pick == "shell" && kind != "projects" {
				terminalKind += "-shell"
			}
			if u.Attach == nil {
				return fmt.Errorf("native attachment unavailable")
			}
			e = u.Attach(terminalKind, terminalID)
		case "read":
			e = u.request("GET", "/term/session/"+id+"/history?lines=300", nil)
		case "messages", "events", "diff", "brief", "notes", "wraps", "capability":
			e = u.request("GET", path+"/"+pick, nil)
		case "send":
			s, err := u.ask("Message", "")
			if err != nil {
				return err
			}
			if s == "" {
				continue
			}
			suffix := "/messages"
			if kind == "sessions" {
				suffix = "/send"
			}
			e = u.request("POST", path+suffix, map[string]any{"text": s})
		case "interrupt":
			e = u.request("POST", path+"/send", map[string]any{"key": "C-c"})
		case "upload":
			filename, err := u.ask("Local file", "")
			if err != nil {
				return err
			}
			if filename == "" {
				continue
			}
			terminalKind := strings.TrimSuffix(kind, "s")
			data, err := u.Client.Upload(terminalKind, id, filename)
			e = err
			if e == nil {
				u.show(data)
			}
		case "files":
			e = u.request("GET", "/term/"+strings.TrimSuffix(kind, "s")+"/"+id+"/files", nil)
		case "handoff", "promote", "followup", "commit":
			e = u.edit("POST", path+"/"+pick)
		case "restore", "dispatch", "takeover", "complete", "cancel", "run", "check":
			e = u.request("POST", path+"/"+pick, map[string]any{})
		case "enable", "disable":
			e = u.request("PATCH", path, map[string]any{"enabled": pick == "enable"})
		case "allow", "deny":
			e = u.request("POST", path+"/decision", map[string]any{"decision": map[string]string{"allow": "approved", "deny": "denied"}[pick]})
		case "delete", "cleanup":
			warning := "Remove this " + strings.TrimSuffix(kind, "s") + "?"
			if kind == "sessions" {
				warning = "Remove this session? Lectern-owned sessions are stopped; adopted sessions keep running."
			}
			if !u.confirm(warning) {
				continue
			}
			method := "DELETE"
			suffix := ""
			if pick == "cleanup" {
				method = "POST"
				suffix = "/cleanup"
			}
			if e = u.request(method, path+suffix, map[string]any{}); e == nil {
				return nil
			}
		default:
			u.say("Choose one of the listed actions.")
			continue
		}
		if e != nil {
			u.say("Error: %v", e)
		} else {
			u.say("Action completed.")
		}
		// Refresh the selected row so edits and state changes are visible.
		data, err := u.Client.JSON("GET", "/"+kind, nil)
		if err == nil {
			var rows []map[string]any
			if json.Unmarshal(data, &rows) == nil {
				for _, r := range rows {
					if fmt.Sprintf("%.0f", r["id"]) == id {
						row = r
					}
				}
			}
		}
	}
}
func (u *UI) discover() error {
	if e := u.request("GET", "/sessions/discover", nil); e != nil {
		return e
	}
	u.say("Track a running tmux session from the results above. Blank target cancels.")
	body, e := u.form([]string{"target_id", "tmux_session", "name", "project_id"})
	if e != nil {
		return e
	}
	if body["target_id"] == nil {
		return nil
	}
	return u.request("POST", "/sessions/adopt", body)
}
func (u *UI) importProjects() error {
	body, e := u.form([]string{"target_id", "root"})
	if e != nil {
		return e
	}
	if body["target_id"] == nil {
		return nil
	}
	if e = u.request("GET", "/projects/import/scan?target_id="+url.QueryEscape(fmt.Sprint(body["target_id"]))+"&root="+url.QueryEscape(fmt.Sprint(body["root"])), nil); e != nil {
		return e
	}
	if !u.confirm("Import repositories from this root?") {
		return nil
	}
	return u.request("POST", "/projects/import", body)
}
func (u *UI) api() error {
	u.say("All API resources: targets, projects, sessions, tasks, routines, approvals, agents, launch-profiles, models, templates, settings, stats, health.\nExamples: GET /agents · PUT /templates · POST /settings/test-notification · POST /admin/janitor\nUse lectern api --help for scripting and file input.")
	line, e := u.ask("METHOD /api/path (blank cancels)", "")
	if e != nil || line == "" {
		return e
	}
	parts := strings.Fields(line)
	if len(parts) != 2 {
		return fmt.Errorf("enter METHOD /path")
	}
	method := strings.ToUpper(parts[0])
	if method == "GET" {
		return u.request(method, parts[1], nil)
	}
	return u.edit(method, parts[1])
}
