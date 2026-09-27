package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/api"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func standInWhisper(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	os.WriteFile(bin, []byte("#!/bin/sh\necho ' Ship it.'\n"), 0o755)
	model := filepath.Join(dir, "ggml-base.en.bin")
	os.WriteFile(model, []byte("x"), 0o644)
	t.Setenv("LECTERN_WHISPER_BIN", bin)
	t.Setenv("LECTERN_WHISPER_MODEL", model)
	api.ResetVoiceCache()
	t.Cleanup(api.ResetVoiceCache)
}

func postWAV(t *testing.T, url string, body []byte) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "audio/wav", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func wavBytes() []byte {
	b := make([]byte, 44+3200)
	copy(b, "RIFF")
	copy(b[8:], "WAVE")
	return b
}

func TestVoiceTranscriptionOnTheHost(t *testing.T) {
	t.Setenv("LECTERN_WHISPER_BIN", filepath.Join(t.TempDir(), "absent"))
	api.ResetVoiceCache()
	h := newHarness(t, func(c *config.Config) { c.Auth = "none" })
	code, raw := h.request("GET", "/api/voice", nil, nil)
	if code != 200 || !strings.Contains(string(raw), `"available":false`) {
		t.Fatalf("without whisper.cpp: %d %s", code, raw)
	}
	if code, _ := postWAV(t, h.URL+"/api/voice/transcribe", wavBytes()); code != 404 {
		t.Fatalf("transcribing with nothing installed: %d", code)
	}

	standInWhisper(t)
	code, raw = h.request("GET", "/api/voice", nil, nil)
	if code != 200 || !strings.Contains(string(raw), `"model":"base.en"`) {
		t.Fatalf("with whisper.cpp: %d %s", code, raw)
	}
	code, out := postWAV(t, h.URL+"/api/voice/transcribe?lang=en", wavBytes())
	if code != 200 || out["text"] != "Ship it." {
		t.Fatalf("transcribe: %d %v", code, out)
	}
	if code, _ := postWAV(t, h.URL+"/api/voice/transcribe", []byte("OggS not a wav file at all, but long enough to pass a length check")); code != 415 {
		t.Fatalf("non-WAV audio: %d", code)
	}
}

// A local process that is not a signed-in person cannot spend the host's CPU
// on transcription, the same rule as deciding an approval.
func TestVoiceTranscriptionNeedsAHuman(t *testing.T) {
	standInWhisper(t)
	h := newHarness(t, func(c *config.Config) { c.Auth = "tailscale"; c.TailscaleUsers = "nobody@example.com" })
	if code, _ := postWAV(t, h.URL+"/api/voice/transcribe", wavBytes()); code != 403 {
		t.Fatalf("got %d, want 403", code)
	}
}

func TestAssetLinksNameThePublishedAppAndExtraBuilds(t *testing.T) {
	t.Setenv("LECTERN_ANDROID_APP_LINKS", "io.github.jeremiahm37.lectern.debug=aa:"+strings.Repeat("BB:", 30)+"CC, bogus=1, x=")
	h := newHarness(t, func(c *config.Config) { c.Auth = "tailscale"; c.TailscaleUsers = "nobody@example.com" })
	resp, err := http.Get(h.URL + "/.well-known/assetlinks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var links []struct {
		Relation []string `json:"relation"`
		Target   struct {
			Package string   `json:"package_name"`
			Certs   []string `json:"sha256_cert_fingerprints"`
		} `json:"target"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&links); err != nil || resp.StatusCode != 200 {
		t.Fatalf("assetlinks: %d %v", resp.StatusCode, err)
	}
	if len(links) != 2 || links[0].Target.Package != "io.github.jeremiahm37.lectern" ||
		!strings.HasPrefix(links[0].Target.Certs[0], "CC:43:8A") ||
		links[1].Target.Package != "io.github.jeremiahm37.lectern.debug" || links[1].Target.Certs[0] != "AA:"+strings.Repeat("BB:", 30)+"CC" {
		t.Fatalf("links: %+v", links)
	}
	if links[0].Relation[0] != "delegate_permission/common.handle_all_urls" {
		t.Fatalf("relation: %v", links[0].Relation)
	}
}
