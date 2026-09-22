package sessions

import (
	"path/filepath"
	"regexp"
	"strings"
)

// DiscoverDelimiter separates the tmux listing from the process listing.
const DiscoverDelimiter = "\x1e---LECTERN-PS---\x1e"

// DiscoverCommand finds agents the operator started themselves.
//
// This is the half of the problem a task board misses: most long-running agents
// were launched by hand in a terminal, and a tool that can only see what it
// started is blind to the actual work. One command, two listings, joined on the
// pane's tty — `pane_current_command` is NOT usable here, because an agent
// launched from a login shell leaves bash in the foreground of the pane.
func DiscoverCommand() string {
	return "tmux list-panes -a -F '#{session_name}\t#{pane_tty}\t#{pane_current_path}' " +
		"2>/dev/null; printf '%s' " + "'" + DiscoverDelimiter + "'" +
		"; ps -eo tty=,args= 2>/dev/null"
}

// Candidate is an agent found running on a target that lectern does not own.
type Candidate struct {
	TmuxSession string `json:"tmux_session"`
	Agent       string `json:"agent"`
	Model       string `json:"model"`
	Workdir     string `json:"workdir"`
	Command     string `json:"command"`
	TargetID    int64  `json:"target_id"`
	TargetName  string `json:"target_name,omitempty"`
	// Adopted is set when lectern is already tracking this tmux session, so the
	// UI can show it as known rather than offering to adopt it twice.
	Adopted   bool   `json:"adopted"`
	SessionID int64  `json:"session_id,omitempty"`
	ProjectID *int64 `json:"project_id,omitempty"`
	// ProjectName is filled when the workdir matches a registered project.
	ProjectName string `json:"project_name,omitempty"`
}

// agentPattern matches an agent invocation in a process's argv. It looks for the
// binary as a whole word — `claude` the CLI, not a path that merely mentions it.
var agentPattern = regexp.MustCompile(`(?:^|/|\s)(claude|codex|gemini)(?:\s|$)`)

var modelFlag = regexp.MustCompile(`(?:--model|-m)[= ]+(\S+)`)

// ParseDiscover joins the tmux and process listings into agent candidates.
func ParseDiscover(out string) []Candidate {
	panesRaw, psRaw, found := strings.Cut(out, DiscoverDelimiter)
	if !found {
		return nil
	}
	// tty -> the most specific agent command seen on it
	byTTY := map[string]string{}
	for _, line := range strings.Split(psRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tty, args, ok := strings.Cut(line, " ")
		if !ok || tty == "?" || tty == "" {
			continue
		}
		args = strings.TrimSpace(args)
		if !agentPattern.MatchString(args) {
			continue
		}
		// prefer the bare agent invocation over the shell that wrapped it, so the
		// model flag we read belongs to the agent and not to `bash -lc "..."`
		if prev, seen := byTTY[tty]; seen && len(prev) <= len(args) {
			continue
		}
		byTTY[tty] = args
	}

	var out2 []Candidate
	for _, line := range strings.Split(panesRaw, "\n") {
		parts := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(parts) < 3 {
			continue
		}
		name, tty, cwd := parts[0], strings.TrimPrefix(parts[1], "/dev/"), parts[2]
		args, ok := byTTY[tty]
		if !ok {
			continue // a pane with no agent on it is just a terminal
		}
		c := Candidate{TmuxSession: name, Workdir: cwd, Command: clip(args, 200)}
		if m := agentPattern.FindStringSubmatch(args); m != nil {
			c.Agent = m[1]
		}
		if m := modelFlag.FindStringSubmatch(args); m != nil {
			c.Model = m[1]
		}
		out2 = append(out2, c)
	}
	return out2
}

// MatchProject picks the registered project a discovered agent is working in:
// the longest repo path that contains (or equals) the pane's working directory.
// Longest wins so a repo nested inside another is preferred over its parent.
func MatchProject(workdir string, repoPaths map[int64]string) (int64, bool) {
	best, bestLen := int64(0), -1
	clean := filepath.Clean(workdir)
	for id, repo := range repoPaths {
		repo = filepath.Clean(repo)
		if repo == "" || repo == "." || repo == "/" {
			continue
		}
		if clean == repo || strings.HasPrefix(clean, repo+string(filepath.Separator)) {
			if len(repo) > bestLen {
				best, bestLen = id, len(repo)
			}
		}
	}
	return best, bestLen >= 0
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
