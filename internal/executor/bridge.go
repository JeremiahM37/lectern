package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A bridged dial reaches a port on a target's loopback when the executor has
// no network path to it of its own: a Proxmox container reached through `pct
// exec`, or an SSH target whose commands run through a wrapper (inside a
// container on the SSH host, say). A small relay runs on the target through
// the executor's ordinary command path, connects to the port there, and
// carries the bytes over its stdin and stdout.
//
// It needs python3 on the target, which Lectern already requires, and a
// command path that passes stdin through. A wrapper that does not (docker exec
// without -i, for one) fails the handshake with a message saying so, rather
// than hanging.

// bridgeScript waits for one byte from the caller (proving stdin reaches it),
// connects, answers K or E<reason>, then relays both ways.
const bridgeScript = `import os,socket,sys,threading
if os.read(0,1)!=b'L': sys.exit(2)
try:
    s=socket.create_connection(('127.0.0.1',int(sys.argv[1])),timeout=15)
    s.settimeout(None)
except Exception as e:
    os.write(1,('E'+str(e).replace('\n',' ')+'\n').encode()); sys.exit(1)
os.write(1,b'K')
def up():
    try:
        while True:
            d=os.read(0,65536)
            if not d: break
            s.sendall(d)
    except Exception: pass
    try: s.shutdown(socket.SHUT_WR)
    except Exception: pass
threading.Thread(target=up,daemon=True).start()
try:
    while True:
        d=s.recv(65536)
        if not d: break
        while d:
            n=os.write(1,d); d=d[n:]
except Exception: pass
`

// ErrNoStdin means the target's command path does not pass input through, so
// nothing can be bridged over it.
var ErrNoStdin = errors.New("this target's command wrapper does not pass input through, so its " +
	"loopback cannot be reached (for docker exec, add -i)")

// bridgeTarget checks addr names a loopback port and returns the port.
func bridgeTarget(addr string) (int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return 0, fmt.Errorf("a bridged dial only reaches the target's loopback, not %s", host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", portText)
	}
	return port, nil
}

// bridgeCommand is the shell command that runs the relay for port.
func bridgeCommand(port int) string {
	return "exec python3 -u -c " + ShellQuote(bridgeScript) + " " + strconv.Itoa(port)
}

// pipeConn is a net.Conn over a relay process's stdin and stdout.
type pipeConn struct {
	r       *bufio.Reader
	w       io.WriteCloser
	closeFn func() error
	once    sync.Once
	err     error
}

type bridgeAddr string

func (a bridgeAddr) Network() string { return "bridge" }
func (a bridgeAddr) String() string  { return string(a) }

func (c *pipeConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *pipeConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *pipeConn) Close() error {
	c.once.Do(func() {
		_ = c.w.Close()
		c.err = c.closeFn()
	})
	return c.err
}
func (c *pipeConn) LocalAddr() net.Addr              { return bridgeAddr("lectern") }
func (c *pipeConn) RemoteAddr() net.Addr             { return bridgeAddr("target") }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

// handshake sends the start byte and waits for the relay's answer.
func handshake(ctx context.Context, c *pipeConn) error {
	if _, err := c.w.Write([]byte("L")); err != nil {
		return ErrNoStdin
	}
	type reply struct {
		line string
		err  error
	}
	got := make(chan reply, 1)
	go func() {
		b, err := c.r.ReadByte()
		if err != nil {
			got <- reply{err: err}
			return
		}
		if b == 'K' {
			got <- reply{}
			return
		}
		line, _ := c.r.ReadString('\n')
		got <- reply{line: string(b) + line}
	}()
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case r := <-got:
		switch {
		case r.err != nil:
			return fmt.Errorf("the relay on the target ended before connecting (is python3 installed?): %w", r.err)
		case r.line != "":
			return fmt.Errorf("could not connect on the target: %s", strings.TrimSpace(strings.TrimPrefix(r.line, "E")))
		}
		return nil
	case <-wait.Done():
		return ErrNoStdin
	}
}

// startBridge runs command locally (for pct, the `sudo pct exec` invocation)
// and returns the connection once the relay has connected.
func startBridge(ctx context.Context, command string) (net.Conn, error) {
	cmd := exec.Command("bash", "-c", command)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &pipeConn{r: bufio.NewReaderSize(stdout, 64<<10), w: stdin, closeFn: func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil
	}}
	if err := handshake(ctx, c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// BridgeCommand dials addr on a machine reached through a local command:
// wrap turns the relay's command line into the full local invocation (a
// `docker exec -i`, say), which must pass stdin through.
func BridgeCommand(ctx context.Context, addr string, wrap func(relay string) string) (net.Conn, error) {
	port, err := bridgeTarget(addr)
	if err != nil {
		return nil, err
	}
	return startBridge(ctx, wrap(bridgeCommand(port)))
}
