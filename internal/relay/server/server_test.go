package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/relay"
)

const secret = "0123456789abcdef0123456789abcdef-test"

func startRelay(t *testing.T, mod func(*Config)) (*Server, string) {
	t.Helper()
	cfg := Config{HostSecret: secret}
	if mod != nil {
		mod(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, "ws" + strings.TrimPrefix(ts.URL, "http")
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	c.SetReadLimit(relay.MaxWireMessage)
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

var ackID int64

// sendAcked sends a host control message and waits for the relay to confirm
// it applied it, rather than guessing how long that takes.
func sendAcked(t *testing.T, c *websocket.Conn, msg relay.Control) {
	t.Helper()
	ackID++
	msg.ID = ackID
	send(t, c, msg)
	if got := recvControl(t, c); got.T != "ack" || got.ID != msg.ID {
		t.Fatalf("expected ack %d, got %+v", msg.ID, got)
	}
}

func recv(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Read(ctx)
}

func recvControl(t *testing.T, c *websocket.Conn) relay.Control {
	t.Helper()
	typ, b, err := recv(t, c)
	if err != nil || typ != websocket.MessageText {
		t.Fatalf("expected a control message, got %v %q %v", typ, b, err)
	}
	var msg relay.Control
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

// closeCode reads until the connection closes and returns the close code.
func closeCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	for {
		if _, _, err := recv(t, c); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

type testHost struct {
	c       *websocket.Conn
	key     ed25519.PrivateKey
	channel string
}

func connectHost(t *testing.T, base string, key ed25519.PrivateKey, hostSecret string) *testHost {
	t.Helper()
	if key == nil {
		_, key, _ = ed25519.GenerateKey(rand.Reader)
	}
	c := dial(t, base+"/v1/host")
	ch := recvControl(t, c)
	nonce, _ := relay.UnB64(ch.Nonce)
	send(t, c, relay.HostProof(key, hostSecret, nonce))
	return &testHost{c: c, key: key, channel: relay.ChannelID(key.Public().(ed25519.PublicKey))}
}

func readyHost(t *testing.T, base string, tokens ...string) *testHost {
	t.Helper()
	h := connectHost(t, base, nil, secret)
	if msg := recvControl(t, h.c); msg.T != "ready" {
		t.Fatalf("host not ready: %+v", msg)
	}
	set := []string{}
	for _, tok := range tokens {
		set = append(set, relay.TokenHash(tok))
	}
	sendAcked(t, h.c, relay.Control{T: "routes", Set: set})
	return h
}

func connectDevice(t *testing.T, base, channel, token string) *websocket.Conn {
	t.Helper()
	c := dial(t, base+"/v1/device?ch="+channel)
	send(t, c, relay.Control{T: "auth", Token: token})
	return c
}

func TestOnlyRelayEndpointsExist(t *testing.T) {
	_, base := startRelay(t, nil)
	httpBase := "http" + strings.TrimPrefix(base, "ws")
	for _, p := range []string{"/", "/index.html", "/sw.js", "/relay-pair", "/api/tasks", "/v1/host/x"} {
		resp, err := http.Get(httpBase + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 404 || len(body) != 0 {
			t.Fatalf("GET %s = %d %q, want a bodyless 404", p, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "html") || strings.Contains(ct, "javascript") {
			t.Fatalf("GET %s served %s", p, ct)
		}
	}
	resp, err := http.Post(httpBase+"/v1/host", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("POST /v1/host = %d", resp.StatusCode)
	}
	resp, _ = http.Get(httpBase + "/healthz")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok\n" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}
}

func TestNewRequiresHostSecret(t *testing.T) {
	if _, err := New(Config{HostSecret: "short"}); err == nil {
		t.Fatal("accepted a short host secret")
	}
}

func TestHostWithWrongSecretIsRefused(t *testing.T) {
	_, base := startRelay(t, nil)
	h := connectHost(t, base, nil, "wrong-secret-wrong-secret-wrong-secret")
	if code := closeCode(t, h.c); code != CodeUnauthorized {
		t.Fatalf("close code = %d", code)
	}
}

func TestHostCannotClaimAnotherChannel(t *testing.T) {
	_, base := startRelay(t, nil)
	_, victim, _ := ed25519.GenerateKey(rand.Reader)
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	c := dial(t, base+"/v1/host")
	ch := recvControl(t, c)
	nonce, _ := relay.UnB64(ch.Nonce)
	proof := relay.HostProof(attacker, secret, nonce)
	proof.Channel = relay.ChannelID(victim.Public().(ed25519.PublicKey))
	send(t, c, proof)
	if code := closeCode(t, c); code != CodeUnauthorized {
		t.Fatalf("close code = %d", code)
	}
}

func TestDeviceNeedsARegisteredRoute(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base, "good-token")
	d := connectDevice(t, base, h.channel, "guessed-token")
	if code := closeCode(t, d); code != CodeUnauthorized {
		t.Fatalf("close code = %d", code)
	}
	// Nothing about the refused device reached the host.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, b, err := h.c.Read(ctx); err == nil {
		t.Fatalf("host received %q for a refused device", b)
	}
}

func TestDeviceMustAuthenticateInTime(t *testing.T) {
	_, base := startRelay(t, func(c *Config) { c.AuthTimeout = 200 * time.Millisecond })
	h := readyHost(t, base, "tok")
	d := dial(t, base+"/v1/device?ch="+h.channel)
	if code := closeCode(t, d); code != CodeAuthTimeout {
		t.Fatalf("close code = %d", code)
	}
}

func TestBadChannelAndOfflineHost(t *testing.T) {
	_, base := startRelay(t, nil)
	d := connectDevice(t, base, "not-a-channel", "tok")
	if code := closeCode(t, d); code != CodeBadRequest {
		t.Fatalf("bad channel close code = %d", code)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	d = connectDevice(t, base, relay.ChannelID(key.Public().(ed25519.PublicKey)), "tok")
	if code := closeCode(t, d); code != CodeHostOffline {
		t.Fatalf("offline host close code = %d", code)
	}
}

func TestFramesAreRoutedBothWays(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	_, base := startRelay(t, func(c *Config) {
		c.OnFrame = func(dir, _ string, _ uint32, p []byte) {
			mu.Lock()
			seen = append(seen, dir+":"+string(p))
			mu.Unlock()
		}
	})
	h := readyHost(t, base, "tok")
	d := connectDevice(t, base, h.channel, "tok")
	if msg := recvControl(t, d); msg.T != "ok" {
		t.Fatalf("device not admitted: %+v", msg)
	}
	_, b, _ := recv(t, h.c)
	kind, conn, payload, err := relay.DecodeWire(b)
	if err != nil || kind != relay.WireOpen || string(payload) != relay.TokenHash("tok") {
		t.Fatalf("open = %d %q %v", kind, payload, err)
	}
	if err := d.Write(context.Background(), websocket.MessageBinary, []byte("cipher-up")); err != nil {
		t.Fatal(err)
	}
	_, b, _ = recv(t, h.c)
	kind, c2, payload, _ := relay.DecodeWire(b)
	if kind != relay.WireData || c2 != conn || string(payload) != "cipher-up" {
		t.Fatalf("data = %d %d %q", kind, c2, payload)
	}
	if err := h.c.Write(context.Background(), websocket.MessageBinary, relay.EncodeWire(relay.WireData, conn, []byte("cipher-down"))); err != nil {
		t.Fatal(err)
	}
	if _, b, err := recv(t, d); err != nil || string(b) != "cipher-down" {
		t.Fatalf("device got %q %v", b, err)
	}
	mu.Lock()
	got := strings.Join(seen, ",")
	mu.Unlock()
	if got != "to_host:cipher-up,to_device:cipher-down" {
		t.Fatalf("frames seen = %s", got)
	}
	// The host closes the device.
	_ = h.c.Write(context.Background(), websocket.MessageBinary, relay.EncodeWire(relay.WireClose, conn, nil))
	if code := closeCode(t, d); code != websocket.StatusNormalClosure {
		t.Fatalf("close code = %d", code)
	}
}

func TestOneTimeRouteWorksOnce(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base)
	sendAcked(t, h.c, relay.Control{T: "route_add", Hash: relay.TokenHash("pair"), TTL: 60, Once: true})
	d := connectDevice(t, base, h.channel, "pair")
	if msg := recvControl(t, d); msg.T != "ok" {
		t.Fatalf("first use refused: %+v", msg)
	}
	d2 := connectDevice(t, base, h.channel, "pair")
	if code := closeCode(t, d2); code != CodeUnauthorized {
		t.Fatalf("second use close code = %d", code)
	}
}

func TestRouteDeletionLocksDeviceOut(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base, "tok")
	sendAcked(t, h.c, relay.Control{T: "route_del", Hash: relay.TokenHash("tok")})
	d := connectDevice(t, base, h.channel, "tok")
	if code := closeCode(t, d); code != CodeUnauthorized {
		t.Fatalf("close code = %d", code)
	}
}

func TestOversizedFrameClosesDevice(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base, "tok")
	for _, tc := range []struct {
		size int
		code websocket.StatusCode
	}{{relay.MaxNoiseMessage + 1, CodeBadRequest}, {relay.MaxWireMessage + 1024, websocket.StatusMessageTooBig}} {
		d := connectDevice(t, base, h.channel, "tok")
		recvControl(t, d)
		_ = d.Write(context.Background(), websocket.MessageBinary, make([]byte, tc.size))
		if code := closeCode(t, d); code != tc.code {
			t.Fatalf("%d-byte frame: close code = %d, want %d", tc.size, code, tc.code)
		}
	}
}

func TestReconnectingHostReplacesOld(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base, "tok")
	d := connectDevice(t, base, h.channel, "tok")
	recvControl(t, d)
	h2 := connectHost(t, base, h.key, secret)
	if msg := recvControl(t, h2.c); msg.T != "ready" {
		t.Fatalf("second host: %+v", msg)
	}
	if code := closeCode(t, h.c); code != CodeReplaced {
		t.Fatalf("old host close code = %d", code)
	}
	if code := closeCode(t, d); code != CodeHostOffline {
		t.Fatalf("device close code = %d", code)
	}
}

func TestHostDisconnectDropsDevices(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base, "tok")
	d := connectDevice(t, base, h.channel, "tok")
	recvControl(t, d)
	h.c.Close(websocket.StatusNormalClosure, "")
	if code := closeCode(t, d); code != CodeHostOffline {
		t.Fatalf("device close code = %d", code)
	}
}

func TestPerIPConnectionLimit(t *testing.T) {
	_, base := startRelay(t, func(c *Config) { c.MaxConnsPerIP = 2 })
	dial(t, base+"/v1/device?ch=x")
	dial(t, base+"/v1/device?ch=x")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, base+"/v1/device?ch=x", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third connection: %v %v", resp, err)
	}
}

