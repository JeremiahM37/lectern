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
// SIGINT, SIGTERM and SIGHUP end the UI like a quit: the running turn is
// aborted, the sessions are saved and the terminal is restored. Either way the
// exit prints the session id and how to resume it, and a signal exits 0. A
// crash inside the UI exits 1, after the sessions are saved and the terminal is
// restored.
func runScribeTUI(ctx context.Context, opts app.Options, args []string) int {
	topts := tui.Options{}
	demo, resumeTurn, telegramFlag := false, false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--demo":
			demo = true
		case "--telegram":
			telegramFlag = true
		case "--pick":
			topts.Pick = true
		case "--wizard":
			topts.Wizard = true
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
			// The TUI numbers tabs from 1 in the order it adds them, and each
			// backend here is created for exactly one new tab, so the count
			// is the tab's id.
			o.TabID = fmt.Sprintf("tab-%d", tabs)
			host := app.NewTUIHost()
			o.Host = host
			a, err := app.Open(sigCtx, o)
			if err != nil {
				return nil, err
			}
			be := app.NewBackend(a, host)
			attachRemote(a, be, first && (telegramFlag || telegramInConfig()))
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
	go func() {
		// A panic in the UI must not lose the sessions: write them out at
		// once, then report it as an error so the normal exit path runs.
		defer func() {
			if r := recover(); r != nil {
				mu.Lock()
				list := append([]tui.Backend(nil), backends...)
				mu.Unlock()
				tui.EmergencySaveAll(list)
				done <- fmt.Errorf("the UI crashed (%v); the sessions were saved as they were", r)
			}
		}()
		done <- tui.Run(sigCtx, newBackend, topts)
	}()
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
		var errs []error
		if !demo {
			errs = endSessions(list)
		}
		restoreTerminal(fd, oldState)
		if !demo {
			printSavedBanners(list)
			printShutdownErrors(os.Stderr, errs)
		}
		return 0
	}
	var errs []error
	if !demo {
		mu.Lock()
		list := append([]tui.Backend(nil), backends...)
		mu.Unlock()
		errs = endSessions(list)
		printSavedBanners(list)
	}
	if err != nil {
		// A crash in the UI: bubbletea has usually restored the terminal
		// already, but restoring again is harmless and covers the rest.
		restoreTerminal(fd, oldState)
		printShutdownErrors(os.Stderr, errs)
		fmt.Fprintf(os.Stderr, "lectern code: %v\n", err)
		return 1
	}
	printShutdownErrors(os.Stderr, errs)
	return 0
}

// endSessions ends every tab's session with tui.EndSessions, so all tabs get
// the same ordered shutdown (06 §8.3): running turns are aborted and waited
// for, then providers, background jobs, each session's close (which saves it),
// MCP and LSP, and the process groups. It returns the steps that failed. The
// caller prints them once the terminal is back to normal.
func endSessions(list []tui.Backend) []error {
	return tui.EndSessions(list, sessionSaveTimeout)
}

// printShutdownErrors prints the shutdown steps that did not finish cleanly.
// The quit screen may already have recorded some of the same failures, so
// each message is printed once.
func printShutdownErrors(w io.Writer, errs []error) {
	seen := map[string]bool{}
	for _, err := range append(tui.ShutdownErrors(), errs...) {
		msg := err.Error()
		if seen[msg] {
			continue
		}
		seen[msg] = true
		fmt.Fprintf(w, "lectern code: shutdown: %v\n", err)
	}
}

