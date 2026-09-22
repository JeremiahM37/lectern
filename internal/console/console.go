package console

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

type UI struct {
	Client *Client
	In     *bufio.Reader
	Out    io.Writer
	Attach func(string, string) error
}

func NewUI(c *Client, in io.Reader, out io.Writer, attach func(string, string) error) *UI {
	return &UI{c, bufio.NewReader(in), out, attach}
}
func (u *UI) say(s string, a ...any) { fmt.Fprintf(u.Out, s+"\n", a...) }
func (u *UI) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(u.Out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(u.Out, "%s: ", label)
	}
	s, e := u.In.ReadString('\n')
	if e != nil {
		return "", e
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	return s, nil
}
func (u *UI) show(data []byte) {
	var v any
	if json.Unmarshal(data, &v) == nil {
		u.say("%s", readable(v))
		return
	} else {
		var cleanText strings.Builder
		for _, r := range string(data) {
			if r == '\n' || r == '\t' || r >= 32 && r != 127 {
				cleanText.WriteRune(r)
			}
		}
		u.say("%s", cleanText.String())
	}
}
func (u *UI) request(method, path string, body any) error {
	data, e := u.Client.JSON(method, path, body)
	if e == nil && len(data) > 0 {
		u.show(data)
	}
	return e
}

// mcpSettings gives the plain terminal client the same complete add/edit/remove
// editor as the dashboard. The server returns only redacted values; @file is
// supported for multi-line JSON, and a conditional retry keeps the draft when a
// browser or another terminal changed the project concurrently.
func (u *UI) mcpSettings(path string) error {
	data, err := u.Client.JSON("GET", path+"/mcp", nil)
	if err != nil {
		return err
	}
	var current struct {
		MCP       map[string]any `json:"mcp"`
		Revision  string         `json:"revision"`
		StrictMCP bool           `json:"strict_mcp"`
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}
	u.say("MCP servers (redacted secrets are retained):")
	u.show(data)
	for {
		s, err := u.ask("MCP JSON or @file (blank cancels)", "")
		if err != nil || s == "" {
			return err
		}
		b := []byte(s)
		if strings.HasPrefix(s, "@") {
			b, err = os.ReadFile(s[1:])
			if err != nil {
				return err
			}
		}
		var servers map[string]any
		if err := json.Unmarshal(b, &servers); err != nil {
			u.say("MCP JSON must be an object: %v", err)
			continue
		}
		strict, err := u.ask("Claude strict replacement (true/false)", fmt.Sprint(current.StrictMCP))
		if err != nil {
			return err
		}
		if strict != "true" && strict != "false" {
			u.say("Enter true or false.")
			continue
		}
		body := map[string]any{"mcp": servers, "revision": current.Revision, "strict_mcp": strict == "true"}
		err = u.request("PUT", path+"/mcp", body)
		if err == nil {
			return nil
		}
		if he, ok := err.(*HTTPError); ok && he.Status == 409 {
			u.say("MCP settings changed elsewhere; your draft is preserved.")
			retry, askErr := u.ask("Retry this draft against the new settings? (yes/no)", "no")
			if askErr != nil || retry != "yes" {
				return err
			}
			fresh, freshErr := u.Client.JSON("GET", path+"/mcp", nil)
			if freshErr != nil {
				return freshErr
			}
			if freshErr = json.Unmarshal(fresh, &current); freshErr != nil {
				return freshErr
			}
			continue
		}
		return err
	}
}

