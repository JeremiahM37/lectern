package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/clipboard"
)

// startClipboardBridge makes this terminal's clipboard available to the
// session it is attached to (docs/clipboard.md): it answers the server's
// requests from the local clipboard, reports when the person types, and
// mirrors screenshots to the session's machine as they are copied. tmuxPath
// and socket, when set, name the private tmux whose client activity stands in
// for typing (the attachment is then tmux's, not ours). It returns what to
// call on each keystroke (nil if the bridge is off) and how to stop.
func startClipboardBridge(controls nativeControls, tmuxPath, socket string) (activity func(), stop func()) {
	noop := func() {}
	if controls.Kind != "session" || controls.Base == "" || !clipboardBridgeEnabled() {
		return nil, noop
	}
	id, err := strconv.ParseInt(controls.ID, 10, 64)
	if err != nil || id <= 0 {
		return nil, noop
	}
	reader := clipboard.NewReader()
	probe, cancelProbe := context.WithTimeout(context.Background(), 3*time.Second)
	_, err = reader.Types(probe)
	cancelProbe()
	if err != nil {
		// nothing to read here (a server, a bare SSH shell): do not register,
		// so this terminal is never the client that gets asked
		return nil, noop
	}
	c := &clipboard.Client{
		Base: controls.Base, Token: controls.Token, ID: clipboard.NewClientID("cli"), Session: id, Kind: "cli",
		Reader: reader, MirrorText: strings.TrimSpace(os.Getenv("LECTERN_CLIPBOARD_MIRROR_TEXT")) == "1",
	}
	ctx, cancel := context.WithCancel(context.Background())
	go c.Serve(ctx)
	if clipboardMirrorEnabled() {
		go c.Watch(ctx, 2*time.Second)
	}
	if socket != "" {
		go watchTmuxActivity(ctx, tmuxPath, socket, c.Active)
	}
	return c.Active, cancel
}

func clipboardBridgeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LECTERN_CLIPBOARD_BRIDGE"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

func clipboardMirrorEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LECTERN_CLIPBOARD_MIRROR"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// watchTmuxActivity calls active whenever the private tmux client saw input.
func watchTmuxActivity(ctx context.Context, tmuxPath, socket string, active func()) {
	last := ""
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		out, err := exec.CommandContext(ctx, tmuxPath, "-S", socket, "list-clients", "-F", "#{client_activity}").Output()
		if err != nil {
			continue
		}
		now := strings.TrimSpace(string(out))
		if now != last {
			if last != "" {
				active()
			}
			last = now
		}
	}
}
