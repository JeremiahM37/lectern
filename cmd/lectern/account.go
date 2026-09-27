package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
)

const accountUsage = `usage: lectern account list
       lectern account add AGENT LABEL [--machine NAME] [--dir PATH]
       lectern account login ID
       lectern account remove ID`

type accountRow struct {
	ID           int64    `json:"id"`
	Agent        string   `json:"agent"`
	Label        string   `json:"label"`
	TargetName   string   `json:"target_name"`
	Default      bool     `json:"default"`
	SignedIn     *bool    `json:"signed_in"`
	BlockedUntil *float64 `json:"blocked_until"`
	LiveSessions int      `json:"live_sessions"`
	Usage        *struct {
		Rate5hPct *int `json:"rate_5h_pct"`
		Rate7dPct *int `json:"rate_7d_pct"`
	} `json:"usage"`
}

// accountCommand manages the logins the swap limit policy moves work between
// (docs/accounts.md). Directories are never printed: the server keeps them.
func accountCommand(cfg *config.Config, args []string, base, token string, local bool, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", accountUsage)
	}
	c := console.New(base, token)
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("%s", accountUsage)
		}
		data, err := c.JSON("GET", "/accounts", nil)
		if err != nil {
			return err
		}
		var rows []accountRow
		if err := json.Unmarshal(data, &rows); err != nil {
			return err
		}
		return printAccounts(out, rows, time.Now())
	case "add":
		body := map[string]string{}
		rest := []string{}
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--machine", "--dir":
				if i+1 >= len(args) {
					return fmt.Errorf("%s needs a value", args[i])
				}
				body[strings.TrimPrefix(args[i], "--")] = args[i+1]
				i++
			default:
				if strings.HasPrefix(args[i], "--") {
					return fmt.Errorf("unknown option %s\n%s", args[i], accountUsage)
				}
				rest = append(rest, args[i])
			}
		}
		if len(rest) != 2 {
			return fmt.Errorf("%s", accountUsage)
		}
		body["agent"], body["label"] = rest[0], rest[1]
		b, _ := json.Marshal(body)
		data, err := c.JSON("POST", "/accounts", bytes.NewReader(b))
		if err != nil {
			return err
		}
		var row accountRow
		if err := json.Unmarshal(data, &row); err != nil {
			return err
		}
		fmt.Fprintf(out, "Added %s account %q (id %d) on %s.\nSign it in: lectern account login %d  (or Settings → Accounts → Sign in)\n",
			row.Agent, row.Label, row.ID, row.TargetName, row.ID)
		return nil
	case "remove", "rm":
		if len(args) != 2 {
			return fmt.Errorf("%s", accountUsage)
		}
		if _, err := c.Request("DELETE", "/accounts/"+url.PathEscape(args[1]), nil, "application/json"); err != nil {
			return err
		}
		fmt.Fprintln(out, "Removed. Its directory and login were left on the machine.")
		return nil
	case "login":
		if len(args) != 2 {
			return fmt.Errorf("%s", accountUsage)
		}
		data, err := c.JSON("POST", "/accounts/"+url.PathEscape(args[1])+"/login", nil)
		if err != nil {
			return err
		}
		var sess struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(data, &sess); err != nil || sess.ID <= 0 {
			return fmt.Errorf("control plane returned an invalid sign-in session")
		}
		attachCfg := *cfg
		if local {
			attachCfg.AuthToken = token
		}
		id := strconv.FormatInt(sess.ID, 10)
		argv, err := attachmentCommandAt(&attachCfg, []string{"session", id}, base, os.Getenv("LECTERN_ATTACH_HOST"))
		if err != nil {
			fmt.Fprintf(out, "Sign-in terminal is session %d; open it in Lectern to finish.\n", sess.ID)
			return nil
		}
		return runAttachment(argv, &nativeControls{Kind: "session", ID: id, Base: base, Token: attachCfg.AuthToken})
	default:
		return fmt.Errorf("unknown account operation %q\n%s", args[0], accountUsage)
	}
}

func printAccounts(out io.Writer, rows []accountRow, now time.Time) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "No accounts. Add one: lectern account add claude work")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGENT\tLABEL\tMACHINE\tSIGNED IN\tSTATE\tUSAGE\tSESSIONS")
	for _, r := range rows {
		signed := "?"
		if r.SignedIn != nil {
			signed = map[bool]string{true: "yes", false: "no"}[*r.SignedIn]
		}
		state := "free"
		if r.BlockedUntil != nil {
			state = "limited until " + time.Unix(int64(*r.BlockedUntil), 0).In(now.Location()).Format("Jan 2 15:04")
		}
		usage := "-"
		if r.Usage != nil {
			parts := []string{}
			if r.Usage.Rate5hPct != nil {
				parts = append(parts, fmt.Sprintf("5h %d%%", *r.Usage.Rate5hPct))
			}
			if r.Usage.Rate7dPct != nil {
				parts = append(parts, fmt.Sprintf("7d %d%%", *r.Usage.Rate7dPct))
			}
			if len(parts) > 0 {
				usage = strings.Join(parts, " ")
			}
		}
		label := r.Label
		if r.Default {
			label += " (CLI default)"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", r.ID, r.Agent, label, r.TargetName, signed, state, usage, r.LiveSessions)
	}
	return tw.Flush()
}
