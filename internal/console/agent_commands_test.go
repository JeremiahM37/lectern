package console

import (
	"strings"
	"testing"
)

func TestTargetCommandCheckIsReadOnly(t *testing.T) {
	m := sampleDashboard()
	m.section = 5 // Machines
	for _, action := range m.actions() {
		if action.Label == "Check agent commands" {
			if action.Method != "GET" || action.Path != "/targets/"+id(m.current())+"/agents" {
				t.Fatalf("unexpected action: %+v", action)
			}
			return
		}
	}
	t.Fatal("target command action missing")
}

func TestCommandDetailExplainsScopeAndStates(t *testing.T) {
	text := formatDetail("Check agent commands", []byte(`[{"name":"fixture","state":"available","path":"/bin/fixture"},{"name":"custom","state":"unchecked","detail":"Custom PATH"}]`))
	for _, want := range []string{"Project/profile overrides may differ", "login and model access are not checked", "fixture · Found", "/bin/fixture", "custom · Not checked", "Custom PATH"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
}
