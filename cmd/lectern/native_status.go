package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

// attachStatusFlag is the private entry point the attach bar polls. It prints
// one line when the attached session is waiting for an approval, and nothing
// otherwise, so the bar says "needs you" in the terminal the person is
// actually looking at.
const attachStatusFlag = "--attach-status"

func attachStatusCommand(args []string) int {
	if len(args) != 3 || args[0] != "session" {
		return 0
	}
	c := console.New(args[2], os.Getenv("LECTERN_AUTH_TOKEN"))
	c.HTTP.Timeout = 3 * time.Second
	data, err := c.JSON("GET", "/approvals?status=pending", nil)
	if err != nil {
		return 0
	}
	fmt.Print(attachStatusLine(data, args[1]))
	return 0
}

// attachStatusLine is the bar's "needs you" note for one session. tmux
// expands formats in a status job's output, and the summary is text the
// agent chose, so every # is doubled and control characters are dropped:
// "#(…)" in a command must never become a job tmux runs.
func attachStatusLine(data []byte, sessionID string) string {
	var rows []struct {
		SessionID int64          `json:"session_id"`
		ToolName  string         `json:"tool_name"`
		Input     map[string]any `json:"input"`
	}
	if json.Unmarshal(data, &rows) != nil {
		return ""
	}
	for _, r := range rows {
		if fmt.Sprint(r.SessionID) != sessionID {
			continue
		}
		summary := r.ToolName
		for _, field := range []string{"command", "path", "file_path", "url", "pattern"} {
			if s, ok := r.Input[field].(string); ok && strings.TrimSpace(s) != "" {
				summary += ": " + strings.TrimSpace(s)
				break
			}
		}
		summary = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, summary)
		if runes := []rune(summary); len(runes) > 60 {
			summary = string(runes[:60]) + "…"
		}
		return strings.ReplaceAll("⏸ Needs you: "+summary+" · Ctrl+] m answers · ", "#", "##")
	}
	return ""
}
