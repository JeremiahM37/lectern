package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"

	"github.com/JeremiahM37/lectern/v2/internal/api"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/relay"
	"github.com/JeremiahM37/lectern/v2/internal/relay/client"
	relayserver "github.com/JeremiahM37/lectern/v2/internal/relay/server"
	"github.com/JeremiahM37/lectern/v2/web"
)

const relaySecret = "integration-relay-secret-0123456789abcdef"

type capturedFrames struct {
	mu sync.Mutex
	b  [][]byte
}

func (c *capturedFrames) add(_, _ string, _ uint32, p []byte) {
	c.mu.Lock()
	c.b = append(c.b, append([]byte(nil), p...))
	c.mu.Unlock()
}

type pairPayload struct {
	Relay, Ch, Hk, Sk, C, Rt string
}

// relayHarness is a full Lectern in token mode — so nothing reaches it
// without a credential — connected to a real relay.
func relayHarness(t *testing.T) (*harness, *capturedFrames) {
	t.Helper()
	frames := &capturedFrames{}
	rs, err := relayserver.New(relayserver.Config{HostSecret: relaySecret, OnFrame: frames.add})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)
	h := newHarness(t, func(c *config.Config) {
		c.Auth, c.AuthToken = "token", "owner-token"
		c.RelayURL = "ws" + strings.TrimPrefix(ts.URL, "http")
		c.RelayHostSecret = relaySecret
	})
	h.App.StartRelay()
	deadline := time.Now().Add(5 * time.Second)
	for !h.App.Server.Relay.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatalf("host never reached the relay: %+v", h.App.Server.Relay.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return h, frames
}

var owner = map[string]string{"Authorization": "Bearer owner-token"}

