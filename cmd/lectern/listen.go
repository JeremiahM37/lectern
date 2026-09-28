package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// listenHost decides the address a hosted server (`lectern serve`, or bare
// `lectern` with no terminal) binds. With nothing configured it stays on
// 127.0.0.1, unless a sign-in that protects remote access is set up (a
// token, or Tailscale identity), in which case it listens on every interface
// as it always has. An explicit LECTERN_HOST is kept, but a non-loopback one
// with no sign-in is refused unless insecure is set: anyone on the network
// could otherwise start agents on this machine.
func listenHost(cfg *config.Config, hostSet, insecure bool, tailscaleUp func() bool) (string, error) {
	if !hostSet {
		switch auth.ResolveMode(cfg.Auth, "0.0.0.0", cfg.AuthToken != "", tailscaleUp) {
		case auth.ModeToken, auth.ModeTailscale:
			return "0.0.0.0", nil
		}
		return "127.0.0.1", nil
	}
	if auth.IsLoopbackHost(cfg.Host) || insecure {
		return cfg.Host, nil
	}
	if auth.ResolveMode(cfg.Auth, cfg.Host, cfg.AuthToken != "", tailscaleUp) != auth.ModeNone {
		return cfg.Host, nil
	}
	return "", fmt.Errorf(`refusing to listen on %s with no sign-in: anyone on your network could run agents on this machine.
Do one of these:
  - set LECTERN_AUTH_TOKEN to a long random value (browsers then ask for it)
  - use Tailscale identity: install Tailscale, or set LECTERN_AUTH=tailscale
  - unset LECTERN_HOST to listen on 127.0.0.1 only
  - pass --insecure-listen (or set LECTERN_INSECURE_LISTEN=1) if something in front of Lectern already checks who is asking`, cfg.Host)
}

// serveArgs parses `lectern serve`'s flags.
func serveArgs(args []string) (insecure, help bool, err error) {
	for _, a := range args {
		switch a {
		case "--insecure-listen":
			insecure = true
		case "--help", "-h":
			help = true
		default:
			return false, false, errors.New("usage: lectern serve [--insecure-listen]")
		}
	}
	return insecure, help, nil
}

// resolveListen applies listenHost to cfg before the server is built.
func resolveListen(cfg *config.Config, insecure bool) error {
	_, hostSet := os.LookupEnv("LECTERN_HOST")
	hostSet = hostSet && strings.TrimSpace(os.Getenv("LECTERN_HOST")) != ""
	insecure = insecure || os.Getenv("LECTERN_INSECURE_LISTEN") == "1"
	host, err := listenHost(cfg, hostSet, insecure, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := auth.NewLocalAPIClient(cfg.TailscaleSocket).Status(ctx)
		return err == nil
	})
	if err != nil {
		return err
	}
	cfg.Host = host
	return nil
}
