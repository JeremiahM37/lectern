package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPrivateIntegrationCatalogBridgeIsReadOnly(t *testing.T) {
	s := autoTestServer(t)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := httptest.NewRecorder()
		s.autoReadBridge(w, httptest.NewRequest(method, "/private-integrations", strings.NewReader(`{"publish":true}`)))
		if w.Code != 405 {
			t.Fatal("catalog accepted mutation", method, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/private-integrations", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("empty ledger unavailable", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.autoReadBridge(w, httptest.NewRequest("GET", "/private-integrations?root_id=%zz", nil))
	if w.Code != 400 {
		t.Fatal("malformed selector ignored", w.Code)
	}
}

func TestPrivateIntegrationCapabilityRequiresInstalledHelpers(t *testing.T) {
	yes := map[string]any{"status": "installed"}
	no := map[string]any{"status": "unavailable"}
	for _, pair := range [][2]map[string]any{{yes, no}, {no, yes}} {
		if autoPrivateIntegrationCapability(pair[0], pair[1])["status"] != "unavailable" {
			t.Fatal("missing mechanism advertised")
		}
	}
	capability := autoPrivateIntegrationCapability(yes, yes)
	if capability["status"] != "on_demand" {
		t.Fatal("installed mechanism hidden")
	}
	raw, _ := json.Marshal(capability)
	for _, required := range []string{"source_integration_id", "sealed combined candidate", "No public push", "Manual integration attestations remain separate"} {
		if !strings.Contains(string(raw), required) {
			t.Fatal("incomplete authority contract", required)
		}
	}
}

func TestPrivateIntegrationCatalogPaginationAndIdentity(t *testing.T) {
	rows := []map[string]any{}
	for i := 28; i > 0; i-- {
		rows = append(rows, map[string]any{"integration_id": fmt.Sprintf("%064x", i), "root_id": fmt.Sprintf("%064x", 1), "status": "rejected"})
	}
	value, status := autoPrivateIntegrationDiscovery(rows, url.Values{})
	if status != 200 {
		t.Fatal(value)
	}
	page := value.(map[string]any)
	items := page["items"].([]map[string]any)
	if len(items) != 25 || items[0]["integration_id"] != fmt.Sprintf("%064x", 1) {
		t.Fatal("unstable or unbounded index")
	}
	if _, ok := rows[0]["details_uri"]; ok {
		t.Fatal("catalog changed ledger projection")
	}
	value, status = autoPrivateIntegrationDiscovery(rows, url.Values{"after": {page["next_after"].(string)}})
	if status != 200 || len(value.(map[string]any)["items"].([]map[string]any)) != 3 {
		t.Fatal("pagination lost rejected history")
	}
	value, status = autoPrivateIntegrationDiscovery(rows, url.Values{"integration_id": {fmt.Sprintf("%064x", 1)}})
	if status != 200 || value.(map[string]any)["integration"].(map[string]any)["status"] != "rejected" {
		t.Fatal("historical rejection changed")
	}
	if _, status = autoPrivateIntegrationDiscovery(rows, url.Values{"integration_id": {fmt.Sprintf("%064x", 99)}}); status != 404 {
		t.Fatal("invented integration")
	}
	for _, q := range []url.Values{{"root_id": {""}}, {"root_id": {"bad"}}, {"root_id": {fmt.Sprintf("%064x", 1), fmt.Sprintf("%064x", 2)}}, {"integration_id": {fmt.Sprintf("%064x", 1)}, "after": {fmt.Sprintf("%064x", 2)}}, {"path": {"/etc/passwd"}}} {
		if _, status = autoPrivateIntegrationDiscovery(rows, q); status != 400 {
			t.Fatal("ambiguous selector accepted", q)
		}
	}
	if _, status = autoPrivateIntegrationDiscovery([]map[string]any{{"root_id": "broken"}}, url.Values{}); status != 503 {
		t.Fatal("malformed ledger disguised as empty")
	}
}
