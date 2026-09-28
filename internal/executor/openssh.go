package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenSSH runs a target's commands through the system ssh(1) client instead
// of the built-in one. It exists for everything only OpenSSH does: a
// ~/.ssh/config alias with its Include and Match blocks, Kerberos/GSSAPI,
// FIDO2 security keys used directly, certificates, and ProxyCommand the
// operator put in their own config. One multiplexed master connection
// (ControlMaster) carries every command, so a security-key touch or a
// Kerberos exchange happens once, not per command.
type OpenSSH struct {
	Host    string
	User    string
	Port    int
	KeyPath string
	Wrapper string
	Opts    SSHOptions
	// Binary is the ssh client; tests point it at a fake.
	Binary string

	dirOnce sync.Once
	dir     string
	track   connTracker
}

// NewOpenSSH builds an OpenSSH-transport executor.
func NewOpenSSH(host, user string, port int, keyPath, wrapper string, opts SSHOptions) *OpenSSH {
	o := &OpenSSH{Host: host, User: user, Port: port, KeyPath: keyPath, Wrapper: wrapper, Opts: opts, Binary: "ssh"}
	o.track.transport, o.track.via = "openssh", opts.ProxyJump
	return o
}

// ConnStatus reports the master connection's state as the last command saw it.
func (o *OpenSSH) ConnStatus() ConnStatus { return o.track.status() }

func (o *OpenSSH) controlDir() string {
	o.dirOnce.Do(func() {
		// Socket paths are limited to ~104 bytes; keep this short.
		dir, err := os.MkdirTemp("", "lec-ssh-")
		if err == nil {
			o.dir = dir
		}
	})
	return o.dir
}

// Destination is what ssh connects to: the alias when there is one.
func (o *OpenSSH) Destination() string {
	if o.Opts.Alias != "" {
		return o.Opts.Alias
	}
	return o.Host
}

// Args are the ssh(1) arguments before the destination.
func (o *OpenSSH) Args() []string {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ConnectTimeout=10"}
	if dir := o.controlDir(); dir != "" {
		args = append(args, "-o", "ControlMaster=auto", "-o", "ControlPath="+filepath.Join(dir, "%C"),
			"-o", "ControlPersist=10m")
	}
	// With an alias the config entry decides the port unless the target
	// names a non-default one.
	if o.User != "" {
		args = append(args, "-l", o.User)
	}
	if o.Port > 0 && (o.Opts.Alias == "" || o.Port != 22) {
		args = append(args, "-p", strconv.Itoa(o.Port))
	}
	if o.KeyPath != "" {
		args = append(args, "-i", expandHome(o.KeyPath))
	}
	return append(args, o.Opts.OpenSSHArgs()...)
}

// wrap applies the target's command wrapper, exactly like the built-in
// transport's buildCommand.
func (o *OpenSSH) wrap(cmd, cwd string) string {
	return (&SSH{Wrapper: o.Wrapper}).buildCommand(cmd, cwd)
}

func (o *OpenSSH) command(ctx context.Context, remote string) *exec.Cmd {
	argv := append(o.Args(), "--", o.Destination(), remote)
	return exec.CommandContext(ctx, o.Binary, argv...)
}

// sshFailed tells ssh's own exit 255 (could not connect or authenticate) from
// a remote command that happened to exit 255.
func sshFailed(rc int, stderr string) bool {
	if rc != 255 {
		return false
	}
	for _, sign := range []string{"ssh:", "Permission denied (", "Connection closed", "Connection refused",
		"Connection timed out", "Could not resolve", "kex_exchange", "Host key verification failed",
		"mux_client", "ControlSocket", "Connection reset"} {
		if strings.Contains(stderr, sign) {
			return true
		}
	}
	return false
}

const rcMarker = "\x1eLECTERN_RC="

// withRCMarker makes the remote shell print the command's exit status at the
// end of stderr. A command that execs away never prints it, and then ssh's
// own status stands.
func withRCMarker(cmd string) string {
	return cmd + "\n__lec_rc=$?; printf '\\036LECTERN_RC=%d\\n' \"$__lec_rc\" >&2; exit $__lec_rc"
}

