package app

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
)

// autoCheckTimeout bounds one automatic check.
const autoCheckTimeout = 120 * time.Second

// autoVerify returns the agent's Verify hook: it runs the project's typecheck
// (else its build) in workdir. It reports the command as label, and label is
// empty when the project has neither, so there is nothing to check.
func autoVerify(workdir string) func(context.Context) (string, bool, string) {
	return func(ctx context.Context) (string, bool, string) {
		prof := exec.Detect(workdir)
		cmd := prof.Commands["typecheck"]
		if cmd == "" {
			cmd = prof.Commands["build"]
		}
		if cmd == "" {
			return "", true, ""
		}
		tctx, cancel := context.WithTimeout(ctx, autoCheckTimeout)
		defer cancel()
		c := osexec.CommandContext(tctx, "bash", "-lc", cmd)
		c.Dir = workdir
		c.Env = append(exec.ScrubEnv(os.Environ()), "FORCE_COLOR=0", "NO_COLOR=1")
		c.WaitDelay = 2 * time.Second
		var buf limitBuffer
		buf.max = 64 << 10
		c.Stdout = &buf
		c.Stderr = &buf
		err := c.Run()
		out := strings.TrimSpace(buf.String())
		if tctx.Err() == context.DeadlineExceeded {
			return cmd, false, out + "\n(timed out after 120s)"
		}
		if ctx.Err() != nil {
			return cmd, true, "" // aborted: say nothing, the loop checks ctx itself
		}
		return cmd, err == nil, out
	}
}
