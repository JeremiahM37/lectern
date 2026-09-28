package localruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/app"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestWiFiHandlerKeepsHostOriginAndLocalRoutesPrivate(t *testing.T) {
	handler := wifiHandler("192.0.2.10:32100", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		host, origin, path string
		want               int
	}{
		{"192.0.2.10:32100", "http://192.0.2.10:32100", "/api/health", 204},
		{"evil.example:32100", "", "/api/health", 421},
		{"192.0.2.10:32100", "https://evil.example", "/api/projects", 403},
		{"192.0.2.10:32100", "", "/__lectern_local/stop", 404},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		r.Host = tc.host
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%+v: got %d", tc, w.Code)
		}
	}
}

func TestWiFiPairsIntoTheExistingRuntimeAndApprovesItsSession(t *testing.T) {
	const token = "test-owner-token"
	cfg := engineConfig(&config.Config{TickInterval: time.Second, ApprovalExpire: time.Hour}, t.TempDir(), 0, token)
	cfg.Mock = true // No target processes; routing, auth, pairing, broker and DB are real.
	instance, err := app.New(&cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	target, err := instance.DB.InsertTarget(&store.Target{Name: "existing machine", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := instance.Sessions.Launch(context.Background(), sessions.LaunchOpts{Name: "Existing session", TargetID: target.ID, Agent: "demo", Workdir: "/mock/project"})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := instance.Broker.CreateForSession(session.ID, "Write", map[string]any{"file_path": "demo-notes.md"})
	if err != nil {
		t.Fatal(err)
	}
	wifi := &wifiListener{handler: instance.Handler()}
	defer wifi.close()
	instance.Server.EnableWiFi = func() (string, error) { return wifi.enableAt("127.0.0.1") }
	instance.Server.WiFiURL = wifi.address
	local := httptest.NewServer(localHandler(instance.Handler(), newGate(token, "test-browser-key"), "same-runtime", func() error { return nil }))
	defer local.Close()
	request := func(client *http.Client, method, address, credential string, body any) (int, []byte) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, err := http.NewRequest(method, address, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if credential == "" && method != "GET" {
			parsed, _ := url.Parse(address)
			req.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err = io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	code, data := request(http.DefaultClient, "POST", local.URL+"/api/phone/wifi", token, map[string]any{})
	if code != 200 {
		t.Fatalf("enable: %d %s", code, data)
	}
	var enabled struct{ URL string }
	if err := json.Unmarshal(data, &enabled); err != nil || enabled.URL == "" {
		t.Fatalf("enable response: %s", data)
	}
	again, err := wifi.enableAt("127.0.0.1")
	if err != nil || again != enabled.URL {
		t.Fatalf("enable changed runtime listener: %q %v", again, err)
	}
	code, data = request(http.DefaultClient, "POST", local.URL+"/api/pair/mint", token, map[string]any{})
	var minted struct{ Code string }
	if json.Unmarshal(data, &minted) != nil || code != 200 || minted.Code == "" {
		t.Fatalf("mint: %d %s", code, data)
	}
	jar, _ := cookiejar.New(nil)
	phone := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	code, data = request(phone, "GET", enabled.URL+"/api/sessions", "", nil)
	if code != 401 {
		t.Fatalf("unpaired phone read sessions: %d %s", code, data)
	}
	code, data = request(phone, "POST", enabled.URL+"/api/pair/exchange", "", map[string]any{"code": minted.Code, "name": "Test phone"})
	if code != 200 {
		t.Fatalf("pair: %d %s", code, data)
	}
	code, data = request(phone, "GET", enabled.URL+"/api/sessions", "", nil)
	var rows []struct {
		ID   int64
		Name string
	}
	if code != 200 || json.Unmarshal(data, &rows) != nil {
		t.Fatalf("read: %d %s", code, data)
	}
	found := false
	for _, row := range rows {
		if row.ID == session.ID && row.Name == session.Name {
			found = true
		}
	}
	if !found {
		t.Fatalf("phone is not seeing existing session: %s", data)
	}
	code, data = request(phone, "POST", fmt.Sprintf("%s/api/approvals/%d/decision", enabled.URL, approval), "", map[string]any{"decision": "approved"})
	if code != 200 {
		t.Fatalf("phone approval: %d %s", code, data)
	}
	stored, err := instance.DB.Approval(approval)
	if err != nil || stored.Status != "approved" {
		t.Fatalf("decision didn't reach same DB: %+v %v", stored, err)
	}
	code, _ = request(phone, "GET", enabled.URL+identityRoute, "", nil)
	if code != 404 {
		t.Fatalf("phone reached local runtime identity: %d", code)
	}
	wifi.close()
	if wifi.address() != "" {
		t.Fatal("stopped listener still advertised")
	}
	// Closing Wi-Fi must leave the local runtime and session in place.
	code, data = request(http.DefaultClient, "GET", local.URL+"/api/sessions", token, nil)
	if code != 200 {
		t.Fatalf("closing Wi-Fi stopped local runtime: %d %s", code, data)
	}
}
