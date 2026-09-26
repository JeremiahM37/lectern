package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// rawDo issues a request with arbitrary headers/cookies and returns the whole
// response — pairing needs to inspect Set-Cookie, which h.post/h.get discard.
func rawDo(t *testing.T, method, url string, body any, headers map[string]string, cookies map[string]*http.Cookie) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) obj {
	t.Helper()
	defer resp.Body.Close()
	var out obj
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v (body=%s)", resp.Request.URL.Path, err, raw)
		}
	}
	return out
}

func setCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func pairingHarness(t *testing.T, tweak ...func(*config.Config)) *harness {
	return newHarness(t, append([]func(*config.Config){
		func(c *config.Config) { c.DevicePairing = true },
	}, tweak...)...)
}

// ---- mint: owner-only ----------------------------------------------------

func TestPairingMintRequiresOwner(t *testing.T) {
	// mode tailscale + a loopback caller (this test's own HTTP client) is
	// resolved as KindLocal, ok but not human — exactly the "authenticated
	// but not an owner" case mint must refuse.
	h := pairingHarness(t, func(c *config.Config) { c.Auth = "tailscale" })
	resp := rawDo(t, "POST", h.URL+"/api/pair/mint", nil, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403 for a non-human (KindLocal) caller, got %d", resp.StatusCode)
	}
}

func TestPairingMintRejectsUnauthenticated(t *testing.T) {
	h := pairingHarness(t, func(c *config.Config) { c.Auth = "token"; c.AuthToken = "secret" })
	resp := rawDo(t, "POST", h.URL+"/api/pair/mint", nil, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 with no credential at all, got %d", resp.StatusCode)
	}
}

func TestPairingMintDisabledByDefault(t *testing.T) {
	h := newHarness(t) // no DevicePairing tweak — the default is off
	code := h.status("POST", "/api/pair/mint", nil)
	if code != 409 {
		t.Fatalf("expected 409 when pairing is off, got %d", code)
	}
}

// ---- exchange: the one unauthenticated write ------------------------------

func TestPairingExchangeRejectsWrongCode(t *testing.T) {
	h := pairingHarness(t)
	resp := rawDo(t, "POST", h.URL+"/api/pair/exchange", obj{"code": "0000000000000000000000000000FF"}, nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for an unknown code, got %d", resp.StatusCode)
	}
}

func TestPairingExchangeRejectsReusedCode(t *testing.T) {
	h := pairingHarness(t)
	minted := h.post("/api/pair/mint", nil, 200)
	code := minted.str("code")

	first := rawDo(t, "POST", h.URL+"/api/pair/exchange", obj{"code": code, "name": "Phone one"}, nil, nil)
	if first.StatusCode != 200 {
		t.Fatalf("expected the first exchange to succeed, got %d", first.StatusCode)
	}
	first.Body.Close()

	second := rawDo(t, "POST", h.URL+"/api/pair/exchange", obj{"code": code, "name": "Phone two"}, nil, nil)
	defer second.Body.Close()
	if second.StatusCode != 400 {
		t.Fatalf("expected 400 for a reused code, got %d", second.StatusCode)
	}
}

func TestPairingExchangeRateLimitTrips(t *testing.T) {
	h := pairingHarness(t)
	tripped := false
	for i := 0; i < 15; i++ {
		resp := rawDo(t, "POST", h.URL+"/api/pair/exchange", obj{"code": "0000000000000000000000000000FF"}, nil, nil)
		status := resp.StatusCode
		resp.Body.Close()
		if status == 429 {
			tripped = true
			break
		}
		if status != 400 {
			t.Fatalf("attempt %d: expected 400 (wrong code) or 429 (rate limited), got %d", i, status)
		}
	}
	if !tripped {
		t.Fatal("expected the per-IP rate limit to trip within 15 wrong-code attempts (limit is 10/min)")
	}
}

// ---- full flow: mint, exchange, device authorizes API calls, CSRF, revoke ----

