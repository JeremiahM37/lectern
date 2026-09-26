package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
)

// agentQuickVerbs are the first-class one-command agent launchers — `lectern
// claude [args...]` / `lectern codex [args...]` — that create or reuse a
// tracked interactive session for the current directory and attach the
// terminal to it, matching Happy's `happy claude`. They are dispatched
// exactly like the other client verbs in main.go (explicit LECTERN_API vs.
// the private local runtime) and share the same console.Client/attach
// plumbing; kept in their own set only so the whole estate of names can be
// checked for collisions in one place (see TestAgentQuickVerbsNeverShadow in
// main_test.go). Only claude and codex are first-class: a truly generic
// `lectern <agent-name>` would need a network round trip against the agent
// registry before main.go could even tell a launch request from a typo,
// which is not "cheap" for the common case of a mistyped subcommand.
var agentQuickVerbs = map[string]bool{"claude": true, "codex": true}

// agentQuickOpts is the documented subset of extra arguments a one-command
// launch accepts. The session API (internal/api/sessions.go's sessionIn) has
// no field for arbitrary passed-through CLI arguments, so anything beyond
// this subset is a clear error rather than a silently dropped argument.
type agentQuickOpts struct {
	New    bool
	Attach bool
	Model  string
	Resume bool
}

func parseAgentQuickArgs(agentName string, args []string) (agentQuickOpts, error) {
	var o agentQuickOpts
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--new":
			o.New = true
		case "--attach":
			o.Attach = true
		case "--resume":
			o.Resume = true
		case "--model":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--model needs a value")
			}
			i++
			o.Model = args[i]
		default:
			return o, fmt.Errorf("lectern %s: unsupported argument %q (supported: --new, --attach, --model NAME, --resume)", agentName, args[i])
		}
	}
	if o.New && o.Attach {
		return o, fmt.Errorf("lectern %s: --new and --attach cannot both be given", agentName)
	}
	return o, nil
}

// quickSessionView is the handful of session fields a one-command launch
// needs, decoded from the same JSON the full sessionView produces.
type quickSessionView struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	Workdir string `json:"workdir"`
}

type quickProjectView struct {
	ID       int64  `json:"id"`
	RepoPath string `json:"repo_path"`
}

