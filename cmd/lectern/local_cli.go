package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/JeremiahM37/lectern/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/mcp"
)

var localClientCommands = map[string]bool{
	"console": true, "tui": true, "shell": true, "api": true, "agent": true,
	"upload": true, "files": true, "download": true, "post": true, "live": true, "expose": true, "skill": true,
	"attach": true, "mcp": true, "promote": true,
}

// localCommand is deliberately a thin routing layer. The local runtime is
// the same API and scheduler as the hosted server; only its private endpoint
// and state directory differ.
func localCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return localClientCommand(cfg, "console", nil)
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(clientHelp)
		return nil
	}
	if args[0] == "status" || args[0] == "stop" {
		if len(args) != 1 {
			return fmt.Errorf("usage: lectern local %s", args[0])
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if args[0] == "stop" {
			return localruntime.Stop(ctx)
		}
		status, err := localruntime.StatusOf(ctx)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}
	if !localClientCommands[args[0]] {
		return errors.New("usage: lectern local [console|tui|shell|api|agent|files|upload|download|skill|attach|promote|mcp|status|stop]")
	}
	return localClientCommand(cfg, args[0], args[1:])
}

func localClientCommand(cfg *config.Config, command string, args []string) error {
	if command == "help" || command == "--help" || command == "-h" || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Print(clientHelp)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	ep, err := localruntime.Ensure(ctx, binary, cfg)
	if err != nil {
		return err
	}
	if command == "mcp" {
		return mcp.New(ep.URL, ep.Token).Serve(os.Stdin, os.Stdout)
	}
	oldTmuxDir, hadTmuxDir := os.LookupEnv("TMUX_TMPDIR")
	if err := os.Setenv("TMUX_TMPDIR", ep.TmuxDir); err != nil {
		return err
	}
	defer func() {
		if hadTmuxDir {
			_ = os.Setenv("TMUX_TMPDIR", oldTmuxDir)
		} else {
			_ = os.Unsetenv("TMUX_TMPDIR")
		}
	}()
	if command == "attach" {
		localCfg := *cfg
		localCfg.AuthToken = ep.Token
		return attachAt(&localCfg, args, ep.URL, "")
	}
	return clientCommandAt(cfg, command, args, ep.URL, ep.Token, true)
}

func localMCPCommand(cfg *config.Config) error { return localClientCommand(cfg, "mcp", nil) }