func TestPairingFullFlowInTokenMode(t *testing.T) {
	h := pairingHarness(t, func(c *config.Config) { c.Auth = "token"; c.AuthToken = "secret" })

	// Sanity: token mode is really gating.
	if code := h.status("GET", "/api/tasks", nil); code != 401 {
		t.Fatalf("expected 401 with no token, got %d", code)
	}

	minted := decodeBody(t, rawDo(t, "POST", h.URL+"/api/pair/mint", nil,
		map[string]string{"Authorization": "Bearer secret"}, nil))
	code, _ := minted["code"].(string)
	if code == "" {
		t.Fatalf("mint did not return a code: %v", minted)
	}

	exchangeResp := rawDo(t, "POST", h.URL+"/api/pair/exchange", obj{"code": code, "name": "Jeremiah's Phone"}, nil, nil)
	deviceCookie := setCookie(exchangeResp, "lectern_device")
	exchanged := decodeBody(t, exchangeResp)
	if deviceCookie == nil {
		t.Fatal("expected a lectern_device cookie to be set")
	}
	if !deviceCookie.Secure || !deviceCookie.HttpOnly || deviceCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("device cookie must be Secure+HttpOnly+SameSite=Strict, got %+v", deviceCookie)
	}
	if exchanged.str("token") == "" {
		t.Fatal("expected a device token in the exchange response, for API clients that cannot read cookies")
	}

	// The device cookie authorizes an ordinary read...
	readResp := rawDo(t, "GET", h.URL+"/api/tasks", nil, nil, map[string]*http.Cookie{"d": deviceCookie})
	readResp.Body.Close()
	if readResp.StatusCode != 200 {
		t.Fatalf("expected the paired device's cookie to authorize a GET, got %d", readResp.StatusCode)
	}

	// ...but a state-changing request with no Origin is refused (CSRF).
	var projects []obj
	{
		resp := rawDo(t, "GET", h.URL+"/api/projects", nil, map[string]string{"Authorization": "Bearer secret"}, nil)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, &projects); err != nil || len(projects) == 0 {
			t.Fatalf("expected a seeded project: err=%v body=%s", err, raw)
		}
	}
	pid := projects[0].id()
	noOrigin := rawDo(t, "POST", h.URL+"/api/tasks",
		obj{"project_id": pid, "title": "from phone", "prompt": "noop"},
		nil, map[string]*http.Cookie{"d": deviceCookie})
	noOrigin.Body.Close()
	if noOrigin.StatusCode != 401 {
		t.Fatalf("expected 401 for a cookie-authenticated POST with no Origin, got %d", noOrigin.StatusCode)
	}

	// The same request with a matching Origin succeeds.
	withOrigin := rawDo(t, "POST", h.URL+"/api/tasks",
		obj{"project_id": pid, "title": "from phone", "prompt": "noop"},
		map[string]string{"Origin": h.URL}, map[string]*http.Cookie{"d": deviceCookie})
	withOrigin.Body.Close()
	if withOrigin.StatusCode != 201 {
		t.Fatalf("expected 201 for a cookie-authenticated POST with a matching Origin, got %d", withOrigin.StatusCode)
	}

	// An API client may instead present the returned token as a bearer —
	// no Origin needed, since no cookie/browser is involved.
	bearerResp := rawDo(t, "GET", h.URL+"/api/tasks", nil,
		map[string]string{"Authorization": "Bearer " + exchanged.str("token")}, nil)
	bearerResp.Body.Close()
	if bearerResp.StatusCode != 200 {
		t.Fatalf("expected the device token to work as a bearer too, got %d", bearerResp.StatusCode)
	}

	// Settings → Devices sees exactly one paired device, as the owner.
	var list []obj
	{
		resp := rawDo(t, "GET", h.URL+"/api/pair/devices", nil, map[string]string{"Authorization": "Bearer secret"}, nil)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatalf("decode device list: %v (%s)", err, raw)
		}
	}
	if len(list) != 1 || list[0].str("name") != "Jeremiah's Phone" {
		t.Fatalf("expected exactly one device named Jeremiah's Phone, got %+v", list)
	}

	// Revoke: the device's next request is a plain 401.
	revokeID := list[0].id()
	revokeResp := rawDo(t, "DELETE", fmt.Sprintf("%s/api/pair/devices/%d", h.URL, revokeID), nil,
		map[string]string{"Authorization": "Bearer secret"}, nil)
	revokeResp.Body.Close()
	if revokeResp.StatusCode != 200 {
		t.Fatalf("expected revoke to succeed, got %d", revokeResp.StatusCode)
	}
	afterRevoke := rawDo(t, "GET", h.URL+"/api/tasks", nil, nil, map[string]*http.Cookie{"d": deviceCookie})
	afterRevoke.Body.Close()
	if afterRevoke.StatusCode != 401 {
		t.Fatalf("expected 401 for a revoked device, got %d", afterRevoke.StatusCode)
	}
}

func TestPairingSettingsRoundTrip(t *testing.T) {
	h := newHarness(t) // pairing off by default here
	got := h.get("/api/pair/settings")
	if got["enabled"] != false {
		t.Fatalf("expected pairing off by default, got %v", got)
	}
	h.request2("PUT", "/api/pair/settings", obj{"enabled": true, "idle_days": float64(14)}, 200)
	got = h.get("/api/pair/settings")
	if got["enabled"] != true {
		t.Fatalf("expected pairing enabled after PUT, got %v", got)
	}
	if got["idle_days"] != float64(14) {
		t.Fatalf("expected idle_days=14, got %v", got)
	}
}
