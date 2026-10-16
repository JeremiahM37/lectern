package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/headless"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// scribeTUI opens Scribe's full-screen terminal UI. The args carry what the
// TUI itself must act on: "--demo" (scripted backend, no model), "--pick"
// (open the session picker) and "--prompt" followed by the text to send first.
var scribeTUI func(ctx context.Context, opts app.Options, args []string) int

func init() { scribeTUI = runScribeTUI }

// runScribeTUI runs the terminal UI. Every tab gets its own App and its own
// approval host. The first tab resumes what the flags asked for; later tabs
// start fresh sessions in the same folder.
func runScribeTUI(ctx context.Context, opts app.Options, args []string) int {
	topts := tui.Options{}
	demo := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--demo":
			demo = true
		case "--pick":
			topts.Pick = true
		case "--prompt":
			if i+1 < len(args) {
				topts.Prompt = args[i+1]
				i++
			}
		}
	}
	newBackend := func() (tui.Backend, error) {
		return tui.NewFakeBackend(), nil
	}
	if !demo {
		tabs := 0
		newBackend = func() (tui.Backend, error) {
			o := opts
			if tabs > 0 {
				o.Resume = ""
			}
			tabs++
			host := app.NewTUIHost()
			o.Host = host
			a, err := app.Open(ctx, o)
			if err != nil {
				return nil, err
			}
			return app.NewBackend(a, host), nil
		}
	}
	if err := tui.Run(ctx, newBackend, topts); err != nil {
		fmt.Fprintf(os.Stderr, "lectern code: %v\n", err)
		return 1
	}
	return 0
}

// headlessFlags make `lectern code` run without the terminal UI.
var headlessFlags = map[string]bool{
	"--headless": true, "-p": true, "--print": true, "--json": true, "--events": true,
	"--chat": true, "--list-providers": true, "--list-models": true, "--set-key": true,
}

// codeCommand is `lectern code` (alias `lectern scribe`). Headless flags, or
// a stdin that is not a terminal, run Scribe non-interactively. Anything else
// opens the full-screen TUI.
func codeCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	maybeLecternBridge()
	if wantsHeadless(args, stdin) {
		return headless.Run(context.Background(), stripHeadlessWords(args), stdin, stdout, stderr)
	}
	return codeTUI(args, stderr)
}

// wantsHeadless reports a headless flag among args, or a stdin that cannot
// drive the TUI.
func wantsHeadless(args []string, stdin io.Reader) bool {
	for _, a := range args {
		if headlessFlags[strings.SplitN(a, "=", 2)[0]] {
			return true
		}
	}
	f, ok := stdin.(*os.File)
	return !ok || !term.IsTerminal(int(f.Fd()))
}

// stripHeadlessWords removes the words that only select headless mode.
// "-p PROMPT" keeps PROMPT as a positional argument.
func stripHeadlessWords(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--headless" || a == "-p" || a == "--print" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// codeTUI parses the terminal-mode flags and hands the session to scribeTUI.
func codeTUI(args []string, stderr io.Writer) int {
	var (
		opts    = app.Options{Version: version.Version}
		tuiArgs []string
		prompt  []string
		resume  bool
		picker  bool
		cont    bool
		cwd     string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		next := func() (string, bool) {
			if hasVal {
				return val, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch name {
		case "--model", "--mode", "--cwd":
			v, ok := next()
			if !ok {
				fmt.Fprintf(stderr, "lectern code: %s needs a value\n", name)
				return 2
			}
			switch name {
			case "--model":
				opts.Model = v
			case "--mode":
				opts.Mode = v
			default:
				cwd = v
			}
		case "--resume":
			resume = true
			if hasVal {
				opts.Resume = val
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				opts.Resume = args[i]
			} else {
				picker = true
			}
		case "--continue", "-c":
			cont = true
		case "--demo":
			tuiArgs = append(tuiArgs, "--demo")
		case "--help", "-h":
			fmt.Fprintln(stderr, "Run `lectern help code` for the flags.")
			return 0
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				fmt.Fprintf(stderr, "lectern code: unknown flag %s (run `lectern help code`)\n", name)
				return 2
			}
			prompt = append(prompt, a)
		}
	}
	if opts.Mode != "" && !knownMode(opts.Mode) {
		fmt.Fprintf(stderr, "lectern code: unknown mode %q (use one of: %s)\n", opts.Mode, strings.Join(agent.Modes, ", "))
		return 2
	}
	if resume && cont {
		fmt.Fprintln(stderr, "lectern code: --resume and --continue cannot be combined")
		return 2
	}
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "lectern code: %v\n", err)
			return 1
		}
		cwd = wd
	}
	opts.Workdir = cwd
	if cont {
		root := app.FindRoot(cwd)
		if metas := session.NewStore(root).List(); len(metas) > 0 {
			opts.Resume = metas[0].ID // newest first
		} else {
			fmt.Fprintln(stderr, "lectern code: no earlier session in this folder; starting a new one")
		}
	}
	if picker {
		tuiArgs = append(tuiArgs, "--pick")
	}
	if len(prompt) > 0 {
		tuiArgs = append(tuiArgs, "--prompt", strings.Join(prompt, " "))
	}
	if scribeTUI == nil {
		fmt.Fprintln(stderr, "lectern code: the Scribe TUI is not built yet. Use -p PROMPT, --chat or --list-providers for now.")
		return 1
	}
	return scribeTUI(context.Background(), opts, tuiArgs)
}

func knownMode(m string) bool {
	for _, known := range agent.Modes {
		if m == known {
			return true
		}
	}
	return false
}
