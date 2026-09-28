package agents

import (
	"strings"
	"testing"
)

// The gate runs `lectern helper approval-hook` on a target with a lectern
// binary and the staged hook.py everywhere else; nothing else about the hook
// changes.
func TestHookSettingsNamesTheTargetsGate(t *testing.T) {
	gate := func(s map[string]any) string {
		return s["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	}
	if got := gate(HookSettings("http://cp:9110", "tok", "", 900)); got != "LECTERN_URL=http://cp:9110 LECTERN_TOKEN=tok python3 .lectern/hook.py" {
		t.Errorf("python gate: %q", got)
	}
	if got := gate(HookSettingsFor("http://cp:9110", "tok", "", 900, "/opt/my lectern/lectern")); got != "LECTERN_URL=http://cp:9110 LECTERN_TOKEN=tok '/opt/my lectern/lectern' helper approval-hook" {
		t.Errorf("go gate: %q", got)
	}
	s, err := BuildSettings(SettingsInput{BaseURL: "u", Token: "t", Gated: true, Lectern: "/l"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gate(s); !strings.HasSuffix(got, " /l helper approval-hook") {
		t.Errorf("BuildSettings does not pass the lectern binary on: %q", got)
	}
}
