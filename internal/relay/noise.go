package relay

import (
	"errors"
	"fmt"
	"sync"

	"github.com/flynn/noise"
)

// Suite is Noise_IK_25519_ChaChaPoly_SHA256. frontend/src/relay/noise.ts
// implements the same suite and is tested against this package.
var Suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// MaxNoiseMessage is the Noise specification's own message limit.
const MaxNoiseMessage = 65535

// Prologue binds a handshake to this protocol and to one channel, so a
// recorded handshake cannot be replayed onto a different host's channel.
func Prologue(channel string) []byte {
	return []byte("lectern-relay-v1\x00" + channel)
}

// Hello is the first handshake message's encrypted payload. IK's first
// payload has weaker forward secrecy than transport messages (it depends on
// the host's static key), so it carries only a version and, once, a pairing
// request.
type Hello struct {
	V    int          `json:"v"`
	Pair *PairRequest `json:"pair,omitempty"`
}

// PairRequest redeems a one-time pairing code for this device's static key.
type PairRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Welcome is the second handshake message's payload.
type Welcome struct {
	OK bool `json:"ok"`
	// Error is a short machine-readable reason when OK is false:
	// "unknown_device", "invalid_code", "version".
	Error      string `json:"error,omitempty"`
	DeviceID   int64  `json:"device_id,omitempty"`
	Name       string `json:"name,omitempty"`
	RouteToken string `json:"route_token,omitempty"`
}

// Session is an established Noise transport: one cipher state per direction,
// each with its own implicit nonce counter. Any decryption failure (tamper,
// replay, reorder, loss) poisons the session: the caller must drop the
// connection, never retry.
type Session struct {
	sendMu sync.Mutex
	send   *noise.CipherState
	recvMu sync.Mutex
	recv   *noise.CipherState
	failed bool
}

// Seal encrypts one transport message.
func (s *Session) Seal(plaintext []byte) ([]byte, error) {
	if len(plaintext)+16 > MaxNoiseMessage {
		return nil, fmt.Errorf("relay: message too large (%d bytes)", len(plaintext))
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.send.Encrypt(nil, nil, plaintext)
}

// ErrDecrypt is returned for any message that fails authentication. It is
// deliberately one error: tampered, replayed and out-of-order messages are
// indistinguishable to the receiver and all end the session.
var ErrDecrypt = errors.New("relay: message failed authentication")

// Open decrypts the next transport message in order.
func (s *Session) Open(msg []byte) ([]byte, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	if s.failed {
		return nil, ErrClosed
	}
	out, err := s.recv.Decrypt(nil, nil, msg)
	if err != nil {
		s.failed = true
		return nil, ErrDecrypt
	}
	return out, nil
}

// Initiator is the phone's side of the handshake. Lectern itself never
// initiates; Go uses this in tests and for a future command-line client.
type Initiator struct {
	hs *noise.HandshakeState
}

// NewInitiator starts a handshake to the host whose static key hostPub was
// pinned at pairing.
func NewInitiator(static noise.DHKey, hostPub []byte, channel string) (*Initiator, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: Suite, Pattern: noise.HandshakeIK, Initiator: true,
		Prologue: Prologue(channel), StaticKeypair: static, PeerStatic: hostPub,
	})
	if err != nil {
		return nil, err
	}
	return &Initiator{hs: hs}, nil
}

// Hello writes handshake message 1 carrying payload.
func (i *Initiator) Hello(payload []byte) ([]byte, error) {
	msg, _, _, err := i.hs.WriteMessage(nil, payload)
	return msg, err
}

// Finish reads handshake message 2 and returns the host's payload and the
// transport session.
func (i *Initiator) Finish(msg []byte) ([]byte, *Session, error) {
	payload, toHost, toDevice, err := i.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, nil, ErrDecrypt
	}
	if toHost == nil || toDevice == nil {
		return nil, nil, errors.New("relay: handshake did not complete")
	}
	return payload, &Session{send: toHost, recv: toDevice}, nil
}

// Responder is the host's side of one handshake.
type Responder struct {
	hs      *noise.HandshakeState
	payload []byte
}

// Accept reads handshake message 1. On success the caller inspects
// PeerStatic and the payload, decides whether to admit the device, and
// answers with Reply either way.
func Accept(static noise.DHKey, channel string, msg []byte) (*Responder, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: Suite, Pattern: noise.HandshakeIK, Initiator: false,
		Prologue: Prologue(channel), StaticKeypair: static,
	})
	if err != nil {
		return nil, err
	}
	payload, _, _, err := hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, ErrDecrypt
	}
	return &Responder{hs: hs, payload: payload}, nil
}

// Payload is the device's decrypted Hello bytes.
func (r *Responder) Payload() []byte { return r.payload }

// PeerStatic is the device's static public key. Message 1 already binds it
// through the ss DH; the device has fully proven it holds the private half
// once its first transport message decrypts (which needs the se DH), and the
// host acts on nothing but transport messages after the handshake.
func (r *Responder) PeerStatic() []byte { return append([]byte(nil), r.hs.PeerStatic()...) }

// Reply writes handshake message 2 and returns the transport session.
func (r *Responder) Reply(payload []byte) ([]byte, *Session, error) {
	msg, toHost, toDevice, err := r.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, err
	}
	return msg, &Session{send: toDevice, recv: toHost}, nil
}