// skillsSettings is the plain terminal editor for target-local project skills.
// Discovery and attachments are shown together, with provider and search
// controls, so managing a project never requires opening the web UI.
func (u *UI) skillsSettings(path string, project map[string]any) error {
	agent := text(project["default_agent"])
	if agent != "claude" && agent != "codex" {
		agent = "claude"
	}
	query := ""
	id := strings.TrimPrefix(path, "/projects/")
	for {
		catalogData, cErr := u.Client.JSON("GET", "/skills?project_id="+id+"&agent="+url.QueryEscape(agent), nil)
		attachedData, dErr := u.Client.JSON("GET", path+"/skills?agent="+url.QueryEscape(agent), nil)
		if cErr != nil || dErr != nil {
			u.say("Skills load failed (%s): %v%s", agent, cErr, func() string {
				if dErr != nil {
					return "; attachments: " + dErr.Error()
				}
				return ""
			}())
			u.say("r: retry · p: provider · b: back")
			pick, err := u.ask("Choose", "r")
			if err != nil || pick == "b" {
				return err
			}
			if pick == "p" {
				agent = u.skillProvider(agent)
			}
			continue
		}
		var catalog struct {
			Skills []struct{ ID, Name, Source, EntryName, Description string } `json:"skills"`
		}
		var attached struct {
			Attachments []struct {
				ID                           int64 `json:"id"`
				SkillID, EntryName, SourceID string
			} `json:"attachments"`
		}
		if err := json.Unmarshal(catalogData, &catalog); err != nil {
			return err
		}
		if err := json.Unmarshal(attachedData, &attached); err != nil {
			return err
		}
		u.say("\nPROJECT SKILLS · provider %s", agent)
		u.say("Attached:")
		if len(attached.Attachments) == 0 {
			u.say("  (none)")
		}
		for _, a := range attached.Attachments {
			u.say("  %d  %s · %s · %s", a.ID, a.EntryName, a.SourceID, a.SkillID)
		}
		u.say("Available:")
		shown := 0
		for _, s := range catalog.Skills {
			label := strings.ToLower(strings.Join([]string{s.ID, s.Name, s.EntryName, s.Source, s.Description}, " "))
			if query != "" && !fuzzy(query, label) {
				continue
			}
			u.say("  %-32s %-20s %-14s %s", s.ID, s.Name, s.Source, oneLine(s.Description))
			shown++
		}
		if shown == 0 {
			u.say("  (no matches)")
		}
		u.say("a: attach · d: detach · s: save target directories · p: provider · /: search · r: reload · b: back")
		pick, err := u.ask("Action", "")
		if err != nil {
			return err
		}
		switch pick {
		case "b", "":
			return nil
		case "r":
			continue
		case "p":
			agent = u.skillProvider(agent)
			query = ""
		case "/":
			query, err = u.ask("Catalog search (blank clears)", query)
			if err != nil {
				return err
			}
		case "a":
			id, e := u.ask("Skill ID", "")
			if e != nil {
				return e
			}
			if id == "" {
				continue
			}
			if e = u.request("POST", path+"/skills", map[string]any{"agent": agent, "skill_id": id}); e != nil {
				u.say("Attach failed: %v", e)
			}
		case "d":
			id, e := u.ask("Attachment ID", "")
			if e != nil {
				return e
			}
			if id == "" {
				continue
			}
			if e = u.request("DELETE", path+"/skills/"+id, nil); e != nil {
				u.say("Detach failed: %v", e)
			}
		case "s":
			raw, e := u.ask("Extra target directories (comma separated; blank clears)", "")
			if e != nil {
				return e
			}
			values := []string{}
			for _, v := range strings.Split(raw, ",") {
				if v = strings.TrimSpace(v); v != "" {
					values = append(values, v)
				}
			}
			if e = u.request("PATCH", path, map[string]any{"skill_sources": values}); e != nil {
				u.say("Directory save failed: %v", e)
			}
		default:
			u.say("Choose a, d, s, p, /, r, or b.")
		}
	}
}

