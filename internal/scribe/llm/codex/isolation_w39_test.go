package codex

import (
	"strings"
	"testing"
)

// codex must not act on the machine itself: its shell, browser, apps and the
// user's own config (MCP servers, plugins) are switched off on every call.
func TestExecArgsSwitchOffCodexOwnTools(t *testing.T) {
	joined := strings.Join(execArgs("m", "/w", "/s"), " ")
	for _, want := range []string{
		"--ignore-user-config", "--ignore-rules", "--sandbox read-only",
		"features.shell_tool=false", "features.unified_exec=false",
		"features.computer_use=false", "features.browser_use=false",
		"features.apps=false", "features.plugins=false",
		`web_search="disabled"`, "project_doc_max_bytes=0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q: %s", want, joined)
		}
	}
	// --disable fails the whole call for a feature name an older or newer
	// codex does not know; the -c form only warns.
	if strings.Contains(joined, "--disable") {
		t.Errorf("argv uses --disable: %s", joined)
	}
}

func TestFailureUnwrapsCodexJSONError(t *testing.T) {
	raw := `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'x' model is not supported when using Codex with a ChatGPT account."}}`
	got := failure(raw).Error()
	if strings.Contains(got, `"status"`) || !strings.Contains(got, "is not supported when using Codex") {
		t.Fatalf("got %q", got)
	}
	if innerMessage("plain text") != "plain text" {
		t.Fatal("plain text changed")
	}
}
