package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/term"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/agentdeck/internal/config"
	"github.com/JeremiahM37/agentdeck/internal/console"
	"github.com/JeremiahM37/agentdeck/internal/mediapost"
)

func promoteCommand(c *console.Client, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: agentdeck promote SESSION-ID")
	}
	sessionID := args[0]
	if _, err := strconv.ParseInt(sessionID, 10, 64); err != nil {
		return fmt.Errorf("session ID must be a number")
	}
	sessionData, err := c.JSON("GET", "/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return err
	}
	var session map[string]any
	if err := json.Unmarshal(sessionData, &session); err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	previewData, err := c.JSON("GET", "/sessions/"+url.PathEscape(sessionID)+"/promote/preview", nil)
	if he, ok := err.(*console.HTTPError); ok && he.Status == 404 {
		return fmt.Errorf("conversation promotion is unavailable on the running server; restart or update AgentDeck, then try again")
	}
	if err != nil {
		return err
	}
	var preview struct {
		Identity map[string]any   `json:"identity"`
		Existing []map[string]any `json:"existing_projects"`
	}
	if err := json.Unmarshal(previewData, &preview); err != nil {
		return fmt.Errorf("read promotion preview: %w", err)
	}
	projects := preview.Existing
	in := bufio.NewReader(os.Stdin)
	ask := func(label, def string) (string, error) {
		if def != "" {
			fmt.Fprintf(os.Stdout, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(os.Stdout, "%s: ", label)
		}
		line, readErr := in.ReadString('\n')
		if readErr != nil {
			return "", readErr
		}
		line = strings.TrimSpace(line)
		if line == "" {
			line = def
		}
		return line, nil
	}
	fmt.Fprintf(os.Stdout, "Detected session %s\n  Agent: %s\n  Directory: %s\n  Terminal: %s\n  Conversation: %s\n  Status: %s\n\n", sessionID, preview.Identity["agent"], preview.Identity["workdir"], preview.Identity["tmux_session"], preview.Identity["cid"], session["status"])
	fmt.Fprintln(os.Stdout, "Choose an existing compatible project, or n for a new project.")
	for i, p := range projects {
		fmt.Fprintf(os.Stdout, "  %d) %s (%s)\n", i+1, fmt.Sprint(p["name"]), fmt.Sprint(p["repo_path"]))
	}
	choice, err := ask("Project", "n")
	if err != nil {
		return err
	}
	body := map[string]any{}
	if strings.EqualFold(choice, "n") || strings.EqualFold(choice, "new") {
		name, err := ask("New project name", "")
		if err != nil {
			return err
		}
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("project name is required")
		}
		body["name"] = name
	} else {
		n, parseErr := strconv.Atoi(choice)
		if parseErr != nil || n < 1 || n > len(projects) {
			return fmt.Errorf("choose n or a project number from the list")
		}
		body["project_id"] = int64(projects[n-1]["id"].(float64))
	}
	body["expected_identity"] = preview.Identity
	confirm, err := ask("Bind this exact agent, directory, terminal, and native history? (yes/no)", "no")
	if err != nil {
		return err
	}
	if !strings.EqualFold(confirm, "yes") {
		fmt.Fprintln(os.Stdout, "Promotion cancelled; the running terminal was left untouched.")
		return nil
	}
	_, err = c.JSON("POST", "/sessions/"+url.PathEscape(sessionID)+"/promote", body)
	if err != nil {
		return promoteError(err)
	}
	fmt.Fprintf(os.Stdout, "Conversation promoted. Session %s, terminal, and native history remain in place.\n", sessionID)
	return nil
}

func promoteError(err error) error {
	if he, ok := err.(*console.HTTPError); ok && he.Status == 404 {
		return fmt.Errorf("conversation promotion is unavailable on the running server; restart or update AgentDeck, then try again")
	}
	return fmt.Errorf("conversation promotion failed: %w", err)
}

