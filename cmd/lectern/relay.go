package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	relayserver "github.com/JeremiahM37/lectern/v2/internal/relay/server"
)

// relayCommand runs `lectern relay`: the end-to-end encrypted relay
// (docs/relay.md). It is a separate, stateless process meant for a small
// VPS; it opens no database and never touches this machine's Lectern.
func relayCommand(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:9120", "address to listen on (put a TLS proxy in front: phones need wss://)")
	trustForwarded := fs.Bool("trust-forwarded", false, "take client IPs from X-Forwarded-For (only behind a proxy you run)")
	var cfg relayserver.Config
	fs.IntVar(&cfg.MaxConns, "max-conns", 1024, "total open connections")
	fs.IntVar(&cfg.MaxConnsPerIP, "max-conns-per-ip", 32, "open connections per client IP")
	fs.IntVar(&cfg.ConnectsPerMinute, "connects-per-minute", 120, "new connections per client IP per minute")
	fs.IntVar(&cfg.MaxChannels, "max-hosts", 64, "connected Lectern hosts")
	fs.IntVar(&cfg.MaxDevicesPerChannel, "max-devices", 32, "open device connections per host")
	fs.IntVar(&cfg.DeviceRate, "device-rate", 256<<10, "device-to-host bytes per second")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: lectern relay [flags]")
		fmt.Fprintln(stderr, "\nRoutes end-to-end encrypted frames between a Lectern host and its paired")
		fmt.Fprintln(stderr, "phones. It never sees keys or plaintext. Requires LECTERN_RELAY_HOST_SECRET")
		fmt.Fprintln(stderr, "(32+ characters), shared with every Lectern allowed to use this relay.")
		fmt.Fprintln(stderr, "See docs/relay.md.\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	cfg.HostSecret = strings.TrimSpace(os.Getenv("LECTERN_RELAY_HOST_SECRET"))
	cfg.TrustForwarded = *trustForwarded
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg.Log = log
	srv, err := relayserver.New(cfg)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	log.Info("lectern relay listening", "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		return err
	case <-stop:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	return nil
}
