package api

// Phone support that is not a page of its own (docs/mobile-sessions.md,
// docs/android.md): dictation transcribed on this host with whisper.cpp, and
// the Digital Asset Links file that lets an Android app built for this
// host's address open its pairing links directly.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/voice"
)

func (s *Server) mobileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/voice", s.voiceStatus)
	mux.HandleFunc("POST /api/voice/transcribe", s.transcribeVoice)
	mux.HandleFunc("GET /.well-known/assetlinks.json", s.assetLinks)
}

// Detection is a PATH lookup and a few globs; cache it briefly so a page
// load does not repeat them, and installing whisper.cpp later needs no
// restart.
var voiceCache struct {
	sync.Mutex
	at     time.Time
	engine *voice.Engine
}

func voiceEngine() *voice.Engine {
	voiceCache.Lock()
	defer voiceCache.Unlock()
	if time.Since(voiceCache.at) > time.Minute {
		voiceCache.engine = voice.Detect()
		voiceCache.at = time.Now()
	}
	return voiceCache.engine
}

// One transcription at a time: whisper uses every core it is given.
var voiceSlot = make(chan struct{}, 1)

func (s *Server) voiceStatus(w http.ResponseWriter, r *http.Request) {
	e := voiceEngine()
	if e == nil {
		writeJSON(w, 200, map[string]any{"available": false, "engine": "whisper.cpp",
			"hint": "Install whisper.cpp (whisper-cli) and a ggml model on this host, or set LECTERN_WHISPER_BIN and LECTERN_WHISPER_MODEL."})
		return
	}
	writeJSON(w, 200, map[string]any{"available": true, "engine": "whisper.cpp",
		"model":    strings.TrimSuffix(strings.TrimPrefix(filepath.Base(e.Model), "ggml-"), ".bin"),
		"language": e.Language, "max_bytes": voice.MaxAudioBytes})
}

// transcribeVoice turns one WAV recording into text for a person to review
// in the box they dictated into; nothing is sent anywhere on their behalf.
// It needs the same signed-in human an approval does: it spends this host's
// CPU, and only a person talking into a phone has a reason to call it.
func (s *Server) transcribeVoice(w http.ResponseWriter, r *http.Request) {
	if principal, _ := auth.FromContext(r.Context()); !s.Auth.CanDecide(principal) {
		httpError(w, 403, "voice transcription requires a signed-in human")
		return
	}
	e := voiceEngine()
	if e == nil {
		httpError(w, 404, "whisper.cpp is not installed on this host")
		return
	}
	audio, err := io.ReadAll(io.LimitReader(r.Body, voice.MaxAudioBytes+1))
	if err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	if len(audio) > voice.MaxAudioBytes {
		httpError(w, 413, "recording too long (five minutes at most)")
		return
	}
	if err := voice.CheckWAV(audio); err != nil {
		httpError(w, 415, "%s", err.Error())
		return
	}
	select {
	case voiceSlot <- struct{}{}:
		defer func() { <-voiceSlot }()
	case <-time.After(30 * time.Second):
		httpError(w, 503, "another recording is still being transcribed; try again")
		return
	case <-r.Context().Done():
		return
	}
	started := time.Now()
	text, err := e.Transcribe(r.Context(), audio, r.URL.Query().Get("lang"))
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"text": text, "seconds": time.Since(started).Seconds()})
}

// androidApp is the published Android app: its package and the SHA-256 of
// the certificate its release APKs are signed with (docs/android.md).
const (
	androidPackage = "io.github.jeremiahm37.lectern"
	androidCert    = "CC:43:8A:C9:A8:C5:8B:58:2E:69:B3:20:FB:D8:50:38:08:2B:AD:56:C3:7A:41:F9:B6:DB:04:D0:F2:AE:BA:56"
)

var certFingerprint = regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`)

// assetLinks lets an Android app open this host's /pair and /relay-pair
// links itself, when the app was built to claim this host's address
// (mobile/build-android.sh, LECTERN_APP_LINK_HOSTS). Android fetches it
// without credentials, so it is public, like any site's. It names the
// published app, plus any builds listed in LECTERN_ANDROID_APP_LINKS as
// comma-separated package=SHA256 pairs (a self-signed or debug build).
func (s *Server) assetLinks(w http.ResponseWriter, r *http.Request) {
	type target struct {
		Namespace    string   `json:"namespace"`
		PackageName  string   `json:"package_name"`
		Fingerprints []string `json:"sha256_cert_fingerprints"`
	}
	certs := map[string][]string{androidPackage: {androidCert}}
	order := []string{androidPackage}
	for _, entry := range strings.Split(os.Getenv("LECTERN_ANDROID_APP_LINKS"), ",") {
		pkg, fp, ok := strings.Cut(strings.TrimSpace(entry), "=")
		fp = strings.ToUpper(strings.TrimSpace(fp))
		if !ok || pkg == "" || !certFingerprint.MatchString(fp) {
			continue
		}
		if _, seen := certs[pkg]; !seen {
			order = append(order, pkg)
		}
		certs[pkg] = append(certs[pkg], fp)
	}
	out := []map[string]any{}
	for _, pkg := range order {
		out = append(out, map[string]any{
			"relation": []string{"delegate_permission/common.handle_all_urls"},
			"target":   target{"android_app", pkg, certs[pkg]},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=3600")
	json.NewEncoder(w).Encode(out)
}
