// relay.go backs Settings → Devices → Encrypted relay (docs/relay.md): the
// relay connection's status, pairing a phone over the relay, revoking relay
// devices, and the signed app-shell manifest a paired phone's service worker
// checks before it accepts a new shell. Everything but the manifest is
// owner-only, like ordinary pairing: minting a credential that can decide
// approvals is at least as sensitive as deciding one.
package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/relay"
	relayhost "github.com/JeremiahM37/lectern/v2/internal/relay/host"
	"github.com/JeremiahM37/lectern/v2/web"
)

type relayStatusOut struct {
	relayhost.Status
	ShellURL string             `json:"shell_url,omitempty"`
	Devices  []relayhost.Device `json:"devices"`
}

// getRelay reports whether the relay is configured and connected, the host's
// key fingerprints, and the paired relay devices.
func (s *Server) getRelay(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	out := relayStatusOut{Devices: []relayhost.Device{}}
	if s.Relay == nil {
		writeJSON(w, 200, out)
		return
	}
	out.Status = s.Relay.Status()
	out.ShellURL = s.Cfg.RelayShellURL
	devices, err := s.RelayStore.Devices()
	if err != nil {
		respondErr(w, err)
		return
	}
	live := s.Relay.ConnectedDevices()
	for i := range devices {
		devices[i].Connected = live[devices[i].ID]
	}
	out.Devices = devices
	writeJSON(w, 200, out)
}

// relayPairPayload is what the pairing QR code carries, in its URL fragment.
// The pairing code (C) is the one secret the relay must never see: it only
// ever travels inside the phone's first Noise message, encrypted to HostKey.
type relayPairPayload struct {
	V          int    `json:"v"`
	Relay      string `json:"relay"`
	Channel    string `json:"ch"`
	HostKey    string `json:"hk"`
	ShellKey   string `json:"sk"`
	Code       string `json:"c"`
	RouteToken string `json:"rt"`
}

// mintRelayPairing is "Pair a phone over the relay".
func (s *Server) mintRelayPairing(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	if s.Relay == nil {
		httpError(w, 409, "the relay is not configured — set LECTERN_RELAY_URL (docs/relay.md)")
		return
	}
	id, err := s.RelayStore.Identity()
	if err != nil {
		respondErr(w, err)
		return
	}
	p, err := s.Relay.MintPairing(owner)
	if errors.Is(err, relayhost.ErrOffline) {
		httpError(w, 503, "Lectern is not connected to the relay right now")
		return
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	payload, _ := json.Marshal(relayPairPayload{
		V: relay.ProtocolVersion, Relay: s.Cfg.RelayURL, Channel: id.Channel(),
		HostKey: relay.B64(id.Noise.Public), ShellKey: relay.B64(id.ShellPublic()),
		Code: p.Code, RouteToken: p.RouteToken,
	})
	writeJSON(w, 200, map[string]any{
		"fragment":         relay.B64(payload),
		"shell_url":        s.Cfg.RelayShellURL,
		"expires_at":       float64(p.ExpiresAt.Unix()),
		"ttl_s":            int(relayhost.PairingTTL.Seconds()),
		"host_fingerprint": relay.Fingerprint(id.Noise.Public),
	})
}

// revokeRelayDevice deletes a relay device, drops its relay route and closes
// its live connections at once.
func (s *Server) revokeRelayDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil || s.RelayStore == nil {
		httpError(w, 404, "no such device")
		return
	}
	if s.Relay != nil {
		_, err = s.Relay.Revoke(id)
	} else {
		_, err = s.RelayStore.Revoke(id)
	}
	if errors.Is(err, relayhost.ErrNotFound) {
		httpError(w, 404, "no such device")
		return
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

// shellManifest lists a SHA-256 for every file of the embedded app shell.
// It is computed once: the shell is compiled into the binary.
var shellManifest struct {
	once  sync.Once
	bytes []byte
	err   error
}

// ShellManifestLabel domain-separates the shell signature from every other
// use of an Ed25519 key. frontend/src/relay/shell.ts uses the same string.
const ShellManifestLabel = "lectern-shell-manifest-v1\x00"

func buildShellManifest() ([]byte, error) {
	files := map[string]string{}
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	files["/"] = sum(web.IndexHTML)
	err := fs.WalkDir(web.Assets, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "static")
		if rel == "/sw.js" {
			// The browser alone decides when sw.js changes; see
			// docs/relay.md "What this does not cover".
			return nil
		}
		b, err := fs.ReadFile(web.Assets, p)
		if err != nil {
			return err
		}
		files[rel] = sum(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// encoding/json writes map keys sorted, so the bytes are stable.
	return json.Marshal(map[string]any{"v": 1, "files": files})
}

// getShellManifest serves the signed manifest. It is public, like the shell
// itself: it holds hashes of files anyone can download, and a signature
// only the host can make.
func (s *Server) getShellManifest(w http.ResponseWriter, _ *http.Request) {
	if s.Relay == nil || s.RelayStore == nil {
		http.NotFound(w, nil)
		return
	}
	shellManifest.once.Do(func() { shellManifest.bytes, shellManifest.err = buildShellManifest() })
	if shellManifest.err != nil {
		httpError(w, 500, "%s", shellManifest.err.Error())
		return
	}
	id, err := s.RelayStore.Identity()
	if err != nil {
		respondErr(w, err)
		return
	}
	sig := ed25519.Sign(id.Shell, append([]byte(ShellManifestLabel), shellManifest.bytes...))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{
		"manifest": relay.B64(shellManifest.bytes),
		"sig":      relay.B64(sig),
		"key":      relay.B64(id.ShellPublic()),
	})
}
