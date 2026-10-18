package config

import "testing"

func TestSetFieldAndClearFieldReachContextManagement(t *testing.T) {
	var c Config
	if ok, err := c.SetField("context_management.clear_tool_uses", true); !ok || err != nil {
		t.Fatalf("SetField: ok=%v err=%v", ok, err)
	}
	if c.ContextManagement.ClearToolUses == nil || !*c.ContextManagement.ClearToolUses {
		t.Fatal("clear_tool_uses was not set")
	}
	if !c.ClearField("context_management.clear_tool_uses") || c.ContextManagement.ClearToolUses != nil {
		t.Fatal("ClearField did not reset clear_tool_uses")
	}
}
