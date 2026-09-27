package relay

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Binary frames on the host's connection to the relay. A device's own
// connection carries bare Noise messages with no header at all: the relay
// adds and strips this routing header, and it is the only thing the relay
// ever parses.
const (
	WireData  byte = 1 // payload: one Noise message
	WireOpen  byte = 2 // relay → host: a device connected (payload: route token hash)
	WireClose byte = 3 // either: close this device connection
)

// wireHeader is kind(1) + connection id(4).
const wireHeader = 5

// MaxWireMessage caps one frame on either relay connection.
const MaxWireMessage = MaxNoiseMessage + wireHeader

// EncodeWire builds a host-connection frame.
func EncodeWire(kind byte, conn uint32, payload []byte) []byte {
	out := make([]byte, wireHeader+len(payload))
	out[0] = kind
	binary.BigEndian.PutUint32(out[1:5], conn)
	copy(out[wireHeader:], payload)
	return out
}

var errBadWire = errors.New("relay: malformed routing frame")

// DecodeWire splits a host-connection frame.
func DecodeWire(b []byte) (kind byte, conn uint32, payload []byte, err error) {
	if len(b) < wireHeader || b[0] < WireData || b[0] > WireClose {
		return 0, 0, nil, errBadWire
	}
	return b[0], binary.BigEndian.Uint32(b[1:5]), b[wireHeader:], nil
}

// Control is every JSON text message on the relay connections:
//
//	relay → host:   {"t":"challenge","nonce"}  then {"t":"ready"}
//	host → relay:   {"t":"hello","v","ch","pub","sig","mac"}
//	host → relay:   {"t":"routes","set":[hash…]}  (full replace, sent on every connect)
//	host → relay:   {"t":"route_add","h","ttl","once"} / {"t":"route_del","h"}
//	device → relay: {"t":"auth","token"}
//	relay → device: {"t":"ok"}
//	relay → either: {"t":"error","error"} just before closing
type Control struct {
	T       string   `json:"t"`
	V       int      `json:"v,omitempty"`
	Nonce   string   `json:"nonce,omitempty"`
	Channel string   `json:"ch,omitempty"`
	Pub     string   `json:"pub,omitempty"`
	Sig     string   `json:"sig,omitempty"`
	MAC     string   `json:"mac,omitempty"`
	Set     []string `json:"set,omitempty"`
	Hash    string   `json:"h,omitempty"`
	TTL     int      `json:"ttl,omitempty"`
	Once    bool     `json:"once,omitempty"`
	Token   string   `json:"token,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func hostSigMessage(channel string, nonce []byte) []byte {
	return append([]byte("lectern-relay-host-v1\x00"+channel+"\x00"), nonce...)
}

func hostMAC(secret, channel string, nonce []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("lectern-relay-admit-v1\x00" + channel + "\x00"))
	m.Write(nonce)
	return m.Sum(nil)
}

// HostProof answers a relay challenge: a signature proving ownership of the
// channel, and an HMAC proving knowledge of the relay's host secret without
// sending it.
func HostProof(route ed25519.PrivateKey, secret string, nonce []byte) Control {
	pub := route.Public().(ed25519.PublicKey)
	ch := ChannelID(pub)
	return Control{
		T: "hello", V: ProtocolVersion, Channel: ch, Pub: B64(pub),
		Sig: B64(ed25519.Sign(route, hostSigMessage(ch, nonce))),
		MAC: B64(hostMAC(secret, ch, nonce)),
	}
}

// VerifyHostProof is the relay's check of HostProof.
func VerifyHostProof(c Control, secret string, nonce []byte) bool {
	pub, err := UnB64(c.Pub)
	if err != nil || len(pub) != ed25519.PublicKeySize || ChannelID(pub) != c.Channel {
		return false
	}
	sig, err := UnB64(c.Sig)
	if err != nil || !ed25519.Verify(pub, hostSigMessage(c.Channel, nonce), sig) {
		return false
	}
	mac, err := UnB64(c.MAC)
	return err == nil && hmac.Equal(mac, hostMAC(secret, c.Channel, nonce))
}
