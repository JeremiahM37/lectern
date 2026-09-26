package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Tunnel frame types, carried inside Noise transport messages. One Noise
// session multiplexes any number of streams; a stream is one HTTP exchange
// or one WebSocket. Device-to-host and host-to-device types never overlap,
// so a peer can reject a frame that could only have come from itself.
const (
	FrameRequest      byte = 1  // device → host: RequestHead JSON
	FrameRequestBody  byte = 2  // device → host: body bytes
	FrameRequestEnd   byte = 3  // device → host: request body complete
	FrameResponse     byte = 4  // host → device: ResponseHead JSON
	FrameResponseBody byte = 5  // host → device: body bytes
	FrameResponseEnd  byte = 6  // host → device: response complete
	FrameCancel       byte = 7  // either: abandon this stream (payload: optional reason)
	FrameWSOpen       byte = 8  // device → host: WSOpen JSON
	FrameWSOpened     byte = 9  // host → device: WSOpened JSON
	FrameWSText       byte = 10 // either: one text message
	FrameWSBinary     byte = 11 // either: one binary message
	FrameWSClose      byte = 12 // either: WSClose JSON
)

// MaxChunk is the largest body or WebSocket payload one frame carries;
// larger bodies are split. Kept well under Noise's 64 KiB so padding and the
// header always fit.
const MaxChunk = 16 << 10

// maxFramePayload bounds a frame's payload: a JSON head or one chunk.
const maxFramePayload = 60000

// padBlock is the size granularity every encrypted frame is rounded up to,
// so the relay sees sizes in 256-byte steps rather than exact lengths.
const padBlock = 256

// frameHeader is type(1) + stream(4) + payload length(4).
const frameHeader = 9

// Frame is one decoded tunnel frame.
type Frame struct {
	Type    byte
	Stream  uint32
	Payload []byte
}

// Header is an ordered list of name/value pairs: order and repeated names
// survive, which a map would lose.
type Header [][2]string

// RequestHead starts an HTTP exchange. URL is a path plus query on the host's
// own origin; Host is the page's origin host, so absolute URLs Lectern builds
// from r.Host point back at the page (whose shims then route them here).
type RequestHead struct {
	Method string `json:"m"`
	URL    string `json:"u"`
	Host   string `json:"host,omitempty"`
	Header Header `json:"h,omitempty"`
}

// ResponseHead answers a RequestHead.
type ResponseHead struct {
	Status int    `json:"s"`
	Header Header `json:"h,omitempty"`
}

// WSOpen asks the host to open a WebSocket to one of its own paths.
type WSOpen struct {
	URL       string   `json:"u"`
	Host      string   `json:"host,omitempty"`
	Protocols []string `json:"p,omitempty"`
}

// WSOpened confirms a WSOpen.
type WSOpened struct {
	Protocol string `json:"p,omitempty"`
}

// WSClose ends a tunnelled WebSocket.
type WSClose struct {
	Code   int    `json:"c"`
	Reason string `json:"r,omitempty"`
}

// EncodeFrame lays out a frame and pads it to a multiple of padBlock.
func EncodeFrame(f Frame) ([]byte, error) {
	if len(f.Payload) > maxFramePayload {
		return nil, fmt.Errorf("relay: frame payload too large (%d bytes)", len(f.Payload))
	}
	n := frameHeader + len(f.Payload)
	padded := (n + padBlock - 1) / padBlock * padBlock
	out := make([]byte, padded)
	out[0] = f.Type
	binary.BigEndian.PutUint32(out[1:5], f.Stream)
	binary.BigEndian.PutUint32(out[5:9], uint32(len(f.Payload)))
	copy(out[frameHeader:], f.Payload)
	return out, nil
}

var errBadFrame = errors.New("relay: malformed tunnel frame")

// DecodeFrame parses a decrypted frame. Padding must be zero: it is covered
// by the AEAD anyway, but checking it keeps the format strict.
func DecodeFrame(b []byte) (Frame, error) {
	if len(b) < frameHeader || len(b)%padBlock != 0 {
		return Frame{}, errBadFrame
	}
	n := binary.BigEndian.Uint32(b[5:9])
	if n > maxFramePayload || int(n) > len(b)-frameHeader {
		return Frame{}, errBadFrame
	}
	for _, c := range b[frameHeader+int(n):] {
		if c != 0 {
			return Frame{}, errBadFrame
		}
	}
	if b[0] < FrameRequest || b[0] > FrameWSClose {
		return Frame{}, errBadFrame
	}
	return Frame{Type: b[0], Stream: binary.BigEndian.Uint32(b[1:5]), Payload: b[frameHeader : frameHeader+int(n)]}, nil
}

// FromDevice reports whether a frame type is one a device may send.
func FromDevice(t byte) bool {
	switch t {
	case FrameRequest, FrameRequestBody, FrameRequestEnd, FrameCancel,
		FrameWSOpen, FrameWSText, FrameWSBinary, FrameWSClose:
		return true
	}
	return false
}
