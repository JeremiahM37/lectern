package sinks

import (
	"encoding/json"
	"strings"
	"testing"
)

const base = "http://cp:9110"

func TestNoSinksConfiguredBuildsNothing(t *testing.T) {
	if got := BuildPayloads(map[string]string{}, base, "t", "b", "/", nil); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	cfg := map[string]string{"discord_webhook": "", "ntfy_server": "", "ntfy_topic": ""}
	if got := BuildPayloads(cfg, base, "t", "b", "/", nil); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDiscordPayload(t *testing.T) {
	out := BuildPayloads(map[string]string{"discord_webhook": "https://discord/hook"},
		base, "Title", "Body", "/", nil)
	if len(out) != 1 || out[0].Kind != "discord" || out[0].URL != "https://discord/hook" {
		t.Fatalf("got %+v", out)
	}
	if out[0].Body["content"] != "**Title** — Body" {
		t.Fatalf("content: %v", out[0].Body["content"])
	}
}

func TestNtfyRequiresServerAndTopic(t *testing.T) {
	if got := BuildPayloads(map[string]string{"ntfy_server": "https://ntfy.sh"},
		base, "t", "b", "/", nil); len(got) != 0 {
		t.Errorf("server without topic: %+v", got)
	}
	if got := BuildPayloads(map[string]string{"ntfy_topic": "lec"},
		base, "t", "b", "/", nil); len(got) != 0 {
		t.Errorf("topic without server: %+v", got)
	}
}

func TestNtfyPlainPayload(t *testing.T) {
	out := BuildPayloads(map[string]string{
		"ntfy_server": "https://ntfy.sh/", "ntfy_topic": "lec"},
		base, "Ready for review", "Fix the bug", "/#task/3", nil)
	if len(out) != 1 || out[0].Kind != "ntfy" || out[0].URL != "https://ntfy.sh" {
		t.Fatalf("got %+v", out)
	}
	msg := out[0].Body
	if msg["topic"] != "lec" {
		t.Errorf("topic: %v", msg["topic"])
	}
	if !strings.HasSuffix(msg["title"].(string), "Ready for review") {
		t.Errorf("title: %v", msg["title"])
	}
	if !strings.HasSuffix(msg["click"].(string), "/#task/3") {
		t.Errorf("click: %v", msg["click"])
	}
	if _, ok := msg["actions"]; ok {
		t.Error("a plain notification must not carry decision buttons")
	}
}

// A phone should be able to decide without opening the app, and without VAPID.
func TestNtfyApprovalCarriesActionButtons(t *testing.T) {
	out := BuildPayloads(map[string]string{
		"ntfy_server": "https://ntfy.sh", "ntfy_topic": "lec"},
		base, "Approval needed", "Bash: rm -rf build/", "/",
		&Extra{Kind: "approval", ApprovalID: 42})
	msg := out[0].Body
	actions, ok := msg["actions"].([]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("actions: %v", msg["actions"])
	}
	var labels []string
	for _, a := range actions {
		am := a.(map[string]any)
		labels = append(labels, am["label"].(string))
		if !strings.HasSuffix(am["url"].(string), "/api/approvals/42/decision") {
			t.Errorf("action url: %v", am["url"])
		}
		if am["method"] != "POST" {
			t.Errorf("action method: %v", am["method"])
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(am["body"].(string)), &body); err != nil {
			t.Fatalf("action body is not JSON: %v", am["body"])
		}
		if d := body["decision"]; d != "approved" && d != "denied" {
			t.Errorf("decision: %v", d)
		}
	}
	joined := strings.Join(labels, " ")
	if !strings.Contains(joined, "Approve") || !strings.Contains(joined, "Deny") {
		t.Errorf("labels: %v", labels)
	}
	if msg["priority"] != 4 {
		t.Errorf("an approval must outrank ordinary notifications: %v", msg["priority"])
	}
}

func TestLongBodiesTruncated(t *testing.T) {
	out := BuildPayloads(map[string]string{"discord_webhook": "x"},
		base, "T", strings.Repeat("y", 5000), "/", nil)
	if len(out[0].Body["content"].(string)) > 1900 {
		t.Fatal("Discord rejects anything over its message limit")
	}
}
