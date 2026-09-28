package ptyhost

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Command is `lectern pty …`, the tmux-language client of this user's
// PTY host (docs/md).
func Command(args []string) int {
	socket, err := SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cli := &CLI{Socket: socket, Bin: bin, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Attach: func(c *Client, name string) error {
			return AttachTerminal(c, name, os.Stdin, os.Stdout)
		}}
	return cli.Run(args)
}

// HostCommand is `lectern ptyhost serve|status|stop`; build labels the host.
func HostCommand(args []string, build string) int {
	if len(args) == 0 {
		args = []string{"status"}
	}
	fs := flag.NewFlagSet("ptyhost "+args[0], flag.ContinueOnError)
	socket := fs.String("socket", "", "socket path (default: this user's)")
	force := fs.Bool("force", false, "stop even when sessions are running (ends them)")
	idle := fs.Duration("idle", 10*time.Second, "exit after this long with no sessions and no clients")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	path := *socket
	if path == "" {
		p, err := SocketPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		path = p
	}
	switch args[0] {
	case "serve":
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		err := Serve(Options{Socket: path, Idle: *idle, Build: build, Log: log,
			History: envInt("LECTERN_PTY_HISTORY", 0)})
		if errors.Is(err, ErrRunning) {
			fmt.Fprintln(os.Stderr, err)
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ptyhost:", err)
			return 1
		}
		return 0
	case "status":
		c, err := Dial(path)
		if err != nil {
			fmt.Printf("PTY host: not running (%s)\n", path)
			return 0
		}
		defer c.Close()
		res, err := c.Do(Request{Op: "list", Detail: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("PTY host: running, pid %d, build %s, protocol %d\nsocket: %s\nsessions: %d\n",
			c.Hello.PID, c.Hello.Build, c.Hello.Protocol, path, len(res.Sessions))
		for _, s := range res.Sessions {
			fmt.Printf("  %s  pid %d  %dx%d  %s  %d attached\n", s.Name, s.PID, s.Cols, s.Rows, s.Current, s.Clients)
		}
		return 0
	case "stop":
		c, err := Dial(path)
		if err != nil {
			fmt.Println("PTY host: not running")
			return 0
		}
		defer c.Close()
		res, err := c.Do(Request{Op: "shutdown", Force: *force})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !res.OK {
			fmt.Fprintln(os.Stderr, res.Error+" — add --force to stop it anyway")
			return 1
		}
		fmt.Println("PTY host stopped")
		return 0
	}
	fmt.Fprintf(os.Stderr, "usage: lectern ptyhost serve|status|stop [--socket PATH] [--force]\n  (%s)\n", strings.Join(args, " "))
	return 2
}

func envInt(key string, def int) int {
	var n int
	if _, err := fmt.Sscanf(os.Getenv(key), "%d", &n); err == nil && n > 0 {
		return n
	}
	return def
}
