package executor

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Local runs commands on the control-plane host itself.
type Local struct{}

// NewLocal builds a Local executor.
func NewLocal() *Local { return &Local{} }

// Run shells out on this host.
func (l *Local) Run(ctx context.Context, cmd string, opts RunOpts) (Result, error) {
	return l.run(ctx, cmd, opts, nil)
}

func (l *Local) run(ctx context.Context, cmd string, opts RunOpts, input io.Reader) (Result, error) {
	d := time.Duration(opts.timeoutOrDefault() * float64(time.Second))
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	shell, err := localShell()
	if err != nil {
		return Result{}, Errf("%v", err)
	}
	c := exec.CommandContext(cctx, shell, "-c", cmd)
	if opts.Cwd != "" {
		c.Dir = opts.Cwd
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	c.Stdin = input
	err = c.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return Result{124, "", "command timed out"}, nil
	}
	rc := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			rc = ee.ExitCode()
		} else {
			return Result{}, Errf("local run failed: %v", err)
		}
	}
	return Result{rc, out.String(), errb.String()}, nil
}

// ReadFile reads from offset to EOF, treating a missing file as empty — the
// scheduler polls for an agent log that does not exist yet on every tick.
func (l *Local) ReadFile(_ context.Context, path string, offset int64) ([]byte, error) {
	f, err := os.Open(nativePath(path))
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, nil
		}
	}
	return io.ReadAll(f)
}

// WriteFile writes data, creating parent directories.
func (l *Local) WriteFile(_ context.Context, path string, data []byte) error {
	path = nativePath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Close is a no-op: nothing is pooled.
func (l *Local) Close() error { return nil }

func asExitError(err error, out **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*out = ee
		return true
	}
	return false
}
