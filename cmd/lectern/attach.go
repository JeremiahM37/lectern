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
		Kind:    args[0],
		ID:      args[1],
		Base:    env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port)),
		Token:   cfg.AuthToken,
		TabView: os.Getenv("LECTERN_TAB_VIEW") == "1",
	})
}

// hostedAttach is the server-side half of an SSH attachment. The generated
// command carries this private marker so a hosted peer cannot interpret its
// attachment as a fresh local-runtime request. It also ignores any inherited
// client attachment/API environment and talks to the hosted loopback service.
//
// By default the peer supplies its own Ctrl-] controls so clients that reach it
// without a local wrapper (legacy Linux clients, direct desktop/SSH launchers)
// still get them. A client-side wrapper that already owns the prefix marks its
// remote command so the peer leaves the attachment alone instead of stacking a
// second controls layer.
func hostedAttach(cfg *config.Config, args []string) error {
	if len(args) < 1 || args[0] != "attach" {
		return fmt.Errorf("usage: --hosted-attach attach KIND ID")
	}
	// Read and immediately drop the marker: this environment may later start
	// another, independent client that must not inherit the client's choice.
	controls := !takeClientControls()
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
	return attachAt(cfg, args[1:], "http://127.0.0.1:"+strconv.Itoa(cfg.Port), "", controls)
}

// clientControlsEnv is the private marker a wrapper-backed client puts on its
// remote command. It never travels in the local environment or through SSH env
// configuration; it is only an argv word of the remote command.
const clientControlsEnv = "LECTERN_CLIENT_CONTROLS"

// clientControlsMarked reports whether the connecting client already supplies
// the Ctrl-] controls prefix.
func clientControlsMarked() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(clientControlsEnv))) {
	case "1", "on", "true", "yes", "enabled":
		return true
	}
	return false
}

// takeClientControls consumes the marker, clearing it so a later, unrelated
// client started from this process cannot inherit the wrapper's decision.
func takeClientControls() bool {
	marked := clientControlsMarked()
	_ = os.Unsetenv(clientControlsEnv)
	return marked
}

// attachAt resolves and runs an attachment. controls is false only when the
// connecting client's own wrapper already owns the Ctrl-] prefix; otherwise
// the resolved attachment is wrapped here so direct clients get controls too.
func attachAt(cfg *config.Config, args []string, base, attachHost string, controls bool) error {
	argv, err := attachmentCommandAt(cfg, args, base, attachHost)
	if err != nil {
		return err
	}
	if !controls {
		return runAttachment(argv, nil)
	}
	return runAttachment(argv, &nativeControls{Kind: args[0], ID: args[1], Base: base, Token: cfg.AuthToken, TabView: os.Getenv("LECTERN_TAB_VIEW") == "1"})
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

// hostedRemoteBinary is the remote command of a generated hosted attachment.
const hostedRemoteBinary = "/usr/local/bin/lectern"

// withClientControlsMarker copies an attachment command and, only for the exact
// SSH shape attachmentCommandAt generates
//
//	[env TERM=xterm-256color ssh -tt HOST /usr/local/bin/lectern --hosted-attach attach KIND ID]
//
// inserts the client-controls marker into the remote command. The marker is set
// only on the peer, via a remote `env` prefix; it is never exported locally and
// never forwarded through arbitrary SSH environment configuration. Every other
// argv (a local tmux attachment, an operator command) is returned unchanged so
// the peer keeps its default server-side controls.
func withClientControlsMarker(argv []string) []string {
	out := append([]string(nil), argv...)
	if len(argv) != 10 ||
		argv[0] != "env" || argv[1] != "TERM=xterm-256color" ||
		argv[2] != "ssh" || argv[3] != "-tt" ||
		argv[5] != hostedRemoteBinary || argv[6] != "--hosted-attach" || argv[7] != "attach" {
		return out
	}
	host := argv[4]
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\r\n") {
		return out
	}
	if err := validateTerminal(argv[8:], false); err != nil {
		return out
	}
	marked := make([]string, 0, len(argv)+2)
	marked = append(marked, argv[:5]...)
	marked = append(marked, "env", clientControlsEnv+"=1")
	marked = append(marked, argv[5:]...)
	return marked
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
	return []string{"tmux", "-S", workspaceSocket(workspace), "display-popup", "-E", "-w", "100%", "-h", "100%", "-T", title, "env -u TMUX " + strings.Join(words, " ")}
}

// TMUX ends with ,server-pid,session-index; socket names themselves may contain commas.
func workspaceSocket(value string) string {
	for i := 0; i < 2; i++ {
		index := strings.LastIndex(value, ",")
		if index < 0 {
			break
		}
		value = value[:index]
	}
	return value
}
