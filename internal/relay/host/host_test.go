package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/relay"
	"github.com/JeremiahM37/lectern/v2/internal/relay/client"
	relayserver "github.com/JeremiahM37/lectern/v2/internal/relay/server"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const hostSecret = "host-secret-host-secret-host-secret-0001"

// marker is plaintext that must never appear in anything the relay handles.
const marker = "TOP-SECRET-APPROVAL-MARKER-7f3a"

type fixture struct {
	t      *testing.T
	host   *Host
	store  *Store
	relay  string
	frames *frameLog
	owner  auth.Principal
}

type frameLog struct {
	mu     sync.Mutex
	frames [][]byte
}

func (l *frameLog) add(_, _ string, _ uint32, p []byte) {
	l.mu.Lock()
	l.frames = append(l.frames, append([]byte(nil), p...))
	l.mu.Unlock()
}

func (l *frameLog) all() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte(nil), l.frames...)
}

// testHandler stands in for Lectern's API: it authenticates exactly the way
// the real auth middleware does (mode none, the most permissive) and echoes
// who it thinks the caller is.
func testHandler() http.Handler {
	resolver := &auth.Resolver{Mode: auth.ModeNone}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		p, ok := resolver.Authenticate(r)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": p.Kind, "login": p.Login, "human": p.Human,
			"can_decide": resolver.CanDecide(p) && p.Kind == auth.KindRelayDevice, "remote": r.RemoteAddr, "host": r.Host})
	})
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := resolver.Authenticate(r); !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		w.Header().Set("X-Method", r.Method)
		_, _ = w.Write([]byte(marker + ":" + buf.String()))
	})
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: %s %d\n\n", marker, i)
			fl.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	mux.HandleFunc("/term/echo", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := resolver.Authenticate(r); !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"tty"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(1 << 20)
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, append([]byte("echo:"), b...)); err != nil {
				return
			}
		}
	})
	return mux
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	frames := &frameLog{}
	rs, err := relayserver.New(relayserver.Config{HostSecret: hostSecret, OnFrame: frames.add})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "lectern.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := NewStore(db)
	relayURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	h := New(Config{URL: relayURL, Secret: hostSecret, Store: st, Handler: testHandler()})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); h.Close() })
	f := &fixture{t: t, host: h, store: st, relay: relayURL, frames: frames,
		owner: auth.Principal{Kind: auth.KindTailscale, Login: "owner@example.com", Human: true}}
	deadline := time.Now().Add(5 * time.Second)
	for !h.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("host never connected: %+v", h.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f
}

func deviceKey(t *testing.T) noise.DHKey {
	t.Helper()
	k, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (f *fixture) options(key noise.DHKey, token string) client.Options {
	id, err := f.store.Identity()
	if err != nil {
		f.t.Fatal(err)
	}
	return client.Options{RelayURL: f.relay, Channel: id.Channel(), RouteToken: token, HostKey: id.Noise.Public, Device: key}
}

// pair runs the whole pairing flow and returns the device's own route token.
func (f *fixture) pair(key noise.DHKey, name string) (relay.Welcome, string) {
	f.t.Helper()
	p, err := f.host.MintPairing(f.owner)
	if err != nil {
		f.t.Fatal(err)
	}
	opts := f.options(key, p.RouteToken)
	opts.Pair = &relay.PairRequest{Code: p.Code, Name: name}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, opts)
	if err != nil {
		f.t.Fatalf("pairing: %v", err)
	}
	cl.Close()
	return cl.Welcome, cl.Welcome.RouteToken
}

func (f *fixture) connect(key noise.DHKey, token string) (*client.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// No retry: the route of a just-paired device is registered at the
	// relay before the pairing reply leaves the host.
	return client.Dial(ctx, f.options(key, token))
}

