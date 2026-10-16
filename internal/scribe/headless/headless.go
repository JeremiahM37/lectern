// Package headless is Scribe's non-interactive runner. `lectern code -p`,
// `--json`, `--events` and `--chat` drive an app.App without the terminal UI
// and report as text, one JSON object, or JSONL events.
//
// Exit codes: 0 success, 1 error (including max steps), 2 timeout, 130
// aborted (SIGINT). In chat a turn that errors does not end the session.
package headless

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// Run executes one headless invocation and returns its exit code. The first
// SIGINT aborts the running turn; a SIGINT while idle ends the run.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	return run(ctx, args, stdin, stdout, stderr, sigs)
}

func run(parent context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	o, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stderr, usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "scribe: %v\nRun `lectern code --help` for the flags.\n", err)
		return 1
	}
	if o.version {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	cwd, err := absDir(o.cwd)
	if err != nil {
		fmt.Fprintf(stderr, "scribe: %v\n", err)
		return 1
	}
	if o.listProviders || o.listModels || o.setKey {
		return manage(parent, o, cwd, stdout, stderr)
	}

	prompt := strings.Join(o.prompt, " ")
	if !o.chat && strings.TrimSpace(prompt) == "" {
		if isTerminal(stdin) {
			fmt.Fprint(stderr, "scribe: no prompt. Give one as arguments, pipe it on stdin, or use --chat.\n\n"+usage)
			return 1
		}
		b, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "scribe: reading stdin: %v\n", err)
			return 1
		}
		prompt = strings.TrimSpace(string(b))
		if prompt == "" {
			fmt.Fprintln(stderr, "scribe: the prompt on stdin is empty")
			return 1
		}
	}

	root, cancelRoot := context.WithCancel(parent)
	defer cancelRoot()
	done := make(chan struct{})
	defer close(done)
	intr := &interrupts{cancelRoot: cancelRoot}
	go intr.watch(sigs, done)

	out := newOutput(stdout, stderr, o)
	a, err := app.Open(root, app.Options{
		Workdir:   cwd,
		Model:     o.model,
		Mode:      o.mode,
		Resume:    o.session,
		NoRepoMap: o.noRepoMap,
		System:    o.system,
		Version:   version.Version,
		// Sessions are kept for --save-session, --chat and --session. A
		// one-off run leaves nothing behind.
		Ephemeral: !(o.saveSession || o.chat || o.session != ""),
		MaxSteps:  o.maxSteps,
	})
	if err != nil {
		out.fail(err.Error())
		return 1
	}
	defer a.Close()

	r := &runner{o: o, app: a, out: out, intr: intr, root: root, includes: o.includes, cwd: cwd}
	a.Subscribe(r.onEvent)
	if o.chat {
		first := ""
		if strings.TrimSpace(prompt) != "" {
			first = prompt
		}
		return r.chat(stdin, first)
	}
	return r.once(prompt)
}

// runner drives one app.App through one or more turns.
type runner struct {
	o        options
	app      *app.App
	out      *output
	intr     *interrupts
	root     context.Context
	cwd      string
	includes []string // consumed by the first turn
}

// turnResult is what one call to turn produced.
type turnResult struct {
	stats    agent.TurnStats
	final    string
	timedOut bool
	// sigAborted is set when SIGINT aborted the turn.
	sigAborted bool
}

func (r *runner) start(chat bool) {
	r.out.start(r.app.Agent.CurrentModel().Ref, r.app.Agent.Mode(), chat, r.app.SessionID())
}

func (r *runner) once(prompt string) int {
	r.start(false)
	res := r.turn(r.withIncludes(prompt))
	r.out.finishRun(r.app.Agent.CurrentModel().Ref, r.app.Agent.Mode(), prompt, res, r.app.SessionID())
	if r.o.saveSession && r.app.SessionID() != "" {
		r.out.saved(r.app.SessionID())
	}
	return exitCode(res)
}

// chat reads turns from stdin until EOF or SIGINT while idle.
func (r *runner) chat(stdin io.Reader, first string) int {
	r.start(true)
	lines := readChatLines(r.root.Done(), stdin)
	turns := 0
	prompt := first
	for {
		if prompt == "" {
			r.out.ready()
			if isTerminal(stdin) {
				r.out.prompt()
			}
			select {
			case <-r.root.Done():
				return r.chatEnd(turns)
			case line, ok := <-lines:
				if !ok {
					return r.chatEnd(turns)
				}
				prompt = line
			}
		}
		turns++
		res := r.turn(r.withIncludes(prompt))
		r.out.turnDone(turns, res)
		prompt = ""
	}
}

