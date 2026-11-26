package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

const restartUsage = `usage: lectern restart --all [--dry-run] [--no-wait]

  lectern restart --all            Restart every session, same conversations
  lectern restart --all --dry-run  Only say what would happen
  lectern restart --all --no-wait  Start it and return`

type restartRow struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func printRestartRows(out io.Writer, rows []restartRow) {
	for _, r := range rows {
		line := fmt.Sprintf("#%d %s: %s", r.ID, r.Name, r.Status)
		if r.Reason != "" {
			line += " (" + r.Reason + ")"
		}
		fmt.Fprintln(out, line)
	}
}

// restartAllCommand is `lectern restart --all` (docs/sessions-restart.md).
func restartAllCommand(c *console.Client, args []string, out io.Writer) error {
	all, dry, wait := false, false, true
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		case "--dry-run":
			dry = true
		case "--no-wait":
			wait = false
		default:
			return fmt.Errorf("%s", restartUsage)
		}
	}
	if !all {
		return fmt.Errorf("%s", restartUsage)
	}
	status := func() (plan, last []restartRow, err error) {
		data, err := c.JSON("GET", "/restart-all", nil)
		if err != nil {
			return nil, nil, err
		}
		var res struct{ Plan, Last []restartRow }
		err = json.Unmarshal(data, &res)
		return res.Plan, res.Last, err
	}
	if dry {
		plan, _, err := status()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Would do:")
		printRestartRows(out, plan)
		return nil
	}
	data, err := c.JSON("POST", "/restart-all", map[string]any{})
	if err != nil {
		return err
	}
	var started struct{ Sessions []restartRow }
	if err := json.Unmarshal(data, &started); err != nil {
		return err
	}
	if !wait {
		printRestartRows(out, started.Sessions)
		return nil
	}
	queued := 0
	for _, r := range started.Sessions {
		if r.Status == "restarting" {
			queued++
		}
	}
	fmt.Fprintf(out, "Restarting %d idle session(s) one at a time; each takes up to a minute and a half…\n", queued)
	// Say each session's result as it lands, so a long run is visibly moving.
	shown := map[int64]string{}
	for _, r := range started.Sessions {
		shown[r.ID] = r.Status
	}
	deadline := time.Now().Add(30 * time.Minute)
	for {
		_, last, err := status()
		if err != nil {
			return err
		}
		busy := false
		for _, r := range last {
			busy = busy || r.Status == "restarting"
			if shown[r.ID] != r.Status && r.Status != "restarting" {
				shown[r.ID] = r.Status
				printRestartRows(out, []restartRow{r})
			}
		}
		if !busy || time.Now().After(deadline) {
			pending := 0
			for _, r := range last {
				if r.Status == "pending" {
					pending++
				}
			}
			if busy {
				fmt.Fprintln(out, "Still restarting after 30 minutes; it carries on in the server (lectern restart --all --dry-run shows the plan).")
			}
			if pending > 0 {
				fmt.Fprintf(out, "%d busy session(s) restart on their own once each is waiting for you; nothing more to do here.\n", pending)
			}
			return nil
		}
		time.Sleep(3 * time.Second)
	}
}