const clientHelp = `AgentDeck — web and terminal control

  agentdeck                         Open the dashboard in an interactive terminal
  agentdeck local                   Start/use a private local runtime, then open the dashboard
  agentdeck local status            Show local runtime status without starting it
  agentdeck local stop              Stop the local runtime (active tasks are refused)
  agentdeck serve                   Start the control-plane server
  agentdeck console                 Live terminal dashboard (also: tui)
  agentdeck console --plain         Line-oriented menu for pipes / accessibility
  agentdeck shell [MACHINE]         Enter a blank persistent shell on a machine
  agentdeck attach KIND ID          Join tmux (Ctrl-b d returns to console)
  agentdeck promote SESSION-ID      Bind a running conversation to a project
  agentdeck api METHOD /path [JSON|@file|-]
  agentdeck upload KIND ID FILE     Add a local file as agent context
  agentdeck files KIND ID [PATH]    Browse files on the agent's machine
  agentdeck download KIND ID REMOTE LOCAL
  agentdeck post FILE|URL [--title T] [--note N] [--session ID]
                                    Show a recording, file or link in the Media feed
  agentdeck live [URL] [--title T] [--machine NAME] [--session ID]
                                    Start a desktop you can watch in Media; prints its DISPLAY
  agentdeck expose PORT [--title T] [--machine NAME] [--session ID]
                                    Reach a machine's localhost:PORT from your own browser
  agentdeck live list | agentdeck live stop ID
  agentdeck agent list
  agentdeck agent save JSON|@file|-
  agentdeck skill list PROJECT [--agent claude|codex]
  agentdeck skill attached PROJECT [--agent claude|codex]
  agentdeck skill attach PROJECT SKILL_ID [--agent claude|codex]
  agentdeck skill detach PROJECT ATTACHMENT_ID
  agentdeck mcp                     MCP on standard input/output
  agentdeck version

KIND: session, attempt, project (upload also accepts task).
API paths can omit /api. JSON goes to stdout; errors go to stderr.
Examples:
  agentdeck api GET /sessions
  agentdeck api POST /tasks/12/takeover '{}'
  agentdeck api PATCH /routines/3 '{"enabled":false}'
  agentdeck api POST /sessions/4/send '{"text":"Run the tests"}'
  agentdeck upload session 4 ./requirements.pdf
  agentdeck post ./demo.mp4 --title "Checkout flow passing"
  agentdeck post http://127.0.0.1:5173 --title "Dev server"
  agentdeck expose 5173 --title "Dev server"
  agentdeck live http://127.0.0.1:18080 --title "Watching the replay"
  agentdeck agent list
  agentdeck agent save @agents.json

AGENTDECK_API selects an explicit hosted server URL.
AGENTDECK_AUTH_TOKEN supplies bearer authentication.
AGENTDECK_ATTACH_HOST sets an SSH alias for native attachment to a remote server.
All web operations use this same API. See docs/terminal-client.md for the catalog.
With no AGENTDECK_API, client commands use the private local runtime automatically.
agentdeck local [COMMAND ...] forces those existing AgentDeck commands to use this machine.
`

// liveCommand opens a desktop or forwards a port. Inside an AgentDeck session it
// uses that session's machine; --machine names another, and outside both it
// falls to the server's default.
func liveCommand(c *console.Client, command string, args []string) ([]byte, error) {
	if command == "live" && len(args) == 1 && args[0] == "list" {
		return c.JSON("GET", "/live", nil)
	}
	if command == "live" && len(args) == 2 && args[0] == "stop" {
		if _, err := strconv.ParseInt(args[1], 10, 64); err != nil {
			return nil, fmt.Errorf("usage: agentdeck live stop ID")
		}
		return c.JSON("DELETE", "/live/"+args[1], nil)
	}
	body := map[string]any{}
	subject, machine := "", ""
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if !strings.HasPrefix(flag, "--") {
			if subject != "" {
				return nil, fmt.Errorf("%s takes one argument; quote a title that has spaces", command)
			}
			subject = flag
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("%s needs a value", flag)
		}
		i++
		switch flag {
		case "--title":
			body["title"] = args[i]
		case "--machine":
			machine = args[i]
		case "--session":
			id, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("--session takes a session id")
			}
			body["session_id"] = id
		default:
			return nil, fmt.Errorf("unknown flag %s", flag)
		}
	}
	if machine != "" {
		data, err := c.JSON("GET", "/targets", nil)
		if err != nil {
			return nil, err
		}
		var targets []shellTarget
		if err := json.Unmarshal(data, &targets); err != nil {
			return nil, err
		}
		chosen, err := chooseShellTarget(machine, targets, strings.NewReader(""), io.Discard)
		if err != nil {
			return nil, err
		}
		body["target_id"] = chosen.ID
	} else if body["session_id"] == nil {
		body["hint_session_id"] = mediapost.SessionID()
		body["tmux_session"] = mediapost.TmuxSession()
	}
	if command == "expose" {
		port, err := strconv.Atoi(subject)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("usage: agentdeck expose PORT [--title T] [--machine NAME] [--session ID]")
		}
		body["port"] = port
		return c.JSON("POST", "/live/ports", body)
	}
	if subject != "" {
		body["url"] = subject
	}
	return c.JSON("POST", "/live/desktops", body)
}

