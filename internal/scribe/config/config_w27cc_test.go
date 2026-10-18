package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestContextManagementProjectOverridesGlobal(t *testing.T) {
	global := Config{ContextManagement: ContextManagement{ClearThinking: boolPtr(true), ClearToolUses: boolPtr(true)}}
	project := Config{ContextManagement: ContextManagement{ClearThinking: boolPtr(false)}}
	got := Merge(global, project)
	if got.ContextManagement.ClearThinking == nil || *got.ContextManagement.ClearThinking {
		t.Errorf("clear_thinking = %v, want the project's explicit false", got.ContextManagement.ClearThinking)
	}
	if got.ContextManagement.ClearToolUses == nil || !*got.ContextManagement.ClearToolUses {
		t.Errorf("clear_tool_uses = %v, want the global true kept when the project is silent", got.ContextManagement.ClearToolUses)
	}
}

func TestContextManagementUnsetIsOff(t *testing.T) {
	got := Merge(Config{}, Config{})
	if Bool(got.ContextManagement.ClearThinking, false) || Bool(got.ContextManagement.ClearToolUses, false) {
		t.Fatalf("unset context management should be off: %+v", got.ContextManagement)
	}
}

func TestContextManagementJSONKeys(t *testing.T) {
	c := Config{ContextManagement: ContextManagement{ClearThinking: boolPtr(true)}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"context_management":{"clear_thinking":true}`) {
		t.Fatalf("json = %s, want context_management.clear_thinking", b)
	}
	var back Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ContextManagement.ClearThinking == nil || !*back.ContextManagement.ClearThinking {
		t.Fatalf("round trip lost clear_thinking: %+v", back.ContextManagement)
	}
}

func TestContextManagementSet(t *testing.T) {
	var cm ContextManagement
	if err := cm.set("clear_thinking", true); err != nil || cm.ClearThinking == nil || !*cm.ClearThinking {
		t.Fatalf("set clear_thinking true: err=%v cm=%+v", err, cm)
	}
	if err := cm.set("clear_tool_uses", "true"); err != nil || cm.ClearToolUses == nil || !*cm.ClearToolUses {
		t.Fatalf("set clear_tool_uses from string: err=%v cm=%+v", err, cm)
	}
	if err := cm.set("clear_thinking", nil); err != nil || cm.ClearThinking != nil {
		t.Fatalf("nil should clear back to off: err=%v cm=%+v", err, cm)
	}
	if err := cm.set("compact", true); err == nil {
		t.Fatal("unknown field accepted")
	}
}