func TestConnectRateLimit(t *testing.T) {
	_, base := startRelay(t, func(c *Config) { c.ConnectsPerMinute = 3 })
	refused := 0
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, resp, err := websocket.Dial(ctx, base+"/v1/device?ch=x", nil)
		cancel()
		if err != nil && resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			refused++
		} else if c != nil {
			c.CloseNow()
		}
	}
	if refused != 2 {
		t.Fatalf("refused %d of 5 connections, want 2", refused)
	}
}

func TestDeviceRateIsThrottled(t *testing.T) {
	_, base := startRelay(t, func(c *Config) { c.DeviceRate = 20000; c.DeviceBurst = 20000 })
	h := readyHost(t, base, "tok")
	d := connectDevice(t, base, h.channel, "tok")
	recvControl(t, d)
	recv(t, h.c) // open
	start := time.Now()
	for i := 0; i < 4; i++ {
		_ = d.Write(context.Background(), websocket.MessageBinary, make([]byte, 10000))
	}
	for i := 0; i < 4; i++ {
		if _, _, err := recv(t, h.c); err != nil {
			t.Fatal(err)
		}
	}
	// 40 KB at 20 KB/s with a 20 KB burst takes about a second.
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Fatalf("40 KB forwarded in %v; rate limit not applied", elapsed)
	}
}

