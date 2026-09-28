//go:build unix

package helpers

// Port of the inline cancel script in internal/worktree/interactive.go: mark
// an allocation's setup cancellation record. argv: cancel PLAN_JSON.

import (
	"errors"
	"io"
)

func init() {
	Register("worktree-cancel", func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		if len(args) < 2 {
			return wtUncaught(stderr, &wtTypeError{"list index out of range"})
		}
		var plan any
		if err := wtTry(func() { plan = wtPlan(args[1]) }); err != nil {
			return wtUncaught(stderr, err)
		}
		err := wtTry(func() {
			c := newSetupControl(plan, true)
			wtMust(c.access(true, nil, false).err)
		})
		var typeErr *wtTypeError
		switch {
		case err == nil:
			io.WriteString(stdout, pyDumps(newObj("workspace", plan))+"\n")
			return 0
		case wtIsTimeout(err) || errors.As(err, &typeErr):
			return wtUncaught(stderr, err)
		}
		io.WriteString(stdout, pyDumps(newObj("error", err.Error()))+"\n")
		return 1
	})
}
