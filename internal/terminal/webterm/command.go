package webterm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Command is `lectern term-server -i SOCKET -W -b BASE COMMAND…`,
// the web terminal server (internal/terminal/webterm). It takes ttyd's
// arguments, the ones terminal.TTYDArgs builds, so either can serve.
func Command(args []string) int {
	var socket, base string
	i := 0
flags:
	for ; i < len(args); i++ {
		switch args[i] {
		case "-i", "-b":
			if i+1 >= len(args) {
				break flags
			}
			if args[i] == "-i" {
				socket = args[i+1]
			} else {
				base = args[i+1]
			}
			i++
		case "-W":
		case "--":
			i++
			break flags
		default:
			break flags
		}
	}
	argv := args[i:]
	if socket == "" || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: lectern term-server -i SOCKET [-W] -b BASE COMMAND...")
		return 2
	}
	self, _ := os.Executable()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := Serve(ctx, Options{Socket: socket, Base: base, Argv: argv, Self: self, Log: log}); err != nil {
		fmt.Fprintln(os.Stderr, "term-server:", err)
		return 1
	}
	return 0
}
