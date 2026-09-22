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

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/console"
)

type shellTarget struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// shellCommandAt launches a blank shell on a selected target and immediately
// attaches to its durable session. The target API is intentionally tiny: the
// operator never has to choose a project, agent, model, or launch profile.
func shellCommandAt(cfg *config.Config, args []string, base, token string, local bool) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: lectern shell [machine]")
	}
	c := console.New(base, token)
	targets, err := listShellTargets(c)
	if err != nil {
		return err
	}
	machine := ""
	if len(args) == 1 {
		machine = strings.TrimSpace(args[0])
	}
	selected, err := chooseShellTarget(machine, targets, os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	attachHost := ""
	if !local {
		attachHost = os.Getenv("LECTERN_ATTACH_HOST")
		if attachHost == "" {
			parsed, parseErr := url.Parse(base)
			if parseErr != nil {
				return parseErr
			}
			host := parsed.Hostname()
			if host != "127.0.0.1" && host != "localhost" && host != "::1" {
				return fmt.Errorf("set LECTERN_ATTACH_HOST to the server's SSH alias before opening a remote shell")
			}
		}
	}
	body, _ := json.Marshal(map[string]string{"machine": selected.Name})
	data, err := c.Request("POST", "/shells", bytes.NewReader(body), "application/json")
	if err != nil {
		return shellEndpointError(err)
	}
	var sess struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &sess); err != nil || sess.ID <= 0 {
		return fmt.Errorf("control plane returned an invalid shell session")
	}
	attachCfg := *cfg
	if local {
		attachCfg.AuthToken = token
	}
	argv, err := attachmentCommandAt(&attachCfg, []string{"session", strconv.FormatInt(sess.ID, 10)}, base, attachHost)
	if err != nil {
		return err
	}
	return runAttachment(argv)
}

func shellEndpointError(err error) error {
	if he, ok := err.(*console.HTTPError); ok && he.Status == 404 {
		return fmt.Errorf("quick shell is unavailable on the running server; restart or update Lectern, then try again")
	}
	return err
}

func listShellTargets(c *console.Client) ([]shellTarget, error) {
	data, err := c.JSON("GET", "/targets", nil)
	if err != nil {
		return nil, err
	}
	var targets []shellTarget
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no machines are configured")
	}
	return targets, nil
}

func chooseShellTarget(query string, targets []shellTarget, in io.Reader, out io.Writer) (shellTarget, error) {
	query = strings.TrimSpace(query)
	if query != "" {
		for _, target := range targets {
			if strings.EqualFold(target.Name, query) || strconv.FormatInt(target.ID, 10) == query {
				return target, nil
			}
		}
		return shellTarget{}, fmt.Errorf("no machine named %q", query)
	}
	if len(targets) == 1 {
		return targets[0], nil
	}
	if !interactiveTerminal() {
		names := make([]string, 0, len(targets))
		for _, target := range targets {
			names = append(names, target.Name)
		}
		return shellTarget{}, fmt.Errorf("machine is required; choose one of %s", strings.Join(names, ", "))
	}
	opts := make([]console.TargetOption, 0, len(targets))
	for _, target := range targets {
		opts = append(opts, console.TargetOption{ID: target.ID, Name: target.Name, Kind: target.Kind})
	}
	selectedID, err := console.PickTarget(in, out, opts)
	if err != nil {
		return shellTarget{}, err
	}
	for _, target := range targets {
		if target.ID == selectedID {
			return target, nil
		}
	}
	return shellTarget{}, fmt.Errorf("selected machine disappeared")
}
