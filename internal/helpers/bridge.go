package helpers

// bridge is internal/executor's bridgeScript: a relay between its own stdin
// and stdout and a TCP port on the machine's loopback, for a target Lectern
// reaches only through a command (pct exec, a wrapped SSH target). It waits
// for one byte from the caller (proving stdin reaches it), connects, answers
// K or E<reason>, then copies both ways until the port closes.
//
//	exec lectern helper bridge PORT

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

func init() { Register("bridge", bridge) }

func bridge(args []string, stdin io.Reader, stdout, _ io.Writer) int {
	var first [1]byte
	if n, _ := stdin.Read(first[:]); n != 1 || first[0] != 'L' {
		return 2
	}
	conn, err := bridgeDial(args)
	if err != nil {
		io.WriteString(stdout, "E"+strings.ReplaceAll(err.Error(), "\n", " ")+"\n")
		return 1
	}
	if _, err := stdout.Write([]byte("K")); err != nil {
		return 0
	}
	go func() {
		// Caller to port; the caller closing its side ends only this half.
		relay(conn, stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	// Port to caller; when the port closes the relay ends, as the Python
	// version's main thread did.
	relay(stdout, conn)
	return 0
}

// relay copies chunk by chunk, writing each as soon as it is read, until
// either side fails or ends.
func relay(dst io.Writer, src io.Reader) {
	buf := make([]byte, 65536)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func bridgeDial(args []string) (net.Conn, error) {
	if len(args) < 1 {
		return nil, errors.New("list index out of range")
	}
	port, err := strconv.Atoi(wPyStrip(args[0]))
	if err != nil {
		return nil, errors.New("invalid literal for int() with base 10: " + pyReprString(args[0]))
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 15*time.Second)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, errors.New("timed out")
		}
		if errno, ok := pyErrno(err); ok {
			return nil, &pyOSError{errno: errno}
		}
		return nil, err
	}
	return conn, nil
}
