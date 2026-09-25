// Package executor abstracts "run a command / move a file on a machine".
// Everything above this layer is target-kind agnostic.
package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Result is the outcome of one command.
type Result struct {
	RC     int
	Stdout string
	Stderr string
}

// OK reports a zero exit status.
func (r Result) OK() bool { return r.RC == 0 }

// Error marks a target that could not be reached at all, as opposed to a command
// that ran and failed.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Errf builds an executor Error.
func Errf(format string, args ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// Executor is one machine lectern can drive. One instance per target.
type Executor interface {
	// Run executes a shell command, optionally in cwd, bounded by timeout.
	Run(ctx context.Context, cmd string, opts RunOpts) (Result, error)
	// ReadFile returns bytes from offset to EOF; empty (not an error) if missing.
	ReadFile(ctx context.Context, path string, offset int64) ([]byte, error)
	// WriteFile writes data, creating parent directories.
	WriteFile(ctx context.Context, path string, data []byte) error
	// Close releases any pooled connection.
	Close() error
}

// RunOpts carries the optional arguments of Run.
type RunOpts struct {
	Cwd     string
	Timeout float64 // seconds; 0 means 120
}

func (o RunOpts) timeoutOrDefault() float64 {
	if o.Timeout <= 0 {
		return 120
	}
	return o.Timeout
}

// Probe is the capability check every target kind shares: versions plus disk.
func Probe(ctx context.Context, ex Executor) (map[string]any, error) {
	checks := []struct{ key, cmd string }{
		{"git", "git --version"},
		{"tmux", "tmux -V"},
		{"claude", "claude --version"},
		{"codex", "codex --version"},
		{"gemini", "gemini --version"},
		// npx is not itself an agent — it is what runs the npm-packaged ACP
		// adapters (claude-code-acp, codex-acp). Probed so Settings → Agents
		// can tell the operator whether an npx-based ACP preset can actually
		// run on a target before they save it (see AgentEditor.tsx).
		{"npx", "npx --version"},
		{"python3", "python3 --version"},
		{"disk_free", "df -h --output=avail / | tail -1"},
	}
	info := map[string]any{}
	for _, c := range checks {
		r, err := ex.Run(ctx, c.cmd, RunOpts{Timeout: 20})
		if err != nil {
			return nil, err
		}
		if r.OK() {
			info[c.key] = strings.TrimSpace(r.Stdout)
		} else {
			info[c.key] = nil
		}
	}
	return info, nil
}

// ShellQuote renders a value as one shell word.
func ShellQuote(s string) string { return shellq.Quote(s) }
