package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/headless"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// scribeTUI opens Scribe's full-screen terminal UI. The args carry what the
// TUI itself must act on: "--demo" (scripted backend, no model), "--pick"
// (open the session picker), "--prompt" followed by the text to send first,
// and "--continue-turn" (re-run the interrupted turn of the session opened by
// `lectern code --continue`).
var scribeTUI func(ctx context.Context, opts app.Options, args []string) int

func init() { scribeTUI = runScribeTUI }

// sessionSaveTimeout bounds how long leaving waits for a running turn to
// write its checkpoints and metadata.
const sessionSaveTimeout = 5 * time.Second

// runScribeTUI runs the terminal UI. Every tab gets its own App and its own
// approval host. The first tab resumes what the flags asked for; later tabs
// start fresh sessions in the same folder.
//
// SIGTERM and SIGHUP end the UI like a quit: the running turn is aborted, the
// sessions are saved and the terminal is restored. Either way the exit prints
// the session id and how to resume it, and a signal exits 0.
func runScribeTUI(ctx context.Context, opts app.Options, args []string) int {
	topts := tui.Options{}
	demo, resumeTurn := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--demo":
			demo = true
		case "--pick":
			topts.Pick = true
		case "--continue-turn":
			resumeTurn = true
		case "--prompt":
			if i+1 < len(args) {
				topts.Prompt = args[i+1]
				i++
			}
		}
	}
	sigCtx, stop := tui.WithTermSignals(ctx)
	defer stop()

	var (
		mu       sync.Mutex
		backends []tui.Backend // every tab's session, for the exit banner
	)
	newBackend := func() (tui.Backend, error) {
		return tui.NewFakeBackend(), nil
	}
	if !demo {
		tabs := 0
		newBackend = func() (tui.Backend, error) {
			o := opts
			first := tabs == 0
			if !first {
				o.Resume = ""
			}
			tabs++
			host := app.NewTUIHost()
			o.Host = host
			a, err := app.Open(sigCtx, o)
			if err != nil {
				return nil, err
			}
			be := app.NewBackend(a, host)
			if c, ok := be.(tui.Continuer); ok && first && resumeTurn {
				started, cerr := c.Continue()
				switch {
				case cerr != nil:
					fmt.Fprintf(os.Stderr, "lectern code: cannot continue the turn: %v\n", cerr)
				case !started:
					fmt.Fprintln(os.Stderr, "lectern code: nothing to continue; the last turn in this session finished")
				}
			}
			mu.Lock()
			backends = append(backends, be)
			mu.Unlock()
			return be, nil
		}
	}
	// Keep the terminal as it was before the UI changed it, so a signal can put
	// it back. Stdin may not be a terminal (then there is nothing to restore).
	fd := int(os.Stdin.Fd())
	oldState, _ := term.GetState(fd)

	done := make(chan error, 1)
	go func() { done <- tui.Run(sigCtx, newBackend, topts) }()
	var err error
	select {
	case err = <-done:
	case <-sigCtx.Done():
		// A signal. Bubbletea does not return from Run when its context is
		// cancelled while a command is still pending, so the sessions are ended
		// and the terminal restored here rather than waiting for it.
		mu.Lock()
		list := append([]tui.Backend(nil), backends...)
		mu.Unlock()
		if !demo {
			endSessions(list)
			printSavedBanners(list)
		}
		restoreTerminal(fd, oldState)
		return 0
	}
	if !demo {
		mu.Lock()
		list := append([]tui.Backend(nil), backends...)
		mu.Unlock()
		endSessions(list)
		printSavedBanners(list)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lectern code: %v\n", err)
		return 1
	}
	return 0
}

// endSessions ends every tab's session: a running turn is aborted and saved.
func endSessions(list []tui.Backend) {
	for _, be := range list {
		if s, ok := be.(tui.Stopper); ok {
			s.Shutdown(sessionSaveTimeout)
		}
	}
}

// printSavedBanners prints the line that tells the user how to resume each
// session.
func printSavedBanners(list []tui.Backend) {
	for _, be := range list {
		if id := be.SessionID(); id != "" {
			fmt.Fprintln(os.Stdout, tui.SavedBanner(id))
		}
	}
}

// restoreTerminal undoes what the full-screen UI changed: raw mode, the
// alternate screen, the cursor, mouse reporting and bracketed paste.
func restoreTerminal(fd int, old *term.State) {
	if old != nil {
		_ = term.Restore(fd, old)
	}
	fmt.Fprint(os.Stdout, "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?25h\x1b[?1049l")
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
		case "--model", "--mode", "--cwd", "--preset":
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
			case "--preset":
				opts.Preset = v
			default:
				cwd = v
			}
		case "--resume", "--session", "-s":
			// -s/--session is the same as --resume: an id, or the picker alone.
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
	if opts.Preset != "" {
		if _, err := config.FindPreset(app.FindRoot(cwd), opts.Preset); err != nil {
			fmt.Fprintf(stderr, "lectern code: %v\n", err)
			return 2
		}
	}
	if cont {
		root := app.FindRoot(cwd)
		if metas := session.NewStore(root).List(); len(metas) > 0 {
			opts.Resume = metas[0].ID // newest first
			// Re-run its interrupted turn, if it has one, once the UI is up.
			tuiArgs = append(tuiArgs, "--continue-turn")
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
