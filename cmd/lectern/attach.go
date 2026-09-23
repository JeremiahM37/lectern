package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Desktop launchers send an ID, never a shell command or a destination host.
// The control plane resolves the current target, port and SSH/WSL wrapper.
func attach(cfg *config.Config, args []string) error {
	argv, err := attachmentCommand(cfg, args)
	if err != nil {
		return err
	}
	return runAttachment(argv, &nativeControls{
		Kind:  args[0],
		ID:    args[1],
		Base:  env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port)),
		Token: cfg.AuthToken,
	})
}

// hostedAttach is the server-side half of an SSH attachment. The generated
// command carries this private marker so a hosted peer cannot interpret its
// attachment as a fresh local-runtime request. It also ignores any inherited
// client attachment/API environment and talks to the hosted loopback service.
func hostedAttach(cfg *config.Config, args []string) error {
	if len(args) < 1 || args[0] != "attach" {
		return fmt.Errorf("usage: --hosted-attach attach KIND ID")
	}
	oldAPI, oldHost := os.Getenv("LECTERN_API"), os.Getenv("LECTERN_ATTACH_HOST")
	_ = os.Unsetenv("LECTERN_API")
	_ = os.Unsetenv("LECTERN_ATTACH_HOST")
	defer func() {
		if oldAPI != "" {
			_ = os.Setenv("LECTERN_API", oldAPI)
		}
		if oldHost != "" {
			_ = os.Setenv("LECTERN_ATTACH_HOST", oldHost)
		}
	}()
	return attachAt(cfg, args[1:], "http://127.0.0.1:"+strconv.Itoa(cfg.Port), "", false)
}

// attachAt resolves and runs an attachment. controls is false on the hosted
// peer: only the outer client owns the Ctrl-] controls prefix.
func attachAt(cfg *config.Config, args []string, base, attachHost string, controls bool) error {
	argv, err := attachmentCommandAt(cfg, args, base, attachHost)
	if err != nil {
		return err
	}
	if !controls {
		return runAttachment(argv, nil)
	}
	return runAttachment(argv, &nativeControls{Kind: args[0], ID: args[1], Base: base, Token: cfg.AuthToken})
}

func attachmentCommand(cfg *config.Config, args []string) ([]string, error) {
	return attachmentCommandAt(cfg, args, env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port)), os.Getenv("LECTERN_ATTACH_HOST"))
}

func attachmentCommandAt(cfg *config.Config, args []string, base, attachHost string) ([]string, error) {
	if err := validateTerminal(args, false); err != nil {
		return nil, err
	}
	if host := attachHost; host != "" {
		if strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\r\n") {
			return nil, fmt.Errorf("invalid SSH alias")
		}
		// The SSH peer may not have the local emulator's terminfo (e.g. xterm-kitty).
		// Scope a portable terminal type to this attachment, for both CLI entry points.
		// The hosted peer must bypass the no-API local auto-start rule. This
		// marker is handled only by the server-side binary and never comes from
		// user input.
		return []string{"env", "TERM=xterm-256color", "ssh", "-tt", host, "/usr/local/bin/lectern", "--hosted-attach", "attach", args[0], args[1]}, nil
	}
	// attach_argv contains paths on the control-plane host. Never execute it on
	// a remote client where those paths name a different machine.
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1" {
		return nil, fmt.Errorf("set LECTERN_ATTACH_HOST to the server's SSH alias for native attachment")
	}
	id, _ := strconv.ParseInt(args[1], 10, 64)
	endpoint := fmt.Sprintf("%s/api/term/%s/%d/info", strings.TrimRight(base, "/"), url.PathEscape(args[0]), id)
	req, _ := http.NewRequest("GET", endpoint, nil)
	if cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("cannot attach: control plane returned %s", res.Status)
	}
	var data struct {
		Argv []string `json:"attach_argv"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return nil, err
	}
	if len(data.Argv) == 0 {
		return nil, fmt.Errorf("no attachment command")
	}
	return data.Argv, nil
}

// A popup owns its input while attached, so the outer workspace's prefix does
// not detach the dashboard itself. Quote the entire inner command: attachment
// argv can contain tmux command separators that belong to the inner client.
func attachmentInWorkspace(argv []string, workspace string) []string {
	return workspacePopup(argv, workspace, "Lectern · Ctrl-b d returns")
}

// workspacePopup shows argv in a full-screen popup on the workspace tmux so
// its prefix cannot steal keys from the attached terminal.
func workspacePopup(argv []string, workspace, title string) []string {
	if workspace == "" {
		return argv
	}
	words := make([]string, len(argv))
	for i, word := range argv {
		words[i] = shellq.Quote(word)
	}
	return []string{"tmux", "display-popup", "-E", "-w", "100%", "-h", "100%", "-T", title, "env -u TMUX " + strings.Join(words, " ")}
}
