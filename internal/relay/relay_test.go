package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/flynn/noise"
)

func mustKey(t *testing.T) noise.DHKey {
	t.Helper()
	k, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// handshake runs a full IK exchange and returns both transport sessions.
func handshake(t *testing.T, device, host noise.DHKey, pinned []byte, channel string) (*Session, *Session, *Responder) {
	t.Helper()
	ini, err := NewInitiator(device, pinned, channel)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := ini.Hello([]byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Accept(host, channel, m1)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	m2, hostSess, err := resp.Reply([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	payload, devSess, err := ini.Finish(m2)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if string(payload) != `{"ok":true}` {
		t.Fatalf("welcome payload = %q", payload)
	}
	return devSess, hostSess, resp
}

func TestHandshakeAndTransport(t *testing.T) {
	device, host := mustKey(t), mustKey(t)
	dev, hs, resp := handshake(t, device, host, host.Public, "chan")
	if !bytes.Equal(resp.PeerStatic(), device.Public) {
		t.Fatal("host did not learn the device's static key")
	}
	if string(resp.Payload()) != `{"v":1}` {
		t.Fatalf("hello payload = %q", resp.Payload())
	}
	for i := 0; i < 3; i++ {
		ct, err := dev.Seal([]byte("ping"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(ct, []byte("ping")) {
			t.Fatal("ciphertext contains plaintext")
		}
		pt, err := hs.Open(ct)
		if err != nil || string(pt) != "ping" {
			t.Fatalf("open = %q, %v", pt, err)
		}
		ct, _ = hs.Seal([]byte("pong"))
		if pt, err := dev.Open(ct); err != nil || string(pt) != "pong" {
			t.Fatalf("open = %q, %v", pt, err)
		}
	}
}

func TestReplayIsRejected(t *testing.T) {
	device, host := mustKey(t), mustKey(t)
	dev, hs, _ := handshake(t, device, host, host.Public, "chan")
	first, _ := dev.Seal([]byte("approve 7"))
	if _, err := hs.Open(first); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Open(first); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("replayed message: err = %v, want ErrDecrypt", err)
	}
	// The session is poisoned after a failure: even a genuine next message
	// is refused, so a caller cannot "skip over" an attack and continue.
	next, _ := dev.Seal([]byte("next"))
	if _, err := hs.Open(next); !errors.Is(err, ErrClosed) {
		t.Fatalf("after failure: err = %v, want ErrClosed", err)
	}
}

func TestReorderIsRejected(t *testing.T) {
	device, host := mustKey(t), mustKey(t)
	dev, hs, _ := handshake(t, device, host, host.Public, "chan")
	a, _ := dev.Seal([]byte("a"))
	b, _ := dev.Seal([]byte("b"))
	_ = a
	if _, err := hs.Open(b); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("out-of-order message: err = %v", err)
	}
}

func TestReplayAcrossSessionsIsRejected(t *testing.T) {
	device, host := mustKey(t), mustKey(t)
	dev1, _, _ := handshake(t, device, host, host.Public, "chan")
	_, hs2, _ := handshake(t, device, host, host.Public, "chan")
	old, _ := dev1.Seal([]byte("approve 7"))
	if _, err := hs2.Open(old); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("message from another session: err = %v", err)
	}
}

func TestTamperingIsRejected(t *testing.T) {
	device, host := mustKey(t), mustKey(t)
	for i := 0; i < 64; i += 7 {
		dev, hs, _ := handshake(t, device, host, host.Public, "chan")
		ct, _ := dev.Seal(bytes.Repeat([]byte("x"), 48))
		ct[i%len(ct)] ^= 0x01
		if _, err := hs.Open(ct); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("flipped byte %d: err = %v", i%len(ct), err)
		}
	}
	// Tampering with the handshake itself fails too.
	ini, _ := NewInitiator(device, host.Public, "chan")
	m1, _ := ini.Hello([]byte(`{"v":1}`))
	m1[len(m1)-1] ^= 0x80
	if _, err := Accept(host, "chan", m1); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered message 1: err = %v", err)
	}
}

func TestWrongHostKeyFails(t *testing.T) {
	device, host, impostor := mustKey(t), mustKey(t), mustKey(t)
	// The phone pinned the real host key; an impostor (say, the relay) that
	// answers with its own key cannot read message 1 at all.
	ini, _ := NewInitiator(device, host.Public, "chan")
	m1, _ := ini.Hello([]byte(`{"v":1,"pair":{"code":"secret"}}`))
	if _, err := Accept(impostor, "chan", m1); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("impostor accepted message 1: %v", err)
	}
	// And a phone that pinned the wrong key cannot complete with the real host.
	ini2, _ := NewInitiator(device, impostor.Public, "chan")
	m1b, _ := ini2.Hello([]byte(`{"v":1}`))
	if _, err := Accept(host, "chan", m1b); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("host accepted a message encrypted to another key: %v", err)
	}
}

