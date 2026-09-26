package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/mcp"
)

var localClientCommands = map[string]bool{
	"console": true, "tui": true, "shell": true, "api": true, "agent": true,
	"upload": true, "files": true, "download": true, "post": true, "live": true, "expose": true, "skill": true,
	"attach": true, "mcp": true, "promote": true, "controls": true,
	// claude/codex/gemini are the one-command agent launchers built into this
	// binary (cmd/lectern/agent_quick.go); `lectern local claude` forces one
	// onto this machine's local runtime the same way it does for every other
	// client command here. This set is used for the usage message below, not
	// as a hard gate — see localCommand's comment on the name check.
	"claude": true, "codex": true, "gemini": true,
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
	if args[0] == "supervise" {
		if len(args) != 1 {
			return errors.New("usage: lectern local supervise")
		}
		return superviseLocal(cfg)
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
	// A name outside localClientCommands is not one of this binary's fixed
	// subcommands, but it might be a custom agent registered in Settings →
	// Agents — so it is not rejected here on a static whitelist. It goes to
	// localClientCommand exactly like every recognised verb; that function's
	// clientCommandAt switch checks an unrecognised name against the live
	// registry (client.go's dynamicAgentQuick) and turns a genuine typo into
	// a clear "unknown command" from there, one round trip later rather than
	// zero.
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
		return attachAt(&localCfg, args, ep.URL, "", true)
	}
	return clientCommandAt(cfg, command, args, ep.URL, ep.Token, true)
}

func localMCPCommand(cfg *config.Config) error { return localClientCommand(cfg, "mcp", nil) }

// Stopping the supervisor leaves sessions and the runtime intact. The service
// adopts that runtime on restart; after reboot Ensure creates it from the same DB.
func superviseLocal(cfg *config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	for {
		startCtx, done := context.WithTimeout(ctx, 25*time.Second)
		_, err := localruntime.Ensure(startCtx, binary, cfg)
		done()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("supervise local runtime: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}
