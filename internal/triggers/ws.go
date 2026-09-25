package triggers

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A minimal RFC 6455 client — just enough for Slack's Socket Mode. Nothing in
// go.mod already provides a websocket client, and adding one would need
// `go get` (network access this repo's isolated test runner deliberately
// does not have — see tools/run-isolated-tests.sh's --unshare-net); Socket
// Mode's own framing is simple enough that a small stdlib implementation
// beats a new dependency here. It supports what Slack's connection actually
// sends: text frames, ping/pong (answered automatically) and close — no
// permessage-deflate, since Socket Mode does not negotiate it.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
)

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// wsDial performs the HTTP Upgrade handshake and returns a connection ready
// for ReadMessage/WriteText.
func wsDial(ctx context.Context, rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	var useTLS bool
	var defaultPort string
	switch u.Scheme {
	case "wss", "https":
		useTLS, defaultPort = true, "443"
	case "ws", "http":
		useTLS, defaultPort = false, "80"
	default:
		return nil, fmt.Errorf("unsupported websocket scheme %q", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":" + defaultPort
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if useTLS {
		conn, err = tls.DialWithDialer(d, "tcp", host, &tls.Config{ServerName: u.Hostname()})
	} else {
		conn, err = d.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	reqURI := u.RequestURI()
	req := "GET " + reqURI + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake failed: %s", resp.Status)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != wsAcceptKey(key) {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: unexpected Sec-WebSocket-Accept")
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, br: br}, nil
}

func wsAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// readFrame reads one frame and, for a fragmented message, recurses to
// collect its continuation frames — Slack's own messages are always small
// and single-frame, so this exists purely so a spec-conformant peer is never
// mishandled.
func (c *wsConn) readFrame() (opcode byte, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.br, header); err != nil {
		return 0, nil, err
	}
	fin := header[0]&0x80 != 0
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.br, ext); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.br, ext); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	if length > 32<<20 {
		return 0, nil, fmt.Errorf("websocket frame too large (%d bytes)", length)
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	if !fin {
		_, rest, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		payload = append(payload, rest...)
	}
	return opcode, payload, nil
}

// writeFrame sends one unfragmented, masked frame — every frame a client
// sends to a server MUST be masked per RFC 6455 §5.1.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	length := len(payload)
	var header []byte
	switch {
	case length <= 125:
		header = []byte{0x80 | opcode, 0x80 | byte(length)}
	case length <= 65535:
		header = make([]byte, 4)
		header[0], header[1] = 0x80|opcode, 0x80|126
		binary.BigEndian.PutUint16(header[2:], uint16(length))
	default:
		header = make([]byte, 10)
		header[0], header[1] = 0x80|opcode, 0x80|127
		binary.BigEndian.PutUint64(header[2:], uint64(length))
	}
	var maskKey [4]byte
	if _, err := rand.Read(maskKey[:]); err != nil {
		return err
	}
	masked := make([]byte, length)
	for i, b := range payload {
		masked[i] = b ^ maskKey[i%4]
	}
	buf := make([]byte, 0, len(header)+4+length)
	buf = append(buf, header...)
	buf = append(buf, maskKey[:]...)
	buf = append(buf, masked...)
	_, err := c.conn.Write(buf)
	return err
}

// WriteText sends one text frame.
func (c *wsConn) WriteText(s string) error { return c.writeFrame(wsOpText, []byte(s)) }

// SetReadDeadline bounds the next ReadMessage — Socket Mode has no
// guaranteed heartbeat cadence, so callers use this to detect a silently
// dead connection and reconnect.
func (c *wsConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// ReadMessage returns the next text/binary/close frame, transparently
// answering ping frames with pong (RFC 6455 §5.5.2) rather than surfacing
// them to the caller.
func (c *wsConn) ReadMessage() (opcode byte, payload []byte, err error) {
	for {
		op, data, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case wsOpPing:
			if werr := c.writeFrame(wsOpPong, data); werr != nil {
				return 0, nil, werr
			}
		case wsOpPong:
			// nothing to do
		case wsOpClose:
			return op, data, io.EOF
		default:
			return op, data, nil
		}
	}
}

func (c *wsConn) Close() error {
	_ = c.writeFrame(wsOpClose, nil)
	return c.conn.Close()
}
