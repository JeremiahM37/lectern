package sessions

import (
	"encoding/base64"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
)

const PollEnd = backend.PollEnd

type PollCapture struct {
	Text            string
	Missing, Failed bool
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