// postCommand shows a file or link in the Media feed. Inside an AgentDeck
// session the post attributes itself; --session is for scripts outside one.
func postCommand(base, token string, args []string) ([]byte, error) {
	post := mediapost.Post{Source: "cli"}
	subject := ""
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if !strings.HasPrefix(flag, "--") {
			if subject != "" {
				return nil, fmt.Errorf("post takes one FILE or URL; quote a title that has spaces")
			}
			subject = flag
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("%s needs a value", flag)
		}
		i++
		switch flag {
		case "--title":
			post.Title = args[i]
		case "--note":
			post.Note = args[i]
		case "--session":
			id, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("--session takes a session id")
			}
			post.SessionID = id
		default:
			return nil, fmt.Errorf("unknown flag %s", flag)
		}
	}
	if subject == "" {
		return nil, fmt.Errorf("usage: agentdeck post FILE|URL [--title T] [--note N] [--session ID]")
	}
	if lower := strings.ToLower(subject); strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		post.URL = subject
	} else {
		post.Path = subject
	}
	if post.Title == "" {
		post.Title = filepath.Base(subject)
	}
	return mediapost.Send(base, token, post)
}

func clientCommand(cfg *config.Config, command string, args []string) error {
	return clientCommandAt(cfg, command, args, env("AGENTDECK_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port)), cfg.AuthToken, false)
}

func clientCommandAt(cfg *config.Config, command string, args []string, base, token string, local bool) error {
	if command == "help" || command == "--help" || command == "-h" || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Print(clientHelp)
		return nil
	}
	c := console.New(base, token)
	var data []byte
	var err error
	switch command {
	case "shell":
		return shellCommandAt(cfg, args, base, token, local)
	case "promote":
		return promoteCommand(c, args)
	case "console", "tui":
		attachClient := func(kind, id string) error {
			var argv []string
			var e error
			if local {
				localCfg := *cfg
				localCfg.AuthToken = token
				argv, e = attachmentCommandAt(&localCfg, []string{kind, id}, base, "")
			} else {
				argv, e = attachmentCommand(cfg, []string{kind, id})
			}
			if e != nil {
				return e
			}
			argv = attachmentInWorkspace(argv, os.Getenv("TMUX"))
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		if len(args) > 0 && (len(args) != 1 || args[0] != "--plain") {
			return fmt.Errorf("usage: agentdeck console [--plain]")
		}
		if len(args) == 0 && interactiveTerminal() {
			return console.RunDashboard(c, os.Stdin, os.Stdout, attachClient)
		}
		return console.NewUI(c, os.Stdin, os.Stdout, attachClient).Run()
	case "api":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: agentdeck api METHOD /path [JSON|@file|-]")
		}
		var body io.Reader
		if len(args) == 3 {
			b := []byte(args[2])
			if args[2] == "-" {
				b, err = io.ReadAll(os.Stdin)
			} else if strings.HasPrefix(args[2], "@") {
				b, err = os.ReadFile(args[2][1:])
			}
			if err != nil {
				return err
			}
			if !json.Valid(b) {
				return fmt.Errorf("request body is not valid JSON")
			}
			body = bytes.NewReader(b)
		}
		data, err = c.Request(strings.ToUpper(args[0]), args[1], body, "application/json")
	case "skill":
		data, err = skillCommand(c, args)
	case "agent":
		data, err = agentCommand(c, args)
	case "post":
		data, err = postCommand(base, token, args)
	case "live", "expose":
		data, err = liveCommand(c, command, args)
	case "upload":
		if len(args) != 3 {
			return fmt.Errorf("usage: agentdeck upload KIND ID FILE")
		}
		if err = validateTerminal(args[:2], true); err != nil {
			return err
		}
		data, err = c.Upload(args[0], args[1], args[2])
	case "files", "download":
		if command == "files" && (len(args) < 2 || len(args) > 3) || command == "download" && len(args) != 4 {
			return fmt.Errorf("usage: agentdeck files KIND ID [PATH] | download KIND ID REMOTE LOCAL")
		}
		if err = validateTerminal(args[:2], false); err != nil {
			return err
		}
		suffix := "/files"
		if command == "download" {
			suffix = "/file"
		}
		path := "/term/" + args[0] + "/" + args[1] + suffix
		if len(args) > 2 {
			path += "?path=" + url.QueryEscape(args[2])
		}
		if command == "download" {
			return c.Download(path, args[3])
		}
		data, err = c.JSON("GET", path, nil)
	default:
		return fmt.Errorf("unknown client command")
	}
	if err != nil {
		return err
	}
	if len(data) > 0 {
		_, err = os.Stdout.Write(data)
		if err == nil && !bytes.HasSuffix(data, []byte("\n")) {
			fmt.Println()
		}
	}
	return err
}

// agentCommand makes the runner registry discoverable without requiring users
// to hand craft an API request. A save replaces the custom definitions exactly
// as the Settings → Agents editor does; JSON can be read from a file or stdin.
func agentCommand(c *console.Client, args []string) ([]byte, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("usage: agentdeck agent list | save JSON|@file|-")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return nil, fmt.Errorf("usage: agentdeck agent list")
		}
		return c.Request("GET", "/agents", nil, "application/json")
	case "save":
		if len(args) != 2 {
			return nil, fmt.Errorf("usage: agentdeck agent save JSON|@file|-")
		}
		b := []byte(args[1])
		var err error
		if args[1] == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else if strings.HasPrefix(args[1], "@") {
			b, err = os.ReadFile(args[1][1:])
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(b) {
			return nil, fmt.Errorf("agent definitions are not valid JSON")
		}
		return c.Request("PUT", "/agents", bytes.NewReader(b), "application/json")
	default:
		return nil, fmt.Errorf("unknown agent operation %q (use list or save)", args[0])
	}
}