func (u *UI) skillProvider(current string) string {
	next, err := u.ask("Provider (claude/codex)", current)
	if err != nil || (next != "claude" && next != "codex") {
		u.say("Provider must be claude or codex.")
		return current
	}
	return next
}
func (u *UI) confirm(label string) bool {
	s, e := u.ask(label+" — type yes to continue", "")
	return e == nil && s == "yes"
}
func (u *UI) Run() error {
	for {
		u.say("\nLECTERN  /  Terminal workspace\n%s\n", u.Client.Base)
		u.say("1  Sessions       2  Tasks          3  Routines\n4  Projects       5  Targets        6  Approvals\n7  Notifications  8  Usage & build  9  Full API\nq  Quit")
		pick, e := u.ask("Open", "")
		if e != nil {
			return nil
		}
		switch pick {
		case "q", "quit":
			return nil
		case "1", "sessions":
			e = u.collection("sessions")
		case "2", "tasks":
			e = u.collection("tasks")
		case "3", "routines":
			e = u.collection("routines")
		case "4", "projects":
			e = u.collection("projects")
		case "5", "targets":
			e = u.collection("targets")
		case "6", "approvals":
			e = u.collection("approvals")
		case "7":
			e = u.edit("PUT", "/settings")
		case "8":
			e = u.request("GET", "/stats", nil)
			if e == nil {
				e = u.request("GET", "/health", nil)
			}
		case "9":
			e = u.api()
		default:
			u.say("Choose a number, a section name, or q.")
		}
		if e != nil {
			if e == io.EOF {
				return nil
			}
			u.say("Error: %v", e)
		}
	}
}
func text(v any) string {
	s := fmt.Sprint(v)
	if v == nil {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 48 {
		s = string(r[:45]) + "…"
	}
	return s
}
func (u *UI) collection(kind string) error {
	for {
		path := "/" + kind
		if kind == "approvals" {
			path += "?status=pending"
		}
		data, e := u.Client.JSON("GET", path, nil)
		if e != nil {
			return e
		}
		var rows []map[string]any
		if e = json.Unmarshal(data, &rows); e != nil {
			return e
		}
		u.say("\n%s  ·  %d items", strings.ToUpper(kind), len(rows))
		w := tabwriter.NewWriter(u.Out, 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tPROJECT / TARGET")
		for _, r := range rows {
			name := r["name"]
			if name == nil {
				name = r["title"]
			}
			if name == nil {
				name = r["tool_name"]
			}
			status := r["status"]
			if kind == "routines" {
				status = r["schedule"]
			}
			place := r["project_name"]
			if place == nil {
				place = r["target_name"]
			}
			fmt.Fprintf(w, "%.0f\t%s\t%s\t%s\n", r["id"], text(name), text(status), text(place))
		}
		w.Flush()
		u.say("ID: open  ·  n: new  ·  r: refresh  ·  b: back")
		if kind == "sessions" {
			u.say("f: find running agents")
		}
		if kind == "projects" {
			u.say("i: import repositories")
		}
		pick, e := u.ask("Choose", "")
		if e != nil {
			return e
		}
		if pick == "b" {
			return nil
		}
		if pick == "r" || pick == "" {
			continue
		}
		if pick == "n" {
			if e = u.create(kind); e != nil {
				return e
			}
			continue
		}
		if kind == "sessions" && pick == "f" {
			if e = u.discover(); e != nil {
				return e
			}
			continue
		}
		if kind == "projects" && pick == "i" {
			if e = u.importProjects(); e != nil {
				return e
			}
			continue
		}
		id, e := strconv.ParseInt(pick, 10, 64)
		if e != nil || id <= 0 {
			u.say("Choose an ID from this list.")
			continue
		}
		var row map[string]any
		for _, r := range rows {
			if r["id"] == float64(id) {
				row = r
				break
			}
		}
		if row == nil {
			u.say("That ID is not in this list.")
			continue
		}
		if e = u.item(kind, pick, row); e != nil {
			u.say("Error: %v", e)
		}
	}
}
func (u *UI) form(fields []string) (map[string]any, error) {
	out := map[string]any{}
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		key := parts[0]
		def := ""
		if len(parts) > 1 {
			def = parts[1]
		}
		val, e := u.ask(strings.ReplaceAll(key, "_", " "), def)
		if e != nil {
			return nil, e
		}
		if val == "" {
			continue
		}
		if key == "project_ids" {
			var ids []int64
			for _, x := range strings.Split(val, ",") {
				n, e := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
				if e != nil || n <= 0 {
					return nil, fmt.Errorf("project IDs must be positive numbers separated by commas")
				}
				ids = append(ids, n)
			}
			out[key] = ids
		} else if strings.HasSuffix(key, "_id") || key == "port" || key == "max_concurrent" {
			n, e := strconv.ParseInt(val, 10, 64)
			if e != nil || n <= 0 {
				return nil, fmt.Errorf("%s must be a positive number", key)
			}
			out[key] = n
		} else {
			out[key] = val
		}
	}
	return out, nil
}
func (u *UI) create(kind string) error {
	fields := map[string][]string{
		"sessions": {"name", "project_id", "target_id", "agent=codex", "model", "workdir"},
		"tasks":    {"title", "project_id", "prompt", "agent=codex", "model", "permission_mode=acceptEdits"},
		"routines": {"name", "project_ids", "prompt", "schedule", "agent=codex", "model", "permission_mode=acceptEdits"},
		"projects": {"name", "target_id", "repo_path", "default_base_branch=main"},
		"targets":  {"name", "kind=ssh", "host", "user", "port=22", "key_path", "max_concurrent=4"},
	}[kind]
	if fields == nil {
		return fmt.Errorf("approvals are created by running agents")
	}
	if kind != "targets" {
		u.say("Reference IDs:")
		data, e := u.Client.JSON("GET", map[bool]string{true: "/targets", false: "/projects"}[kind == "projects"], nil)
		if e != nil {
			return e
		}
		u.show(data)
	}
	body, e := u.form(fields)
	if e != nil {
		return e
	}
	if kind == "sessions" && body["project_id"] == nil && body["workdir"] == nil {
		body["scratch"] = true
	}
	u.say("Optional: enter a JSON object to set additional fields (blank keeps these values).")
	extra, e := u.ask("Additional fields", "")
	if e != nil {
		return e
	}
	if extra != "" {
		var v map[string]any
		if e = json.Unmarshal([]byte(extra), &v); e != nil {
			return e
		}
		for k, v := range v {
			body[k] = v
		}
	}
	return u.request("POST", "/"+kind, body)
}
func (u *UI) edit(method, path string) error {
	if method == "PUT" {
		if e := u.request("GET", path, nil); e != nil {
			return e
		}
	}
	u.say("Enter changed fields as JSON, or @/path/to/file. Blank cancels.")
	s, e := u.ask("Fields", "")
	if e != nil || s == "" {
		return e
	}
	b := []byte(s)
	if strings.HasPrefix(s, "@") {
		b, e = os.ReadFile(s[1:])
		if e != nil {
			return e
		}
	}
	var body map[string]any
	if e = json.Unmarshal(b, &body); e != nil {
		return e
	}
	return u.request(method, path, body)
}
