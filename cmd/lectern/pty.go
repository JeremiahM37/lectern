package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// ptyCommand is `lectern pty …`, the tmux-language client of this user's
// PTY host (docs/ptyhost.md).
func ptyCommand(args []string) int {
	socket, err := ptyhost.SocketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cli := &ptyhost.CLI{Socket: socket, Bin: bin, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Attach: func(c *ptyhost.Client, name string) error {
			return ptyhost.AttachTerminal(c, name, os.Stdin, os.Stdout)
		}}
	return cli.Run(args)
}

func buildLabel() string {
	v := version.Current()
	label := v.Version
	if v.Revision != "" {
		label += " (" + v.Revision + ")"
	}
	return label
}

// ptyhostCommand is `lectern ptyhost serve|status|stop`.
func ptyhostCommand(args []string) int {
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
		p, err := ptyhost.SocketPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		path = p
	}
	switch args[0] {
	case "serve":
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		err := ptyhost.Serve(ptyhost.Options{Socket: path, Idle: *idle, Build: buildLabel(), Log: log,
			History: envInt("LECTERN_PTY_HISTORY", 0)})
		if errors.Is(err, ptyhost.ErrRunning) {
			fmt.Fprintln(os.Stderr, err)
			return 0
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ptyhost:", err)
			return 1
		}
		return 0
	case "status":
		c, err := ptyhost.Dial(path)
		if err != nil {
			fmt.Printf("PTY host: not running (%s)\n", path)
			return 0
		}
		defer c.Close()
		res, err := c.Do(ptyhost.Request{Op: "list"})
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
		c, err := ptyhost.Dial(path)
		if err != nil {
			fmt.Println("PTY host: not running")
			return 0
		}
		defer c.Close()
		res, err := c.Do(ptyhost.Request{Op: "shutdown", Force: *force})
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