// pairPhone mints a pairing as the owner and redeems it like a phone would,
// returning a connected tunnel plus the decoded QR payload.
func pairPhone(t *testing.T, h *harness, key noise.DHKey) (*client.Client, pairPayload) {
	t.Helper()
	code, raw := h.request("POST", "/api/relay/pair", nil, owner)
	if code != 200 {
		t.Fatalf("mint: %d %s", code, raw)
	}
	var minted struct{ Fragment string }
	_ = json.Unmarshal(raw, &minted)
	b, err := relay.UnB64(minted.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	var p pairPayload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	hk, _ := relay.UnB64(p.Hk)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	paired, err := client.Dial(ctx, client.Options{RelayURL: p.Relay, Channel: p.Ch, RouteToken: p.Rt, HostKey: hk, Device: key,
		Pair: &relay.PairRequest{Code: p.C, Name: "Integration phone"}})
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	paired.Close()
	// No retry: the device's route is live at the relay before the pairing
	// reply is sent.
	cl, err := client.Dial(ctx, client.Options{RelayURL: p.Relay, Channel: p.Ch, RouteToken: paired.Welcome.RouteToken, HostKey: hk, Device: key})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return cl, p
}

func tunnelJSON(t *testing.T, cl *client.Client, method, path string, body any, want int) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := cl.DoJSON(ctx, method, path, body)
	if err != nil {
		t.Fatalf("%s %s over the relay: %v", method, path, err)
	}
	if resp.Status != want {
		t.Fatalf("%s %s over the relay = %d, want %d: %s", method, path, resp.Status, want, resp.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(resp.Body, &out)
	return out
}

func tunnelList(t *testing.T, cl *client.Client, path string) []map[string]any {
	t.Helper()
	resp, err := cl.Do(context.Background(), "GET", path, nil)
	if err != nil || resp.Status != 200 {
		t.Fatalf("GET %s over the relay: %v %v", path, err, resp)
	}
	var out []map[string]any
	_ = json.Unmarshal(resp.Body, &out)
	return out
}

// TestApprovalRoundTripOverRelay is the whole feature end to end: a real
// relay, a real Lectern that refuses unauthenticated callers, a phone that
// pairs, creates a gated task, sees the approval and approves it — all
// through the relay, which sees nothing it can read.
func TestApprovalRoundTripOverRelay(t *testing.T) {
	h, frames := relayHarness(t)
	const title = "Relay-approval-marker-5b1e"

	// Without a credential, Lectern refuses the plain HTTP API.
	if code, _ := h.request("GET", "/api/approvals", nil, nil); code != 401 {
		t.Fatalf("unauthenticated API call = %d, want 401", code)
	}

	key, _ := noise.DH25519.GenerateKeypair(rand.Reader)
	cl, qr := pairPhone(t, h, key)
	defer cl.Close()

	who := tunnelJSON(t, cl, "GET", "/api/whoami", nil, 200)
	if who["kind"] != "relay-device" || who["human"] != true {
		t.Fatalf("whoami over the relay = %v", who)
	}

	// Live board events arrive over the same tunnel (SSE).
	events := make(chan string, 64)
	go func() {
		id, ch := cl.Open()
		head, _ := json.Marshal(relay.RequestHead{Method: "GET", URL: "/api/stream"})
		_ = cl.Send(relay.FrameRequest, id, head)
		_ = cl.Send(relay.FrameRequestEnd, id, nil)
		for f := range ch {
			if f.Type == relay.FrameResponseBody {
				events <- string(f.Payload)
			}
		}
	}()

	projects := tunnelList(t, cl, "/api/projects")
	if len(projects) == 0 {
		t.Fatal("no projects over the relay")
	}
	pid := projects[0]["id"]
	task := tunnelJSON(t, cl, "POST", "/api/tasks", map[string]any{
		"project_id": pid, "title": title, "prompt": "deploy it [mock:approval]", "permission_mode": "default"}, 201)
	taskID := int64(task["id"].(float64))
	tunnelJSON(t, cl, "POST", fmt.Sprintf("/api/tasks/%d/dispatch", taskID), map[string]any{}, 200)

	var approvalID int64
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !client.Wait(ctx, 50*time.Millisecond, func() bool {
		for _, a := range tunnelList(t, cl, "/api/approvals?status=pending") {
			if int64(a["task_id"].(float64)) == taskID {
				approvalID = int64(a["id"].(float64))
				return true
			}
		}
		return false
	}) {
		t.Fatal("the approval never appeared over the relay")
	}
	decided := tunnelJSON(t, cl, "POST", fmt.Sprintf("/api/approvals/%d/decision", approvalID),
		map[string]any{"decision": "approved", "note": "ok from the relay"}, 200)
	if decided["status"] != "approved" {
		t.Fatalf("decision = %v", decided)
	}
	if !client.Wait(ctx, 50*time.Millisecond, func() bool {
		return tunnelJSON(t, cl, "GET", fmt.Sprintf("/api/tasks/%d", taskID), nil, 200)["status"] == "review"
	}) {
		t.Fatal("the agent did not finish after the relayed approval")
	}
	select {
	case ev := <-events:
		if !strings.Contains(ev, "connected") && !strings.Contains(ev, "data") {
			t.Fatalf("unexpected SSE chunk %q", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live events over the relay")
	}

	// The relay forwarded every byte of that and could read none of it.
	frames.mu.Lock()
	captured := append([][]byte(nil), frames.b...)
	frames.mu.Unlock()
	if len(captured) < 20 {
		t.Fatalf("only %d frames crossed the relay", len(captured))
	}
	for _, p := range captured {
		for _, needle := range []string{title, "Relay-approval", "approved", "ok from the relay", "/api/", "mock:approval",
			"relay-device", "Integration phone", "text/event-stream", qr.C} {
			if bytes.Contains(p, []byte(needle)) {
				t.Fatalf("the relay saw plaintext %q", needle)
			}
		}
	}

	// Revoking the phone cuts the live tunnel at once.
	devices := tunnelJSON(t, cl, "GET", "/api/relay", nil, 200)["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("devices = %v", devices)
	}
	id := int64(devices[0].(map[string]any)["id"].(float64))
	if code, raw := h.request("DELETE", fmt.Sprintf("/api/relay/devices/%d", id), nil, owner); code != 200 {
		t.Fatalf("revoke: %d %s", code, raw)
	}
	select {
	case <-cl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("revoked phone kept its tunnel")
	}
}

// Relay pairing is owner-only, exactly like deciding an approval.
func TestRelayPairingNeedsTheOwner(t *testing.T) {
	h, _ := relayHarness(t)
	if code, _ := h.request("POST", "/api/relay/pair", nil, nil); code != 401 {
		t.Fatalf("anonymous mint = %d", code)
	}
	if code, _ := h.request("GET", "/api/relay", nil, nil); code != 401 {
		t.Fatalf("anonymous status = %d", code)
	}
}

// The shell manifest is signed by the key the QR code pins, and lists the
// hash of every shell file the service worker caches.
func TestShellManifestIsSignedByThePinnedKey(t *testing.T) {
	h, _ := relayHarness(t)
	key, _ := noise.DH25519.GenerateKeypair(rand.Reader)
	cl, qr := pairPhone(t, h, key)
	cl.Close()
	resp, err := http.Get(h.URL + "/shell-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var signed struct{ Manifest, Sig, Key string }
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil || resp.StatusCode != 200 {
		t.Fatalf("manifest: %d %v", resp.StatusCode, err)
	}
	pinned, _ := relay.UnB64(qr.Sk)
	manifest, _ := relay.UnB64(signed.Manifest)
	sig, _ := relay.UnB64(signed.Sig)
	if !ed25519.Verify(pinned, append([]byte(api.ShellManifestLabel), manifest...), sig) {
		t.Fatal("manifest signature does not verify against the pinned shell key")
	}
	var m struct{ Files map[string]string }
	_ = json.Unmarshal(manifest, &m)
	sum := sha256.Sum256(web.IndexHTML)
	if m.Files["/"] != hex.EncodeToString(sum[:]) || m.Files["/icon.svg"] == "" {
		t.Fatalf("manifest files = %d entries, / = %q", len(m.Files), m.Files["/"])
	}
	if _, ok := m.Files["/sw.js"]; ok {
		t.Fatal("sw.js must not be in the manifest")
	}
}