func TestTooManyDevicesPerChannel(t *testing.T) {
	_, base := startRelay(t, func(c *Config) { c.MaxDevicesPerChannel = 1 })
	h := readyHost(t, base, "tok")
	d := connectDevice(t, base, h.channel, "tok")
	recvControl(t, d)
	d2 := connectDevice(t, base, h.channel, "tok")
	if code := closeCode(t, d2); code != CodeTooMany {
		t.Fatalf("close code = %d", code)
	}
}

// A host that answers and closes at once (a refused handshake) must still
// have its answer delivered before the close.
func TestHostCloseDeliversQueuedFrames(t *testing.T) {
	_, base := startRelay(t, nil)
	h := readyHost(t, base, "tok")
	for i := 0; i < 20; i++ {
		d := connectDevice(t, base, h.channel, "tok")
		recvControl(t, d)
		_, b, _ := recv(t, h.c)
		_, conn, _, _ := relay.DecodeWire(b)
		_ = h.c.Write(context.Background(), websocket.MessageBinary, relay.EncodeWire(relay.WireData, conn, []byte("last words")))
		_ = h.c.Write(context.Background(), websocket.MessageBinary, relay.EncodeWire(relay.WireClose, conn, nil))
		if _, got, err := recv(t, d); err != nil || string(got) != "last words" {
			t.Fatalf("attempt %d: got %q, %v", i, got, err)
		}
		if code := closeCode(t, d); code != websocket.StatusNormalClosure {
			t.Fatalf("close code = %d", code)
		}
		recv(t, h.c) // the relay's own close notice for this device, if any
	}
}

// A host that has connected but not yet sent its route set must not have its
// phones told "not authorized": a phone treats that as revocation. The relay
// says "host offline" (which a phone retries) until the routes arrive.
func TestDevicesWaitForTheHostsRouteSet(t *testing.T) {
	_, base := startRelay(t, nil)
	h := connectHost(t, base, nil, secret)
	if msg := recvControl(t, h.c); msg.T != "ready" {
		t.Fatalf("host not ready: %+v", msg)
	}
	d := connectDevice(t, base, h.channel, "tok")
	if code := closeCode(t, d); code != CodeHostOffline {
		t.Fatalf("before the route set: close code = %d, want %d", code, CodeHostOffline)
	}
	sendAcked(t, h.c, relay.Control{T: "routes", Set: []string{relay.TokenHash("tok")}})
	d = connectDevice(t, base, h.channel, "tok")
	if msg := recvControl(t, d); msg.T != "ok" {
		t.Fatalf("after the route set: %+v", msg)
	}
}