func skillCommand(c *console.Client, args []string) ([]byte, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("usage: agentdeck skill list|attached|attach|detach PROJECT ...")
	}
	op, project := args[0], args[1]
	agent := ""
	for i := 2; i < len(args); i++ {
		if args[i] == "--agent" && i+1 < len(args) {
			agent = args[i+1]
			i++
		} else if strings.HasPrefix(args[i], "--") {
			return nil, fmt.Errorf("unknown skill option %s", args[i])
		}
	}
	var method, path string
	var body io.Reader
	switch op {
	case "list":
		method = "GET"
		path = "/api/skills?project_id=" + url.QueryEscape(project)
		if agent != "" {
			path += "&agent=" + url.QueryEscape(agent)
		}
	case "attached":
		method = "GET"
		path = "/api/projects/" + url.PathEscape(project) + "/skills"
		if agent != "" {
			path += "?agent=" + url.QueryEscape(agent)
		}
	case "attach":
		if len(args) < 3 {
			return nil, fmt.Errorf("usage: agentdeck skill attach PROJECT SKILL_ID [--agent claude|codex]")
		}
		method = "POST"
		path = "/api/projects/" + url.PathEscape(project) + "/skills"
		b, _ := json.Marshal(map[string]any{"skill_id": args[2], "agent": agent})
		body = bytes.NewReader(b)
	case "detach":
		if len(args) < 3 {
			return nil, fmt.Errorf("usage: agentdeck skill detach PROJECT ATTACHMENT_ID")
		}
		method = "DELETE"
		path = "/api/projects/" + url.PathEscape(project) + "/skills/" + url.PathEscape(args[2])
	default:
		return nil, fmt.Errorf("unknown skill operation %q", op)
	}
	return c.Request(method, path, body, "application/json")
}
func validateTerminal(args []string, task bool) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: agentdeck attach KIND ID")
	}
	switch args[0] {
	case "session", "attempt", "project", "session-shell", "attempt-shell":
	case "task":
		if !task {
			return fmt.Errorf("use the task's attempt ID for terminal access")
		}
	default:
		return fmt.Errorf("invalid terminal kind")
	}
	id, e := strconv.ParseInt(args[1], 10, 64)
	if e != nil || id <= 0 {
		return fmt.Errorf("invalid terminal ID")
	}
	return nil
}

func interactiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
