package lsp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// offFor returns a gate that turns off exactly the named toggles.
func offFor(names ...string) func(string) bool {
	off := map[string]bool{}
	for _, n := range names {
		off[n] = true
	}
	return func(f string) bool { return !off[f] }
}

func gateRunTool(t *testing.T, r *tools.Registry, name, args string) tools.Result {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tool.Run(context.Background(), &tools.Env{Workdir: t.TempDir()}, json.RawMessage(args))
}

func TestEditorToggleRefusesItsTool(t *testing.T) {
	r := tools.NewRegistry()
	RegisterGated(r, NewManager(t.TempDir()), offFor("diagnostics", "hover", "rename", "format", "code_actions"))

	for _, c := range []struct{ tool, args, feature string }{
		{"diagnostics", `{"path":"a.go"}`, "diagnostics"},
		{"navigate", `{"action":"hover","path":"a.go","line":1,"column":1}`, "hover"},
		{"rename_symbol", `{"path":"a.go","line":1,"column":1,"new_name":"x"}`, "rename"},
		{"format_file", `{"path":"a.go"}`, "format"},
		{"code_action", `{"path":"a.go","line":1}`, "code_actions"},
		{"refactor", `{"action":"extract_function","path":"a.go"}`, "code_actions"},
	} {
		res := gateRunTool(t, r, c.tool, c.args)
		if !res.IsError || !strings.Contains(res.Output, `"`+c.feature+`" editor integration is off`) {
			t.Errorf("%s with %s off: %+v", c.tool, c.feature, res)
		}
	}
}

// A toggle that is on does not block the tool, and the gate does not hide
// other errors of the call.
func TestEditorToggleLeavesOtherToolsRunning(t *testing.T) {
	r := tools.NewRegistry()
	RegisterGated(r, NewManager(t.TempDir()), offFor("hover"))

	res := gateRunTool(t, r, "navigate", `{"action":"definition","path":"a.go","line":1,"column":1}`)
	if strings.Contains(res.Output, "editor integration is off") {
		t.Errorf("definition was gated by the hover toggle: %+v", res)
	}
	res = gateRunTool(t, r, "diagnostics", `{"path":"a.go"}`)
	if strings.Contains(res.Output, "editor integration is off") {
		t.Errorf("diagnostics was gated with its toggle on: %+v", res)
	}
}

// navigate picks its toggle from the action, so every hierarchy action is
// gated by references and symbols by symbols.
func TestNavigateFeatureFromAction(t *testing.T) {
	cases := map[string]string{
		"definition":        "definition",
		"implementation":    "definition",
		"references":        "references",
		"incoming-calls":    "references",
		"supertypes":        "references",
		"hover":             "hover",
		"symbols":           "symbols",
		"workspace_symbols": "symbols",
		"read_symbol":       "symbols",
		"nonsense":          "",
	}
	for action, want := range cases {
		args, _ := json.Marshal(map[string]string{"action": action})
		if got := navigateFeature(args); got != want {
			t.Errorf("navigateFeature(%q) = %q, want %q", action, got, want)
		}
	}
}

// The gated wrapper must keep the per-call read-only answer the mode gate uses.
func TestGatedToolKeepsReadOnlyCall(t *testing.T) {
	r := tools.NewRegistry()
	RegisterGated(r, NewManager(t.TempDir()), nil)
	tool, _ := r.Get("code_action")
	c, ok := tool.(tools.CallReadOnly)
	if !ok {
		t.Fatal("gated code_action lost ReadOnlyCall")
	}
	inner := &codeActionTool{}
	args := json.RawMessage(`{"path":"a.go"}`)
	if c.ReadOnlyCall(args) != inner.ReadOnlyCall(args) {
		t.Error("ReadOnlyCall answer changed by the wrapper")
	}
}
