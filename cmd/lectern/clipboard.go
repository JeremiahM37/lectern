package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/clipboard"
	"github.com/JeremiahM37/lectern/v2/internal/clipboard/xhost"
)

// clipboardCommand is `lectern clipboard ...` (docs/clipboard.md). The
// target-side subcommands (shim, ensure, daemon, set) run on an agent machine
// before any configuration is read.
func clipboardCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: lectern clipboard {serve|set|ensure|daemon|shim}\nsee docs/clipboard.md")
		return 2
	}
	switch args[0] {
	case "shim":
		if len(args) < 2 {
			return 2
		}
		return clipboard.Run(context.Background(), args[1], args[2:], os.Environ(),
			clipboard.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, nil)
	case "ensure":
		fs := flag.NewFlagSet("ensure", flag.ContinueOnError)
		bin := fs.String("bin", "", "lectern binary the shims run")
		dir := fs.String("dir", "", "state directory")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		self := *bin
		if self == "" {
			self, _ = os.Executable()
		}
		d := *dir
		if d == "" {
			d = xhost.Dir()
		}
		shims := filepath.Join(d, "bin")
		if err := os.MkdirAll(d, 0o700); err == nil {
			err = clipboard.WriteShims(shims, self)
			if err == nil {
				fmt.Printf("SHIMS=%s\n", shims)
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
		}
		env, err := xhost.Ensure(d, self)
		if err != nil {
			fmt.Fprintln(os.Stderr, "clipboard:", err)
			return 0 // shims alone still help; the caller reads what was printed
		}
		fmt.Printf("DISPLAY=%s\nXAUTHORITY=%s\n", env.Display, env.Xauthority)
		return 0
	case "daemon":
		fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
		dir := fs.String("dir", "", "state directory")
		ttl := fs.Duration("ttl", xhost.DefaultTTL, "clear mirrored content after")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		d := *dir
		if d == "" {
			d = xhost.Dir()
		}
		err := xhost.Daemon(d, *ttl, func(f string, a ...any) {
			fmt.Fprintf(os.Stderr, time.Now().Format(time.RFC3339)+" "+f+"\n", a...)
		})
		fmt.Fprintln(os.Stderr, "clipboard daemon:", err)
		return 1
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		mime := fs.String("type", "image/png", "MIME type")
		file := fs.String("file", "", "read the item from this file (default: standard input)")
		remove := fs.Bool("remove", false, "delete --file afterwards")
		clear := fs.Bool("clear", false, "empty the clipboard")
		dir := fs.String("dir", "", "state directory")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		var data []byte
		var err error
		if !*clear {
			if *file != "" {
				data, err = os.ReadFile(*file)
				if *remove {
					os.Remove(*file)
				}
			} else {
				data, err = io.ReadAll(io.LimitReader(os.Stdin, xhost.MaxItem+1))
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			if !clipboard.AllowedType(*mime) {
				fmt.Fprintln(os.Stderr, "unsupported type", *mime)
				return 2
			}
		}
		if err := xhost.Set(*dir, normalizeMime(*mime), data); err != nil {
			fmt.Fprintln(os.Stderr, "clipboard:", err)
			return 1
		}
		return 0
	}
	return -1 // client subcommands are handled where a server is known
}

func normalizeMime(m string) string {
	if m == "text/plain;charset=utf-8" {
		return "text/plain"
	}
	return m
}

// clipboardServeCommand is `lectern clipboard serve`: a standalone bridge for
// someone who reaches the host over plain SSH and so is not attached through
// Lectern. It runs until interrupted, offering this computer's clipboard to
// the named session (or, with no session, to whichever of their sessions is
// asking). The person cannot be seen typing in another program's terminal, so
// it is registered as an explicit bridge: it answers while it runs.
func clipboardServeCommand(args []string, base, token string) error {
	if len(args) == 0 || args[0] != "serve" {
		return fmt.Errorf("usage: lectern clipboard serve [--session ID] [--text]")
	}
	fs := flag.NewFlagSet("clipboard serve", flag.ContinueOnError)
	session := fs.Int64("session", 0, "session to serve (default: any of your sessions)")
	text := fs.Bool("text", false, "also copy the text clipboard to the session's machine")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	reader := clipboard.NewReader()
	if _, err := reader.Types(context.Background()); err != nil {
		return err
	}
	c := &clipboard.Client{Base: base, Token: token, ID: clipboard.NewClientID("bridge"), Session: *session,
		Kind: "bridge", Always: true, Reader: reader, MirrorText: *text,
		Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "Offering this computer's clipboard to %s. Ctrl+C stops.\n", map[bool]string{true: "your sessions", false: fmt.Sprintf("session #%d", *session)}[*session == 0])
	if *session > 0 {
		go c.Watch(ctx, 2*time.Second)
	}
	c.Serve(ctx)
	return nil
}