// printSavedBanners prints the line that tells the user how to resume each
// session.
func printSavedBanners(list []tui.Backend) {
	for _, be := range list {
		if id := be.SessionID(); id != "" {
			for _, line := range tui.SavedBannerLines(id, be.Transcript()) {
				fmt.Fprintln(os.Stdout, line)
			}
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

// valueFlags take the next word as their value.
var valueFlags = map[string]bool{
	"--model": true, "-m": true, "--mode": true, "--cwd": true, "--preset": true,
	"--system": true, "--include": true, "--session": true, "--resume": true, "-s": true,
	"--max-steps": true, "--timeout": true, "--plugin": true,
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
	// --version answers the same at a terminal and in a pipe, and does not
	// attach to a Lectern session or start a UI.
	for _, a := range args {
		if a == "--version" || a == "-v" {
			fmt.Fprintln(stdout, version.Version)
			return 0
		}
	}
	// --presets lists what this folder can apply. It is not a session, so it
	// answers before the bridge and the headless check.
	for _, a := range args {
		if a == "--presets" {
			return listPresetsCommand(args, stdout, stderr)
		}
	}
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
	for i, a := range args {
		// A "-p" that is the value of a flag before it (--model -p hi) is
		// kept, so the headless parser reports the missing value instead of
		// reading "hi" as the model.
		if i > 0 && valueFlags[args[i-1]] {
			out = append(out, a)
			continue
		}
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
		plugins []string // --plugin NAME, repeatable: presets for this run only
		verbose bool     // --verbose-presets
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
		case "--model", "-m", "--mode", "--cwd", "--preset":
			v, ok := next()
			if !ok {
				fmt.Fprintf(stderr, "lectern code: %s needs a value\n", name)
				return 2
			}
			switch name {
			case "--model", "-m":
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
		case "--plugin":
			v, ok := next()
			if !ok {
				fmt.Fprintln(stderr, "lectern code: --plugin needs a preset name")
				return 2
			}
			plugins = append(plugins, v)
		case "--verbose-presets":
			verbose = true
		case "--demo":
			tuiArgs = append(tuiArgs, "--demo")
		case "--wizard":
			tuiArgs = append(tuiArgs, "--wizard")
		case "--telegram":
			// The bot token is needed before the UI opens, so say so here.
			if !telegramTokenSet() {
				fmt.Fprintf(stderr, "lectern code: --telegram: no Telegram bot token: set %s to the token from @BotFather\n", telegramTokenEnv)
				return 2
			}
			tuiArgs = append(tuiArgs, "--telegram")
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
	// Check the folder before anything reads or creates it, so a typo gets one
	// clear line rather than a mkdir error from deep inside the session.
	if fi, err := os.Stat(cwd); err != nil {
		fmt.Fprintf(stderr, "lectern code: --cwd: %v\n", err)
		return 1
	} else if !fi.IsDir() {
		fmt.Fprintf(stderr, "lectern code: --cwd: %s is not a directory\n", cwd)
		return 1
	}
	opts.Workdir = cwd
	// SCRIBE_PRESETS, then --plugin, then --preset. A run applies them in memory
	// only; app.Open merges the joined list over the config.
	root := app.FindRoot(cwd)
	rp, err := config.ResolveRunPresets(root, plugins, opts.Preset)
	if err != nil {
		fmt.Fprintf(stderr, "lectern code: %v\n", err)
		return 2
	}
	for _, w := range rp.Warnings {
		fmt.Fprintf(stderr, "lectern code: %s\n", w)
	}
	if verbose {
		printRunPresetChanges(root, rp.Names, stderr)
	}
	opts.Preset = strings.Join(rp.Names, ",")
	if cont {
		root := app.FindRoot(cwd)
		// Only sessions this Scribe home recorded: one from another
		// SCRIBE_HOME is not ours to reopen here.
		if metas := session.NewStore(root).ListHere(); len(metas) > 0 {
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

// listPresetsCommand prints the presets the folder named by --cwd (default the
// current folder) can apply, and where each one lives. The TUI's /preset
// command shows the same list. There is no interactive wizard yet.
func listPresetsCommand(args []string, stdout, stderr io.Writer) int {
	cwd := ""
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if name != "--cwd" {
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "lectern code: --cwd needs a value")
				return 2
			}
			i++
			val = args[i]
		}
		cwd = val
	}
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "lectern code: %v\n", err)
			return 1
		}
		cwd = wd
	}
	root := app.FindRoot(cwd)
	ps, errs := config.ListPresets(root)
	for _, e := range errs {
		fmt.Fprintf(stderr, "lectern code: skipped: %v\n", e)
	}
	if len(ps) == 0 {
		fmt.Fprintf(stdout, "No presets. Add one as %s/NAME.json (global) or %s/NAME.json (this folder).\n",
			config.PresetDir(), config.ProjectPresetDir(root))
		return 0
	}
	fmt.Fprintln(stdout, "Presets (apply for one run with --plugin NAME, or set SCRIBE_PRESETS=a,b):")
	for _, p := range ps {
		fmt.Fprintf(stdout, "  %-20s %-8s %s\n", p.Name, p.Scope(), p.Path)
	}
	return 0
}

// printRunPresetChanges reports, before the session opens, what each preset
// for this run changes. It reads the same config app.Open will merge over.
func printRunPresetChanges(root string, names []string, stderr io.Writer) {
	lines, err := config.RunPresetReport(root, names)
	if err != nil {
		fmt.Fprintf(stderr, "lectern code: presets: %v\n", err)
		return
	}
	for _, l := range lines {
		fmt.Fprintf(stderr, "lectern code: %s\n", l)
	}
}
