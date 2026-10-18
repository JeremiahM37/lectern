package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// Unset pruning is none, and the Pruning accessor says so.
func TestPruningDefaultsToNone(t *testing.T) {
	var cm ContextManagement
	if cm.Pruning() != PruningNone {
		t.Fatalf("unset pruning = %q, want none", cm.Pruning())
	}
	if got := (Config{}).ContextManagement.Pruning(); got != PruningNone {
		t.Fatalf("zero config pruning = %q, want none", got)
	}
}

// Each accepted target is stored; none and the empty value clear the field.
func TestSetFieldPruningTarget(t *testing.T) {
	var c Config
	for _, v := range []string{"main", "subagents", "both"} {
		ok, err := c.SetField("context_management.pruning_target", v)
		if !ok || err != nil {
			t.Fatalf("set %q: handled=%v err=%v", v, ok, err)
		}
		if c.ContextManagement.Pruning() != v {
			t.Fatalf("set %q: got %q", v, c.ContextManagement.Pruning())
		}
	}
	if ok, err := c.SetField("context_management.pruning_target", "none"); !ok || err != nil {
		t.Fatalf("set none: handled=%v err=%v", ok, err)
	}
	if c.ContextManagement.PruningTarget != "" || c.ContextManagement.Pruning() != PruningNone {
		t.Fatalf("none should store the empty value, got %q", c.ContextManagement.PruningTarget)
	}
	c.ContextManagement.PruningTarget = PruningBoth
	if _, err := c.SetField("context_management.pruning_target", nil); err != nil || c.ContextManagement.PruningTarget != "" {
		t.Fatalf("nil should clear the target, got %q (%v)", c.ContextManagement.PruningTarget, err)
	}
}

// An unknown target is refused with the valid list, and nothing is written.
func TestSetFieldPruningRejectsUnknownTarget(t *testing.T) {
	c := Config{ContextManagement: ContextManagement{PruningTarget: PruningMain}}
	ok, err := c.SetField("context_management.pruning_target", "turbo")
	if !ok || err == nil || !strings.Contains(err.Error(), "none, main, subagents or both") {
		t.Fatalf("bad target: handled=%v err=%v", ok, err)
	}
	if c.ContextManagement.PruningTarget != PruningMain {
		t.Fatalf("a refused target changed the setting to %q", c.ContextManagement.PruningTarget)
	}
}

// A project file's target overrides the global one; a project that is silent
// keeps the global value.
func TestPruningTargetMergesProjectOverGlobal(t *testing.T) {
	global := Config{ContextManagement: ContextManagement{PruningTarget: PruningMain}}
	if got := Merge(global, Config{}).ContextManagement.Pruning(); got != PruningMain {
		t.Fatalf("silent project: %q, want the global main", got)
	}
	if got := Merge(global, Config{ContextManagement: ContextManagement{PruningTarget: PruningNone}}).ContextManagement.Pruning(); got != PruningNone {
		t.Fatalf("project none should override global main, got %q", got)
	}
	if got := Merge(global, Config{ContextManagement: ContextManagement{PruningTarget: PruningSubagents}}).ContextManagement.Pruning(); got != PruningSubagents {
		t.Fatalf("project subagents: %q", got)
	}
}

// The key is pruning_target inside context_management, and an unset value is
// left out of the file.
func TestPruningTargetJSON(t *testing.T) {
	b, err := json.Marshal(Config{ContextManagement: ContextManagement{PruningTarget: PruningBoth}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"context_management":{"pruning_target":"both"}`) {
		t.Fatalf("unexpected JSON: %s", b)
	}
	b, _ = json.Marshal(Config{})
	if strings.Contains(string(b), "pruning_target") {
		t.Fatalf("unset target should be omitted: %s", b)
	}
}

// The accepted list matches the spec's choices, in order.
func TestPruningTargetsList(t *testing.T) {
	want := "none main subagents both"
	if got := strings.Join(PruningTargets, " "); got != want {
		t.Fatalf("PruningTargets = %q, want %q", got, want)
	}
}
