package relay

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flynn/noise"
)

// vectorsPath is shared with frontend/src/relay/noise.test.ts, which must
// reproduce every byte: that is what keeps the browser's Noise state machine
// honest against flynn/noise.
var vectorsPath = filepath.Join("..", "..", "frontend", "src", "relay", "testdata", "noise-vectors.json")

type noiseVectors struct {
	Channel      string   `json:"channel"`
	DeviceStatic string   `json:"device_static"`
	DeviceEph    string   `json:"device_ephemeral"`
	HostStatic   string   `json:"host_static"`
	HostEph      string   `json:"host_ephemeral"`
	HostPublic   string   `json:"host_public"`
	Hello        string   `json:"hello"`
	Welcome      string   `json:"welcome"`
	Message1     string   `json:"message1"`
	Message2     string   `json:"message2"`
	ToHost       []string `json:"to_host"`
	ToHostCT     []string `json:"to_host_ciphertext"`
	ToDevice     []string `json:"to_device"`
	ToDeviceCT   []string `json:"to_device_ciphertext"`
}

func fixedKey(t *testing.T, seed byte) noise.DHKey {
	t.Helper()
	k, err := noise.DH25519.GenerateKeypair(bytes.NewReader(bytes.Repeat([]byte{seed}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func buildVectors(t *testing.T) noiseVectors {
	t.Helper()
	dev, host := fixedKey(t, 0x11), fixedKey(t, 0x22)
	devEph, hostEph := bytes.Repeat([]byte{0x33}, 32), bytes.Repeat([]byte{0x44}, 32)
	channel := "vectorChannel0000000A"
	hello, welcome := []byte(`{"v":1,"pair":{"code":"c0de","name":"Vector phone"}}`), []byte(`{"ok":true,"device_id":1}`)

	ini, err := noise.NewHandshakeState(noise.Config{CipherSuite: Suite, Pattern: noise.HandshakeIK, Initiator: true,
		Prologue: Prologue(channel), StaticKeypair: dev, PeerStatic: host.Public, Random: bytes.NewReader(devEph)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noise.NewHandshakeState(noise.Config{CipherSuite: Suite, Pattern: noise.HandshakeIK,
		Prologue: Prologue(channel), StaticKeypair: host, Random: bytes.NewReader(hostEph)})
	if err != nil {
		t.Fatal(err)
	}
	m1, _, _, err := ini.WriteMessage(nil, hello)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resp.ReadMessage(nil, m1); err != nil {
		t.Fatal(err)
	}
	m2, hostRecv, hostSend, err := resp.WriteMessage(nil, welcome)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ini.ReadMessage(nil, m2); err != nil {
		t.Fatal(err)
	}
	v := noiseVectors{
		Channel: channel, DeviceStatic: hex.EncodeToString(dev.Private), DeviceEph: hex.EncodeToString(devEph),
		HostStatic: hex.EncodeToString(host.Private), HostEph: hex.EncodeToString(hostEph),
		HostPublic: hex.EncodeToString(host.Public), Hello: string(hello), Welcome: string(welcome),
		Message1: hex.EncodeToString(m1), Message2: hex.EncodeToString(m2),
	}
	// The phone's transport ciphertexts are made by the host's receiving
	// state's twin; reproduce them by encrypting with a fresh cipher state
	// over the same key, which is what the initiator holds.
	devSend := noise.UnsafeNewCipherState(Suite, hostRecv.UnsafeKey(), 0)
	for _, p := range []string{"GET /api/whoami", "second frame"} {
		ct, err := devSend.Encrypt(nil, nil, []byte(p))
		if err != nil {
			t.Fatal(err)
		}
		if pt, err := hostRecv.Decrypt(nil, nil, ct); err != nil || string(pt) != p {
			t.Fatalf("host could not open %q", p)
		}
		v.ToHost = append(v.ToHost, p)
		v.ToHostCT = append(v.ToHostCT, hex.EncodeToString(ct))
	}
	for _, p := range []string{"HTTP/1.1 200 OK"} {
		ct, err := hostSend.Encrypt(nil, nil, []byte(p))
		if err != nil {
			t.Fatal(err)
		}
		v.ToDevice = append(v.ToDevice, p)
		v.ToDeviceCT = append(v.ToDeviceCT, hex.EncodeToString(ct))
	}
	return v
}

// TestNoiseVectorsAreCurrent regenerates the browser's interop vectors with
// UPDATE_NOISE_VECTORS=1 and otherwise checks the committed file matches.
func TestNoiseVectorsAreCurrent(t *testing.T) {
	want, err := json.MarshalIndent(buildVectors(t), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_NOISE_VECTORS") == "1" {
		if err := os.MkdirAll(filepath.Dir(vectorsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vectorsPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("%v (run with UPDATE_NOISE_VECTORS=1)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("frontend Noise vectors are stale; run with UPDATE_NOISE_VECTORS=1")
	}
}
