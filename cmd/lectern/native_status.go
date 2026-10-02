package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	if len(args) < 3 || args[0] != "session" {
		return 0
	}
	c := console.New(args[2], os.Getenv("LECTERN_AUTH_TOKEN"))
	c.HTTP.Timeout = 3 * time.Second
	data, err := c.JSON("GET", "/approvals?status=pending", nil)
	if err != nil {
		return 0
	}
	if len(args) == 4 && args[3] == "allow" {
		// Ctrl+] y: allow the waiting request once, and say so on the bar.
		message := "Nothing is waiting for you · Ctrl+] m has the other actions"
		if id := pendingApprovalID(data, args[1]); id != "" {
			message = "Allowed once: " + strings.TrimPrefix(needsYouNote(data, args[1]), "⏸ Needs you: ")
			if _, err := c.JSON("POST", "/approvals/"+id+"/decision", map[string]any{"decision": "approved"}); err != nil {
				message = "Couldn't allow it: " + err.Error()
			}
		}
		_ = exec.Command("tmux", "display-message", "-d", "4000", "--", strings.ReplaceAll(message, "#", "##")).Run()
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
	note := needsYouNote(data, sessionID)
	if note == "" {
		return ""
	}
	// The keys lead, so a narrow terminal cuts the request, never the answer.
	return strings.ReplaceAll("⏸ Needs you · Ctrl+] y allow · Ctrl+] m more · "+strings.TrimPrefix(note, "⏸ Needs you: ")+" · ", "#", "##")
}

// needsYouNote is "⏸ Needs you: <what it asks>" for a session with a pending
// approval, with control characters dropped, or "".
func needsYouNote(data []byte, sessionID string) string {
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
		return "⏸ Needs you: " + summary
	}
	return ""
}