// splitRCMarker removes the marker and returns the status it carried.
func splitRCMarker(stderr string) (string, int, bool) {
	i := strings.LastIndex(stderr, rcMarker)
	if i < 0 {
		return stderr, 0, false
	}
	rest := strings.TrimSpace(stderr[i+len(rcMarker):])
	n, err := strconv.Atoi(rest)
	if err != nil {
		return stderr, 0, false
	}
	return stderr[:i], n, true
}

// Run executes a command over the multiplexed connection.
func (o *OpenSSH) Run(ctx context.Context, cmd string, opts RunOpts) (Result, error) {
	return o.run(ctx, cmd, opts, nil, nil)
}

func (o *OpenSSH) run(parent context.Context, cmd string, opts RunOpts, input io.Reader, stdout io.Writer) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(opts.timeoutOrDefault()*float64(time.Second)))
	defer cancel()
	c := o.command(ctx, o.wrap(withRCMarker(cmd), opts.Cwd))
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr, c.Stdin = &out, &errb, input
	if stdout != nil {
		c.Stdout = stdout
	}
	err := c.Run()
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return Result{}, parent.Err()
		}
		return Result{124, out.String(), "command timed out"}, nil
	}
	rc := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			o.track.set("down", err.Error())
			return Result{}, Errf("ssh: %v", err)
		}
		rc = ee.ExitCode()
	}
	stderr, marked, ok := splitRCMarker(errb.String())
	if ok {
		// The command's own status, reported by the command itself. A
		// multiplexed ssh client sometimes loses the exit message and says
		// 255 for a command that succeeded.
		rc = marked
	}
	errb.Reset()
	errb.WriteString(stderr)
	if !ok && sshFailed(rc, errb.String()) {
		msg := strings.TrimSpace(errb.String())
		o.track.set("down", msg)
		return Result{}, Errf("ssh %s: %s", o.Destination(), msg)
	}
	o.track.set("connected", "")
	return Result{rc, out.String(), errb.String()}, nil
}

// ReadFile tails from a byte offset.
func (o *OpenSSH) ReadFile(ctx context.Context, path string, offset int64) ([]byte, error) {
	r, err := o.Run(ctx, fmt.Sprintf("tail -c +%d %s 2>/dev/null || true", offset+1, ShellQuote(path)), RunOpts{Timeout: 60})
	if err != nil {
		return nil, err
	}
	return []byte(r.Stdout), nil
}

// WriteFile streams bytes over stdin; a wrapped target gets bounded chunks.
func (o *OpenSSH) WriteFile(ctx context.Context, path string, data []byte) error {
	if o.Wrapper != "" {
		return writeFileChunks(ctx, o.Run, path, data, 2048)
	}
	r, err := o.run(ctx, streamFileCommand(path), RunOpts{Timeout: 120}, bytes.NewReader(data), nil)
	return fileWriteResult(r, err)
}

// Stream runs cmd and copies its stdout to w as it arrives.
func (o *OpenSSH) Stream(ctx context.Context, cmd string, w io.Writer, timeout float64) (Result, error) {
	return o.run(ctx, cmd, RunOpts{Timeout: timeout}, nil, w)
}

// DialTarget is `ssh -W`: a TCP stream to the target's loopback over the
// master connection. A wrapped target gets the python relay instead, like the
// built-in transport.
func (o *OpenSSH) DialTarget(ctx context.Context, addr string) (net.Conn, error) {
	var c *exec.Cmd
	bridged := o.Wrapper != ""
	if bridged {
		port, err := bridgeTarget(addr)
		if err != nil {
			return nil, err
		}
		c = o.command(context.Background(), o.wrap(bridgeCommand(o, port), ""))
	} else {
		if _, err := bridgeTarget(addr); err != nil {
			return nil, err
		}
		argv := append(o.Args(), "-W", addr, "--", o.Destination())
		c = exec.Command(o.Binary, argv...)
	}
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	pc := &pipeConn{r: bufio.NewReaderSize(stdout, 64<<10), w: stdin, closeFn: func() error {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
		_ = c.Wait()
		return nil
	}}
	if bridged {
		if err := handshake(ctx, pc); err != nil {
			pc.Close()
			return nil, err
		}
	}
	return pc, nil
}

// Close ends the master connection.
func (o *OpenSSH) Close() error {
	if o.dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	argv := append(o.Args(), "-O", "exit", "--", o.Destination())
	_ = exec.CommandContext(ctx, o.Binary, argv...).Run()
	return nil
}