func TestPairAndActAsOwner(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	w, token := f.pair(key, "Test phone")
	if !w.OK || w.DeviceID == 0 || token == "" || w.Name != "Test phone" {
		t.Fatalf("welcome = %+v", w)
	}
	cl, err := f.connect(key, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	resp, err := cl.Do(context.Background(), "GET", "/api/whoami", nil)
	if err != nil {
		t.Fatal(err)
	}
	var who map[string]any
	_ = json.Unmarshal(resp.Body, &who)
	if resp.Status != 200 || who["kind"] != auth.KindRelayDevice || who["login"] != "owner@example.com" || who["human"] != true {
		t.Fatalf("whoami = %d %s", resp.Status, resp.Body)
	}
	if remote, _ := who["remote"].(string); !strings.HasPrefix(remote, "relay-device-") {
		t.Fatalf("remote addr %q looks like a network address", remote)
	}
	if who["host"] != "phone.test" {
		t.Fatalf("host header = %v", who["host"])
	}
	devices, _ := f.store.Devices()
	if len(devices) != 1 || devices[0].Name != "Test phone" || !f.host.ConnectedDevices()[devices[0].ID] {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestPairingCodeIsSingleUseAndChecked(t *testing.T) {
	f := newFixture(t)
	p, err := f.host.MintPairing(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	opts := f.options(deviceKey(t), p.RouteToken)
	opts.Pair = &relay.PairRequest{Code: "wrong-code", Name: "x"}
	ctx := context.Background()
	cl, err := client.Dial(ctx, opts)
	if err == nil || cl == nil || cl.Welcome.Error != "invalid_code" {
		t.Fatalf("wrong code: %v %+v", err, cl)
	}
	// The one-time route was spent by that attempt too.
	opts.Pair.Code = p.Code
	if _, err := client.Dial(ctx, opts); err == nil || !strings.Contains(err.Error(), "relay refused") {
		t.Fatalf("second use of a one-time route: %v", err)
	}
	// Each attempt needs its own one-time route, so a pairing code cannot be
	// brute-forced through one QR code. Redeem the real code once...
	p2, _ := f.host.MintPairing(f.owner)
	opts = f.options(deviceKey(t), p2.RouteToken)
	opts.Pair = &relay.PairRequest{Code: p.Code, Name: "x"}
	if cl, err = client.Dial(ctx, opts); err != nil || !cl.Welcome.OK {
		t.Fatalf("first use of the code: %v", err)
	}
	cl.Close()
	// ...and a second redemption, on yet another fresh route, fails.
	p3, _ := f.host.MintPairing(f.owner)
	opts = f.options(deviceKey(t), p3.RouteToken)
	opts.Pair = &relay.PairRequest{Code: p.Code, Name: "y"}
	cl, err = client.Dial(ctx, opts)
	if err == nil || cl.Welcome.Error != "invalid_code" {
		t.Fatalf("reused code: %v", err)
	}
}

func TestExpiredPairingCode(t *testing.T) {
	f := newFixture(t)
	p, _ := f.host.MintPairing(f.owner)
	setNow(func() time.Time { return time.Now().Add(PairingTTL + time.Second) })
	defer setNow(time.Now)
	if _, _, _, err := f.store.Pair(p.Code, make([]byte, 32), "x"); err != ErrNotFound {
		t.Fatalf("expired code: %v", err)
	}
}

func TestUnpairedKeyIsRefused(t *testing.T) {
	f := newFixture(t)
	_, token := f.pair(deviceKey(t), "real")
	// Someone who learned a device's route token but not its key gets past
	// the relay and is refused by the host.
	cl, err := f.connect(deviceKey(t), token)
	if err == nil || cl == nil || cl.Welcome.Error != "unknown_device" {
		t.Fatalf("stranger key: %v", err)
	}
}

func TestDeviceMustUseItsOwnRoute(t *testing.T) {
	f := newFixture(t)
	a, b := deviceKey(t), deviceKey(t)
	_, tokenA := f.pair(a, "a")
	f.pair(b, "b")
	cl, err := f.connect(b, tokenA)
	if err == nil || cl.Welcome.Error != "unknown_device" {
		t.Fatalf("device b on device a's route: %v", err)
	}
}

func TestRevokedDeviceIsCutOff(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	w, token := f.pair(key, "phone")
	cl, err := f.connect(key, token)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := cl.Do(context.Background(), "GET", "/api/whoami", nil); err != nil || resp.Status != 200 {
		t.Fatalf("before revoke: %v %v", resp, err)
	}
	if _, err := f.host.Revoke(w.DeviceID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("live connection survived revocation")
	}
	// The relay no longer routes its token...
	if _, err := f.connect(key, token); err == nil || !strings.Contains(err.Error(), "relay refused") {
		t.Fatalf("reconnect after revoke: %v", err)
	}
	// ...and even if a relay still did, the host refuses the key.
	if _, err := f.store.Active(key.Public); err != ErrNotFound {
		t.Fatalf("revoked key still active: %v", err)
	}
}

func TestRevocationAppliesToTheNextRequest(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	w, token := f.pair(key, "phone")
	cl, err := f.connect(key, token)
	if err != nil {
		t.Fatal(err)
	}
	// Delete the row behind the host's back: no connection teardown, so only
	// the per-request check can stop the next request.
	if _, err := f.store.Revoke(w.DeviceID); err != nil {
		t.Fatal(err)
	}
	resp, err := cl.Do(context.Background(), "GET", "/api/whoami", nil)
	if err == nil && resp.Status != 401 {
		t.Fatalf("request after revocation = %d %s", resp.Status, resp.Body)
	}
}

func TestIdleDeviceExpires(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	_, token := f.pair(key, "phone")
	setNow(func() time.Time { return time.Now().Add(31 * 24 * time.Hour) })
	defer setNow(time.Now)
	if _, err := f.connect(key, token); err == nil {
		t.Fatal("idle device connected")
	}
}

func TestReplayedFrameEndsTheTunnel(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	_, token := f.pair(key, "phone")
	cl, err := f.connect(key, token)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cl.Open()
	head, _ := json.Marshal(relay.RequestHead{Method: "POST", URL: "/api/echo"})
	sealed, _ := cl.Seal(relay.FrameRequest, id, head)
	if err := cl.WriteRaw(sealed); err != nil {
		t.Fatal(err)
	}
	if err := cl.WriteRaw(sealed); err != nil { // the relay (or anyone) replays it
		t.Fatal(err)
	}
	select {
	case <-cl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("host accepted a replayed frame")
	}
}

func TestTamperedFrameEndsTheTunnel(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	_, token := f.pair(key, "phone")
	cl, err := f.connect(key, token)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cl.Open()
	head, _ := json.Marshal(relay.RequestHead{Method: "GET", URL: "/api/whoami"})
	sealed, _ := cl.Seal(relay.FrameRequest, id, head)
	sealed[3] ^= 0x40
	_ = cl.WriteRaw(sealed)
	select {
	case <-cl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("host accepted a tampered frame")
	}
}

func TestTunnelRejectsOtherOrigins(t *testing.T) {
	for _, u := range []string{"http://example.com/", "//example.com/x", "api/x", "/a b"} {
		if validPath(u) {
			t.Fatalf("validPath(%q) = true", u)
		}
	}
	if validHost("evil.com/x") || validHost("a@b") || !validHost("phone.test:8443") {
		t.Fatal("validHost")
	}
}

func TestBodiesStreamsAndWebSocketsOverTheRelayStayEncrypted(t *testing.T) {
	f := newFixture(t)
	key := deviceKey(t)
	_, token := f.pair(key, "phone")
	cl, err := f.connect(key, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A request body larger than one chunk, and the response.
	big := strings.Repeat(marker, 3000)
	resp, err := cl.Do(ctx, "POST", "/api/echo", []byte(big))
	if err != nil || resp.Status != 200 || string(resp.Body) != marker+":"+big {
		t.Fatalf("echo: %v status=%v len=%d", err, resp, len(resp.Body))
	}
	// Server-sent events arrive as a stream.
	resp, err = cl.Do(ctx, "GET", "/api/stream", nil)
	if err != nil || strings.Count(string(resp.Body), marker) != 3 {
		t.Fatalf("stream: %v %q", err, resp.Body)
	}
	// A WebSocket, with a message larger than one frame.
	id, ch := cl.Open()
	open, _ := json.Marshal(relay.WSOpen{URL: "/term/echo", Protocols: []string{"tty"}})
	_ = cl.Send(relay.FrameWSOpen, id, open)
	fr := <-ch
	if fr.Type != relay.FrameWSOpened || !strings.Contains(string(fr.Payload), "tty") {
		t.Fatalf("ws open: %d %q", fr.Type, fr.Payload)
	}
	msg := []byte(strings.Repeat(marker, 1500)) // ~45 KB: three frames
	for len(msg) > 0 {
		n := min(len(msg), relay.MaxChunk)
		more := relay.WSMore
		if n == len(msg) {
			more = relay.WSFinal
		}
		_ = cl.Send(relay.FrameWSBinary, id, append([]byte{more}, msg[:n]...))
		msg = msg[n:]
	}
	var got []byte
	for {
		fr := <-ch
		if fr.Type != relay.FrameWSBinary {
			t.Fatalf("ws frame type %d", fr.Type)
		}
		got = append(got, fr.Payload[1:]...)
		if fr.Payload[0] == relay.WSFinal {
			break
		}
	}
	if string(got) != "echo:"+strings.Repeat(marker, 1500) {
		t.Fatalf("ws echo: %d bytes", len(got))
	}
	cls, _ := json.Marshal(relay.WSClose{Code: 1000})
	_ = cl.Send(relay.FrameWSClose, id, cls)

	// Everything above crossed the relay. None of it was readable there.
	frames := f.frames.all()
	unpadded := 0
	if len(frames) < 10 {
		t.Fatalf("only %d frames crossed the relay", len(frames))
	}
	for _, p := range frames {
		for _, needle := range []string{marker, "TOP-SECRET", "/api/", "/term/", "whoami", "owner@example.com", "Test phone", `"kind"`, `"tty"`, "Content-Type"} {
			if bytes.Contains(p, []byte(needle)) {
				t.Fatalf("relay saw plaintext %q in a %d-byte frame", needle, len(p))
			}
		}
		if (len(p)-16)%256 != 0 {
			unpadded++
		}
	}
	// Transport frames are padded to 256-byte steps (plus the 16-byte tag).
	// Only the handshake messages of the two connections (pairing, then
	// use) are not.
	if unpadded > 4 {
		t.Fatalf("%d frames were not padded", unpadded)
	}
}
