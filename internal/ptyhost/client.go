package ptyhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"
)

// Client is one connection to a host.
type Client struct {
	conn  net.Conn
	Hello hello
}

// Dial connects to the host on socket and shakes hands.
func Dial(socket string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotRunning, err)
	}
	c := &Client{conn: conn}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeJSON(conn, hello{Hello: helloName, Protocol: Protocol, Min: MinProtocol}); err != nil {
		conn.Close()
		return nil, err
	}
	if err := readJSON(conn, &c.Hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("the PTY host did not answer: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	if c.Hello.Error != "" {
		conn.Close()
		return nil, errors.New(c.Hello.Error)
	}
	if c.Hello.Hello != helloName || c.Hello.Protocol < MinProtocol || c.Hello.Min > Protocol {
		conn.Close()
		return nil, fmt.Errorf("the PTY host speaks protocol %d-%d; this lectern speaks %d-%d",
			c.Hello.Min, c.Hello.Protocol, MinProtocol, Protocol)
	}
	return c, nil
}

// Supports reports whether the host announced op.
func (c *Client) Supports(op string) bool { return slices.Contains(c.Hello.Ops, op) }

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Do sends one request and reads its answer.
func (c *Client) Do(req Request) (Response, error) {
	if !c.Supports(req.Op) {
		return Response{}, fmt.Errorf("the running PTY host (build %s) does not support %q; it is replaced once its sessions have ended", c.Hello.Build, req.Op)
	}
	if err := writeJSON(c.conn, req); err != nil {
		return Response{}, err
	}
	var res Response
	if err := readJSON(c.conn, &res); err != nil {
		return Response{}, err
	}
	return res, nil
}

// Stream is an attached session.
type Stream struct {
	conn net.Conn
	wmu  sync.Mutex
	// Exited is set once the host said the session ended.
	Exited bool
}

// Attach turns the connection into a stream of the named session. The size,
// when given, becomes the session's size.
func (c *Client) Attach(name string, cols, rows int) (*Stream, error) {
	res, err := c.Do(Request{Op: "attach", Name: name, Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, responseError(res, name)
	}
	return &Stream{conn: c.conn}, nil
}

// Read returns terminal output. It returns io.EOF when the session ends or
// the host goes away.
func (s *Stream) Read() ([]byte, error) {
	for {
		kind, payload, err := readFrame(s.conn)
		if err != nil {
			return nil, err
		}
		switch kind {
		case frameOutput:
			return payload, nil
		case frameExit:
			s.Exited = true
			return nil, io.EOF
		}
	}
}

// Write sends keyboard input.
func (s *Stream) Write(b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeFrame(s.conn, frameInput, b)
}

// Resize changes the session's size.
func (s *Stream) Resize(cols, rows int) error {
	b, _ := json.Marshal(resize{Cols: cols, Rows: rows})
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeFrame(s.conn, frameResize, b)
}

// Close detaches.
func (s *Stream) Close() error { return s.conn.Close() }

// responseError turns a failed response into the error tmux would print, so
// callers that key on tmux's wording keep working.
func responseError(res Response, name string) error {
	if res.Missing {
		return &MissingError{Name: name}
	}
	return errors.New(res.Error)
}

// MissingError is tmux's "can't find session".
type MissingError struct{ Name string }

func (e *MissingError) Error() string { return "can't find session: " + e.Name }

// IsNotRunning reports whether err means no host is listening.
func IsNotRunning(err error) bool { return errors.Is(err, errNotRunning) }

// Ensure connects to the host on socket, starting one from bin when none is
// running. Only a caller that is about to create a session should start one;
// everything else treats "not running" as "no sessions".
func Ensure(socket, bin string) (*Client, error) {
	if c, err := Dial(socket); err == nil {
		return c, nil
	} else if !IsNotRunning(err) {
		return nil, err
	}
	if err := ensureDir(socket); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(logPath(socket), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd := exec.Command(bin, "ptyhost", "serve", "--socket", socket)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	cmd.Env = os.Environ()
	proc, err := startDetached(cmd)
	if err != nil {
		return nil, fmt.Errorf("start the PTY host: %w", err)
	}
	exited := make(chan struct{})
	go func() { _, _ = proc.Wait(); close(exited) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := Dial(socket)
		if err == nil {
			return c, nil
		}
		select {
		case <-exited:
			// Another starter may have won the lock; its host is then the
			// one to use.
			if c, err := Dial(socket); err == nil {
				return c, nil
			}
			return nil, fmt.Errorf("the PTY host exited at start; see %s", logPath(socket))
		default:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the PTY host did not start: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
