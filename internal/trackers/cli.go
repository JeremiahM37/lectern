package trackers

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// cli runs one code host's CLI on the project's target.
type cli struct {
	ex   executor.Executor
	bin  string // gh | glab
	name string // GitHub | GitLab, for messages
}

// CLIError is a CLI call that ran and failed, already worded for a person.
type CLIError struct {
	Msg string
	// Auth is set when the CLI is not signed in or not installed — nothing
	// the caller retries can fix it.
	Auth bool
}

func (e *CLIError) Error() string { return e.Msg }

// command renders bin + args as one shell command, quoting each argument.
func command(bin string, args ...string) string {
	q := make([]string, 0, len(args)+1)
	q = append(q, bin)
	for _, a := range args {
		q = append(q, executor.ShellQuote(a))
	}
	return strings.Join(q, " ")
}

func (c cli) run(ctx context.Context, timeout float64, args ...string) (string, error) {
	return c.runCmd(ctx, timeout, command(c.bin, args...))
}

func (c cli) runCmd(ctx context.Context, timeout float64, cmd string) (string, error) {
	res, err := c.ex.Run(ctx, cmd, executor.RunOpts{Timeout: timeout})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return res.Stdout, c.classify(res.RC, res.Stdout+"\n"+res.Stderr)
	}
	return res.Stdout, nil
}

func (c cli) classify(rc int, out string) error {
	low := strings.ToLower(out)
	switch {
	case rc == 127 || strings.Contains(low, "command not found"):
		return &CLIError{Auth: true, Msg: fmt.Sprintf("%s is not installed on this project's machine (%s)", c.bin, c.name)}
	case strings.Contains(low, c.bin+" auth login") || strings.Contains(low, "not logged in") ||
		strings.Contains(low, "authentication required") || strings.Contains(low, "bad credentials") ||
		strings.Contains(low, "http 401") || strings.Contains(low, "401 unauthorized"):
		return &CLIError{Auth: true, Msg: fmt.Sprintf("%s is not signed in to %s on this project's machine — run `%s auth login` there", c.bin, c.name, c.bin)}
	}
	return &CLIError{Msg: fmt.Sprintf("%s failed: %s", c.bin, clip(out, 400))}
}