func TestWrongChannelFails(t *testing.T) {
	device, host := mustKey(t), mustKey(t)
	ini, _ := NewInitiator(device, host.Public, "chan-a")
	m1, _ := ini.Hello([]byte(`{"v":1}`))
	if _, err := Accept(host, "chan-b", m1); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("handshake replayed onto another channel: %v", err)
	}
}

func TestResponderImpostorCannotFinish(t *testing.T) {
	// A man in the middle that relays message 1 to the real host but swaps
	// message 2 for its own cannot make the phone accept it.
	device, host, mitm := mustKey(t), mustKey(t), mustKey(t)
	ini, _ := NewInitiator(device, host.Public, "chan")
	m1, _ := ini.Hello([]byte(`{"v":1}`))
	fake, err := Accept(mitm, "chan", m1)
	if err == nil {
		m2, _, _ := fake.Reply([]byte(`{"ok":true}`))
		if _, _, err := ini.Finish(m2); err == nil {
			t.Fatal("phone accepted a reply from a key it did not pin")
		}
	}
}

func TestFrameRoundTripAndPadding(t *testing.T) {
	for _, n := range []int{0, 1, 246, 247, 248, 1000, MaxChunk} {
		payload := bytes.Repeat([]byte{0xAB}, n)
		enc, err := EncodeFrame(Frame{Type: FrameResponseBody, Stream: 42, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		if len(enc)%padBlock != 0 {
			t.Fatalf("len %d not padded", len(enc))
		}
		f, err := DecodeFrame(enc)
		if err != nil || f.Type != FrameResponseBody || f.Stream != 42 || !bytes.Equal(f.Payload, payload) {
			t.Fatalf("round trip n=%d: %+v %v", n, f, err)
		}
	}
	enc, _ := EncodeFrame(Frame{Type: FrameRequest, Stream: 1, Payload: []byte("x")})
	enc[len(enc)-1] = 1
	if _, err := DecodeFrame(enc); err == nil {
		t.Fatal("nonzero padding accepted")
	}
	enc, _ = EncodeFrame(Frame{Type: FrameRequest, Stream: 1, Payload: []byte("x")})
	enc[0] = 99
	if _, err := DecodeFrame(enc); err == nil {
		t.Fatal("unknown frame type accepted")
	}
	if _, err := DecodeFrame(enc[:10]); err == nil {
		t.Fatal("short frame accepted")
	}
	if _, err := EncodeFrame(Frame{Payload: make([]byte, maxFramePayload+1)}); err == nil {
		t.Fatal("oversized payload accepted")
	}
	// The biggest frame still fits in one Noise message.
	big, _ := EncodeFrame(Frame{Type: FrameRequest, Payload: make([]byte, maxFramePayload)})
	if len(big)+16 > MaxNoiseMessage {
		t.Fatalf("max frame %d does not fit a Noise message", len(big))
	}
}

func TestHostProof(t *testing.T) {
	_, route, _ := ed25519.GenerateKey(rand.Reader)
	nonce := []byte("0123456789abcdef0123456789abcdef")
	c := HostProof(route, "s3cret", nonce)
	if c.Channel != ChannelID(route.Public().(ed25519.PublicKey)) || !ValidChannelID(c.Channel) {
		t.Fatalf("channel = %q", c.Channel)
	}
	if !VerifyHostProof(c, "s3cret", nonce) {
		t.Fatal("valid proof rejected")
	}
	if VerifyHostProof(c, "wrong", nonce) {
		t.Fatal("proof accepted with the wrong host secret")
	}
	if VerifyHostProof(c, "s3cret", []byte("another nonce another nonce 0000")) {
		t.Fatal("proof replayed against a new challenge")
	}
	// Claiming someone else's channel with your own key fails.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	squat := HostProof(other, "s3cret", nonce)
	squat.Channel = c.Channel
	if VerifyHostProof(squat, "s3cret", nonce) {
		t.Fatal("proof accepted for a channel the key does not own")
	}
}

func TestWireRoundTrip(t *testing.T) {
	b := EncodeWire(WireData, 7, []byte("ct"))
	k, c, p, err := DecodeWire(b)
	if err != nil || k != WireData || c != 7 || string(p) != "ct" {
		t.Fatalf("%d %d %q %v", k, c, p, err)
	}
	if _, _, _, err := DecodeWire([]byte{9, 0, 0, 0, 1}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if _, _, _, err := DecodeWire([]byte{1, 0}); err == nil {
		t.Fatal("short frame accepted")
	}
}

func TestTokenHashAndFingerprint(t *testing.T) {
	tok, _ := RandomToken()
	h := TokenHash(tok)
	if !ValidTokenHash(h) || h == TokenHash(tok+"x") {
		t.Fatal("token hash")
	}
	if len(Fingerprint([]byte("k"))) != 19 {
		t.Fatalf("fingerprint %q", Fingerprint([]byte("k")))
	}
}
