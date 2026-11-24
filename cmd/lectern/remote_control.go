package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

const remoteControlUsage = `usage: lectern remote-control --all | --default on|off | --status

  lectern remote-control --all          Turn it on in every running claude session
  lectern remote-control --default off  New claude sessions start without it
  lectern remote-control --status       Show the default`

// remoteControlCommand is `lectern remote-control` (docs/remote-control.md
// "Every session").
func remoteControlCommand(c *console.Client, args []string, out io.Writer) error {
	switch {
	case len(args) == 1 && args[0] == "--all":
		data, err := c.JSON("POST", "/remote-control/all", map[string]any{})
		if err != nil {
			return err
		}
		var res struct {
			Sessions []struct {
				ID     int64  `json:"id"`
				Name   string `json:"name"`
				Status string `json:"status"`
				Reason string `json:"reason"`
				URL    string `json:"url"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return err
		}
		if len(res.Sessions) == 0 {
			fmt.Fprintln(out, "No running sessions.")
			return nil
		}
		for _, s := range res.Sessions {
			line := fmt.Sprintf("#%d %s: %s", s.ID, s.Name, s.Status)
			if s.URL != "" {
				line += "  " + s.URL
			}
			if s.Reason != "" {
				line += " (" + s.Reason + ")"
			}
			fmt.Fprintln(out, line)
		}
		return nil
	case len(args) == 2 && args[0] == "--default" && (args[1] == "on" || args[1] == "off"):
		value := "1"
		if args[1] == "off" {
			value = "0"
		}
		if _, err := c.JSON("PUT", "/settings", map[string]any{"remote_control_default": value}); err != nil {
			return err
		}
		fmt.Fprintf(out, "New claude sessions open in the Claude app: %s.\n", args[1])
		return nil
	case len(args) == 1 && args[0] == "--status":
		data, err := c.JSON("GET", "/settings", nil)
		if err != nil {
			return err
		}
		var values map[string]string
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		state := "on"
		if strings.TrimSpace(values["remote_control_default"]) == "0" {
			state = "off"
		}
		fmt.Fprintf(out, "New claude sessions open in the Claude app: %s.\n", state)
		return nil
	}
	return fmt.Errorf("%s", remoteControlUsage)
}
