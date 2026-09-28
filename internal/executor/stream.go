package executor

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

// Streamer is an executor that can copy a command's output straight to a
// writer instead of holding it in memory: how a folder download reaches the
// browser as it is produced.
type Streamer interface {
	Stream(ctx context.Context, cmd string, w io.Writer, timeout float64) (Result, error)
}

// Stream on the control plane.
func (l *Local) Stream(ctx context.Context, cmd string, w io.Writer, timeout float64) (Result, error) {
	return streamLocal(ctx, cmd, w, timeout)
}

// Stream inside a Proxmox container.
func (p *Pct) Stream(ctx context.Context, cmd string, w io.Writer, timeout float64) (Result, error) {
	return streamLocal(ctx, Wrap(p.VMID, cmd, ""), w, timeout)
}

func streamLocal(ctx context.Context, cmd string, w io.Writer, timeout float64) (Result, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(RunOpts{Timeout: timeout}.timeoutOrDefault()*float64(time.Second)))
	defer cancel()
	shell, err := localShell()
	if err != nil {
		return Result{}, Errf("%v", err)
	}
	c := exec.CommandContext(cctx, shell, "-c", cmd)
	var errb bytes.Buffer
	c.Stdout, c.Stderr = w, &errb
	err = c.Run()
	if cctx.Err() != nil {
		return Result{124, "", "command timed out"}, nil
	}
	rc := 0
	if err != nil {
		var ee *exec.ExitError
		if !asExitError(err, &ee) {
			return Result{}, Errf("local run failed: %v", err)
		}
		rc = ee.ExitCode()
	}
	return Result{RC: rc, Stderr: errb.String()}, nil
}

// Stream over the pooled SSH connection.
func (s *SSH) Stream(ctx context.Context, cmd string, w io.Writer, timeout float64) (Result, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(RunOpts{Timeout: timeout}.timeoutOrDefault()*float64(time.Second)))
	defer cancel()
	conn, err := s.client(cctx)
	if err != nil {
		return Result{}, err
	}
	sess, err := conn.NewSession()
	if err != nil {
		s.dropClient(conn)
		return Result{}, Errf("ssh session failed: %v", err)
	}
	defer sess.Close()
	stop := context.AfterFunc(cctx, func() { _ = sess.Close() })
	defer stop()
	var errb bytes.Buffer
	sess.Stdout, sess.Stderr = w, &errb
	err = sess.Run(s.buildCommand(cmd, ""))
	if cctx.Err() != nil {
		return Result{124, "", "command timed out"}, nil
	}
	rc := 0
	if err != nil {
		if exit, ok := err.(interface{ ExitStatus() int }); ok {
			rc = exit.ExitStatus()
		} else {
			return Result{}, Errf("ssh run failed: %v", err)
		}
	}
	return Result{RC: rc, Stderr: errb.String()}, nil
}
