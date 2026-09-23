// Command lectern is the control plane: one binary that serves the API, the
// PWA and the scheduler that drives every dispatched agent.
//
// Usage:
//
//	lectern            run the control plane
//	lectern mcp        speak MCP on stdio against a running control plane
//	lectern version    print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/app"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/mcp"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg := config.Load()
	if len(os.Args) > 1 && os.Args[1] == "--local-engine" {
		if err := runLocalEngine(cfg, os.Args[2:], log); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--hosted-attach" {
		if err := hostedAttach(cfg, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "recovery-checkpoint" {
		if err := recoveryCheckpoint(cfg, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	// An agent launched by a hosted server inherits that server's
	// LECTERN_BASE_URL but no LECTERN_API. Its MCP tools and posts belong on
	// the board that launched it, not in a private runtime nobody is watching.
	if len(os.Args) > 1 && (os.Args[1] == "mcp" || os.Args[1] == "post" || os.Args[1] == "live" || os.Args[1] == "expose") &&
		strings.TrimSpace(os.Getenv("LECTERN_API")) == "" {
		if hosted := strings.TrimSpace(os.Getenv("LECTERN_BASE_URL")); hosted != "" {
			_ = os.Setenv("LECTERN_API", hosted)
		}
	}
	explicitRemote := strings.TrimSpace(os.Getenv("LECTERN_API")) != ""
	if len(os.Args) == 1 && interactiveTerminal() {
		var err error
		if explicitRemote {
			err = clientCommand(cfg, "console", nil)
		} else {
			err = localCommand(cfg, nil)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "console", "tui", "shell", "api", "agent", "upload", "files", "download", "post", "live", "expose", "skill", "promote", "controls", "help", "--help", "-h":
			var err error
			if explicitRemote || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
				err = clientCommand(cfg, os.Args[1], os.Args[2:])
			} else {
				err = localCommand(cfg, append([]string{os.Args[1]}, os.Args[2:]...))
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "local":
			if err := localCommand(cfg, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "serve":
			if len(os.Args) != 2 {
				fmt.Fprintln(os.Stderr, "usage: lectern serve")
				os.Exit(2)
			}
		case "attach":
			if explicitRemote {
				if err := attach(cfg, os.Args[2:]); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			} else if err := localCommand(cfg, append([]string{"attach"}, os.Args[2:]...)); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "mcp":
			if !explicitRemote {
				if err := localMCPCommand(cfg); err != nil {
					os.Exit(1)
				}
				return
			}
			// stdio belongs to the protocol here — logs would corrupt the stream
			api := env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port))
			if err := mcp.New(api, cfg.AuthToken).Serve(os.Stdin, os.Stdout); err != nil {
				os.Exit(1)
			}
			return
		case "version", "--version", "-v":
			fmt.Println(version.Version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (try: --help)\n", os.Args[1])
			os.Exit(2)
		}
	}

	a, err := app.New(cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	defer a.Close()

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	srv := &http.Server{
		Addr:    addr,
		Handler: a.Handler(),
		// no WriteTimeout: SSE streams are open-ended by design and any deadline
		// here silently truncates a live timeline
		ReadHeaderTimeout: 15 * time.Second,
	}

	if aliased := config.LegacyEnvAliased(); len(aliased) > 0 {
		log.Info("reading settings under their old names; rename them to LECTERN_*",
			"legacy", aliased)
	}
	go func() {
		log.Info("lectern listening", "addr", addr, "version", version.Version,
			"mock", cfg.Mock)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.Server.DrainStreams()
	_ = srv.Shutdown(ctx)
}

func runLocalEngine(cfg *config.Config, args []string, log *slog.Logger) error {
	values := map[string]string{}
	for len(args) > 0 {
		if len(args) < 2 {
			return errors.New("usage: --local-engine --local-state-dir DIR --local-token-fd FD --local-lock-fd FD")
		}
		key, value := args[0], args[1]
		if key != "--local-state-dir" && key != "--local-token-fd" && key != "--local-lock-fd" {
			return fmt.Errorf("unknown local engine option %q", key)
		}
		values[key] = value
		args = args[2:]
	}
	fd, err := strconv.Atoi(values["--local-lock-fd"])
	tokenFD, tokenErr := strconv.Atoi(values["--local-token-fd"])
	if err != nil || fd < 3 || tokenErr != nil || tokenFD < 3 || values["--local-state-dir"] == "" {
		return errors.New("usage: --local-engine --local-state-dir DIR --local-token-fd FD --local-lock-fd FD")
	}
	tokenFile := os.NewFile(uintptr(tokenFD), "lectern-local-token")
	if tokenFile == nil {
		return errors.New("local engine token pipe is unavailable")
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(tokenFile, 1024))
	_ = tokenFile.Close()
	if err != nil {
		return fmt.Errorf("read local runtime token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("local engine token is invalid")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return localruntime.Engine(ctx, cfg, values["--local-state-dir"], token, fd, log)
}

// env reads an environment variable with a fallback.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
