package lecternbridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// Everything the bridge posts to Lectern passes through redactBody, so a key
// the agent printed or was given never reaches the Lectern hook.
func TestRedactBodyMasksEveryStringValue(t *testing.T) {
	secret := "sk-ant-api03-" + strings.Repeat("A1b2", 6)
	body := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_input": map[string]any{
			"command": "curl -H 'x-api-key: " + secret + "' https://example.test",
			"nested":  []any{"plain", "also " + secret},
		},
		"options": []string{"use " + secret, "no"},
	}
	out := redactBody(body)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret survived redaction: %s", raw)
	}
	m := out.(map[string]any)
	if m["hook_event_name"] != "PreToolUse" {
		t.Fatalf("ordinary value changed: %v", m["hook_event_name"])
	}
	if body["hook_event_name"] != "PreToolUse" || !strings.Contains(body["tool_input"].(map[string]any)["command"].(string), secret) {
		t.Fatal("redactBody must not change the caller's body")
	}
}
