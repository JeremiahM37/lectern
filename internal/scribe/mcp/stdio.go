package mcp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// inheritedEnv is what an MCP server process gets from Scribe's own
// environment. Everything else (API keys, tokens) is left out; a server
// that needs one must name it in its configured env.
var inheritedEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TERM",
	"TMPDIR", "TMP", "TEMP", "SHELL", "SystemRoot", "SYSTEMROOT", "USERPROFILE",
	"HOMEDRIVE", "HOMEPATH", "COMSPEC", "PATHEXT", "WINDIR", "APPDATA", "LOCALAPPDATA",
}

// serverEnv is the allowlisted inherited environment plus the server's own
// env entries. Later entries win, which exec.Cmd guarantees.
func serverEnv(extra map[string]string) []string {
	var env []string
	for _, k := range inheritedEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+expandString(v))
	}
	return env
}

// expandString replaces ${VAR} with the value of VAR from Scribe's
// environment. Only explicit references are expanded.
func expandString(s string) string { return os.Expand(s, os.Getenv) }

func expandMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = expandString(v)
	}
	return out
}

// stdioTransport runs the server as a child process and speaks
// newline-delimited JSON-RPC on its stdin and stdout.
type stdioTransport struct {
	c      *Client
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	stderr *tailBuf
	// exitErr is why the server exited; set before exited is closed.
	exitErr error
	exited  chan struct{}
	once    sync.Once
}

func newStdioTransport(c *Client, spec config.MCPServer) (*stdioTransport, error) {
	args := make([]string, len(spec.Args))
	for i, a := range spec.Args {
		args[i] = expandString(a)
	}
	cmd := exec.Command(expandString(spec.Command), args...)
	cmd.Env = serverEnv(spec.Env)
	setProcGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	t := &stdioTransport{c: c, cmd: cmd, stdin: stdin, stderr: &tailBuf{max: 2048}, exited: make(chan struct{})}
	cmd.Stderr = t.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s: %w", spec.Command, err)
	}
	go func() {
		// Read to EOF before Wait: Wait closes the stdout pipe.
		t.readLoop(stdout)
		err := cmd.Wait()
		reason := "server exited"
		if err != nil {
			reason = fmt.Sprintf("server exited (%v)", err)
		}
		if tail := t.stderr.String(); tail != "" {
			reason += ": " + tail
		}
		t.exitErr = fmt.Errorf("%s", reason)
		c.fail(t.exitErr)
		close(t.exited)
	}()
	return t, nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			t.c.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (t *stdioTransport) send(_ context.Context, msg []byte, _ string) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.stdin.Write(append(msg, '\n'))
	if err != nil {
		// A failed write means the server is going away. Its exit reason
		// carries its stderr ("missing API token"), which says far more than
		// "broken pipe", so wait briefly for it.
		select {
		case <-t.exited:
			return t.exitErr
		case <-time.After(time.Second):
		}
	}
	return err
}

func (t *stdioTransport) open() {}

// close asks the server to exit by closing its stdin, then kills its
// process group if it has not gone within two seconds.
func (t *stdioTransport) close() error {
	t.once.Do(func() {
		t.stdin.Close()
		select {
		case <-t.exited:
			return
		case <-time.After(2 * time.Second):
		}
		_ = killTree(t.cmd)
		select {
		case <-t.exited:
		case <-time.After(2 * time.Second):
		}
	})
	return nil
}

// tailBuf keeps the last max bytes written to it, for error messages.
type tailBuf struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.TrimSpace(b.buf))
}
