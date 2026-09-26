package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sseEvent is one server-sent event as received, stamped on arrival.
type sseEvent struct {
	Event string
	Data  []byte
	At    time.Time
}

// streamSSE reads base+path until stop closes or the connection drops, calling
// fn for every event. It returns the error that ended the stream (nil on stop).
func streamSSE(base, path string, stop <-chan struct{}, fn func(sseEvent)) error {
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		return err
	}
	cancel := make(chan struct{})
	req.Cancel = cancel //nolint:staticcheck // a plain client without a context tree
	go func() { <-stop; close(cancel) }()
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		select {
		case <-stop:
			return nil
		default:
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("SSE %s: HTTP %d", path, resp.StatusCode)
	}
	r := bufio.NewReaderSize(resp.Body, 1<<20)
	var ev sseEvent
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			select {
			case <-stop:
				return nil
			default:
			}
			return err
		}
		s := strings.TrimRight(string(line), "\r\n")
		switch {
		case s == "":
			if ev.Event != "" {
				ev.At = time.Now()
				fn(ev)
			}
			ev = sseEvent{}
		case strings.HasPrefix(s, "event: "):
			ev.Event = s[len("event: "):]
		case strings.HasPrefix(s, "data: "):
			ev.Data = append(ev.Data, s[len("data: "):]...)
		}
	}
}

// ttydFirstOutput opens a ttyd websocket through Lectern's same-origin proxy
// exactly as the browser terminal does (token, then /ws with the "tty"
// subprotocol and an init message) and returns once the first byte of terminal
// output arrives. It is a deliberately minimal RFC 6455 client: text frames
// out, any frames in.
func ttydFirstOutput(base, termPath string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	resp, err := (&http.Client{Timeout: timeout}).Get(base + termPath + "token")
	if err != nil {
		return 0, err
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if err != nil {
		return 0, fmt.Errorf("terminal token: %w", err)
	}
	u, _ := url.Parse(base + termPath + "ws")
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	conn.SetDeadline(deadline)
	keyRaw := make([]byte, 16)
	rand.Read(keyRaw)
	key := base64.StdEncoding.EncodeToString(keyRaw)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: tty\r\n\r\n",
		u.RequestURI(), u.Host, key)
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("websocket handshake: %w", err)
	}
	if !strings.Contains(status, " 101 ") {
		return 0, fmt.Errorf("websocket handshake: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return 0, err
		}
		if line == "\r\n" {
			break
		}
	}
	init, _ := json.Marshal(map[string]any{"AuthToken": tok.Token, "columns": 120, "rows": 40})
	if err := writeFrame(conn, 0x1, init); err != nil {
		return 0, err
	}
	for {
		op, payload, err := readFrame(br)
		if err != nil {
			return 0, fmt.Errorf("waiting for output: %w", err)
		}
		if op == 0x8 {
			return 0, fmt.Errorf("terminal closed before any output")
		}
		// ttyd prefixes output with '0'; titles and preferences use other codes
		if (op == 0x1 || op == 0x2) && len(payload) > 1 && payload[0] == '0' {
			d := time.Since(start)
			writeFrame(conn, 0x8, nil)
			return d, nil
		}
	}
}

func writeFrame(w io.Writer, op byte, payload []byte) error {
	var hdr []byte
	hdr = append(hdr, 0x80|op)
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	_, err := w.Write(append(hdr, masked...))
	return err
}

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if h[1]&0x80 != 0 {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	if n > 16<<20 {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if h[1]&0x80 != 0 {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return h[0] & 0x0f, payload, nil
}
