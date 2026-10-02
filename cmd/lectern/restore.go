package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
)

const restoreUsage = `usage: lectern restore [QUERY|ID] [--last] [--agent NAME] [--model M] [--profile ID] [--all] [--no-attach]

  lectern restore               List what can be restored, newest first
  lectern restore parser        Restore the one closed session matching "parser"
  lectern restore 42            Restore session 42
  lectern restore --last        Restore the session closed most recently
  lectern restore 42 --agent codex
                                Continue it in another agent, primed with its
                                last handoff or the end of its conversation`

type restoreOpts struct {
	Query, Agent, Model string
	Profile             int64
	Last, All, NoAttach bool
}

func parseRestoreArgs(args []string) (restoreOpts, error) {
	var o restoreOpts
	var words []string
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value\n%s", flag, restoreUsage)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
		case "--last":
			o.Last = true
		case "--all":
			o.All = true
		case "--no-attach":
			o.NoAttach = true
		case "--agent":
			o.Agent, err = value(&i, "--agent")
		case "--model":
			o.Model, err = value(&i, "--model")
		case "--profile":
			var raw string
			if raw, err = value(&i, "--profile"); err == nil {
				if o.Profile, err = strconv.ParseInt(raw, 10, 64); err != nil || o.Profile < 1 {
					err = fmt.Errorf("--profile needs a launch profile id")
				}
			}
		case "-h", "--help":
			return o, fmt.Errorf("%s", restoreUsage)
		default:
			if strings.HasPrefix(args[i], "-") {
				return o, fmt.Errorf("unknown option %q\n%s", args[i], restoreUsage)
			}
			words = append(words, args[i])
		}
		if err != nil {
			return o, err
		}
	}
	o.Query = strings.TrimSpace(strings.Join(words, " "))
	if o.Last && o.Query != "" {
		return o, fmt.Errorf("--last takes no query\n%s", restoreUsage)
	}
	return o, nil
}

type restorableRow struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Agent       string   `json:"agent"`
	Model       string   `json:"model"`
	ProjectName string   `json:"project_name"`
	ReasonLabel string   `json:"reason_label"`
	Action      string   `json:"action"`
	ActionLabel string   `json:"action_label"`
	Preview     string   `json:"preview"`
	EndedAt     *float64 `json:"ended_at"`
	UpdatedAt   float64  `json:"updated_at"`
}

func (r restorableRow) line() string {
	when := r.UpdatedAt
	if r.EndedAt != nil {
		when = *r.EndedAt
	}
	project := r.ProjectName
	if project == "" {
		project = "no project"
	}
	agent := r.Agent
	if r.Model != "" {
		agent += " · " + r.Model
	}
	out := fmt.Sprintf("#%-5d %s  (%s · %s · %s · %s)\n       → %s", r.ID, r.Name, project, agent, r.ReasonLabel,
		ageText(time.Since(time.Unix(int64(when), 0))), r.ActionLabel)
	if r.Preview != "" {
		preview := []rune(r.Preview)
		if len(preview) > 90 {
			preview = append(preview[:89], '…')
		}
		out += ": “" + string(preview) + "”"
	}
	return out
}

func ageText(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh %dm ago", int(age/time.Hour), int(age/time.Minute)%60)
	}
	return fmt.Sprintf("%dd %dh ago", int(age/(24*time.Hour)), int(age/time.Hour)%24)
}

func listRestorable(c *console.Client, query string, limit int, all bool) ([]restorableRow, error) {
	path := "/sessions/restorable?limit=" + strconv.Itoa(limit)
	if query != "" {
		path += "&q=" + url.QueryEscape(query)
	}
	if all {
		path += "&all=true"
	}
	data, err := c.JSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var rows []restorableRow
	return rows, json.Unmarshal(data, &rows)
}

// restoreCommand is `lectern restore`: find a closed, archived or interrupted
// session and reopen it the way the web Restore list does.
func restoreCommand(cfg *config.Config, args []string, base, token string, local bool, out io.Writer, interactive bool) error {
	return restoreCommandWith(cfg, args, base, token, local, out, interactive, func(sessionID string) error {
		return runConsoleDashboard(cfg, base, token, local, console.DashboardOptions{HistorySessionID: sessionID})
	})
}

// restoreCommandWith is restoreCommand with the history picker it opens for
// a session whose conversation cannot be found (nil: say how instead).
func restoreCommandWith(cfg *config.Config, args []string, base, token string, local bool, out io.Writer, interactive bool, openHistory func(sessionID string) error) error {
	o, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	c := console.New(base, token)
	var target *restorableRow
	id, numeric := strconv.ParseInt(strings.TrimPrefix(o.Query, "#"), 10, 64)
	switch {
	case o.Query != "" && numeric == nil:
		target = &restorableRow{ID: id, Name: "session " + strconv.FormatInt(id, 10)}
	case o.Last:
		rows, err := listRestorable(c, "", 1, false)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("nothing to restore")
		}
		target = &rows[0]
	case o.Query == "":
		rows, err := listRestorable(c, "", 20, o.All)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Fprintln(out, "Nothing to restore.")
			return nil
		}
		for _, row := range rows {
			fmt.Fprintln(out, row.line())
		}
		fmt.Fprintln(out, "\nRestore one with: lectern restore ID  (or a word from its name, folder or last message)")
		return nil
	default:
		rows, err := listRestorable(c, o.Query, 20, o.All)
		if err != nil {
			return err
		}
		switch len(rows) {
		case 0:
			return fmt.Errorf("no restorable session matches %q; run `lectern restore` to list them", o.Query)
		case 1:
			target = &rows[0]
		default:
			fmt.Fprintf(out, "%d sessions match %q:\n", len(rows), o.Query)
			for _, row := range rows {
				fmt.Fprintln(out, row.line())
			}
			return fmt.Errorf("pass an id (lectern restore %d) or a narrower query", rows[0].ID)
		}
	}
	body := map[string]any{}
	if o.Agent != "" {
		body["agent"] = o.Agent
	}
	if o.Model != "" {
		body["model"] = o.Model
	}
	if o.Profile != 0 {
		body["profile_id"] = o.Profile
	}
	data, err := c.JSON("POST", "/sessions/"+strconv.FormatInt(target.ID, 10)+"/reopen", body)
	if err != nil {
		if he, ok := err.(*console.HTTPError); ok && he.Status == 409 && (strings.Contains(he.Detail, "saved conversations") || strings.Contains(he.Detail, "history picker")) {
			if interactive && openHistory != nil {
				// Which conversation to continue is a choice: show the
				// list of them right away.
				fmt.Fprintf(out, "%s\nOpening its saved conversations so you can pick one…\n", he.Detail)
				return openHistory(strconv.FormatInt(target.ID, 10))
			}
			return fmt.Errorf("%s — run `lectern restore %d` in a terminal to pick one from a list", he.Detail, target.ID)
		}
		return err
	}
	var result struct {
		Session struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Agent string `json:"agent"`
		} `json:"session"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	fmt.Fprintf(out, "Restored #%d %q. %s\n", result.Session.ID, result.Session.Name, result.Message)
	if o.NoAttach || !interactive {
		fmt.Fprintf(out, "Attach with: lectern attach session %d\n", result.Session.ID)
		return nil
	}
	return attachAgentSession(cfg, base, token, local, result.Session.ID)
}
