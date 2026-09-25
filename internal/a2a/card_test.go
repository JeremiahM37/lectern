package a2a

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// The card is served unauthenticated, so its shape is a contract: exactly the
// fields the protocol defines and nothing else. A leaked field here is a leak
// to anyone who can reach the port.
func TestCardJSONShape(t *testing.T) {
	raw, err := json.Marshal(Card("https://lectern.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	const want = "capabilities defaultInputModes defaultOutputModes description name " +
		"security securitySchemes skills supportedInterfaces version"
	gotKeys := make([]string, 0, len(got))
	for k := range got {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if strings.Join(gotKeys, " ") != want {
		t.Fatalf("agent card fields are %v, want %v", gotKeys, strings.Split(want, " "))
	}

	if got["name"] != "Lectern" {
		t.Errorf("name = %v", got["name"])
	}
	if got["version"] != version.Version {
		t.Errorf("version = %v, internal/version says %q", got["version"], version.Version)
	}
	if s, _ := got["description"].(string); len(strings.TrimSpace(s)) < 40 {
		t.Errorf("description is too thin to be useful: %q", s)
	}

	caps, _ := got["capabilities"].(map[string]any)
	if caps["streaming"] != false || caps["pushNotifications"] != false {
		t.Errorf("capabilities = %v, want both false", caps)
	}
	for _, field := range []string{"defaultInputModes", "defaultOutputModes"} {
		modes, _ := got[field].([]any)
		if len(modes) != 1 || modes[0] != "text/plain" {
			t.Errorf("%s = %v, want [text/plain]", field, modes)
		}
	}
}

func TestCardInterfaceURL(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://lectern.example.com", "https://lectern.example.com/a2a/v1"},
		{"https://lectern.example.com/", "https://lectern.example.com/a2a/v1"},
		{"http://127.0.0.1:9110", "http://127.0.0.1:9110/a2a/v1"},
	} {
		card := Card(tc.base)
		if len(card.SupportedInterfaces) != 1 {
			t.Fatalf("%s: %d interfaces, want 1", tc.base, len(card.SupportedInterfaces))
		}
		iface := card.SupportedInterfaces[0]
		if iface.URL != tc.want {
			t.Errorf("base %q: url = %q, want %q", tc.base, iface.URL, tc.want)
		}
		if iface.ProtocolBinding != "JSONRPC" {
			t.Errorf("protocolBinding = %q", iface.ProtocolBinding)
		}
		if iface.ProtocolVersion != "1.0" {
			t.Errorf("protocolVersion = %q", iface.ProtocolVersion)
		}
	}
}

func TestCardSkills(t *testing.T) {
	card := Card("https://lectern.example.com")
	want := []string{"dispatch-task", "best-of-n", "project-status"}
	if len(card.Skills) != len(want) {
		t.Fatalf("%d skills, want %d", len(card.Skills), len(want))
	}
	for i, id := range want {
		skill := card.Skills[i]
		if skill.ID != id {
			t.Errorf("skill %d id = %q, want %q", i, skill.ID, id)
		}
		if skill.Name == "" || len(skill.Description) < 30 {
			t.Errorf("skill %s needs a name and a real description: %+v", id, skill)
		}
		if len(skill.Tags) == 0 || len(skill.Examples) == 0 {
			t.Errorf("skill %s needs tags and examples: %+v", id, skill)
		}
	}
}

// Every scheme a client can authenticate with has to be declared, and the
// bearer token is the one a non-tailnet caller uses.
func TestCardSecurity(t *testing.T) {
	card := Card("https://lectern.example.com")
	bearer, ok := card.SecuritySchemes["bearer"]
	if !ok {
		t.Fatal("no bearer scheme declared")
	}
	if bearer.Type != "http" || bearer.Scheme != "bearer" {
		t.Errorf("bearer scheme = %+v", bearer)
	}
	ts, ok := card.SecuritySchemes["tailscale"]
	if !ok {
		t.Fatal("no tailscale scheme declared")
	}
	if !strings.Contains(ts.Description, "Tailscale") {
		t.Errorf("tailscale scheme must say what it accepts: %+v", ts)
	}
	seen := map[string]bool{}
	for _, requirement := range card.Security {
		for name := range requirement {
			seen[name] = true
		}
	}
	if !seen["bearer"] || !seen["tailscale"] {
		t.Errorf("security = %v, want both schemes", card.Security)
	}
}

// The card is marshalled from a typed struct, so the only way a secret could
// reach it is through a field someone added. This pins the wire bytes: the
// card for two different base URLs must differ in the interface URL alone.
func TestCardCarriesNothingBeyondItsFields(t *testing.T) {
	raw, err := json.Marshal(Card("https://lectern.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["provider"]; ok {
		t.Error("provider is not part of the advertised card")
	}
	if s := string(raw); strings.Contains(s, "http://127.0.0.1") || strings.Contains(s, "secret") {
		t.Errorf("card leaked something about this install: %s", s)
	}
}
