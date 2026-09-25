package triggers

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// testWSServer is a minimal RFC 6455 SERVER — the mirror image of wsDial's
// client — used only by tests. Lectern itself is never a websocket server in
// production (Socket Mode is outbound-only), so this exists purely to give
// ws_test.go and slack_test.go something real to dial into.
//
// Every method here returns an error instead of calling t.Fatal: these run
// inside a background goroutine in every caller, and testing.T's own docs
// are explicit that FailNow (which t.Fatal calls) must only run on the
// goroutine executing the test function — calling it elsewhere unwinds that
// one goroutine via runtime.Goexit without ever unblocking whatever the main
// goroutine is waiting on, which reads as a hang, not a failure. Callers
// select on a channel with their own timeout instead.
type testWSServer struct {
	ln   net.Listener
	conn net.Conn
	br   *bufio.Reader
}

func newTestWSServer(t *testing.T) *testWSServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testWSServer{ln: ln}
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testWSServer) url() string { return "ws://" + s.ln.Addr().String() + "/link" }

// accept performs the server side of the upgrade handshake and returns once
// the connection is ready for frame exchange. It bounds the wait on its own
// listener so a client that never dials fails this call instead of hanging
// for the whole test binary's timeout.
func (s *testWSServer) accept() error {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := s.ln.Accept()
		ch <- result{conn, err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("no client connected within 5s")
	}
	if r.err != nil {
		return r.err
	}
	conn := r.conn
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		conn.Close()
		return err
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return err
	}
	conn.SetDeadline(time.Time{})
	s.conn, s.br = conn, br
	return nil
}

// writeServerFrame sends one unmasked frame — a server's frames to the
// client MUST NOT be masked, the opposite rule from wsConn.writeFrame.
func (s *testWSServer) writeServerFrame(opcode byte, payload []byte) error {
	length := len(payload)
	var header []byte
	switch {
	case length <= 125:
		header = []byte{0x80 | opcode, byte(length)}
	case length <= 65535:
		header = make([]byte, 4)
		header[0], header[1] = 0x80|opcode, 126
		binary.BigEndian.PutUint16(header[2:], uint16(length))
	default:
		return fmt.Errorf("test server only supports frames up to 65535 bytes, got %d", length)
	}
	_, err := s.conn.Write(append(header, payload...))
	return err
}

// readClientFrame reads one MASKED frame (every client frame must be masked)
// and returns its unmasked payload, bounded so a client that never sends one
// fails this call instead of hanging.
func (s *testWSServer) readClientFrame() (opcode byte, payload []byte, err error) {
	s.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer s.conn.SetReadDeadline(time.Time{})
	header := make([]byte, 2)
	if _, err := readFull(s.br, header); err != nil {
		return 0, nil, err
	}
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := readFull(s.br, ext); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(ext))
	case 127:
		return 0, nil, fmt.Errorf("test server does not support frames that large")
	}
	var mask [4]byte
	if masked {
		if _, err := readFull(s.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err := readFull(s.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func readFull(br *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := br.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *testWSServer) close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func TestWSDialHandshakeAndRoundTrip(t *testing.T) {
	server := newTestWSServer(t)
	result := make(chan error, 1)
	go func() {
		if err := server.accept(); err != nil {
			result <- err
			return
		}
		defer server.close()
		op, payload, err := server.readClientFrame()
		if err != nil {
			result <- err
			return
		}
		if op != wsOpText || string(payload) != "hello" {
			result <- fmt.Errorf("server expected a text 'hello' frame, got op=%d payload=%q", op, payload)
			return
		}
		result <- server.writeServerFrame(wsOpText, []byte("world"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := wsDial(ctx, server.url())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteText("hello"); err != nil {
		t.Fatal(err)
	}
	op, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if op != wsOpText || string(payload) != "world" {
		t.Fatalf("expected text 'world', got op=%d payload=%q", op, payload)
	}
	if err := waitChan(t, result, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWSReadMessageAnswersPing(t *testing.T) {
	server := newTestWSServer(t)
	result := make(chan error, 1)
	go func() {
		if err := server.accept(); err != nil {
			result <- err
			return
		}
		defer server.close()
		if err := server.writeServerFrame(wsOpPing, []byte("ping-data")); err != nil {
			result <- err
			return
		}
		op, payload, err := server.readClientFrame()
		if err != nil {
			result <- err
			return
		}
		if op != wsOpPong || string(payload) != "ping-data" {
			result <- fmt.Errorf("expected an automatic pong echoing the ping payload, got op=%d payload=%q", op, payload)
			return
		}
		result <- server.writeServerFrame(wsOpText, []byte("after-ping"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := wsDial(ctx, server.url())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	op, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if op != wsOpText || string(payload) != "after-ping" {
		t.Fatalf("ping must be answered transparently, not surfaced: op=%d payload=%q", op, payload)
	}
	if err := waitChan(t, result, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// waitChan bounds a receive from a background goroutine's result channel so
// a bug that stops it from ever sending fails the test instead of hanging
// it for the whole binary's timeout.
func waitChan(t *testing.T, ch <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("timed out after %s waiting for the background goroutine", timeout)
	}
}