// resolvePathForCompare normalizes a path for equality/containment checks:
// symlinks resolved where possible, otherwise just cleaned. Mirrors up.go's
// samePath, generalized to prefix matching for project containment.
func resolvePathForCompare(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// projectForWorkdir finds the registered project whose repository contains
// workdir — workdir is the repo root itself, or somewhere underneath it —
// preferring the most specific (longest) repo_path when projects nest.
func projectForWorkdir(c *console.Client, workdir string) (*int64, error) {
	data, err := c.JSON("GET", "/projects", nil)
	if err != nil {
		return nil, err
	}
	var projects []quickProjectView
	if err := json.Unmarshal(data, &projects); err != nil {
		return nil, err
	}
	target := resolvePathForCompare(workdir)
	var best *quickProjectView
	var bestRepo string
	for i := range projects {
		repo := resolvePathForCompare(projects[i].RepoPath)
		if repo == "" {
			continue
		}
		if target != repo && !strings.HasPrefix(target, repo+string(filepath.Separator)) {
			continue
		}
		if best == nil || len(repo) > len(bestRepo) {
			best, bestRepo = &projects[i], repo
		}
	}
	if best == nil {
		return nil, nil
	}
	id := best.ID
	return &id, nil
}

// findLiveSession looks for a session on the same agent and workdir. The
// default GET /sessions listing already excludes ended, archived and dead
// sessions (internal/api/sessions.go's listSessions / store.DB.Sessions), so
// anything returned here is live by construction.
func findLiveSession(c *console.Client, agentName, workdir string) (*quickSessionView, error) {
	data, err := c.JSON("GET", "/sessions", nil)
	if err != nil {
		return nil, err
	}
	var rows []quickSessionView
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	target := resolvePathForCompare(workdir)
	for i := range rows {
		if rows[i].Agent == agentName && resolvePathForCompare(rows[i].Workdir) == target {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// currentGitBranch reports the checked-out branch, if git is available, dir
// is inside a repository, and HEAD is not detached.
func currentGitBranch(dir string) (string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", false
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", false
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", false
	}
	return branch, true
}

// sessionDisplayName is the folder's basename, plus the checked-out git
// branch when there is one — "lectern · main".
func sessionDisplayName(workdir string) string {
	base := filepath.Base(workdir)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = workdir
	}
	if branch, ok := currentGitBranch(workdir); ok {
		return base + " · " + branch
	}
	return base
}

// resolveAgentSession decides which session `lectern <agent>` ends up
// attached to: an existing live session for the same agent and workdir
// (subject to opts and, on an interactive terminal, a confirmation), or a
// freshly created one. confirm is only ever called when a live session
// exists, neither --new nor --attach was given, and interactive is true —
// kept as a parameter (rather than reading the terminal directly) so tests
// can drive every branch without a real TTY.
func resolveAgentSession(c *console.Client, agentName, workdir string, opts agentQuickOpts, interactive bool, confirm func(*quickSessionView) (bool, error)) (*quickSessionView, error) {
	existing, err := findLiveSession(c, agentName, workdir)
	if err != nil {
		return nil, fmt.Errorf("check for an existing session: %w", err)
	}
	reuse := false
	switch {
	case existing == nil:
		if opts.Attach {
			return nil, fmt.Errorf("lectern %s --attach: no existing session is running for %s", agentName, workdir)
		}
	case opts.New:
		reuse = false
	case opts.Attach:
		reuse = true
	case !interactive:
		reuse = true
	default:
		reuse, err = confirm(existing)
		if err != nil {
			return nil, err
		}
	}
	if reuse {
		return existing, nil
	}
	projectID, err := projectForWorkdir(c, workdir)
	if err != nil {
		return nil, fmt.Errorf("look up the project for %s: %w", workdir, err)
	}
	body := map[string]any{
		"agent":   agentName,
		"workdir": workdir,
		"name":    sessionDisplayName(workdir),
	}
	if projectID != nil {
		body["project_id"] = *projectID
	}
	if opts.Model != "" {
		body["model"] = opts.Model
	}
	if opts.Resume {
		body["resume"] = true
	}
	data, err := c.JSON("POST", "/sessions", body)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", agentName, err)
	}
	var created quickSessionView
	if err := json.Unmarshal(data, &created); err != nil {
		return nil, fmt.Errorf("read new session: %w", err)
	}
	return &created, nil
}

// confirmReuse is resolveAgentSession's real, interactive confirm function:
// "Attach to existing session 'x' (#id)? [Y/n]", default yes.
func confirmReuse(existing *quickSessionView) (bool, error) {
	fmt.Fprintf(os.Stdout, "Attach to existing session %q (#%d)? [Y/n] ", existing.Name, existing.ID)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return !strings.HasPrefix(line, "n"), nil
}

// attachAgentSession hands the current terminal to the resolved session
// through the existing native attach path — the same argv resolution and
// Ctrl-] controls wrapper `lectern attach session ID` and the console's own
// attach callback use (attachmentCommandAt/attachmentCommand, runAttachment
// in cmd/lectern/native_attach.go). Nothing here talks to tmux or an agent
// binary directly.
func attachAgentSession(cfg *config.Config, base, token string, local bool, id int64) error {
	idStr := strconv.FormatInt(id, 10)
	var argv []string
	var err error
	if local {
		localCfg := *cfg
		localCfg.AuthToken = token
		argv, err = attachmentCommandAt(&localCfg, []string{"session", idStr}, base, "")
	} else {
		argv, err = attachmentCommand(cfg, []string{"session", idStr})
	}
	if err != nil {
		return err
	}
	return runAttachment(argv, &nativeControls{Kind: "session", ID: idStr, Base: base, Token: token, TabView: os.Getenv("LECTERN_TAB_VIEW") == "1"})
}

// agentQuickCommand implements `lectern claude`/`lectern codex`: resolve (or
// create) a tracked session for the current directory and this agent, print
// where it also is on the phone, then attach.
func agentQuickCommand(cfg *config.Config, agentName string, args []string, base, token string, local bool) error {
	opts, err := parseAgentQuickArgs(agentName, args)
	if err != nil {
		return err
	}
	workdir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine the working directory: %w", err)
	}
	if workdir, err = filepath.Abs(workdir); err != nil {
		return err
	}
	c := console.New(base, token)
	sess, err := resolveAgentSession(c, agentName, workdir, opts, interactiveTerminal(), confirmReuse)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Session #%d %q — also on your phone at %s/#session/%d\n",
		sess.ID, sess.Name, strings.TrimRight(phoneBase(c, base), "/"), sess.ID)
	return attachAgentSession(cfg, base, token, local, sess.ID)
}

// phoneBase is the dashboard origin to print for a phone: the server's tailnet
// HTTPS address when it has one (a loopback or LAN API base is not reachable
// from a phone), else the API base itself.
func phoneBase(c *console.Client, base string) string {
	var health struct {
		PhoneURL string `json:"phone_url"`
	}
	if data, err := c.JSON("GET", "/health", nil); err == nil &&
		json.Unmarshal(data, &health) == nil && strings.TrimSpace(health.PhoneURL) != "" {
		return health.PhoneURL
	}
	return base
}
