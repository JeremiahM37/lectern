// Package push is the web-push (VAPID) notification sink. It degrades to a no-op
// until keys are configured, so a fresh install never fails on it.
//
// The payload is encrypted with RFC 8291 aes128gcm and authorised with an RFC
// 8292 VAPID JWT. Implemented directly on crypto/* rather than pulling a
// dependency: it is ~120 lines and the alternative is another module to audit.
package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Subscription is a browser's push endpoint plus its encryption keys.
type Subscription struct {
	Endpoint string            `json:"endpoint"`
	Keys     map[string]string `json:"keys"`
}

// Sender holds the VAPID identity.
type Sender struct {
	PrivateKey string // base64url raw P-256 scalar
	PublicKey  string // base64url uncompressed P-256 point
	Email      string
	Client     *http.Client
	Log        *slog.Logger
}

// Enabled reports whether VAPID keys are configured.
func (s *Sender) Enabled() bool { return s != nil && s.PrivateKey != "" && s.PublicKey != "" }

// GenerateKeyPair mints a fresh P-256 VAPID key pair, in the same base64url
// raw-scalar (private) / uncompressed-point (public) encoding every other key
// in this package uses. Used to auto-provision a Sender when no
// LECTERN_VAPID_PRIVATE/PUBLIC are set — see ResolveKeys.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	d := make([]byte, 32)
	key.D.FillBytes(d)
	return base64.RawURLEncoding.EncodeToString(d),
		base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y)), nil
}

// Send delivers one notification. It returns whether the subscription is gone
// (404/410), so the caller can prune it.
func (s *Sender) Send(sub Subscription, payload []byte) (gone bool, err error) {
	if !s.Enabled() {
		return false, nil
	}
	body, err := encrypt(sub, payload)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest("POST", sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	jwt, err := s.vapidJWT(sub.Endpoint)
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.PublicKey)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		return true, nil
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("push endpoint returned %d", resp.StatusCode)
	}
	return false, nil
}

func (s *Sender) vapidJWT(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	header := b64(`{"typ":"JWT","alg":"ES256"}`)
	claims, _ := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": time.Now().Add(12 * time.Hour).Unix(),
		"sub": "mailto:" + s.Email,
	})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s.PrivateKey, "="))
	if err != nil {
		return "", fmt.Errorf("VAPID private key is not base64url: %w", err)
	}
	priv := new(ecdsa.PrivateKey)
	priv.Curve = elliptic.P256()
	priv.D = new(big.Int).SetBytes(raw)
	priv.PublicKey.X, priv.PublicKey.Y = priv.Curve.ScalarBaseMult(raw)

	sum := sha256.Sum256([]byte(signing))
	der, err := ecdsa.SignASN1(rand.Reader, priv, sum[:])
	if err != nil {
		return "", err
	}
	var parsed struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &parsed); err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	parsed.R.FillBytes(sig[:32])
	parsed.S.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// encrypt implements RFC 8291 "Message Encryption for Web Push".
func encrypt(sub Subscription, payload []byte) ([]byte, error) {
	clientPubRaw, err := b64decode(sub.Keys["p256dh"])
	if err != nil {
		return nil, fmt.Errorf("bad p256dh: %w", err)
	}
	auth, err := b64decode(sub.Keys["auth"])
	if err != nil {
		return nil, fmt.Errorf("bad auth secret: %w", err)
	}
	curve := ecdh.P256()
	clientPub, err := curve.NewPublicKey(clientPubRaw)
	if err != nil {
		return nil, err
	}
	localPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	localPub := localPriv.PublicKey().Bytes()
	shared, err := localPriv.ECDH(clientPub)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	info := append([]byte("WebPush: info\x00"), clientPubRaw...)
	info = append(info, localPub...)
	prk := hkdfBytes(auth, shared, info, 32)
	cek := hkdfBytes(salt, prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfBytes(salt, prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// aes128gcm requires a padding delimiter byte before the ciphertext ends
	ciphertext := gcm.Seal(nil, nonce, append(payload, 0x02), nil)

	var buf bytes.Buffer
	buf.Write(salt)
	binary.Write(&buf, binary.BigEndian, uint32(4096))
	buf.WriteByte(byte(len(localPub)))
	buf.Write(localPub)
	buf.Write(ciphertext)
	return buf.Bytes(), nil
}

func hkdfBytes(salt, secret, info []byte, n int) []byte {
	out := make([]byte, n)
	io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out)
	return out
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func b64decode(s string) ([]byte, error) {
	s = strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "=")
	return base64.RawURLEncoding.DecodeString(s)
}
