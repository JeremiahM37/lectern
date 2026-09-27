// Package relay holds the pieces of Lectern's end-to-end encrypted relay that
// both ends share: key material, the Noise handshake between a phone and the
// host, the framing of HTTP and WebSocket traffic inside that encrypted
// channel, and the relay's own routing frames. The relay server lives in
// internal/relay/server and the host side in internal/relay/host.
//
// The design and threat model are in docs/relay.md. In short: the relay only
// ever sees Noise ciphertext plus the routing header it needs to deliver it;
// the phone pins the host's static key from a QR code shown on the host's own
// screen; the host admits a phone only if its static key is paired.
package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/flynn/noise"
)

// ProtocolVersion is carried in the QR payload and the first handshake
// payload; a peer that sees another version refuses rather than guessing.
const ProtocolVersion = 1

// channelLabel domain-separates the channel id hash from every other use of
// the routing key.
const channelLabel = "lectern-relay-channel-v1\x00"

// ChannelID derives a host's public routing id from its Ed25519 routing key.
// Deriving it (rather than letting a host pick one) is what lets the relay
// keep no state: a host proves it owns a channel by signing with the key the
// id is the hash of, so nobody can squat or take over someone else's channel
// even after a relay restart. 128 bits, base64url.
func ChannelID(routePub ed25519.PublicKey) string {
	sum := sha256.Sum256(append([]byte(channelLabel), routePub...))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// ValidChannelID reports whether s has the shape ChannelID produces, so the
// relay can reject junk before it looks anything up.
func ValidChannelID(s string) bool {
	if len(s) != 22 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 16
}

// Fingerprint is a short, human-comparable form of any public key: the
// first 8 bytes of its SHA-256 as grouped hex. Shown next to the host key
// and every paired device key in Settings.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	return strings.Join([]string{h[0:4], h[4:8], h[8:12], h[12:16]}, " ")
}

// Identity is the host's long-lived relay key material.
type Identity struct {
	// Route is the Ed25519 key the host proves channel ownership with.
	Route ed25519.PrivateKey
	// Noise is the host's static X25519 key pair, pinned by every phone.
	Noise noise.DHKey
	// Shell signs the app shell manifest the phone's service worker checks.
	Shell ed25519.PrivateKey
}

// GenerateIdentity creates a fresh set of host keys.
func GenerateIdentity() (*Identity, error) {
	_, route, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	_, shell, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	nk, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{Route: route, Noise: nk, Shell: shell}, nil
}

// Channel is this identity's relay channel id.
func (id *Identity) Channel() string {
	return ChannelID(id.Route.Public().(ed25519.PublicKey))
}

// ShellPublic is the public half of the shell signing key.
func (id *Identity) ShellPublic() ed25519.PublicKey { return id.Shell.Public().(ed25519.PublicKey) }

// RandomToken returns 32 random bytes as base64url: route tokens and pairing
// codes both use it.
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenHash is what the relay stores and compares for a route token: never
// the token itself.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte("lectern-relay-route-v1\x00" + token))
	return hex.EncodeToString(sum[:])
}

// ValidTokenHash reports whether s looks like a TokenHash result.
func ValidTokenHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// B64 and UnB64 are the one encoding every key crosses the wire in.
func B64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func UnB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// ErrClosed reports use of a connection that has already failed or closed.
var ErrClosed = errors.New("relay: connection closed")