func (r *runner) chatEnd(turns int) int {
	usage, _ := r.app.Agent.Usage()
	sid := r.app.SessionID()
	r.out.chatDone(turns, usage, sid)
	if r.intr.quitRequested() {
		return 130
	}
	return 0
}

// withIncludes prepends the --include files to the first turn only.
func (r *runner) withIncludes(prompt string) string {
	paths := r.includes
	r.includes = nil
	var blocks []string
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(r.cwd, p)
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			r.out.warn(fmt.Sprintf("skipping --include %s: %v", p, err))
			continue
		}
		blocks = append(blocks, "["+p+"]\n"+strings.TrimRight(string(b), "\n"))
	}
	if len(blocks) == 0 {
		return prompt
	}
	return strings.Join(append(blocks, prompt), "\n\n")
}

// turn runs one prompt. --timeout bounds this turn only.
func (r *runner) turn(prompt string) turnResult {
	base, cancel := context.WithCancel(r.root)
	defer cancel()
	ctx := base
	if r.o.timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(base, r.o.timeout)
		defer stop()
	}
	r.out.beginTurn()
	r.intr.begin(cancel)
	stats, err := r.app.Run(ctx, prompt, nil)
	sigAborted := r.intr.end()

	res := turnResult{stats: stats, final: r.out.finalText()}
	res.timedOut = r.o.timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) && (stats.Error != "" || stats.Aborted)
	res.sigAborted = sigAborted
	switch {
	case res.timedOut:
		res.stats.Error = fmt.Sprintf("timed out after %s", r.o.timeout)
	case stats.Aborted || sigAborted:
		res.stats.Aborted = true
		if res.stats.Error == "" {
			res.stats.Error = "aborted"
		}
	case stats.Error == "" && err != nil:
		res.stats.Error = err.Error()
	}
	if res.stats.Error != "" {
		r.out.turnError(res.stats.Error)
	}
	r.out.endText(res.stats)
	return res
}

func exitCode(res turnResult) int {
	switch {
	case res.sigAborted || (res.stats.Aborted && !res.timedOut):
		return 130
	case res.timedOut:
		return 2
	case res.stats.Error != "":
		return 1
	}
	return 0
}

// onEvent forwards the main agent's events. Subagent activity is not part of
// the run's output and is left out.
func (r *runner) onEvent(e agent.Event) {
	if e.AgentID != "" {
		return
	}
	r.out.handle(e)
}

// interrupts routes SIGINT. While a turn runs the first SIGINT aborts it.
// Otherwise, or on a second SIGINT, the whole run is cancelled.
type interrupts struct {
	mu          sync.Mutex
	cancelTurn  context.CancelFunc
	turnAborted bool
	quit        bool
	cancelRoot  context.CancelFunc
}

func (i *interrupts) watch(sigs <-chan os.Signal, done <-chan struct{}) {
	for {
		select {
		case <-sigs:
			i.hit()
		case <-done:
			return
		}
	}
}

func (i *interrupts) hit() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cancelTurn != nil && !i.turnAborted {
		i.turnAborted = true
		i.cancelTurn()
		return
	}
	i.quit = true
	i.cancelRoot()
}

func (i *interrupts) begin(cancel context.CancelFunc) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cancelTurn = cancel
	i.turnAborted = false
}

// end clears the running turn and reports whether SIGINT aborted it.
func (i *interrupts) end() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cancelTurn = nil
	return i.turnAborted
}

func (i *interrupts) quitRequested() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.quit
}

// readChatLines yields one submitted prompt per element. A line ending in a
// backslash continues onto the next line. Blank input is skipped.
func readChatLines(done <-chan struct{}, in io.Reader) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		send := func(s string) bool {
			select {
			case ch <- s:
				return true
			case <-done:
				return false
			}
		}
		var acc strings.Builder
		for sc.Scan() {
			line := sc.Text()
			if strings.HasSuffix(line, `\`) {
				acc.WriteString(strings.TrimSuffix(line, `\`))
				acc.WriteString("\n")
				continue
			}
			acc.WriteString(line)
			if s := strings.TrimSpace(acc.String()); s != "" && !send(s) {
				return
			}
			acc.Reset()
		}
		if s := strings.TrimSpace(acc.String()); s != "" {
			send(s)
		}
	}()
	return ch
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func absDir(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return wd, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("--cwd: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("--cwd: %s is not a folder", dir)
	}
	return abs, nil
}
