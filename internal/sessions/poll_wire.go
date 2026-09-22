package sessions

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/internal/shellq"
)

const PollEnd = "ADK-POLL-END-v2"

type PollCapture struct {
	Text            string
	Missing, Failed bool
}

func buildPollCommand(names []string, lines int) string {
	var command strings.Builder
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		// Encode payloads so a pane cannot forge another pane's frame. Known tmux
		// absence errors are distinct from socket permissions, bad arguments, etc.
		fmt.Fprintf(&command, `if lec_poll_text=$(LC_ALL=C tmux capture-pane -p -t %s -S -%d 2>&1); then lec_poll_state=ok; else case "$lec_poll_text" in "can't find session:"*|"no server running on "*|"error connecting to "*" (No such file or directory)") lec_poll_state=missing; lec_poll_text='' ;; *) lec_poll_state=error ;; esac; fi; lec_poll_payload=$(printf '%%s' "$lec_poll_text" | base64) || exit 1; lec_poll_payload=$(printf '%%s' "$lec_poll_payload" | tr -d '\r\n') || exit 1; printf '%%s\t%%s\t%%s\n' %s "$lec_poll_state" "$lec_poll_payload"; `,
			shellq.Quote("="+name+":"), lines, shellq.Quote(base64.StdEncoding.EncodeToString([]byte(name))))
	}
	fmt.Fprintf(&command, "printf '%%s\\n' %s", shellq.Quote(PollEnd))
	return command.String()
}

// A complete, validated snapshot is required before any session is updated.
// Missing frames, duplicate names and partial output never imply a dead agent.
func ParsePollSnapshot(output string, names []string) (map[string]PollCapture, bool) {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != PollEnd || len(lines) != len(wanted)+1 {
		return nil, false
	}
	panes := map[string]PollCapture{}
	for _, line := range lines[:len(lines)-1] {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, false
		}
		name, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil || !wanted[string(name)] {
			return nil, false
		}
		if _, exists := panes[string(name)]; exists {
			return nil, false
		}
		body, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			return nil, false
		}
		pane := PollCapture{Text: string(body)}
		switch parts[1] {
		case "ok":
		case "missing":
			if len(body) != 0 {
				return nil, false
			}
			pane.Missing = true
		case "error":
			pane.Failed = true
		default:
			return nil, false
		}
		panes[string(name)] = pane
	}
	return panes, true
}
