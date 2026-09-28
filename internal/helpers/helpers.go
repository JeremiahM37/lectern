// Package helpers holds the target-side helpers: small programs Lectern runs
// on an agent machine to read or change something there — a workspace file
// operation, a native conversation search, an agent hook.
//
// They used to be Python scripts passed to `python3 -c`, which made python3 a
// requirement on every agent machine. Each has a Go port here with the same
// arguments, the same output and the same exit status, run as
// `lectern helper NAME ARG…`. A target without a lectern binary keeps running
// the Python version (see Command), so an SSH box that has never had Lectern
// installed works exactly as before (docs/ptyhost.md §5).
package helpers

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Func runs one helper. It returns the process exit status.
type Func func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

var (
	mu       sync.RWMutex
	registry = map[string]Func{}
)

// Register adds a helper. Helpers register themselves from init.
func Register(name string, fn Func) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic("helpers: duplicate helper " + name)
	}
	registry[name] = fn
}

// Lookup returns a registered helper.
func Lookup(name string) (Func, bool) {
	mu.RLock()
	defer mu.RUnlock()
	fn, ok := registry[name]
	return fn, ok
}

// Names lists the registered helpers, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Main is `lectern helper NAME ARG…`.
func Main(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(os.Stderr, "usage: lectern helper NAME [ARG...]\nhelpers: %s\n", strings.Join(Names(), ", "))
		return 2
	}
	fn, ok := Lookup(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "lectern helper: unknown helper %q\n", args[0])
		return 2
	}
	return fn(args[1:], os.Stdin, os.Stdout, os.Stderr)
}

// Command renders a helper invocation for the target ex drives: the Go port
// when the target has a lectern binary, otherwise fallback, the Python
// command line it replaces. args are quoted here.
//
// The target's session backend travels with the call as
// LECTERN_SESSION_BACKEND, so a helper that asks about a session's pane asks
// the right multiplexer (see Mux).
func Command(ex executor.Executor, name string, args []string, fallback string) string {
	env := executor.TargetEnvOf(ex)
	if env.Lectern == "" {
		return fallback
	}
	cmd := Invocation(env.Lectern, name, args)
	if env.SessionBackend == "pty" {
		cmd = "LECTERN_SESSION_BACKEND=pty " + cmd
	}
	return cmd
}

// Mux returns the command that runs a tmux-language command (display-message,
// list-panes, …) against this machine's session backend: tmux, or this very
// binary's `pty` subcommand when LECTERN_SESSION_BACKEND=pty.
func Mux(args ...string) *exec.Cmd {
	if os.Getenv("LECTERN_SESSION_BACKEND") == "pty" {
		if self, err := os.Executable(); err == nil {
			return exec.Command(self, append([]string{"pty"}, args...)...)
		}
	}
	return exec.Command("tmux", args...)
}

// Invocation renders `BIN helper NAME ARG…` with every word quoted.
func Invocation(bin, name string, args []string) string {
	words := make([]string, 0, len(args)+3)
	words = append(words, shellq.Quote(bin), "helper", shellq.Quote(name))
	for _, arg := range args {
		words = append(words, shellq.Quote(arg))
	}
	return strings.Join(words, " ")
}
